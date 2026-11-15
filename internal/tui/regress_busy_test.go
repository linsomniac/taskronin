package tui

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"taskronin/internal/client"
	"taskronin/internal/config"
	"taskronin/internal/testutil"
)

func TestRegressBusyRetryCommitsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.sock")
	srv := testutil.StartServer(t)
	var mu sync.Mutex
	var keys []string
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", srv.Socket)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(&url.URL{Scheme: "http", Host: "localhost"})
		},
		ModifyResponse: func(r *http.Response) error {
			if r.Request.Method != http.MethodPost || r.Request.URL.Path != "/api/v1/tasks" {
				return nil
			}
			mu.Lock()
			defer mu.Unlock()
			keys = append(keys, r.Request.Header.Get("Idempotency-Key"))
			if len(keys) > 2 {
				return nil
			}
			// Simulate S1' after commit: both the original response and the
			// client's automatic same-key retry say busy. The next Enter must
			// replay that committed operation, not create another task.
			if r.StatusCode < 200 || r.StatusCode >= 300 {
				t.Errorf("upstream did not commit/replay: %s", r.Status)
			}
			if err := r.Body.Close(); err != nil {
				return err
			}
			body := `{"code":"busy","status":503,"title":"server busy","retry_after":0}`
			r.StatusCode, r.Status = http.StatusServiceUnavailable, "503 Service Unavailable"
			r.Body = io.NopCloser(strings.NewReader(body))
			r.ContentLength = int64(len(body))
			r.Header.Del("Content-Length")
			r.Header.Set("Content-Type", "application/problem+json")
			r.Header.Set("Retry-After", "0")
			return nil
		},
	}
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(proxy)
	if err := server.Listener.Close(); err != nil {
		t.Fatal(err)
	}
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	cfg := config.DefaultClient(srv.Paths)
	cfg.Socket = path
	c, err := client.NewFromConfig(cfg, "tui", nil)
	if err != nil {
		t.Fatal(err)
	}
	clk, err := c.Clock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	a := newTestApp(t, newFake(), func(cfg *Config) { cfg.API, cfg.Clock = c, *clk })
	press(t, a, "a")
	typeText(t, a, "one committed task")
	press(t, a, "enter")
	if a.form == nil {
		t.Fatal("busy closed the form")
	}
	if a.banner.text != "server busy — enter resends the same request" {
		t.Error("busy banner:", a.banner.text)
	}
	press(t, a, "enter")
	eq(t, a.form, nil)
	mu.Lock()
	attemptKeys := append([]string(nil), keys...)
	mu.Unlock()
	eq(t, len(attemptKeys), 3)
	if attemptKeys[0] == "" || attemptKeys[0] != attemptKeys[1] || attemptKeys[0] != attemptKeys[2] {
		t.Errorf("attempts changed idempotency key: %v", attemptKeys)
	}
	tasks, err := srv.Client.ListTasks(t.Context(), client.TaskQuery{View: "triage"})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, len(tasks.Items), 1)
	ops, err := srv.Client.ListOps(t.Context(), client.OpsQuery{})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, len(ops.Items), 1)
}
