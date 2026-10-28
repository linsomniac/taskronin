package server_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"taskronin/internal/api"
	"taskronin/internal/domain"
	"taskronin/internal/server"
	"taskronin/internal/service"
	"taskronin/internal/testutil"
	"taskronin/internal/textsafe"
)

func TestStatusForEveryCode(t *testing.T) {
	codes := map[domain.Code]int{
		domain.CodeMalformed: 400, domain.CodeUnauthenticated: 401, domain.CodeForbidden: 403, domain.CodePeerRejected: 403,
		domain.CodePasswordRequired: 403, domain.CodePasswordInvalid: 403, domain.CodeNotFound: 404, domain.CodeDeleted: 409,
		domain.CodeConflict: 409, domain.CodeRevertConflict: 409, domain.CodeNotRevertible: 409, domain.CodePrecondition: 412,
		domain.CodeTooLarge: 413, domain.CodeUnsupportedMedia: 415, domain.CodeValidation: 422, domain.CodeIdemMismatch: 422,
		domain.CodeNothingToUndo: 409, domain.CodeNothingToRedo: 409, domain.CodePreconditionReq: 428, domain.CodeIdemRequired: 428,
		domain.CodeGenerationMismatch: 412, domain.CodeRateLimited: 429, domain.CodeInternal: 500, domain.CodeBusy: 503,
	}
	if len(codes) != 24 {
		t.Fatal("incomplete code table")
	}
	for code, want := range codes {
		if got := server.StatusFor(code); got != want {
			t.Errorf("%s: %d, want %d", code, got, want)
		}
	}
	if server.StatusFor("unknown") != 500 {
		t.Fatal("unknown error must fail closed")
	}
}

func TestProblemBody(t *testing.T) {
	s, _, token := newTestServer(t, nil)
	for _, tc := range []struct {
		name  string
		err   error
		code  domain.Code
		retry string
	}{
		{"validation", &domain.Error{Code: domain.CodeValidation, Detail: "invalid title", Fields: []domain.FieldError{{Field: "title", Message: "required"}}}, domain.CodeValidation, ""},
		{"internal", &domain.Error{Code: domain.CodeInternal, Detail: "secret sql", Err: errors.New("secret sql")}, domain.CodeInternal, ""},
		{"untyped", errors.New("secret sql"), domain.CodeInternal, ""},
		{"rate", &domain.Error{Code: domain.CodeRateLimited, RetryAfter: 90 * time.Second}, domain.CodeRateLimited, "90"},
		{"busy", &domain.Error{Code: domain.CodeBusy}, domain.CodeBusy, "1"},
		{"fractional", &domain.Error{Code: domain.CodeBusy, RetryAfter: 1500 * time.Millisecond}, domain.CodeBusy, "2"},
		{"current", &domain.Error{Code: domain.CodePrecondition, Current: domain.TaskView{Task: domain.Task{ID: 7, Title: "current task", Version: 3}}}, domain.CodePrecondition, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := server.HandlerFor(s, []server.Route{{Method: http.MethodGet, Pattern: "/api/v1/x", Handler: func(w http.ResponseWriter, r *http.Request) { server.WriteProblem(s, w, r, tc.err) }}})
			w := do(t, h, sock, request(http.MethodGet, "/api/v1/x", token, ""))
			p := problem(t, w, tc.code)
			if p.Type != api.ProblemTypePrefix+string(tc.code) || p.Title != http.StatusText(w.Code) || p.RequestID == "" || p.RequestID != w.Header().Get(api.HeaderRequestID) {
				t.Fatalf("problem = %+v", p)
			}
			if w.Header().Get("Retry-After") != tc.retry {
				t.Fatalf("Retry-After = %q", w.Header().Get("Retry-After"))
			}
			if tc.name == "validation" && (p.Detail != "invalid title" || len(p.Errors) != 1 || p.Errors[0].Field != "title") {
				t.Fatalf("validation = %+v", p)
			}
			if tc.code == domain.CodeInternal && (p.Detail != "internal error" || strings.Contains(w.Body.String(), "secret sql")) {
				t.Fatalf("internal = %+v", p)
			}
			if tc.name == "rate" && p.RetryAfter != 90 {
				t.Fatalf("retry_after = %d", p.RetryAfter)
			}
			if tc.name == "current" {
				var current api.Task
				if err := json.Unmarshal(p.Current, &current); err != nil || current.ID != 7 || current.ETag == "" {
					t.Fatalf("current = %s, %v", p.Current, err)
				}
			}
		})
	}
}

func TestWriteOp(t *testing.T) {
	s, svc, token := newTestServer(t, nil)
	created, err := svc.AddTask(t.Context(), testutil.Meta(testutil.Owner(), svc.Generation()), service.AddTaskIn{Title: "task"})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := svc.AddEntry(t.Context(), testutil.Meta(testutil.Owner(), svc.Generation()), service.AddEntryIn{Title: "entry", TaskID: sql.Null[domain.TaskID]{V: created.Task.ID, Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		res    *service.OpResult
		status int
	}{
		{"created", created, 201}, {"entry", entry, 201}, {"noop", &service.OpResult{NoChange: true}, 200},
		{"replay", &service.OpResult{Created: true, Replayed: true, Task: created.Task, OpID: created.OpID}, 201},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := server.HandlerFor(s, []server.Route{{Method: http.MethodPost, Pattern: "/api/v1/x", Handler: func(w http.ResponseWriter, r *http.Request) { server.WriteOp(s, w, r, tc.res, "/api/v1/tasks/1") }}})
			w := do(t, h, sock, request(http.MethodPost, "/api/v1/x", token, ""))
			var p api.OpResponse
			if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.status || p.Replayed != tc.res.Replayed {
				t.Fatalf("status %d, body %s", w.Code, w.Body)
			}
			if tc.status == 201 && w.Header().Get("Location") != "/api/v1/tasks/1" {
				t.Fatal("missing location")
			}
			if tc.status == 200 && (w.Header().Get("Location") != "" || p.OpID != nil) {
				t.Fatal("no-op metadata")
			}
			if p.Effects == nil || p.Warnings == nil {
				t.Fatal("empty arrays must not be null")
			}
			if tc.name == "entry" && (p.Entry == nil || p.Entry.TaskTitle == nil || *p.Entry.TaskTitle != "task") {
				t.Fatal("entry task title lost")
			}
		})
	}
}

type failingJSON struct{ err error }

func (v failingJSON) MarshalJSON() ([]byte, error) { return nil, v.err }

func TestProblemDiagnostics(t *testing.T) {
	var logs bytes.Buffer
	s, _, token := newTestServer(t, slog.New(slog.NewJSONHandler(&logs, nil)))
	cause := "database failure\x1b[2J\n\t\u202e\xff"
	for _, tc := range []struct {
		name   string
		err    error
		code   domain.Code
		encode bool
	}{
		{"internal cause", &domain.Error{Code: domain.CodeInternal, Detail: "operation failed", Err: errors.New(cause)}, domain.CodeInternal, false},
		{"wrapped internal", fmt.Errorf("adapter: %w", &domain.Error{Code: domain.CodeInternal, Err: errors.New(cause)}), domain.CodeInternal, false},
		{"untyped", fmt.Errorf("adapter: %w", errors.New(cause)), domain.CodeInternal, false},
		{"internal detail", &domain.Error{Code: domain.CodeInternal, Detail: cause}, domain.CodeInternal, false},
		{"busy cause", &domain.Error{Code: domain.CodeBusy, Detail: "try again", Err: errors.New(cause)}, domain.CodeBusy, false},
		{"JSON serialization", errors.New(cause), domain.CodeInternal, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()
			h := server.HandlerFor(s, []server.Route{{Method: http.MethodPost, Pattern: "/api/v1/x", Handler: func(w http.ResponseWriter, r *http.Request) {
				if tc.encode {
					server.WriteJSON(s, w, http.StatusOK, failingJSON{err: tc.err})
					return
				}
				server.WriteProblem(s, w, r, tc.err)
			}}})
			w := do(t, h, sock, request(http.MethodPost, "/api/v1/x?secret=private-query", token, `{"private-body":"never log"}`))
			p := problem(t, w, tc.code)
			if p.RequestID == "" || p.RequestID != w.Header().Get(api.HeaderRequestID) {
				t.Fatalf("request ID = %q, header = %q", p.RequestID, w.Header().Get(api.HeaderRequestID))
			}
			if tc.code == domain.CodeInternal && p.Detail != "internal error" {
				t.Fatalf("internal detail = %q", p.Detail)
			}
			if strings.Contains(w.Body.String(), "database failure") {
				t.Fatalf("diagnostic leaked to client: %s", w.Body)
			}
			decoder := json.NewDecoder(bytes.NewReader(logs.Bytes()))
			found := false
			for {
				var record map[string]any
				if err := decoder.Decode(&record); errors.Is(err, io.EOF) {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				if record["level"] != "ERROR" {
					continue
				}
				if record["request_id"] != p.RequestID || record["code"] != string(tc.code) {
					t.Fatalf("uncorrelated diagnostic: %v", record)
				}
				for _, v := range record {
					if text, ok := v.(string); ok {
						if text != textsafe.SanitizeLine(text) {
							t.Fatalf("unsanitized diagnostic: %q", text)
						}
						found = found || strings.Contains(text, textsafe.SanitizeLine(cause))
					}
				}
			}
			if !found {
				t.Fatalf("missing sanitized cause in error log: %s", &logs)
			}
			for _, secret := range []string{token, "Authorization", "private-body", "private-query"} {
				if strings.Contains(logs.String(), secret) {
					t.Fatalf("request secret logged: %q", secret)
				}
			}
		})
	}
}
