package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/domain"
)

type observingWriter struct {
	buf     bytes.Buffer
	written chan struct{}
}

func (w *observingWriter) Write(p []byte) (int, error) {
	n, e := w.buf.Write(p)
	select {
	case w.written <- struct{}{}:
	default:
	}
	return n, e
}

type failingExportWriter struct{ err error }

func (w failingExportWriter) Write([]byte) (int, error) { return 0, w.err }

func TestExportStreamsIncrementally(t *testing.T) {
	out := &observingWriter{written: make(chan struct{}, 1)}
	c, _ := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/export" || r.Header.Get(api.HeaderIdempotencyKey) != "" {
			t.Error("incorrect export request")
		}
		time.Sleep(30 * time.Millisecond)
		_, _ = io.WriteString(w, `{"format":`)
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err)
			return
		}
		select {
		case <-out.written:
		case <-time.After(slowRunnerWait):
			t.Error("client buffered export instead of streaming")
			return
		}
		_, _ = io.WriteString(w, `"taskronin-export"}`)
	}))
	c.opts.Timeout = time.Millisecond
	if err := c.Export(t.Context(), out); err != nil {
		t.Fatal(err)
	}
	if out.buf.String() != `{"format":"taskronin-export"}` {
		t.Fatal("incorrect streamed document")
	}
}

func TestExportFailureNeverAppendsRetry(t *testing.T) {
	for _, mode := range []string{"truncated", "writer"} {
		t.Run(mode, func(t *testing.T) {
			sentinel := errors.New("destination full")
			c, rec := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Length", "100000")
				_, _ = io.WriteString(w, strings.Repeat("x", 8192))
				_ = http.NewResponseController(w).Flush()
				dropResponse(t, w)
			}))
			pending := pendingFor(t, c)
			var out io.Writer = new(bytes.Buffer)
			cause := io.ErrUnexpectedEOF
			if mode == "writer" {
				out = failingExportWriter{err: sentinel}
				cause = sentinel
			}
			err := c.Export(t.Context(), out)
			if !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, ErrNotSent) {
				t.Errorf("export error=%v; want outcome unknown", err)
			}
			if !errors.Is(err, cause) {
				t.Errorf("export error=%v; want preserved cause %v", err, cause)
			}
			var unknown *OutcomeUnknownError
			if !errors.As(err, &unknown) || unknown.Saved || unknown.Key != "" {
				t.Errorf("export error=%v; want unsaved outcome unknown with no key", err)
			}
			if got := len(rec.at("/api/v1/export")); got != 1 {
				t.Fatalf("stream attempts=%d", got)
			}
			noPending(t, pending)
		})
	}
}

func TestExportProblem(t *testing.T) {
	c, _ := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { writeProblem(w, 403, domain.CodeForbidden) }))
	var out bytes.Buffer
	if err := c.Export(t.Context(), &out); !IsCode(err, domain.CodeForbidden) || out.Len() != 0 {
		t.Fatalf("problem streamed as document: %v", err)
	}
}

func TestAdminExemptNeverPending(t *testing.T) {
	for _, tc := range []struct {
		path   string
		invoke func(context.Context, *Client) error
	}{
		{"/settings", func(ctx context.Context, c *Client) error {
			_, e := c.UpdateSettings(ctx, api.SettingsRequest{Password: newToken()})
			return e
		}},
		{"/tokens", func(ctx context.Context, c *Client) error {
			_, e := c.CreateToken(ctx, api.CreateTokenRequest{Name: "ai", Scopes: "read", ExpiresAt: api.Opt[api.Time]{Set: true, Null: true}})
			return e
		}},
		{"/password", func(ctx context.Context, c *Client) error {
			return c.ChangePassword(ctx, api.PasswordRequest{Current: newToken(), New: newToken()})
		}},
		{"/backups", func(ctx context.Context, c *Client) error { _, e := c.RunBackup(ctx); return e }},
	} {
		t.Run(tc.path, func(t *testing.T) {
			c, rec := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { dropResponse(t, w) }))
			pending := pendingFor(t, c)
			if err := tc.invoke(t.Context(), c); !errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("error=%v", err)
			}
			requests := rec.at("/api/v1" + tc.path)
			if len(requests) != 1 {
				t.Fatalf("exempt attempts=%d", len(requests))
			}
			if requests[0].Header.Get(api.HeaderIdempotencyKey) != "" || requests[0].Header.Get(api.HeaderGeneration) != testGeneration {
				t.Fatal("incorrect exempt headers")
			}
			noPending(t, pending)
		})
	}
}

func TestRunBackupUsesLongBudget(t *testing.T) {
	c, _ := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(30 * time.Millisecond)
		_, _ = io.WriteString(w, `{"kind":"manual"}`)
	}))
	if _, err := c.Generation(t.Context()); err != nil {
		t.Fatal(err)
	}
	c.opts.Timeout = time.Millisecond
	snapshot, err := c.RunBackup(t.Context())
	if err != nil || snapshot.Kind != "manual" {
		t.Fatalf("backup=%+v %v", snapshot, err)
	}
}

func TestRevokeTokenUsesKeyedPolicy(t *testing.T) {
	c, rec := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { dropResponse(t, w) }))
	_ = pendingFor(t, c)
	key := api.NewIdempotencyKey()
	_, err := c.RevokeToken(t.Context(), Mut{Key: key}, 42)
	var unknown *OutcomeUnknownError
	if !errors.As(err, &unknown) || !unknown.Saved {
		t.Fatalf("revocation did not preserve uncertain outcome: %v", err)
	}
	requests := rec.at("/api/v1/tokens/42/revoke")
	if len(requests) != 3 {
		t.Fatalf("keyed attempts=%d", len(requests))
	}
	for _, r := range requests {
		if r.Header.Get(api.HeaderIdempotencyKey) != key || r.Header.Get(api.HeaderGeneration) != testGeneration || string(r.Body) != "{}" {
			t.Fatal("incorrect keyed request")
		}
	}
}
