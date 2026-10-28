package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/clock"
	"taskronin/internal/config"
	"taskronin/internal/domain"
)

func TestTokenFileChecks(t *testing.T) {
	for _, kind := range []string{"missing", "mode", "symlink", "directory", "empty", "prefix", "newline"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "token")
			if kind != "missing" {
				if err := os.WriteFile(path, []byte(newToken()), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "mode":
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				link := path + ".link"
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			case "directory":
				path = filepath.Dir(path)
			case "empty", "prefix", "newline":
				content := map[string]string{"empty": "", "prefix": "invalid", "newline": newToken() + "\nInjected: header"}[kind]
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := New(Options{TokenFile: path})
			if !errors.Is(err, ErrTokenFile) || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "run `tkr init`") || !strings.Contains(err.Error(), "--token-file") {
				t.Fatalf("token error = %v", err)
			}
		})
	}
	c, rec := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	if err := os.WriteFile(rec.tokenFile, []byte(rec.token+" \r\n\n "), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := New(c.opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Health(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := rec.at("/healthz")[0].Header.Get("Authorization"); got != "Bearer "+rec.token || strings.Contains(got, "\r") {
		t.Fatal("Authorization did not contain exactly the trimmed credential")
	}
}

func TestMutationHeaders(t *testing.T) {
	c, rec := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) }))
	for range 2 {
		if err := doCall(t.Context(), c, mutation(), nil); err != nil {
			t.Fatal(err)
		}
	}
	k := call{Kind: callRead, Method: http.MethodGet, Path: "/tasks", Query: url.Values{"tag": {"a b&c"}}, IfNoneMatch: `"aggregate"`}
	if err := doCall(t.Context(), c, k, nil); err != nil {
		t.Fatal(err)
	}
	posts := rec.at("/api/v1/tasks")
	for _, req := range posts[:2] {
		if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(req.Header.Get(api.HeaderIdempotencyKey)) {
			t.Fatal("invalid generated key")
		}
		for name, want := range map[string]string{api.HeaderGeneration: testGeneration, "Content-Type": api.ContentTypeJSON, api.HeaderChannel: "cli", "If-Match": mutation().Mut.IfMatch, "Authorization": "Bearer " + rec.token} {
			if req.Header.Get(name) != want {
				t.Errorf("incorrect %s header", name)
			}
		}
	}
	if posts[0].Header.Get(api.HeaderIdempotencyKey) == posts[1].Header.Get(api.HeaderIdempotencyKey) {
		t.Fatal("distinct calls reused a key")
	}
	get := posts[2]
	if get.Header.Get(api.HeaderIdempotencyKey) != "" || get.Header.Get(api.HeaderGeneration) != "" || get.Header.Get("If-None-Match") != k.IfNoneMatch || get.Query != k.Query.Encode() {
		t.Fatal("incorrect GET headers or query")
	}
	actor, err := c.Actor(t.Context())
	if err != nil || actor != "owner" || len(rec.at("/api/v1/whoami")) != 1 {
		t.Fatalf("actor = %q, err = %v; identity fetches = %d", actor, err, len(rec.at("/api/v1/whoami")))
	}
}

func TestRetrySameKey(t *testing.T) {
	var attempts int
	c, rec := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < 3 {
			dropResponse(t, w)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"replayed":true}`))
	}))
	k := mutation()
	k.Mut.Key = "explicit_key_123456"
	var out api.OpResponse
	if err := doCall(t.Context(), c, k, &out); err != nil {
		t.Fatal(err)
	}
	requests := rec.at("/api/v1/tasks")
	if len(requests) != 3 || !out.Replayed {
		t.Fatalf("attempts = %d, response = %+v", len(requests), out)
	}
	for _, r := range requests {
		if r.Header.Get(api.HeaderIdempotencyKey) != k.Mut.Key || string(r.Body) != string(requests[0].Body) {
			t.Fatal("retry changed key/body")
		}
	}
	if !reflect.DeepEqual(rec.clock.delays, []time.Duration{100 * time.Millisecond, 400 * time.Millisecond}) {
		t.Fatalf("delays = %v", rec.clock.delays)
	}
}

func TestNotSent(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprint(cached), func(t *testing.T) {
			c, rec := newFake(t, http.NotFoundHandler())
			p := pendingFor(t, c)
			if cached {
				if _, err := c.Generation(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			_ = rec.listener.Close()
			err := doCall(t.Context(), c, mutation(), nil)
			if !errors.Is(err, ErrNotSent) || errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("error = %v", err)
			}
			noPending(t, p)
		})
	}
}

func TestTransmissionSticky(t *testing.T) {
	var rec *recorder
	c, r := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = rec.listener.Close()
		dropResponse(t, w)
	}))
	rec = r
	pendingFor(t, c)
	err := doCall(t.Context(), c, mutation(), nil)
	var unknown *OutcomeUnknownError
	if !errors.As(err, &unknown) || !unknown.Saved || errors.Is(err, ErrNotSent) {
		t.Fatalf("error = %#v", err)
	}
	if len(rec.at("/api/v1/tasks")) != 1 || len(rec.clock.delays) != 2 {
		t.Fatal("expected one transmitted request and two dial retries")
	}
}

func TestOutcomeUnknownSaved(t *testing.T) {
	c, rec := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { dropResponse(t, w) }))
	p := pendingFor(t, c)
	err := doCall(t.Context(), c, mutation(), nil)
	var unknown *OutcomeUnknownError
	if !errors.As(err, &unknown) || !unknown.Saved || !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("error = %#v", err)
	}
	path := filepath.Join(p.Dir, unknown.Key+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "tkr_") {
		t.Fatal("credential leaked into pending file")
	}
	var got PendingRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Key != unknown.Key || got.Method != http.MethodPost || got.Path != "/tasks" || got.IfMatch != mutation().Mut.IfMatch || got.Generation != testGeneration || got.Actor != "owner" || !got.CreatedAt.Equal(rec.clock.Now()) || !got.LastAttemptAt.IsZero() || got.LastError != "" || string(got.Body) != string(rec.at("/api/v1/tasks")[0].Body) {
		t.Fatalf("pending = %+v", got)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"key", "method", "path", "if_match", "generation", "actor", "body", "created_at", "last_error", "last_attempt_at"} {
		if _, ok := fields[field]; !ok {
			t.Errorf("missing JSON field %s", field)
		}
	}
	for name, mode := range map[string]os.FileMode{p.Dir: 0o700, path: 0o600} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Errorf("%s mode = %o", name, info.Mode().Perm())
		}
	}
}

func TestExemptNeverPending(t *testing.T) {
	for _, endpoint := range []struct{ method, path string }{{http.MethodPatch, "/settings"}, {http.MethodPost, "/tokens"}, {http.MethodPost, "/password"}, {http.MethodPost, "/backups"}} {
		t.Run(endpoint.path, func(t *testing.T) {
			c, rec := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { dropResponse(t, w) }))
			p := pendingFor(t, c)
			err := doCall(t.Context(), c, call{Kind: callExempt, Method: endpoint.method, Path: endpoint.path, Body: map[string]string{"password": newToken()}}, nil)
			var unknown *OutcomeUnknownError
			if !errors.As(err, &unknown) || unknown.Saved || unknown.Key != "" {
				t.Fatalf("error = %#v", err)
			}
			requests := rec.at("/api/v1" + endpoint.path)
			if len(requests) != 1 || requests[0].Header.Get(api.HeaderIdempotencyKey) != "" || requests[0].Header.Get(api.HeaderGeneration) != testGeneration || requests[0].Header.Get("Content-Type") != api.ContentTypeJSON {
				t.Fatal("incorrect exempt attempt count or headers")
			}
			noPending(t, p)
		})
	}
}

func TestPendingWriteFailure(t *testing.T) {
	c, _ := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { dropResponse(t, w) }))
	p := pendingFor(t, c)
	if err := os.Mkdir(p.Dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p.Dir, 0o700) })
	err := doCall(t.Context(), c, mutation(), nil)
	var unknown *OutcomeUnknownError
	if !errors.As(err, &unknown) || unknown.Saved || !errors.Is(err, ErrOutcomeUnknown) || !strings.Contains(unknown.Err.Error(), "write pending") {
		t.Fatalf("error = %#v", err)
	}
	noPending(t, p)
}

func TestBusyRetry(t *testing.T) {
	for _, kind := range []callKind{callRead, callKeyed, callExempt} {
		for _, seconds := range []string{"1", "5"} {
			t.Run(fmt.Sprintf("%d/%s", kind, seconds), func(t *testing.T) {
				var attempts int
				c, rec := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					attempts++
					if attempts == 1 {
						w.Header().Set("Retry-After", seconds)
						writeProblem(w, http.StatusServiceUnavailable, domain.CodeBusy)
						return
					}
					w.WriteHeader(http.StatusCreated)
				}))
				k := mutation()
				k.Kind = kind
				if kind == callRead {
					k.Method = http.MethodGet
				}
				err := doCall(t.Context(), c, k, nil)
				if seconds == "1" {
					if err != nil || len(rec.at("/api/v1/tasks")) != 2 || !reflect.DeepEqual(rec.clock.delays, []time.Duration{time.Second}) {
						t.Fatalf("err = %v, delays = %v", err, rec.clock.delays)
					}
				} else if !IsCode(err, domain.CodeBusy) || len(rec.at("/api/v1/tasks")) != 1 || len(rec.clock.delays) != 0 {
					t.Fatalf("busy error = %v", err)
				}
			})
		}
	}
}

func TestNo4xxRetry(t *testing.T) {
	c, rec := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, http.StatusUnprocessableEntity, domain.CodeValidation)
	}))
	p := pendingFor(t, c)
	err := doCall(t.Context(), c, mutation(), nil)
	var problem *ProblemError
	if !errors.As(err, &problem) || !IsCode(fmt.Errorf("wrapped: %w", err), domain.CodeValidation) || problem.Status != http.StatusUnprocessableEntity || len(problem.Problem.Errors) != 1 || problem.Problem.Errors[0].Field != "title" || len(rec.at("/api/v1/tasks")) != 1 {
		t.Fatalf("problem = %#v, err = %v", problem, err)
	}
	noPending(t, p)
}

func TestGenerationMismatchRefreshes(t *testing.T) {
	var rec *recorder
	c, r := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get(api.HeaderGeneration) == testGeneration {
			rec.mu.Lock()
			rec.identity.Generation = "fedcba9876543210"
			rec.mu.Unlock()
			writeProblem(w, http.StatusPreconditionFailed, domain.CodeGenerationMismatch)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	rec = r
	err := doCall(t.Context(), c, mutation(), nil)
	var problem *ProblemError
	if !errors.As(err, &problem) || !errors.Is(err, ErrGenerationMismatch) || len(rec.at("/api/v1/tasks")) != 1 {
		t.Fatalf("error = %v", err)
	}
	if err := doCall(t.Context(), c, mutation(), nil); err != nil {
		t.Fatal(err)
	}
	if len(rec.at("/api/v1/whoami")) != 2 || rec.at("/api/v1/tasks")[1].Header.Get(api.HeaderGeneration) != "fedcba9876543210" {
		t.Fatal("generation was not refreshed")
	}
}

func TestTimeout(t *testing.T) {
	c, rec := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		select {
		case <-req.Context().Done():
		case <-time.After(time.Second):
			w.WriteHeader(http.StatusOK)
		}
	}))
	c.opts.Timeout = 200 * time.Millisecond
	start := (clock.Real{}).Now()
	err := c.Health(t.Context())
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second || len(rec.at("/healthz")) != 1 {
		t.Fatalf("timeout = %v, elapsed = %v", err, time.Since(start))
	}
}

func TestReadNeverPendingAndNoRetryPolicy(t *testing.T) {
	for _, noRetry := range []bool{false, true} {
		t.Run(fmt.Sprint(noRetry), func(t *testing.T) {
			c, rec := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { dropResponse(t, w) }))
			p := pendingFor(t, c)
			err := doCall(t.Context(), c, call{Kind: callRead, Method: http.MethodGet, Path: "/status", NoRetry: noRetry}, nil)
			want := 3
			if noRetry {
				want = 1
			}
			if !errors.Is(err, ErrOutcomeUnknown) || len(rec.at("/api/v1/status")) != want {
				t.Fatalf("err = %v, attempts = %d", err, len(rec.at("/api/v1/status")))
			}
			noPending(t, p)
		})
	}
}

func TestViewsAndConfig(t *testing.T) {
	c, rec := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/api/v1/clock" {
			if err := json.NewEncoder(w).Encode(api.Clock{Today: civil.MustDate("2026-10-28"), Generation: testGeneration, Timezone: "America/Denver"}); err != nil {
				t.Errorf("encode clock: %v", err)
			}
			return
		}
		if req.URL.Path != "/healthz" {
			t.Errorf("unexpected path: %s", req.URL.Path)
		}
		_, _ = w.Write([]byte("ok"))
	}))
	c, err := NewFromConfig(&config.Client{Socket: c.opts.Socket, TokenFile: rec.tokenFile, Timeout: time.Second}, "tui", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Health(t.Context()); err != nil {
		t.Fatal(err)
	}
	view, err := c.Clock(t.Context())
	if err != nil || view.Generation != testGeneration || view.Timezone != "America/Denver" {
		t.Fatalf("clock = %+v, err = %v", view, err)
	}
	who, err := c.WhoAmI(t.Context())
	if err != nil || who.Actor != "owner" || !who.Owner {
		t.Fatalf("whoami = %+v, err = %v", who, err)
	}
	if rec.at("/healthz")[0].Header.Get(api.HeaderChannel) != "tui" || c.opts.Timeout != time.Second {
		t.Fatal("config options not propagated")
	}
}

func TestConcurrentIdentityCache(t *testing.T) {
	c, rec := newFake(t, http.NotFoundHandler())
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			g, err := c.Generation(t.Context())
			if err != nil || g != testGeneration {
				t.Errorf("generation = %q, error = %v", g, err)
			}
		})
	}
	wg.Wait()
	if len(rec.at("/api/v1/whoami")) != 1 {
		t.Fatal("concurrent lazy fetches duplicated")
	}
}

func TestTruncatedResponseRetries(t *testing.T) {
	c, rec := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte(`{"replayed":`))
	}))
	pendingFor(t, c)
	var out api.OpResponse
	err := doCall(t.Context(), c, mutation(), &out)
	var unknown *OutcomeUnknownError
	if !errors.As(err, &unknown) || !unknown.Saved || len(rec.at("/api/v1/tasks")) != 3 {
		t.Fatalf("truncated response error = %#v", err)
	}
}

func TestPendingSaveRejectsUnsafeKey(t *testing.T) {
	p := &PendingStore{Dir: filepath.Join(t.TempDir(), "pending")}
	if err := p.Save(PendingRequest{Key: "../outside"}); err == nil {
		t.Fatal("unsafe filename accepted")
	}
	noPending(t, p)
}

func TestBusyRetryLimitAndNoRetryPolicy(t *testing.T) {
	for _, noRetry := range []bool{false, true} {
		t.Run(fmt.Sprint(noRetry), func(t *testing.T) {
			c, rec := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "1")
				writeProblem(w, http.StatusServiceUnavailable, domain.CodeBusy)
			}))
			p := pendingFor(t, c)
			err := doCall(t.Context(), c, call{Kind: callRead, Method: http.MethodGet, Path: "/status", NoRetry: noRetry}, nil)
			want := 2
			if noRetry {
				want = 1
			}
			if !IsCode(err, domain.CodeBusy) || len(rec.at("/api/v1/status")) != want {
				t.Fatalf("error = %v, attempts = %d", err, len(rec.at("/api/v1/status")))
			}
			noPending(t, p)
		})
	}
}

func TestBudgetIncludesBackoff(t *testing.T) {
	c, rec := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { dropResponse(t, w) }))
	// A fake clock whose After never fires proves the real context budget also
	// bounds injected retry waits, without depending on wall-clock sleep lengths.
	c.opts.Clock = clock.NewFake(rec.clock.Now())
	pendingFor(t, c)
	k := mutation()
	k.Budget = 100 * time.Millisecond
	start := (clock.Real{}).Now()
	err := doCall(t.Context(), c, k, nil)
	var unknown *OutcomeUnknownError
	if !errors.As(err, &unknown) || !unknown.Saved || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second || len(rec.at("/api/v1/tasks")) != 1 {
		t.Fatalf("budget error = %#v, elapsed = %v", err, time.Since(start))
	}
}

func TestPendingWriteIOError(t *testing.T) {
	c, _ := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { dropResponse(t, w) }))
	p := pendingFor(t, c)
	if err := os.WriteFile(p.Dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := doCall(t.Context(), c, mutation(), nil)
	var unknown *OutcomeUnknownError
	var pathError *os.PathError
	if !errors.As(err, &unknown) || unknown.Saved || !errors.As(unknown.Err, &pathError) {
		t.Fatalf("write cause not preserved: %#v", err)
	}
}

func TestRefreshFailureInvalidatesCache(t *testing.T) {
	c, rec := newFake(t, http.NotFoundHandler())
	if _, err := c.Generation(t.Context()); err != nil {
		t.Fatal(err)
	}
	rec.mu.Lock()
	rec.identity.Generation = "fedcba9876543210"
	rec.mu.Unlock()
	// Hold the lookup gate as another caller would. A canceled refresh must
	// invalidate the old cache even though it cannot acquire this gate.
	c.identityMu <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := c.RefreshGeneration(ctx)
	<-c.identityMu
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("refresh error = %v", err)
	}
	generation, err := c.Generation(t.Context())
	if err != nil || generation != "fedcba9876543210" || len(rec.at("/api/v1/whoami")) != 2 {
		t.Fatalf("generation = %q, error = %v", generation, err)
	}
}

func TestConditionalResponse(t *testing.T) {
	c, _ := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"aggregate"`)
		w.WriteHeader(http.StatusNotModified)
	}))
	var out api.Status
	resp, err := c.do(t.Context(), call{Kind: callRead, Method: http.MethodGet, Path: "/status", IfNoneMatch: `"aggregate"`}, &out)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotModified || resp.Header.Get("ETag") != `"aggregate"` {
		t.Fatalf("unexpected conditional response: %v", resp)
	}
}

func TestBusyAfterDroppedResponse(t *testing.T) {
	for _, tc := range []struct {
		name, retryAfter               string
		cancelWait, expireWait, replay bool
		attempts                       int
	}{
		{name: "retry_limit", retryAfter: "1", attempts: 3},
		{name: "retry_after_too_long", retryAfter: "5", attempts: 2},
		{name: "invalid_retry_after", retryAfter: "invalid", attempts: 2},
		{name: "canceled_backoff", retryAfter: "1", cancelWait: true, attempts: 2},
		{name: "expired_backoff", retryAfter: "1", expireWait: true, attempts: 2},
		{name: "successful_replay", retryAfter: "1", replay: true, attempts: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts int
			c, rec := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempts++
				if attempts == 1 {
					dropResponse(t, w)
					return
				}
				if tc.replay && attempts == 3 {
					_, _ = w.Write([]byte(`{"replayed":true}`))
					return
				}
				w.Header().Set("Retry-After", tc.retryAfter)
				writeProblem(w, http.StatusServiceUnavailable, domain.CodeBusy)
			}))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancelWait {
				rec.clock.onAfter = func(d time.Duration) {
					if d == time.Second {
						cancel()
					}
				}
			}
			if tc.expireWait {
				c.opts.Clock = clock.Real{}
				c.opts.Timeout = 500 * time.Millisecond
			}
			p := pendingFor(t, c)
			k := mutation()
			var out api.OpResponse
			err := doCall(ctx, c, k, &out)
			requests := rec.at("/api/v1/tasks")
			if len(requests) != tc.attempts {
				t.Fatalf("attempts = %d, want %d", len(requests), tc.attempts)
			}
			key := requests[0].Header.Get(api.HeaderIdempotencyKey)
			for _, req := range requests {
				if req.Header.Get(api.HeaderIdempotencyKey) != key || string(req.Body) != string(requests[0].Body) {
					t.Fatal("retry changed the original key or body")
				}
			}
			if tc.replay {
				if err != nil || !out.Replayed {
					t.Fatalf("replay error = %v, replayed = %v", err, out.Replayed)
				}
				noPending(t, p)
				return
			}
			var unknown *OutcomeUnknownError
			if !errors.As(err, &unknown) || !unknown.Saved || unknown.Key != key || !errors.Is(err, ErrOutcomeUnknown) || !IsCode(err, domain.CodeBusy) || !errors.Is(err, io.EOF) {
				t.Fatalf("unresolved mutation error = %#v", err)
			}
			if tc.cancelWait && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation cause: %v", err)
			}
			if tc.expireWait && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("lost deadline cause: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(p.Dir, key+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var saved PendingRequest
			if err := json.Unmarshal(data, &saved); err != nil {
				t.Fatal(err)
			}
			if saved.Key != key || saved.Method != k.Method || saved.Path != k.Path || saved.IfMatch != k.Mut.IfMatch || saved.Generation != testGeneration || saved.Actor != "owner" || string(saved.Body) != string(requests[0].Body) {
				t.Fatalf("incorrect pending request: %+v", saved)
			}
		})
	}
}

func TestBusyWithoutDroppedResponse(t *testing.T) {
	for _, cancelWait := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelWait), func(t *testing.T) {
			c, rec := newFake(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "1")
				writeProblem(w, http.StatusServiceUnavailable, domain.CodeBusy)
			}))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if cancelWait {
				rec.clock.onAfter = func(time.Duration) { cancel() }
			}
			p := pendingFor(t, c)
			err := doCall(ctx, c, mutation(), nil)
			var problem *ProblemError
			if !errors.As(err, &problem) || !IsCode(err, domain.CodeBusy) || errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("definitive busy error = %#v", err)
			}
			wantAttempts := 2
			if cancelWait {
				wantAttempts = 1
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("lost cancellation cause: %v", err)
				}
			}
			if len(rec.at("/api/v1/tasks")) != wantAttempts {
				t.Fatalf("attempts = %d, want %d", len(rec.at("/api/v1/tasks")), wantAttempts)
			}
			noPending(t, p)
		})
	}
}
