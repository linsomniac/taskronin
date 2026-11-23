package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/auth"
	"github.com/linsomniac/taskronin/internal/buildinfo"
	"github.com/linsomniac/taskronin/internal/clock"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/server"
	"github.com/linsomniac/taskronin/internal/service"
	"github.com/linsomniac/taskronin/internal/testutil"
)

func TestSCP06_ForwardedHeadersRejected(t *testing.T) {
	h, _, token := newTestAPI(t)
	for _, name := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host", "X-Forwarded-Anything", "Via", "x-forwarded-for"} {
		for _, value := range []string{"", "spoofed"} {
			r := request(http.MethodGet, "/api/v1/x", token, "")
			r.Header[name] = []string{value}
			problem(t, do(t, h, sock, r), domain.CodePeerRejected)
		}
	}
}

func TestSCP04_PeerUIDRejected(t *testing.T) {
	h, _, token := newTestAPI(t)
	for _, ci := range []server.ConnInfo{{Transport: server.TransportUnix, PeerOK: true, PeerUID: sock.PeerUID + 1}, {Transport: server.TransportUnix, PeerUID: sock.PeerUID}, {Transport: server.TransportTCP, PeerOK: true, PeerUID: sock.PeerUID}, {}} {
		problem(t, do(t, h, ci, request(http.MethodGet, "/api/v1/x", token, "")), domain.CodePeerRejected)
	}
}

func TestSCP05_CookieOnlyIsUnauthenticated(t *testing.T) {
	h, _, _ := newTestAPI(t)
	r := request(http.MethodGet, "/api/v1/x", "", "")
	r.Header.Set("Cookie", "tr_session=x")
	w := do(t, h, sock, r)
	problem(t, w, domain.CodeUnauthenticated)
	if w.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatal("missing bearer challenge")
	}
}

func TestRouteClassAPI(t *testing.T) {
	s, _, token := newTestServer(t, nil)
	h := s.APIHandler()
	for _, path := range []string{"/ui/", "/static/file", "/", "/api/v10/x", "/healthz/", "/ui/../api/v1/x"} {
		w := do(t, h, sock, request(http.MethodGet, path, token, ""))
		if w.Code != 404 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	w := do(t, h, sock, request(http.MethodGet, "/healthz", "", ""))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "ok" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("health: %d %s", w.Code, w.Body)
	}
	h = server.HandlerFor(s, []server.Route{{Method: http.MethodGet, Pattern: "/api/v1/x", Handler: func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			t.Error("cookie reached handler")
		}
		w.WriteHeader(http.StatusNoContent)
	}}})
	r := request(http.MethodGet, "/api/v1/x", token, "")
	r.Header.Set("Cookie", "tr_session=x")
	if w = do(t, h, sock, r); w.Code != 204 {
		t.Fatalf("status %d", w.Code)
	}
}

func TestAuthnChannel(t *testing.T) {
	s, _, token := newTestServer(t, nil)
	h := server.HandlerFor(s, []server.Route{{Method: http.MethodGet, Pattern: "/api/v1/x", Handler: func(w http.ResponseWriter, r *http.Request) {
		actor := server.Meta(s, r, nil).Actor
		if actor.Channel != domain.ChanTUI || actor.RemoteKey != "socket" {
			t.Errorf("actor = %+v", actor)
		}
		w.WriteHeader(http.StatusNoContent)
	}}})
	problem(t, do(t, h, sock, request(http.MethodGet, "/api/v1/x", "invalid", "")), domain.CodeUnauthenticated)
	for _, ch := range []string{"web", "system", "CLI", "other"} {
		r := request(http.MethodGet, "/api/v1/x", token, "")
		r.Header.Set(api.HeaderChannel, ch)
		problem(t, do(t, h, sock, r), domain.CodeMalformed)
	}
	r := request(http.MethodGet, "/api/v1/x", token, "")
	r.Header.Set(api.HeaderChannel, "tui")
	if w := do(t, h, sock, r); w.Code != 204 {
		t.Fatalf("tui: %d", w.Code)
	}
}

func TestOwnerTokenRequiresVerifiedSocketAndRejectsMCP(t *testing.T) {
	s, svc, _ := newTestServer(t, nil)
	raw, err := auth.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	id, err := svc.ResetPasswordOffline(t.Context(), service.ResetPasswordIn{NewPassword: testutil.Password, OwnerSecret: raw})
	if err != nil {
		t.Fatal(err)
	}
	token := auth.FormatToken(id, raw)
	h := server.HandlerFor(s, []server.Route{{Method: http.MethodGet, Pattern: "/api/v1/x", Handler: func(w http.ResponseWriter, r *http.Request) {
		if server.Meta(s, r, nil).Actor.Kind != domain.ActorOwner {
			t.Error("expected owner")
		}
		w.WriteHeader(http.StatusNoContent)
	}}})
	for _, ci := range []server.ConnInfo{{}, {Transport: server.TransportTCP}, sock} {
		r := request(http.MethodGet, "/api/v1/x", token, "")
		w := do(t, h, ci, r)
		if ci == sock {
			if w.Code != 204 {
				t.Fatalf("owner socket: %d", w.Code)
			}
		} else {
			problem(t, w, domain.CodePeerRejected)
		}
	}
	r := request(http.MethodGet, "/api/v1/x", token, "")
	r.Header.Set(api.HeaderChannel, "mcp")
	w := do(t, h, sock, r)
	problem(t, w, domain.CodeForbidden)
	if w.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("403 must not challenge")
	}
}

// TestAuthnVerifiesSocketItself is final review A-Minor-1 (A§6.1): authn
// enforces "owner token only on a verified socket" on its own, so the rule
// survives a chain without socketGuard (a later TCP mount of the API).
func TestAuthnVerifiesSocketItself(t *testing.T) {
	s, svc, _ := newTestServer(t, nil)
	raw, err := auth.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	id, err := svc.ResetPasswordOffline(t.Context(), service.ResetPasswordIn{NewPassword: testutil.Password, OwnerSecret: raw})
	if err != nil {
		t.Fatal(err)
	}
	owner := auth.FormatToken(id, raw)
	// The reset revoked every earlier token; mint a fresh non-owner one.
	created, err := svc.CreateToken(t.Context(), service.Meta{Actor: testutil.Owner(), Gen: svc.Generation()}, service.CreateTokenIn{Name: "after-reset", Scopes: domain.AllScopes})
	if err != nil {
		t.Fatal(err)
	}
	token := created.Secret
	h := server.WithRequestState(server.Authn(s, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })))
	unverified := []server.ConnInfo{{}, {Transport: server.TransportTCP, PeerOK: true, PeerUID: sock.PeerUID},
		{Transport: server.TransportUnix, PeerUID: sock.PeerUID}, {Transport: server.TransportUnix, PeerOK: true, PeerUID: sock.PeerUID + 1}}
	for _, ci := range unverified {
		problem(t, do(t, h, ci, request(http.MethodGet, "/api/v1/x", owner, "")), domain.CodeUnauthenticated)
		// A non-owner token does not depend on the transport.
		if w := do(t, h, ci, request(http.MethodGet, "/api/v1/x", token, "")); w.Code != http.StatusNoContent {
			t.Fatalf("API token over %+v: %d", ci, w.Code)
		}
	}
	if w := do(t, h, sock, request(http.MethodGet, "/api/v1/x", owner, "")); w.Code != http.StatusNoContent {
		t.Fatalf("owner token on the verified socket: %d %s", w.Code, w.Body)
	}
}

func TestBodyRules(t *testing.T) {
	s, _, token := newTestServer(t, nil)
	h := server.HandlerFor(s, []server.Route{decodingRoute(s)})
	for _, tc := range []struct {
		name, body, media string
		code              domain.Code
	}{
		{"too large", strings.Repeat(" ", api.MaxBodyBytes+1), api.ContentTypeJSON, domain.CodeTooLarge},
		{"plain", "{}", "text/plain", domain.CodeUnsupportedMedia},
		{"missing type", "{}", "", domain.CodeUnsupportedMedia},
		{"unknown", `{"unknown":1}`, api.ContentTypeJSON, domain.CodeMalformed},
		{"nested unknown", `{"place":{"how":"block","surprise":true}}`, api.ContentTypeJSON, domain.CodeMalformed},
		{"trailing", "{} {}", api.ContentTypeJSON, domain.CodeMalformed},
		{"trailing invalid", "{} x", api.ContentTypeJSON, domain.CodeMalformed},
		{"utf8", "{\"title\":\"\xff\"}", api.ContentTypeJSON, domain.CodeMalformed},
		{"whitespace", " ", api.ContentTypeJSON, domain.CodeMalformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, length := range []int64{int64(len(tc.body)), -1, 0} {
				r := request(http.MethodPost, "/api/v1/x", token, tc.body)
				r.ContentLength = length
				r.Header.Set("Content-Type", tc.media)
				problem(t, do(t, h, sock, r), tc.code)
			}
		})
	}
	for _, body := range []string{"", ` { "title": "ok" } `, strings.Repeat(" ", api.MaxBodyBytes-2) + "{}"} {
		r := request(http.MethodPost, "/api/v1/x", token, body)
		if body != "" {
			r.Header.Set("Content-Type", "application/json; charset=utf-8")
		}
		if w := do(t, h, sock, r); w.Code != 200 {
			t.Fatalf("valid body: %d %s", w.Code, w.Body)
		}
	}
}

func TestAPIHeaders(t *testing.T) {
	s, _, token := newTestServer(t, nil)
	h := server.HandlerFor(s, []server.Route{decodingRoute(s), {Method: http.MethodGet, Pattern: "/api/v1/panic", Handler: func(http.ResponseWriter, *http.Request) { panic("private panic") }}})
	for _, tc := range []struct {
		req    *http.Request
		ci     server.ConnInfo
		status int
	}{
		{request(http.MethodGet, "/healthz", "", ""), sock, 200},
		{request(http.MethodGet, "/api/v1/x", "", ""), sock, 401},
		{request(http.MethodGet, "/api/v1/x", token, ""), server.ConnInfo{}, 403},
		{request(http.MethodPost, "/api/v1/x", token, "{}"), sock, 415},
		{request(http.MethodGet, "/api/v1/panic", token, ""), sock, 500},
	} {
		if tc.status == 415 {
			tc.req.Header.Set("Content-Type", "text/plain")
		}
		tc.req.Header.Set(api.HeaderRequestID, "untrusted-request-id")
		w := do(t, h, tc.ci, tc.req)
		if w.Code != tc.status {
			t.Fatalf("status %d want %d", w.Code, tc.status)
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get(api.HeaderVersion) != buildinfo.Version {
			t.Fatalf("headers = %v", w.Header())
		}
		if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(w.Header().Get(api.HeaderRequestID)) {
			t.Fatal("invalid request id")
		}
	}
}

func TestAccessLogRedaction(t *testing.T) {
	var logs bytes.Buffer
	s, _, token := newTestServer(t, slog.New(slog.NewJSONHandler(&logs, nil)))
	h := server.HandlerFor(s, []server.Route{{Method: http.MethodPost, Pattern: "/api/v1/tasks/{id}", Handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }}})
	w := do(t, h, sock, request(http.MethodPost, "/api/v1/tasks/private-path?secret=private-query", token, `{"private-body":"never log"}`))
	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["method"] != "POST" || record["route"] != "POST /api/v1/tasks/{id}" || record["status"] != float64(204) || record["request_id"] != w.Header().Get(api.HeaderRequestID) || record["actor"] != "token:api-test" || record["duration"] == nil {
		t.Fatalf("log = %v", record)
	}
	for _, secret := range []string{token, "Authorization", "private-body", "private-path", "private-query"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("logged %q", secret)
		}
	}
}

func TestAccessLogRedirectRedaction(t *testing.T) {
	var logs bytes.Buffer
	s, _, token := newTestServer(t, slog.New(slog.NewJSONHandler(&logs, nil)))
	h := s.APIHandler()
	do(t, h, sock, request(http.MethodGet, "/api/v1//private-path?secret=private-query", token, ""))
	if strings.Contains(logs.String(), "private-path") || strings.Contains(logs.String(), "private-query") {
		t.Fatalf("redirect logged raw request target: %s", &logs)
	}
}

func TestRecoverer(t *testing.T) {
	var logs bytes.Buffer
	s, _, token := newTestServer(t, slog.New(slog.NewJSONHandler(&logs, nil)))
	h := server.HandlerFor(s, []server.Route{{Method: http.MethodGet, Pattern: "/api/v1/x", Handler: func(http.ResponseWriter, *http.Request) { panic("private panic") }}})
	w := do(t, h, sock, request(http.MethodGet, "/api/v1/x", token, ""))
	problem(t, w, domain.CodeInternal)
	if !strings.Contains(logs.String(), `"stack"`) || !strings.Contains(logs.String(), "TestRecoverer") || !strings.Contains(logs.String(), `"status":500`) {
		t.Fatalf("missing stack/status: %s", &logs)
	}
	if strings.Contains(logs.String(), "private panic") || strings.Contains(w.Body.String(), "private panic") {
		t.Fatal("panic value leaked")
	}
}

func TestRecovererPreservesAbortHandler(t *testing.T) {
	s, _, token := newTestServer(t, nil)
	h := server.HandlerFor(s, []server.Route{{Method: http.MethodGet, Pattern: "/api/v1/x", Handler: func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }}})
	defer func() {
		if got, ok := recover().(error); !ok || !errors.Is(got, http.ErrAbortHandler) {
			t.Fatalf("panic = %v", got)
		}
	}()
	do(t, h, sock, request(http.MethodGet, "/api/v1/x", token, ""))
}

func TestIfMatchParsing(t *testing.T) {
	gen := domain.NewGeneration()
	valid := domain.FormatETag(gen, 7)
	for _, tc := range []struct {
		values []string
		want   domain.Expect
		bad    bool
	}{
		{nil, domain.Expect{}, false}, {[]string{valid}, domain.Expect{Gen: gen, Version: 7}, false},
		{[]string{"*"}, domain.Expect{}, true}, {[]string{"W/" + valid}, domain.Expect{}, true},
		{[]string{valid + ", " + valid}, domain.Expect{}, true}, {[]string{valid, valid}, domain.Expect{}, true}, {[]string{""}, domain.Expect{}, true},
	} {
		r := request(http.MethodPost, "/api/v1/x", "", "")
		if tc.values != nil {
			r.Header["If-Match"] = tc.values
		}
		got, err := server.IfMatch(r)
		if tc.bad {
			testutil.RequireCode(t, err, domain.CodeMalformed)
		} else if err != nil || got != tc.want {
			t.Fatalf("IfMatch = %+v %v", got, err)
		}
	}
}

func TestMetaAndPathInt(t *testing.T) {
	s, svc, token := newTestServer(t, nil)
	key := api.NewIdempotencyKey()
	h := server.HandlerFor(s, []server.Route{{Method: http.MethodPost, Pattern: "/api/v1/tasks/{id}", Handler: func(w http.ResponseWriter, r *http.Request) {
		id, err := server.PathInt(r, "id")
		if err != nil {
			server.WriteProblem(s, w, r, err)
			return
		}
		if id != 42 {
			t.Errorf("id = %d", id)
		}
		m := server.Meta(s, r, []byte(`{}`))
		if m.Idem.Key != key || m.Idem.Method != http.MethodPost || m.Idem.Route != "POST /api/v1/tasks/42" || string(m.Idem.Body) != "{}" || m.Gen != svc.Generation() || m.Actor.Key() != "token:api-test" {
			t.Errorf("meta = %+v", m)
		}
		ex := server.ExemptMeta(s, r)
		if ex.Idem.Key != "" || ex.Idem.Method != "" || ex.Idem.Route != "" || ex.Idem.Body != nil || ex.Actor != m.Actor || ex.Gen != m.Gen {
			t.Errorf("exempt meta = %+v", ex)
		}
		w.WriteHeader(http.StatusNoContent)
	}}})
	for _, id := range []string{"42", "nope", "9223372036854775808"} {
		r := request(http.MethodPost, "/api/v1/tasks/"+id+"?x=1", token, "")
		r.Header.Set(api.HeaderIdempotencyKey, key)
		r.Header.Set(api.HeaderGeneration, string(svc.Generation()))
		w := do(t, h, sock, r)
		if id == "42" {
			if w.Code != 204 {
				t.Fatal(w.Code)
			}
		} else {
			problem(t, w, domain.CodeMalformed)
		}
	}
}

func TestRouteBudgetsAndLongCancellation(t *testing.T) {
	s, _, token := newTestServer(t, nil)
	for _, tc := range []struct {
		method string
		long   bool
		budget time.Duration
	}{{"GET", false, 10 * time.Second}, {"HEAD", false, 10 * time.Second}, {"POST", false, 0}, {"GET", true, 10 * time.Minute}} {
		h := server.HandlerFor(s, []server.Route{{Method: tc.method, Pattern: "/api/v1/x", Long: tc.long, Handler: func(w http.ResponseWriter, r *http.Request) {
			deadline, ok := r.Context().Deadline()
			if tc.budget == 0 {
				if ok {
					t.Error("ordinary writer got read deadline")
				}
			} else if remaining := deadline.Sub(clock.Real{}.Now()); !ok || remaining > tc.budget || remaining < tc.budget-time.Second {
				t.Errorf("remaining %s, want %s", remaining, tc.budget)
			}
			w.WriteHeader(http.StatusNoContent)
		}}})
		if w := do(t, h, sock, request(tc.method, "/api/v1/x", token, "")); w.Code != 204 {
			t.Fatal(w.Code)
		}
	}
	ctx, cancel := server.LongCtx(s, request(http.MethodGet, "/api/v1/x", token, ""))
	defer cancel()
	s.BeginShutdown()
	s.BeginShutdown()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel long context")
	}
	ctx2, cancel2 := server.LongCtx(s, request(http.MethodGet, "/api/v1/x", token, ""))
	defer cancel2()
	if ctx2.Err() == nil {
		t.Fatal("new long request after shutdown is live")
	}
	s2, _, _ := newTestServer(t, nil)
	parent, stop := context.WithCancel(t.Context())
	ctx3, cancel3 := server.LongCtx(s2, request(http.MethodGet, "/api/v1/x", token, "").WithContext(parent))
	defer cancel3()
	stop()
	if !errors.Is(ctx3.Err(), context.Canceled) {
		t.Fatal("request cancellation lost")
	}
}

// AIDEV-NOTE: ResponseController must reach the network writer through every
// middleware wrapper so cancellation can interrupt an export blocked in Write.
type deadlineWriter struct {
	*httptest.ResponseRecorder
	deadlines chan time.Time
}

func (w *deadlineWriter) SetWriteDeadline(v time.Time) error { w.deadlines <- v; return nil }

func TestLongRouteWriteDeadline(t *testing.T) {
	for _, shutdown := range []string{"request", "server", "service"} {
		s, svc, token := newTestServer(t, nil)
		parent, cancel := context.WithCancel(t.Context())
		defer cancel()
		w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder(), deadlines: make(chan time.Time, 4)}
		h := server.HandlerFor(s, []server.Route{{Method: "GET", Pattern: "/api/v1/x", Long: true, Handler: func(_ http.ResponseWriter, r *http.Request) {
			select {
			case deadline := <-w.deadlines:
				if remaining := deadline.Sub(clock.Real{}.Now()); remaining < 9*time.Minute || remaining > 10*time.Minute {
					t.Errorf("write budget %s", remaining)
				}
			default:
				t.Error("no write deadline")
			}
			switch shutdown {
			case "server":
				s.BeginShutdown()
			case "service":
				svc.BeginShutdown()
			default:
				cancel()
			}
			select {
			case deadline := <-w.deadlines:
				if !deadline.Equal(time.Unix(1, 0)) {
					t.Error("cancellation deadline is not the prescribed past instant")
				}
			case <-time.After(time.Second):
				t.Error("write not interrupted")
			}
			if r.Context().Err() == nil {
				t.Error("context still live")
			}
		}}})
		r := request("GET", "/api/v1/x", token, "").WithContext(server.WithConnInfo(parent, sock))
		h.ServeHTTP(w, r)
	}
}

func TestLongRouteBlockedSocketWrite(t *testing.T) {
	s, _, token := newTestServer(t, nil)
	path := filepath.Join(t.TempDir(), "api.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	entered, finished := make(chan struct{}), make(chan error, 1)
	h := server.HandlerFor(s, []server.Route{{Method: http.MethodGet, Pattern: "/api/v1/export", Long: true, Handler: func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		// The client deliberately never reads: this exceeds the socket buffer.
		_, err := w.Write(make([]byte, 8<<20))
		finished <- err
	}}})
	srv := &http.Server{Handler: h, ConnContext: server.ConnContext, ReadHeaderTimeout: time.Second}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = ln.Close()
		if err := <-served; !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	})
	var dialer net.Dialer
	conn, err := dialer.DialContext(t.Context(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	// AIDEV-NOTE: Real time bounds a failed socket test regardless of the fake domain clock.
	if err := conn.SetDeadline(clock.Real{}.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "GET /api/v1/export HTTP/1.1\r\nHost: taskronin\r\nAuthorization: Bearer "+token+"\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request never reached handler")
	}
	s.BeginShutdown()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("blocked socket write unexpectedly succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown failed to interrupt socket write")
	}
}

func TestRoutingProblems(t *testing.T) {
	s, _, token := newTestServer(t, nil)
	called := false
	h := server.HandlerFor(s, []server.Route{{Method: http.MethodGet, Pattern: "/api/v1/tasks/{id}", Handler: func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.PathValue("id") != "42" {
			t.Errorf("path id = %q", r.PathValue("id"))
		}
		w.WriteHeader(http.StatusNoContent)
	}}})
	for _, tc := range []struct{ method, path, token string }{
		{http.MethodGet, "/api/v1/missing", token},
		{http.MethodPost, "/api/v1/tasks/42", token},
		{http.MethodPatch, "/api/v1/tasks/42", token},
		{http.MethodPost, "/healthz", ""},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			w := do(t, h, sock, request(tc.method, tc.path, tc.token, ""))
			p := problem(t, w, domain.CodeNotFound)
			if p.Type != api.ProblemTypePrefix+string(domain.CodeNotFound) || p.Title != http.StatusText(http.StatusNotFound) || p.Detail == "" || p.RequestID == "" || p.RequestID != w.Header().Get(api.HeaderRequestID) {
				t.Fatalf("routing problem = %+v", p)
			}
			if w.Header().Get(api.HeaderVersion) != buildinfo.Version || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("routing headers = %v", w.Header())
			}
			if called {
				t.Fatal("unmatched method/path called a route handler")
			}
		})
	}
	// The fallback stays behind authentication, and matched routes retain mux
	// path values and GET's implicit HEAD support.
	problem(t, do(t, h, sock, request(http.MethodGet, "/api/v1/missing", "", "")), domain.CodeUnauthenticated)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		w := do(t, h, sock, request(method, "/api/v1/tasks/42", token, ""))
		if w.Code != http.StatusNoContent || !called {
			t.Fatalf("matched %s: status %d, called %t", method, w.Code, called)
		}
		called = false
	}
	w := do(t, h, sock, request(http.MethodGet, "/api/v1//tasks/42", token, ""))
	if w.Code != http.StatusTemporaryRedirect || w.Header().Get("Location") != "/api/v1/tasks/42" || called {
		t.Fatalf("mux redirect = %d %v, called %t", w.Code, w.Header(), called)
	}
}
