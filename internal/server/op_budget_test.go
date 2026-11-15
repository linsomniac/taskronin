package server_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"taskronin/internal/api"
	"taskronin/internal/domain"
	"taskronin/internal/server"
	"taskronin/internal/service"
	"taskronin/internal/testutil"
)

func TestFinalKeyedBudgetOverrunHTTP(t *testing.T) {
	scenario := testutil.NewScenario(t, func(o *service.Options) { o.OpBudget = 2 * time.Second })
	svc := scenario.Svc
	scenario.Store.Reader.SetMaxOpenConns(1)
	conn, err := scenario.Store.Reader.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = svc.AddTask(t.Context(), scenario.Meta(), service.AddTaskIn{Title: "committed before result timeout"})
	if err == nil {
		t.Fatal("expected injected budget expiry")
	}
	var count int
	if err := scenario.Store.Writer.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM tasks`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("post-commit fixture: count=%d err=%v", count, err)
	}
	s := server.New(svc, api.Conv{Gen: svc.Generation(), Loc: svc.Zone().Location()}, scenario.Clock, nil)
	w := httptest.NewRecorder()
	server.WriteProblem(s, w, request(http.MethodPost, "/api/v1/tasks", "", ""), err)
	problem(t, w, domain.CodeBusy)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatal(w.Code)
	}
	seconds, err := strconv.Atoi(w.Header().Get("Retry-After"))
	if err != nil || seconds <= 0 {
		t.Fatal("missing positive Retry-After", w.Header())
	}
}
