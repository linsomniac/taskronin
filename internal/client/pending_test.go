package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/clock"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/service"
)

func pendingClock() *clock.Fake {
	return clock.NewFake(time.Date(2026, 10, 28, 16, 0, 0, 0, time.UTC))
}

func pendingRequest(now time.Time) PendingRequest {
	return PendingRequest{
		Key: "saved-request-key", Method: http.MethodPost, Path: "/tasks",
		IfMatch: `"0123456789abcdef-1"`, Generation: testGeneration, Actor: "owner",
		Body: json.RawMessage(`{"title":"Remember this"}`), CreatedAt: now,
	}
}

func TestPendingListSorted(t *testing.T) {
	clk := pendingClock()
	s := &PendingStore{Dir: filepath.Join(t.TempDir(), "pending"), Clock: clk}
	first := pendingRequest(clk.Now())
	first.Key = "zzzzzzzzzzzzzzzz"
	if err := s.Save(first); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Minute)
	second := pendingRequest(clk.Now())
	second.Key = "aaaaaaaaaaaaaaaa"
	if err := s.Save(second); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, ".tkr-tmp-unfinished"), []byte("incomplete"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.List()
	if err != nil || !reflect.DeepEqual(got, []PendingRequest{first, second}) {
		t.Fatalf("List() = %+v, %v", got, err)
	}
	for path, mode := range map[string]fs.FileMode{
		s.Dir: 0o700, filepath.Join(s.Dir, first.Key+".json"): 0o600,
		filepath.Join(s.Dir, second.Key+".json"): 0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Errorf("%s mode = %o, want %o", path, info.Mode().Perm(), mode)
		}
	}
}

func TestPendingListMissingAndMalformed(t *testing.T) {
	s := &PendingStore{Dir: filepath.Join(t.TempDir(), "pending"), Clock: pendingClock()}
	if got, err := s.List(); err != nil || len(got) != 0 {
		t.Fatalf("missing directory List() = %+v, %v", got, err)
	}
	if err := s.Save(pendingRequest(s.Clock.Now())); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(); err == nil {
		t.Fatal("List accepted malformed pending JSON")
	}
}

func TestResendReplays(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			c, rec := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"op_id":"0123456789abcdef0123456789abcdef","replayed":true,"effects":[],"warnings":[]}`)
			}))
			clk := pendingClock()
			c.opts.Clock = clk
			c.opts.NewKey = func() string { t.Error("resend generated a new key"); return "unexpected-key" }
			s := pendingFor(t, c)
			p := pendingRequest(clk.Now())
			p.Method, p.Path = method, "/tasks/42"
			// Preserve the saved wire bytes, including whitespace and HTML characters.
			p.Body = json.RawMessage("{\n  \"title\": \"<saved>&\"\n}")
			if err := s.Save(p); err != nil {
				t.Fatal(err)
			}
			before := pendingBytes(t, s, p.Key)
			out, err := c.Resend(t.Context(), p)
			if err != nil || out == nil || out.Op == nil || !out.Op.Replayed || out.Token != nil {
				t.Fatalf("Resend() = %+v, %v", out, err)
			}
			requests := rec.at("/api/v1" + p.Path)
			if len(requests) != 1 {
				t.Fatalf("mutation count = %d", len(requests))
			}
			assertSavedRequest(t, requests[0], p)
			if !bytes.Equal(before, pendingBytes(t, s, p.Key)) {
				t.Fatal("Resend changed the pending file; only the caller may discard it")
			}
		})
	}
}

func TestResendExpired(t *testing.T) {
	c, rec := newFake(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unexpected mutation") }))
	c.opts.Clock = pendingClock()
	p := pendingRequest(c.opts.Clock.Now().Add(-7*24*time.Hour - time.Second))
	// Expiry must win even when the later identity guards would also fail.
	p.Generation, p.Actor = "aaaaaaaaaaaaaaaa", "token:ai"
	if out, err := c.Resend(t.Context(), p); out != nil || !errors.Is(err, ErrPendingExpired) {
		t.Fatalf("Resend() = %+v, %v", out, err)
	}
	if len(rec.at("/api/v1/whoami")) != 0 || len(rec.at("/api/v1/tasks")) != 0 {
		t.Fatal("expired resend made a request")
	}
}

// TestResendAtExpiryBoundary is final review A-Minor-3: CreatedAt is stamped
// up to the 20 s call budget after the server committed, and the server keeps
// idempotency rows for exactly 7 d by commit time. A resend therefore stops one
// hour short of 7 d, or an op without If-Match could re-execute (undo/redo on
// a different stack entry) instead of replaying.
func TestResendAtExpiryBoundary(t *testing.T) {
	if PendingResendWindow != service.IdemTTL-time.Hour {
		t.Fatalf("PendingResendWindow = %v, want the server's IdemTTL (%v) minus 1h", PendingResendWindow, service.IdemTTL)
	}
	c, rec := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"replayed":false,"effects":[],"warnings":[]}`)
	}))
	c.opts.Clock = pendingClock()
	for _, age := range []time.Duration{7*24*time.Hour - time.Hour, 7 * 24 * time.Hour} {
		p := pendingRequest(c.opts.Clock.Now().Add(-age))
		if out, err := c.Resend(t.Context(), p); out != nil || !errors.Is(err, ErrPendingExpired) {
			t.Fatalf("age %v: Resend() = %+v, %v; want ErrPendingExpired", age, out, err)
		}
	}
	if len(rec.at("/api/v1/tasks")) != 0 {
		t.Fatal("a resend at the safety boundary was sent")
	}
	p := pendingRequest(c.opts.Clock.Now().Add(-(7*24*time.Hour - time.Hour - time.Millisecond)))
	if out, err := c.Resend(t.Context(), p); err != nil || out == nil || out.Op == nil || out.Op.Replayed {
		t.Fatalf("just inside the window: Resend() = %+v, %v", out, err)
	}
	if len(rec.at("/api/v1/tasks")) != 1 {
		t.Fatal("a resend inside the window was not sent")
	}
}

func TestResendGenerationChanged(t *testing.T) {
	c, rec := newFake(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unexpected mutation") }))
	c.opts.Clock = pendingClock()
	p := pendingRequest(c.opts.Clock.Now())
	p.Generation, p.Actor = "aaaaaaaaaaaaaaaa", "token:ai"
	if out, err := c.Resend(t.Context(), p); out != nil || !errors.Is(err, ErrGenerationMismatch) {
		t.Fatalf("Resend() = %+v, %v", out, err)
	}
	if len(rec.at("/api/v1/whoami")) != 1 || len(rec.at("/api/v1/tasks")) != 0 {
		t.Fatal("generation mismatch must stop after whoami")
	}
}

func TestResendActorMismatch(t *testing.T) {
	c, rec := newFake(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unexpected mutation") }))
	c.opts.Clock = pendingClock()
	p := pendingRequest(c.opts.Clock.Now())
	p.Actor = "token:ai"
	if out, err := c.Resend(t.Context(), p); out != nil || !errors.Is(err, ErrActorMismatch) {
		t.Fatalf("Resend() = %+v, %v", out, err)
	}
	if len(rec.at("/api/v1/whoami")) != 1 || len(rec.at("/api/v1/tasks")) != 0 {
		t.Fatal("actor mismatch must stop after whoami")
	}
}

func TestResendIdentityFailure(t *testing.T) {
	c, rec := newFake(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unexpected mutation") }))
	c.opts.Clock = pendingClock()
	rec.whoami = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, http.StatusUnauthorized, domain.CodeUnauthenticated)
	})
	if out, err := c.Resend(t.Context(), pendingRequest(c.opts.Clock.Now())); out != nil || !IsCode(err, domain.CodeUnauthenticated) {
		t.Fatalf("Resend() = %+v, %v", out, err)
	}
	if len(rec.at("/api/v1/tasks")) != 0 {
		t.Fatal("failed identity lookup sent a mutation")
	}
}

func TestResendIdentityTransportFailure(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		busy, cancelBefore, cancelWait bool
		attempts                       int
		cause                          error
	}{
		{name: "dropped responses", attempts: 3, cause: io.EOF},
		{name: "cancelled context", cancelBefore: true, cause: context.Canceled},
		{name: "cancelled backoff", cancelWait: true, attempts: 1, cause: context.Canceled},
		{name: "dropped then busy", busy: true, attempts: 3},
		{name: "dropped then busy cancelled", busy: true, cancelWait: true, attempts: 2, cause: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := newFake(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unexpected mutation") }))
			rec.mu.Lock()
			rec.whoami = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if !tc.busy || len(rec.at("/api/v1/whoami")) == 1 {
					dropResponse(t, w)
					return
				}
				w.Header().Set("Retry-After", "1")
				writeProblem(w, http.StatusServiceUnavailable, domain.CodeBusy)
			})
			rec.mu.Unlock()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancelBefore {
				cancel()
			}
			if tc.cancelWait {
				rec.clock.onAfter = func(d time.Duration) {
					if !tc.busy || d == time.Second {
						cancel()
					}
				}
			}
			s := pendingFor(t, c)
			p := pendingRequest(c.opts.Clock.Now())
			if err := s.Save(p); err != nil {
				t.Fatal(err)
			}
			before := pendingBytes(t, s, p.Key)
			out, err := c.Resend(ctx, p)
			var unknown *OutcomeUnknownError
			if out != nil || err == nil || errors.Is(err, ErrOutcomeUnknown) || errors.As(err, &unknown) {
				t.Errorf("unattempted resend = %+v, %#v", out, err)
			}
			if tc.busy {
				var problem *ProblemError
				if !errors.As(err, &problem) || problem.Status != http.StatusServiceUnavailable ||
					!IsCode(err, domain.CodeBusy) || errors.Is(err, ErrNotSent) {
					t.Errorf("identity problem lost: %v", err)
				}
			} else if !errors.Is(err, ErrNotSent) {
				t.Errorf("identity transport failure = %v; want ErrNotSent", err)
			}
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Errorf("identity failure = %v; want cause %v", err, tc.cause)
			}
			if got := len(rec.at("/api/v1/whoami")); got != tc.attempts {
				t.Errorf("identity attempts = %d, want %d", got, tc.attempts)
			}
			if len(rec.at("/api/v1/tasks")) != 0 {
				t.Error("mutation sent despite failed identity lookup")
			}
			if !bytes.Equal(before, pendingBytes(t, s, p.Key)) {
				t.Error("identity failure changed the saved request")
			}
			if got, err := s.List(); err != nil || len(got) != 1 {
				t.Fatalf("pending files = %+v, %v", got, err)
			}
		})
	}
}

func TestResendTokenRevocation(t *testing.T) {
	c, rec := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":42,"name":"ai","scopes":"read,capture","created_at":"2026-10-27T16:00:00Z","revoked_at":"2026-10-28T16:00:00Z"}`)
	}))
	c.opts.Clock = pendingClock()
	p := pendingRequest(c.opts.Clock.Now())
	p.Path, p.IfMatch, p.Body = "/tokens/42/revoke", "", json.RawMessage(`{}`)
	out, err := c.Resend(t.Context(), p)
	if err != nil || out == nil || out.Token == nil || out.Op != nil {
		t.Fatalf("Resend() = %+v, %v", out, err)
	}
	if out.Token.ID != 42 || out.Token.Name != "ai" || out.Token.RevokedAt == nil {
		t.Fatalf("revocation metadata = %+v", out.Token)
	}
	requests := rec.at("/api/v1" + p.Path)
	if len(requests) != 1 {
		t.Fatalf("revocation count = %d", len(requests))
	}
	assertSavedRequest(t, requests[0], p)
}

func TestResendFailureKeepsPending(t *testing.T) {
	for _, mode := range []string{"transport", "conflict", "generation"} {
		t.Run(mode, func(t *testing.T) {
			c, rec := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				switch mode {
				case "transport":
					dropResponse(t, w)
				case "conflict":
					writeProblem(w, http.StatusConflict, domain.CodeConflict)
				case "generation":
					writeProblem(w, http.StatusPreconditionFailed, domain.CodeGenerationMismatch)
				}
			}))
			// The existing fake's immediate retry clock exercises both transport retries.
			// Save an hour earlier so overwriting the creation time is observable.
			s := pendingFor(t, c)
			s.Clock = clock.NewFake(c.opts.Clock.Now().Add(-time.Hour))
			p := pendingRequest(s.Clock.Now())
			p.LastError, p.LastAttemptAt = "conflict: earlier", p.CreatedAt.Add(time.Minute)
			if err := s.Save(p); err != nil {
				t.Fatal(err)
			}
			before := pendingBytes(t, s, p.Key)
			// If Resend accidentally saves, use current time and expose the reset.
			s.Clock = c.opts.Clock
			out, err := c.Resend(t.Context(), p)
			if out != nil || err == nil {
				t.Fatalf("Resend() = %+v, %v", out, err)
			}
			wantAttempts := 1
			switch mode {
			case "transport":
				var unknown *OutcomeUnknownError
				if !errors.As(err, &unknown) || !errors.Is(err, ErrOutcomeUnknown) || unknown.Key != p.Key || unknown.Saved {
					t.Fatalf("unknown outcome = %#v", err)
				}
				wantAttempts = 3
			case "conflict":
				if !IsCode(err, domain.CodeConflict) {
					t.Fatal(err)
				}
			case "generation":
				if !errors.Is(err, ErrGenerationMismatch) || len(rec.at("/api/v1/whoami")) != 2 {
					t.Fatalf("generation failure did not refresh identity: %v", err)
				}
			}
			requests := rec.at("/api/v1" + p.Path)
			if len(requests) != wantAttempts {
				t.Fatalf("attempt count = %d, want %d", len(requests), wantAttempts)
			}
			for _, req := range requests {
				assertSavedRequest(t, req, p)
			}
			if !bytes.Equal(before, pendingBytes(t, s, p.Key)) {
				t.Fatal("failed resend changed the saved request")
			}
			if got, err := s.List(); err != nil || len(got) != 1 {
				t.Fatalf("pending files = %+v, %v", got, err)
			}
			if mode == "conflict" {
				var problem *ProblemError
				if !errors.As(err, &problem) {
					t.Fatal(err)
				}
				if err := s.MarkFailed(p.Key, problem.Problem.Code+": "+problem.Problem.Detail); err != nil {
					t.Fatal(err)
				}
				got, err := s.List()
				if err != nil || len(got) != 1 || got[0].LastError != "conflict: request refused" {
					t.Fatalf("caller MarkFailed: %+v, %v", got, err)
				}
			}
		})
	}
}

// delayedPendingWriteConn exposes the interval after bytes reach the socket but
// before countingConn.Write has recorded them.
type delayedPendingWriteConn struct {
	net.Conn
	release <-chan struct{}
}

func (c *delayedPendingWriteConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	<-c.release
	return n, err
}

func TestResendResponseProvesTransmission(t *testing.T) {
	c, rec := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = io.WriteString(w, `{"replayed":`)
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err)
		}
		dropResponse(t, w)
	}))
	if _, err := c.Generation(t.Context()); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	defer close(release)
	tr := c.http.Transport.(*http.Transport)
	dial := tr.DialContext
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		counted := conn.(*countingConn)
		counted.Conn = &delayedPendingWriteConn{Conn: counted.Conn, release: release}
		return counted, nil
	}
	s := pendingFor(t, c)
	p := pendingRequest(c.opts.Clock.Now())
	if out, err := c.Resend(t.Context(), p); out != nil || !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, ErrNotSent) {
		t.Fatalf("received a truncated HTTP response, but Resend() = %+v, %v", out, err)
	}
	if len(rec.at("/api/v1/tasks")) != 3 {
		t.Fatal("expected the original request and two transport retries")
	}
	noPending(t, s)
}

func TestMarkFailedKeepsBody(t *testing.T) {
	clk := pendingClock()
	s := &PendingStore{Dir: filepath.Join(t.TempDir(), "pending"), Clock: clk}
	if err := os.Mkdir(s.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := pendingRequest(clk.Now())
	p.Key = "k"
	// Raw disk fixture: Save requires protocol-length keys, but the brief uses
	// "k" to exercise maintenance of an existing file independently of Save.
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "k.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Minute)
	if err := s.MarkFailed("k", "conflict: x"); err != nil {
		t.Fatal(err)
	}
	p.LastError, p.LastAttemptAt = "conflict: x", clk.Now()
	got, err := s.List()
	if err != nil || !reflect.DeepEqual(got, []PendingRequest{p}) {
		t.Fatalf("List() = %+v, %v; want %+v", got, err, p)
	}
	info, err := os.Stat(filepath.Join(s.Dir, "k.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("marked file mode: %v, %v", info, err)
	}
	if err := s.MarkFailed("missing", "conflict: x"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing MarkFailed error = %v", err)
	}
}

func TestDiscard(t *testing.T) {
	s := &PendingStore{Dir: filepath.Join(t.TempDir(), "pending"), Clock: pendingClock()}
	p := pendingRequest(s.Clock.Now())
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := s.Discard(p.Key); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, p.Key+".json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("discarded file stat = %v", err)
	}
	if got, err := s.List(); err != nil || len(got) != 0 {
		t.Fatalf("discarded List() = %+v, %v", got, err)
	}
	if err := s.Discard(p.Key); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing Discard error = %v", err)
	}
}

func TestPendingMaintenanceRejectsUnsafeKey(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside.json")
	if err := os.WriteFile(outside, []byte(`{"key":"outside"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &PendingStore{Dir: filepath.Join(dir, "pending"), Clock: pendingClock()}
	if err := os.Mkdir(s.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"../outside", outside, "", ".", ".."} {
		if err := s.MarkFailed(key, "conflict: x"); err == nil {
			t.Errorf("MarkFailed accepted key %q", key)
		}
		if err := s.Discard(key); err == nil {
			t.Errorf("Discard accepted key %q", key)
		}
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != `{"key":"outside"}` {
		t.Fatalf("outside file modified: %s, %v", got, err)
	}
}

func pendingBytes(t *testing.T, s *PendingStore, key string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(s.Dir, key+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertSavedRequest(t *testing.T, got recordedRequest, p PendingRequest) {
	t.Helper()
	if got.Method != p.Method || got.Path != "/api/v1"+p.Path || !bytes.Equal(got.Body, p.Body) ||
		got.Header.Get(api.HeaderIdempotencyKey) != p.Key || got.Header.Get("If-Match") != p.IfMatch ||
		got.Header.Get(api.HeaderGeneration) != p.Generation {
		t.Errorf("resend did not preserve method, path, body, key, If-Match and generation: method=%s path=%s body=%s key=%s If-Match=%s generation=%s",
			got.Method, got.Path, got.Body, got.Header.Get(api.HeaderIdempotencyKey), got.Header.Get("If-Match"), got.Header.Get(api.HeaderGeneration))
	}
}
