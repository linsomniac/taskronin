package web_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"taskronin/internal/testutil"
	"taskronin/internal/web"
)

func newHandler(t *testing.T, listen string) http.Handler {
	t.Helper()
	svc, clk := testutil.Service(t)
	h, err := web.New(web.Options{Svc: svc, Clock: clk, Zone: testutil.Zone,
		WeekStart: time.Monday, Listen: listen, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHostAllowlist(t *testing.T) {
	for _, tc := range []struct {
		listen string
		hosts  []string
	}{
		{"127.0.0.1:7878", []string{"127.0.0.1:7878", "localhost:7878", "LOCALHOST:7878"}},
		{"[::1]:7878", []string{"[::1]:7878", "localhost:7878"}},
		{"127.0.0.2:7878", []string{"127.0.0.2:7878"}},
	} {
		t.Run(tc.listen, func(t *testing.T) {
			h := newHandler(t, tc.listen)
			for _, host := range []string{"127.0.0.1:7878", "localhost:7878", "LOCALHOST:7878", "[::1]:7878", "127.0.0.2:7878", "127.0.0.1:7879", "evil.example:7878", "127.0.0.1", "localhost", ""} {
				t.Run(host, func(t *testing.T) {
					want := http.StatusBadRequest
					for _, allowed := range tc.hosts {
						if strings.EqualFold(host, allowed) {
							want = http.StatusOK
						}
					}
					r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil)
					r.Host = host
					w := httptest.NewRecorder()
					h.ServeHTTP(w, r)
					if w.Code != want {
						t.Fatalf("status = %d, want %d", w.Code, want)
					}
					if want == http.StatusBadRequest && w.Body.String() != "bad host\n" {
						t.Errorf("body = %q, want bad host", w.Body.String())
					}
				})
			}
		})
	}
}

func TestNewRejectsBadListen(t *testing.T) {
	for _, listen := range []string{"", "7878", "127.0.0.1", "127.0.0.1:http", "127.0.0.1:", "127.0.0.1:-1", "127.0.0.1:65536"} {
		t.Run(listen, func(t *testing.T) {
			if _, err := web.New(web.Options{Listen: listen}); err == nil {
				t.Fatal("New accepted an invalid listen address")
			}
			if _, err := web.HostAllowlist(listen); err == nil {
				t.Fatal("HostAllowlist accepted an invalid listen address")
			}
		})
	}

	allowed, err := web.HostAllowlist("LOCALHOST:7878")
	if err != nil {
		t.Fatal(err)
	}
	if len(allowed) != 1 || !allowed["localhost:7878"] {
		t.Fatalf("allowlist = %v, want lowercase literal only", allowed)
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	h := newHandler(t, "127.0.0.1:7878")
	for _, tc := range []struct{ method, path, host, contentType, body string }{
		{http.MethodGet, "/healthz", "127.0.0.1:7878", "", ""},
		{http.MethodGet, "/", "127.0.0.1:7878", "", ""},
		{http.MethodGet, "/static/app.css", "127.0.0.1:7878", "", ""},
		{http.MethodGet, "/nope", "127.0.0.1:7878", "", ""},
		{http.MethodGet, "/api/v1/tasks", "127.0.0.1:7878", "", ""},
		{http.MethodGet, "/healthz", "evil.example:7878", "", ""},
		{http.MethodPost, "/ui/tasks", "127.0.0.1:7878", "application/json", "{}"},
		{http.MethodPost, "/ui/tasks", "127.0.0.1:7878", "application/x-www-form-urlencoded", "a=%zz"},
		{http.MethodPost, "/ui/tasks", "127.0.0.1:7878", "application/x-www-form-urlencoded", strings.Repeat("a", 1<<20+1)},
	} {
		t.Run(tc.method+tc.path+tc.host+tc.contentType, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, strings.NewReader(tc.body))
			r.Host = tc.host
			r.Header.Set("Content-Type", tc.contentType)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			for name, want := range map[string]string{
				"Content-Security-Policy":    web.CSP,
				"X-Content-Type-Options":     "nosniff",
				"Referrer-Policy":            "no-referrer",
				"Cross-Origin-Opener-Policy": "same-origin",
				"Permissions-Policy":         "camera=(), geolocation=(), microphone=()",
			} {
				if got := w.Header().Get(name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
		})
	}
}

func TestSCP04_WebListenerRefusesAPI(t *testing.T) {
	h := newHandler(t, "127.0.0.1:7878")
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/tasks"},
		{http.MethodPost, "/api/v1/tasks"},
		{http.MethodGet, "/api/v1/whoami"},
		{http.MethodGet, "/api/v1"},
	} {
		for _, requestID := range []string{"", "0123456789abcdef"} {
			t.Run(tc.method+tc.path+requestID, func(t *testing.T) {
				r := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)
				r.Host = "127.0.0.1:7878"
				r.Header.Set("Authorization", "Bearer tkr_1_"+strings.Repeat("A", 43))
				r.Header.Set("X-Request-ID", "untrusted-request-header")
				w := httptest.NewRecorder()
				if requestID != "" {
					w.Header().Set("X-Request-ID", requestID)
				}
				h.ServeHTTP(w, r)
				if w.Code != http.StatusUnauthorized || w.Header().Get("Content-Type") != "application/problem+json" || w.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("response = %d %v %s", w.Code, w.Header(), w.Body)
				}
				var problem struct {
					Type      string `json:"type"`
					Title     string `json:"title"`
					Status    int    `json:"status"`
					Code      string `json:"code"`
					Detail    string `json:"detail"`
					RequestID string `json:"request_id"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
					t.Fatal(err)
				}
				if problem.Type != "urn:taskronin:problem:unauthenticated" || problem.Title != "Unauthorized" || problem.Status != 401 || problem.Code != "unauthenticated" {
					t.Errorf("problem = %+v", problem)
				}
				if problem.Detail != "the API is served only on the unix socket" || problem.RequestID != requestID {
					t.Errorf("problem detail/request ID = %+v", problem)
				}
			})
		}
	}
}

func TestRouteClass(t *testing.T) {
	h := newHandler(t, "127.0.0.1:7878")
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/", http.StatusSeeOther}, {"/healthz", http.StatusOK},
		{"/favicon.ico", http.StatusNotFound}, {"/metrics", http.StatusNotFound},
		{"/uix", http.StatusNotFound}, {"/api/v10/tasks", http.StatusNotFound},
	} {
		t.Run(tc.path, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, nil)
			r.Host = "127.0.0.1:7878"
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
			if tc.path == "/" && w.Header().Get("Location") != "/ui/" {
				t.Errorf("Location = %q", w.Header().Get("Location"))
			}
			if tc.path == "/healthz" && (w.Body.String() != "ok" || w.Header().Get("Content-Type") != "text/plain; charset=utf-8") {
				t.Errorf("health response = %v %q", w.Header(), w.Body.String())
			}
		})
	}
}

func TestBodyLimit(t *testing.T) {
	for _, tc := range []struct {
		name, method, contentType, body string
		want                            int
	}{
		{"too large", http.MethodPost, "application/x-www-form-urlencoded", strings.Repeat("a", 1<<20+1), http.StatusRequestEntityTooLarge},
		{"at limit", http.MethodPost, "application/x-www-form-urlencoded", "a=" + strings.Repeat("x", 1<<20-2), http.StatusNoContent},
		{"JSON", http.MethodPost, "application/json", "{}", http.StatusUnsupportedMediaType},
		{"missing type", http.MethodPost, "", "a=1", http.StatusUnsupportedMediaType},
		{"bad type", http.MethodPost, "application/x-www-form-urlencoded; charset", "a=1", http.StatusUnsupportedMediaType},
		{"charset", http.MethodPost, "application/x-www-form-urlencoded; charset=UTF-8", "a=1", http.StatusNoContent},
		{"bad form", http.MethodPost, "application/x-www-form-urlencoded", "a=%zz", http.StatusBadRequest},
		{"GET untouched", http.MethodGet, "application/json", "{}", http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), tc.method, "/ui/tasks?a=query", strings.NewReader(tc.body))
			// Exercise streaming enforcement without a trustworthy Content-Length.
			r.ContentLength = -1
			r.Header.Set("Content-Type", tc.contentType)
			originalBody := r.Body
			var called bool
			var form url.Values
			h := newTestApp(t).App.BodyLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called, form = true, r.PostForm
				if tc.method == http.MethodGet && (r.Body != originalBody || r.PostForm != nil || r.Form != nil) {
					t.Error("GET body/form was changed")
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want || called != (tc.want == http.StatusNoContent) {
				t.Fatalf("status = %d, called = %v, want %d", w.Code, called, tc.want)
			}
			if tc.name == "charset" && form.Get("a") != "1" {
				t.Errorf("PostForm = %v, want a=1", form)
			}
		})
	}
}

func TestReadDeadline(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), method, "/ui/probe", strings.NewReader("a=1"))
			r.Host = "127.0.0.1:7878"
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Authorization", "Bearer must-be-stripped")
			var seen context.Context
			h, err := web.NewWithProbe(web.Options{Listen: r.Host}, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				seen = req.Context()
				deadline, ok := seen.Deadline()
				if method == http.MethodPost {
					if seen != r.Context() || ok {
						t.Error("POST context was changed")
					}
					if req.PostForm.Get("a") != "1" {
						t.Error("POST reached the mux without its parsed form")
					}
				} else if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 10*time.Second {
					t.Errorf("read deadline = %v, set = %v; want a 10-second budget", deadline, ok)
				}
				if req.Header.Get("Authorization") != "" {
					t.Error("Authorization reached the mux")
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if seen == nil || w.Code != http.StatusNoContent {
				t.Fatalf("probe was not reached: %d %s", w.Code, w.Body)
			}
			if method != http.MethodPost && seen.Err() != context.Canceled {
				t.Error("read context was not canceled after the handler returned")
			}
		})
	}
}
