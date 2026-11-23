package server_test

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/client"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/server"
	"github.com/linsomniac/taskronin/internal/testutil"
)

type routeContract struct {
	method, path                 string
	scopes                       domain.Scopes
	owner, exempt, ifMatch, long bool
}

// AIDEV-NOTE: Independently transcribed from A§6.4, never derived from Routes.
// Zero scopes means any authenticated actor, or scopes of the reversal target.
// /healthz is the separate unauthenticated probe, outside the API route table.
var expectedRoutes = []routeContract{
	{"GET", "/clock", 0, false, false, false, false},
	{"GET", "/whoami", 0, false, false, false, false},
	{"GET", "/today", domain.ScopeRead, false, false, false, false},
	{"GET", "/status", domain.ScopeRead, false, false, false, false},
	{"GET", "/days/{date}", domain.ScopeRead, false, false, false, false},
	{"GET", "/upcoming", domain.ScopeRead, false, false, false, false},
	{"GET", "/report", domain.ScopeRead, false, false, false, false},
	{"GET", "/search", domain.ScopeRead, false, false, false, false},
	{"GET", "/tags", domain.ScopeRead, false, false, false, false},
	{"GET", "/tasks", domain.ScopeRead, false, false, false, false},
	{"POST", "/tasks", domain.ScopeCapture, false, false, false, false},
	{"GET", "/tasks/{id}", domain.ScopeRead, false, false, false, false},
	{"PATCH", "/tasks/{id}", domain.ScopeWrite, false, false, true, false},
	{"POST", "/tasks/{id}/due", domain.ScopeWrite, false, false, true, false},
	{"POST", "/tasks/{id}/place", domain.ScopeWrite, false, false, true, false},
	{"POST", "/tasks/{id}/defer", domain.ScopeWrite, false, false, true, false},
	{"POST", "/tasks/{id}/block", domain.ScopeWrite, false, false, true, false},
	{"POST", "/tasks/{id}/triage", domain.ScopeWrite, false, false, true, false},
	{"POST", "/tasks/{id}/complete", domain.ScopeWrite, false, false, true, false},
	{"POST", "/tasks/{id}/reopen", domain.ScopeWrite, false, false, true, false},
	{"POST", "/tasks/{id}/drop", domain.ScopeDestroy, false, false, true, false},
	{"POST", "/tasks/{id}/delete", domain.ScopeDestroy, false, false, true, false},
	{"POST", "/tasks/{id}/undelete", domain.ScopeWrite, false, false, true, false},
	{"GET", "/tasks/{id}/history", domain.ScopeRead, false, false, false, false},
	{"POST", "/triage/accept-new", domain.ScopeWrite, false, false, false, false},
	{"GET", "/entries", domain.ScopeRead, false, false, false, false},
	{"POST", "/entries", domain.ScopeCapture, false, false, false, false},
	{"GET", "/entries/{id}", domain.ScopeRead, false, false, false, false},
	{"PATCH", "/entries/{id}", domain.ScopeDestroy, false, false, true, false},
	{"POST", "/entries/{id}/relink", domain.ScopeWrite, false, false, true, false},
	{"POST", "/entries/{id}/delete", domain.ScopeDestroy, false, false, true, false},
	{"POST", "/entries/{id}/undelete", domain.ScopeDestroy, false, false, true, false},
	{"GET", "/blocks", domain.ScopeRead, false, false, false, false},
	{"GET", "/blocks/{id}", domain.ScopeRead, false, false, false, false},
	{"POST", "/blocks/{id}/reblock", domain.ScopeWrite, false, false, true, false},
	{"POST", "/blocks/{id}/unblock", domain.ScopeWrite, false, false, true, false},
	{"POST", "/blocks/{id}/outcome", domain.ScopeWrite, false, false, true, false},
	{"POST", "/focus", domain.ScopeWrite, false, false, false, false},
	{"GET", "/ops", domain.ScopeRead, false, false, false, false},
	{"GET", "/ops/{op_id}", domain.ScopeRead, false, false, false, false},
	{"POST", "/ops/{op_id}/revert", 0, false, false, false, false},
	{"POST", "/undo", 0, false, false, false, false},
	{"POST", "/redo", 0, false, false, false, false},
	{"GET", "/ai-changes", domain.ScopeRead, false, false, false, false},
	{"POST", "/ai-changes/review", domain.ScopeWrite, true, false, false, false},
	{"GET", "/settings", domain.ScopeRead, false, false, false, false},
	{"PATCH", "/settings", domain.ScopeAdmin, false, true, false, false},
	{"GET", "/tokens", domain.ScopeAdmin, false, false, false, false},
	{"POST", "/tokens", domain.ScopeAdmin, false, true, false, false},
	{"POST", "/tokens/{id}/revoke", domain.ScopeAdmin, false, false, false, false},
	{"POST", "/password", domain.ScopeAdmin, true, true, false, false},
	{"GET", "/backups", domain.ScopeAdmin, false, false, false, false},
	{"POST", "/backups", domain.ScopeAdmin, false, true, false, true},
	{"GET", "/export", domain.ScopeAdmin, false, false, false, true},
}

func TestRouteInventory(t *testing.T) {
	expected := map[string]routeContract{}
	for _, r := range expectedRoutes {
		key := r.method + " /api/v1" + r.path
		if _, ok := expected[key]; ok {
			t.Fatal("duplicate expected route", key)
		}
		expected[key] = r
	}
	actual := map[string]routeContract{}
	for _, r := range new(server.Server).Routes() {
		key := r.Method + " " + r.Pattern
		if _, ok := actual[key]; ok {
			t.Fatal("duplicate route", key)
		}
		if r.Handler == nil {
			t.Fatal("missing handler", key)
		}
		actual[key] = routeContract{r.Method, r.Pattern[len("/api/v1"):], r.Scopes, r.OwnerOnly, r.Exempt, r.IfMatch, r.Long}
	}
	if !reflect.DeepEqual(actual, expected) {
		for key, want := range expected {
			if got, ok := actual[key]; !ok || got != want {
				t.Errorf("%s = %+v (present=%v), want %+v", key, got, ok, want)
			}
		}
		for key := range actual {
			if _, ok := expected[key]; !ok {
				t.Error("unexpected route", key)
			}
		}
	}
}

type matrixCall func(*client.Client) error

// Each row supplies valid transport inputs and owner-fetched entity ETags.
func matrixInputs(t *testing.T, srv *testutil.Server) map[string]matrixCall {
	t.Helper()
	ctx := t.Context()
	task := addHTTPTask(t, srv, `{"title":"matrix task"}`)
	date := civil.MustDate("2026-10-28").AddDays(int(task.ID))
	block, err := srv.Client.BlockTask(ctx, client.Mut{IfMatch: task.ETag}, task.ID, api.BlockRequest{Date: date, Start: 11 * 60, End: 12 * 60})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := srv.Client.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	task = detail.Task
	entry, err := srv.Client.AddEntry(ctx, client.Mut{}, api.AddEntryRequest{TaskID: api.Opt[int64]{Set: true, Null: true}, Title: "matrix entry"})
	if err != nil {
		t.Fatal(err)
	}
	e, err := srv.Client.GetEntry(ctx, entry.Entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := srv.Client.GetBlock(ctx, block.Block.ID)
	if err != nil {
		t.Fatal(err)
	}
	tm, em, bm := client.Mut{IfMatch: task.ETag}, client.Mut{IfMatch: e.ETag}, client.Mut{IfMatch: b.ETag}
	span := api.BlockRequest{Date: date, Start: 13 * 60, End: 14 * 60}
	return map[string]matrixCall{
		"GET /clock":       func(c *client.Client) error { _, e := c.Clock(ctx); return e },
		"GET /whoami":      func(c *client.Client) error { _, e := c.WhoAmI(ctx); return e },
		"GET /today":       func(c *client.Client) error { _, _, _, e := c.Today(ctx, ""); return e },
		"GET /status":      func(c *client.Client) error { _, _, _, e := c.Status(ctx, ""); return e },
		"GET /days/{date}": func(c *client.Client) error { _, e := c.Day(ctx, date); return e },
		"GET /upcoming":    func(c *client.Client) error { _, e := c.Upcoming(ctx); return e },
		"GET /report": func(c *client.Client) error {
			_, e := c.Report(ctx, client.ReportQuery{From: date, To: date})
			return e
		},
		"GET /search": func(c *client.Client) error {
			_, e := c.Search(ctx, client.SearchQuery{Q: "matrix", Kind: "all"})
			return e
		},
		"GET /tags":  func(c *client.Client) error { _, e := c.Tags(ctx); return e },
		"GET /tasks": func(c *client.Client) error { _, e := c.ListTasks(ctx, client.TaskQuery{View: "all"}); return e },
		"POST /tasks": func(c *client.Client) error {
			_, e := c.AddTask(ctx, client.Mut{}, api.AddTaskRequest{Title: "capture"})
			return e
		},
		"GET /tasks/{id}": func(c *client.Client) error { _, e := c.GetTask(ctx, task.ID); return e },
		"PATCH /tasks/{id}": func(c *client.Client) error {
			_, e := c.UpdateTask(ctx, tm, task.ID, api.UpdateTaskRequest{})
			return e
		},
		"POST /tasks/{id}/due": func(c *client.Client) error {
			_, e := c.SetDue(ctx, tm, task.ID, api.DueRequest{Due: api.Opt[api.Moment]{Set: true, Null: true}})
			return e
		},
		"POST /tasks/{id}/place": func(c *client.Client) error {
			_, e := c.PlaceTask(ctx, tm, task.ID, api.PlaceRequest{How: "bottom"})
			return e
		},
		"POST /tasks/{id}/defer": func(c *client.Client) error {
			_, e := c.DeferTask(ctx, tm, task.ID, api.DeferRequest{Until: api.Opt[api.Moment]{Set: true, Null: true}})
			return e
		},
		"POST /tasks/{id}/block":  func(c *client.Client) error { _, e := c.BlockTask(ctx, tm, task.ID, span); return e },
		"POST /tasks/{id}/triage": func(c *client.Client) error { _, e := c.MarkManual(ctx, tm, task.ID); return e },
		"POST /tasks/{id}/complete": func(c *client.Client) error {
			_, e := c.CompleteTask(ctx, tm, task.ID, api.CompleteRequest{})
			return e
		},
		"POST /tasks/{id}/reopen":   func(c *client.Client) error { _, e := c.ReopenTask(ctx, tm, task.ID, api.ReopenRequest{}); return e },
		"POST /tasks/{id}/drop":     func(c *client.Client) error { _, e := c.DropTask(ctx, tm, task.ID); return e },
		"POST /tasks/{id}/delete":   func(c *client.Client) error { _, e := c.DeleteTask(ctx, tm, task.ID); return e },
		"POST /tasks/{id}/undelete": func(c *client.Client) error { _, e := c.UndeleteTask(ctx, tm, task.ID); return e },
		"GET /tasks/{id}/history":   func(c *client.Client) error { _, e := c.TaskHistory(ctx, task.ID, client.PageQuery{}); return e },
		"POST /triage/accept-new":   func(c *client.Client) error { _, e := c.AcceptNew(ctx, client.Mut{}, api.AcceptNewRequest{}); return e },
		"GET /entries": func(c *client.Client) error {
			_, e := c.ListEntries(ctx, client.EntryQuery{From: &date, To: &date})
			return e
		},
		"POST /entries": func(c *client.Client) error {
			_, e := c.AddEntry(ctx, client.Mut{}, api.AddEntryRequest{TaskID: api.Opt[int64]{Set: true, Null: true}, Title: "capture"})
			return e
		},
		"GET /entries/{id}": func(c *client.Client) error { _, e := c.GetEntry(ctx, e.ID); return e },
		"PATCH /entries/{id}": func(c *client.Client) error {
			_, err := c.UpdateEntry(ctx, em, e.ID, api.UpdateEntryRequest{})
			return err
		},
		"POST /entries/{id}/relink": func(c *client.Client) error {
			_, err := c.RelinkEntry(ctx, em, e.ID, api.RelinkRequest{TaskID: api.Opt[int64]{Set: true, Null: true}})
			return err
		},
		"POST /entries/{id}/delete":   func(c *client.Client) error { _, err := c.DeleteEntry(ctx, em, e.ID); return err },
		"POST /entries/{id}/undelete": func(c *client.Client) error { _, err := c.UndeleteEntry(ctx, em, e.ID); return err },
		"GET /blocks": func(c *client.Client) error {
			_, e := c.ListBlocks(ctx, client.BlockQuery{From: &date, To: &date})
			return e
		},
		"GET /blocks/{id}": func(c *client.Client) error { _, e := c.GetBlock(ctx, b.ID); return e },
		"POST /blocks/{id}/reblock": func(c *client.Client) error {
			_, e := c.Reblock(ctx, bm, b.ID, api.ReblockRequest{BlockRequest: span, TaskETag: task.ETag})
			return e
		},
		"POST /blocks/{id}/unblock": func(c *client.Client) error { _, e := c.Unblock(ctx, bm, b.ID); return e },
		"POST /blocks/{id}/outcome": func(c *client.Client) error {
			_, e := c.SetOutcome(ctx, bm, b.ID, api.OutcomeRequest{Outcome: "auto"})
			return e
		},
		"POST /focus": func(c *client.Client) error {
			_, e := c.SetFocus(ctx, client.Mut{}, api.FocusRequest{TaskID: api.Opt[int64]{Set: true, Null: true}})
			return e
		},
		"GET /ops":                 func(c *client.Client) error { _, e := c.ListOps(ctx, client.OpsQuery{}); return e },
		"GET /ops/{op_id}":         func(c *client.Client) error { _, e := c.GetOp(ctx, *entry.OpID); return e },
		"POST /ops/{op_id}/revert": func(c *client.Client) error { _, e := c.Revert(ctx, client.Mut{}, *entry.OpID); return e },
		"POST /undo":               func(c *client.Client) error { _, e := c.Undo(ctx, client.Mut{}); return e },
		"POST /redo":               func(c *client.Client) error { _, e := c.Redo(ctx, client.Mut{}); return e },
		"GET /ai-changes":          func(c *client.Client) error { _, e := c.AIChanges(ctx, false, client.PageQuery{}); return e },
		"POST /ai-changes/review": func(c *client.Client) error {
			_, e := c.ReviewAIChanges(ctx, client.Mut{}, api.ReviewRequest{})
			return e
		},
		"GET /settings":   func(c *client.Client) error { _, e := c.Settings(ctx); return e },
		"PATCH /settings": func(c *client.Client) error { _, e := c.UpdateSettings(ctx, api.SettingsRequest{}); return e },
		"GET /tokens":     func(c *client.Client) error { _, e := c.Tokens(ctx); return e },
		"POST /tokens": func(c *client.Client) error {
			_, e := c.CreateToken(ctx, api.CreateTokenRequest{Name: "mint-" + api.NewIdempotencyKey(), Scopes: "admin", ExpiresAt: api.Opt[api.Time]{Set: true, Null: true}})
			return e
		},
		"POST /tokens/{id}/revoke": func(c *client.Client) error { _, e := c.RevokeToken(ctx, client.Mut{}, 1); return e },
		"POST /password": func(c *client.Client) error {
			return c.ChangePassword(ctx, api.PasswordRequest{Current: testutil.Password, New: testutil.Password})
		},
		"GET /backups":  func(c *client.Client) error { _, e := c.Backups(ctx); return e },
		"POST /backups": func(c *client.Client) error { _, e := c.RunBackup(ctx); return e },
		"GET /export":   func(c *client.Client) error { return c.Export(ctx, new(bytes.Buffer)) },
	}
}

func TestScopeMatrix(t *testing.T) {
	srv := testutil.StartServer(t)
	clients := map[domain.Scopes]*client.Client{}
	token := func(scopes domain.Scopes) *client.Client {
		if clients[scopes] == nil {
			clients[scopes] = srv.TokenClient(t, fmt.Sprintf("scope-%d", scopes), scopes, 0, "api")
		}
		return clients[scopes]
	}
	for _, r := range expectedRoutes {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			inputs := matrixInputs(t, srv)
			if len(inputs) != len(expectedRoutes) {
				t.Fatalf("matrix inputs=%d routes=%d", len(inputs), len(expectedRoutes))
			}
			invoke, ok := inputs[r.method+" "+r.path]
			if !ok {
				t.Fatal("missing matrix input")
			}
			if r.scopes != 0 {
				requireReadProblem(t, invoke(token(domain.AllScopes&^r.scopes)), 403, domain.CodeForbidden, "")
				if r.owner {
					requireReadProblem(t, invoke(token(domain.AllScopes)), 403, domain.CodeForbidden, "")
					if err := invoke(srv.Client); err != nil {
						t.Fatal("owner denied:", err)
					}
					return
				}
				if err := invoke(token(r.scopes)); client.IsCode(err, domain.CodeForbidden) {
					t.Fatal("exact scopes denied:", err)
				}
			} else {
				if r.path == "/ops/{op_id}/revert" {
					requireReadProblem(t, invoke(token(domain.AllScopes)), 403, domain.CodeForbidden, "")
					return
				}
				err := invoke(token(domain.ScopeCapture))
				if r.path == "/clock" || r.path == "/whoami" {
					if err != nil {
						t.Fatal(err)
					}
				} else if client.IsCode(err, domain.CodeForbidden) {
					t.Fatal(err)
				}
			}
		})
	}
	t.Run("owner placement", func(t *testing.T) {
		in := api.AddTaskRequest{Title: "placed", Place: &api.AddPlacement{How: "bottom"}}
		_, err := token(domain.AllScopes).AddTask(t.Context(), client.Mut{}, in)
		requireReadProblem(t, err, 403, domain.CodeForbidden, "")
		if _, err := srv.Client.AddTask(t.Context(), client.Mut{}, in); err != nil {
			t.Fatal(err)
		}
	})
}

func TestScopeMatrixReversalIsolation(t *testing.T) {
	srv := testutil.StartServer(t)
	owner, err := srv.Client.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: "owner history"})
	if err != nil {
		t.Fatal(err)
	}
	c := srv.TokenClient(t, "isolated", domain.AllScopes, 0, "api")
	_, err = c.Revert(t.Context(), client.Mut{}, *owner.OpID)
	requireReadProblem(t, err, 403, domain.CodeForbidden, "")
	_, err = c.Undo(t.Context(), client.Mut{})
	requireReadProblem(t, err, 409, domain.CodeNothingToUndo, "")
	_, err = c.Redo(t.Context(), client.Mut{})
	requireReadProblem(t, err, 409, domain.CodeNothingToRedo, "")
	own, err := c.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: "token history"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Undo(t.Context(), client.Mut{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Redo(t.Context(), client.Mut{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Revert(t.Context(), client.Mut{}, *own.OpID); err != nil {
		t.Fatal(err)
	}
	task, err := srv.Client.GetTask(t.Context(), owner.Task.ID)
	if err != nil || task.View == "deleted" {
		t.Fatal("token changed owner history", err)
	}
}
