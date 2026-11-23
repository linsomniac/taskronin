package server_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/server"
	"github.com/linsomniac/taskronin/internal/service"
	"github.com/linsomniac/taskronin/internal/testutil"
)

var sock = server.ConnInfo{Transport: server.TransportUnix, PeerOK: true, PeerUID: uint32(os.Getuid())}

func newTestServer(t *testing.T, log *slog.Logger) (*server.Server, *service.Service, string) {
	t.Helper()
	svc, clk := testutil.Service(t)
	created, err := svc.CreateToken(t.Context(), service.Meta{Actor: testutil.Owner(), Gen: svc.Generation()}, service.CreateTokenIn{Name: "api-test", Scopes: domain.AllScopes})
	if err != nil {
		t.Fatal(err)
	}
	return server.New(svc, api.Conv{Gen: svc.Generation(), Loc: svc.Zone().Location()}, clk, log), svc, created.Secret
}

func newTestAPI(t *testing.T) (http.Handler, *service.Service, string) {
	t.Helper()
	s, svc, tok := newTestServer(t, nil)
	return s.APIHandler(), svc, tok
}

func do(t *testing.T, h http.Handler, ci server.ConnInfo, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req.WithContext(server.WithConnInfo(req.Context(), ci)))
	return w
}

func request(method, path, token, body string) *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		r.Header.Set("Content-Type", api.ContentTypeJSON)
	}
	return r
}

func problem(t *testing.T, w *httptest.ResponseRecorder, code domain.Code) api.Problem {
	t.Helper()
	var p api.Problem
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("problem JSON: %v (%s)", err, w.Body.String())
	}
	if p.Code != string(code) || p.Status != server.StatusFor(code) || w.Code != p.Status {
		t.Fatalf("problem = %+v, HTTP %d; want %s", p, w.Code, code)
	}
	if w.Header().Get("Content-Type") != api.ContentTypeProblem {
		t.Fatalf("Content-Type = %q", w.Header().Get("Content-Type"))
	}
	return p
}

func decodingRoute(s *server.Server) server.Route {
	return server.Route{Method: http.MethodPost, Pattern: "/api/v1/x", Handler: func(w http.ResponseWriter, r *http.Request) {
		_, canonical, err := server.DecodeBody[api.AddTaskRequest](r)
		if err != nil {
			server.WriteProblem(s, w, r, err)
			return
		}
		w.Header().Set("Content-Type", api.ContentTypeJSON)
		_, _ = w.Write(canonical)
	}}
}
