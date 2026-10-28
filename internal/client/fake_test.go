package client

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"taskronin/internal/api"
	"taskronin/internal/domain"
)

const testGeneration = "0123456789abcdef"

type recordedRequest struct {
	Method, Path, Query string
	Header              http.Header
	Body                []byte
}

type recorder struct {
	mu        sync.Mutex
	requests  []recordedRequest
	identity  api.WhoAmI
	listener  net.Listener
	tokenFile string
	token     string
	clock     *retryClock
}

// retryClock makes backoffs immediate but records the requested delays.
type retryClock struct {
	mu     sync.Mutex
	delays []time.Duration
}

func (*retryClock) Now() time.Time { return time.Date(2026, 10, 28, 16, 0, 0, 0, time.UTC) }
func (c *retryClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	c.delays = append(c.delays, d)
	c.mu.Unlock()
	ch := make(chan time.Time, 1)
	ch <- c.Now().Add(d)
	return ch
}

func newFake(t *testing.T, h http.Handler) (*Client, *recorder) {
	t.Helper()
	// Keep unix paths short even for long test/subtest names.
	dir, err := os.MkdirTemp("", "tkr-client-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "sock")
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	r := &recorder{
		listener: ln, tokenFile: filepath.Join(dir, "token"), token: newToken(), clock: &retryClock{},
		identity: api.WhoAmI{Actor: "owner", Scopes: "read,capture,write,destroy,admin", Owner: true, Generation: testGeneration},
	}
	if err := os.WriteFile(r.tokenFile, []byte(r.token), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		r.mu.Lock()
		r.requests = append(r.requests, recordedRequest{Method: req.Method, Path: req.URL.Path, Query: req.URL.RawQuery, Header: req.Header.Clone(), Body: body})
		identity := r.identity
		r.mu.Unlock()
		if req.URL.Path == "/api/v1/whoami" {
			w.Header().Set("Content-Type", api.ContentTypeJSON)
			_ = json.NewEncoder(w).Encode(identity)
			return
		}
		h.ServeHTTP(w, req)
	})}
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close(); <-done })
	c, err := New(Options{Socket: socket, TokenFile: r.tokenFile, Channel: "cli", Clock: r.clock})
	if err != nil {
		t.Fatal(err)
	}
	return c, r
}

// BF30: valid credentials are generated at runtime, including the CRLF fixture.
func newToken() string {
	var secret [32]byte
	_, _ = rand.Read(secret[:])
	return "tkr_1_" + base64.RawURLEncoding.EncodeToString(secret[:])
}

func (r *recorder) at(path string) []recordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	var found []recordedRequest
	for _, req := range r.requests {
		if req.Path == path {
			found = append(found, req)
		}
	}
	return found
}

func dropResponse(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		t.Errorf("hijack: %v", err)
		return
	}
	_ = conn.Close()
}

func writeProblem(w http.ResponseWriter, status int, code domain.Code) {
	w.Header().Set("Content-Type", api.ContentTypeProblem)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(api.Problem{Status: status, Code: string(code), Detail: "request refused", Errors: []api.FieldError{{Field: "title", Message: "required"}}})
}

func mutation() call {
	return call{Kind: callKeyed, Method: http.MethodPost, Path: "/tasks", Body: map[string]string{"title": "Remember this"}, Mut: Mut{IfMatch: `"0123456789abcdef-1"`}}
}

func pendingFor(t *testing.T, c *Client) *PendingStore {
	t.Helper()
	p := &PendingStore{Dir: filepath.Join(t.TempDir(), "pending"), Clock: c.opts.Clock}
	c.opts.Pending = p
	return p
}

func noPending(t *testing.T, p *PendingStore) {
	t.Helper()
	entries, err := os.ReadDir(p.Dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("unexpected pending files: %v", entries)
	}
}

func doCall(ctx context.Context, c *Client, k call, out any) error {
	resp, err := c.do(ctx, k, out)
	if resp != nil {
		_ = resp.Body.Close()
	}
	return err
}
