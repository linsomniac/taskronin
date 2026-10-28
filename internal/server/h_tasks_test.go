package server_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/client"
	"taskronin/internal/domain"
	"taskronin/internal/testutil"
)

func mutateAPI(t *testing.T, srv *testutil.Server, method, path, body, etag string, overrides http.Header) (int, http.Header, []byte) {
	t.Helper()
	token, err := os.ReadFile(srv.Paths.OwnerToken)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", srv.Socket)
	}}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(t.Context(), method, "http://taskronin/api/v1"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Content-Type", api.ContentTypeJSON)
	req.Header.Set(api.HeaderGeneration, string(srv.Svc.Generation()))
	req.Header.Set(api.HeaderIdempotencyKey, api.NewIdempotencyKey())
	if etag != "" {
		req.Header.Set("If-Match", etag)
	}
	for key, values := range overrides {
		req.Header.Del(key)
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	resp, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, data
}

func taskOp(t *testing.T, status, want int, body []byte) api.OpResponse {
	t.Helper()
	var op api.OpResponse
	if err := json.Unmarshal(body, &op); err != nil || status != want {
		t.Fatalf("mutation = HTTP %d %s, %v; want %d", status, body, err, want)
	}
	return op
}

func taskProblem(t *testing.T, status, want int, body []byte, code domain.Code, field string) api.Problem {
	t.Helper()
	var p api.Problem
	if err := json.Unmarshal(body, &p); err != nil || status != want || p.Status != want || p.Code != string(code) {
		t.Fatalf("mutation = HTTP %d %s, %v; want %d %s", status, body, err, want, code)
	}
	requireProblemField(t, p, field)
	return p
}

func addHTTPTask(t *testing.T, srv *testutil.Server, body string) api.Task {
	t.Helper()
	status, _, data := mutateAPI(t, srv, http.MethodPost, "/tasks", body, "", nil)
	op := taskOp(t, status, 201, data)
	if op.Task == nil {
		t.Fatalf("missing task: %s", data)
	}
	return *op.Task
}

func taskPath(id int64) string { return "/tasks/" + strconv.FormatInt(id, 10) }

func TestAddTaskCreated(t *testing.T) {
	srv := testutil.StartServer(t)
	status, headers, body := mutateAPI(t, srv, http.MethodPost, "/tasks", `{"title":"capture"}`, "", nil)
	op := taskOp(t, status, 201, body)
	if headers.Get("Location") != "/api/v1/tasks/1" || op.Task == nil || op.Task.ID != 1 || op.Task.Title != "capture" || op.Task.View != "triage" || op.Task.ETag != `"`+string(srv.Svc.Generation())+`-1"` || op.Replayed {
		t.Fatalf("created = %+v, headers = %v", op, headers)
	}
	if len(op.Task.Reasons) != 1 || op.Task.Reasons[0].Reason != "new" || op.OpID == nil {
		t.Fatalf("created = %+v", op)
	}
	if id, err := hex.DecodeString(*op.OpID); err != nil || len(id) != 16 {
		t.Fatalf("op_id = %q, %v", *op.OpID, err)
	}
}

func TestAddTaskReplayAndMismatch(t *testing.T) {
	srv := testutil.StartServer(t)
	headers := http.Header{api.HeaderIdempotencyKey: {api.NewIdempotencyKey()}}
	status, _, body := mutateAPI(t, srv, http.MethodPost, "/tasks", `{"title":"capture"}`, "", headers)
	first := taskOp(t, status, 201, body)
	if first.Task == nil {
		t.Fatal("missing task")
	}
	status, _, body = mutateAPI(t, srv, http.MethodPatch, taskPath(first.Task.ID), `{"title":"edited"}`, first.Task.ETag, nil)
	edited := taskOp(t, status, 200, body)
	status, location, body := mutateAPI(t, srv, http.MethodPost, "/tasks", ` { "title" : "capture" } `, "", headers)
	replay := taskOp(t, status, 201, body)
	if !replay.Replayed || replay.Task == nil || !reflect.DeepEqual(replay.Task, edited.Task) || replay.OpID == nil || *replay.OpID != *first.OpID || location.Get("Location") != "/api/v1/tasks/1" {
		t.Fatalf("replay = %+v", replay)
	}
	status, _, body = mutateAPI(t, srv, http.MethodPost, "/tasks", `{"title":"other"}`, "", headers)
	taskProblem(t, status, 422, body, domain.CodeIdemMismatch, "")
}

func TestIDM06_IdempotencyHeader(t *testing.T) {
	srv := testutil.StartServer(t)
	for _, tc := range []struct {
		key    []string
		status int
		code   domain.Code
	}{{nil, 428, domain.CodeIdemRequired}, {[]string{"short"}, 400, domain.CodeMalformed}} {
		status, _, body := mutateAPI(t, srv, http.MethodPost, "/tasks", `{"title":"capture"}`, "", http.Header{api.HeaderIdempotencyKey: tc.key})
		taskProblem(t, status, tc.status, body, tc.code, "")
	}
}

func TestGenerationHeader(t *testing.T) {
	srv := testutil.StartServer(t)
	for _, tc := range []struct {
		gen    []string
		status int
		code   domain.Code
	}{{nil, 428, domain.CodePreconditionReq}, {[]string{string(domain.NewGeneration())}, 412, domain.CodeGenerationMismatch}} {
		status, _, body := mutateAPI(t, srv, http.MethodPost, "/tasks", `{"title":"capture"}`, "", http.Header{api.HeaderGeneration: tc.gen})
		taskProblem(t, status, tc.status, body, tc.code, "")
	}
}

func TestTRI19_IfMatchHTTP(t *testing.T) {
	srv := testutil.StartServer(t)
	task := addHTTPTask(t, srv, `{"title":"capture"}`)
	status, _, body := mutateAPI(t, srv, http.MethodPatch, taskPath(task.ID), `{"notes":"edited"}`, task.ETag, nil)
	current := taskOp(t, status, 200, body).Task
	for _, tc := range []struct {
		etag   string
		status int
		code   domain.Code
	}{{"", 428, domain.CodePreconditionReq}, {task.ETag, 412, domain.CodePrecondition}, {"*", 400, domain.CodeMalformed}} {
		status, _, body := mutateAPI(t, srv, http.MethodPatch, taskPath(task.ID), `{"title":"changed"}`, tc.etag, nil)
		p := taskProblem(t, status, tc.status, body, tc.code, "")
		if tc.status == 412 {
			var got api.Task
			if err := json.Unmarshal(p.Current, &got); err != nil || !reflect.DeepEqual(&got, current) {
				t.Fatalf("current = %s, %v; want %+v", p.Current, err, current)
			}
		}
	}
}

func TestPatchOptSemantics(t *testing.T) {
	srv := testutil.StartServer(t)
	task := addHTTPTask(t, srv, `{"title":"capture","notes":"notes","project":"work","priority":"H","tags":["old"]}`)
	status, _, body := mutateAPI(t, srv, http.MethodPatch, taskPath(task.ID), `{"project":null,"priority":null,"tags_add":["new"],"tags_remove":["old"]}`, task.ETag, nil)
	op := taskOp(t, status, 200, body)
	if op.Task == nil || op.Task.Project != nil || op.Task.Priority != nil || op.Task.Title != task.Title || op.Task.Notes != task.Notes || !reflect.DeepEqual(op.Task.Tags, []string{"new"}) {
		t.Fatalf("cleared = %+v", op.Task)
	}
	task = *op.Task
	status, _, body = mutateAPI(t, srv, http.MethodPatch, taskPath(task.ID), `{}`, task.ETag, nil)
	op = taskOp(t, status, 200, body)
	if op.OpID != nil || op.Effects == nil || len(op.Effects) != 0 || !reflect.DeepEqual(op.Task, &task) {
		t.Fatalf("no-op = %s", body)
	}
	for _, field := range []string{"title", "notes"} {
		status, _, body = mutateAPI(t, srv, http.MethodPatch, taskPath(task.ID), `{"`+field+`":null}`, task.ETag, nil)
		taskProblem(t, status, 422, body, domain.CodeValidation, field)
	}
}

func TestKeyRequiredOpts(t *testing.T) {
	srv := testutil.StartServer(t)
	task := addHTTPTask(t, srv, `{"title":"capture","due":{"date":"2026-11-02","time":"13:00"}}`)
	for _, tc := range []struct{ action, field string }{{"due", "due"}, {"defer", "until"}} {
		status, _, body := mutateAPI(t, srv, http.MethodPost, taskPath(task.ID)+"/"+tc.action, `{}`, task.ETag, nil)
		taskProblem(t, status, 422, body, domain.CodeValidation, tc.field)
	}
	status, _, body := mutateAPI(t, srv, http.MethodPost, taskPath(task.ID)+"/due", `{"due":null}`, task.ETag, nil)
	op := taskOp(t, status, 200, body)
	if op.Task == nil || op.Task.Due != nil {
		t.Fatalf("cleared due = %+v", op.Task)
	}
	status, _, body = mutateAPI(t, srv, http.MethodPost, taskPath(task.ID)+"/defer", `{"until":null}`, op.Task.ETag, nil)
	op = taskOp(t, status, 200, body)
	if op.Task == nil || op.Task.Defer != nil || op.Task.View != "backlog" || len(op.Task.Reasons) != 0 {
		t.Fatalf("clear defer placement = %+v", op.Task)
	}
}

func TestBlockCreated(t *testing.T) {
	srv := testutil.StartServer(t)
	task := addHTTPTask(t, srv, `{"title":"capture"}`)
	headers := http.Header{api.HeaderIdempotencyKey: {api.NewIdempotencyKey()}}
	for _, replayed := range []bool{false, true} {
		status, location, body := mutateAPI(t, srv, http.MethodPost, taskPath(task.ID)+"/block", `{"date":"2026-10-28","start":"11:00","end":"12:00"}`, task.ETag, headers)
		op := taskOp(t, status, 201, body)
		if op.Task == nil || op.Block == nil || op.Block.TaskID != task.ID || op.Block.TaskETag != op.Task.ETag || op.Replayed != replayed || location.Get("Location") != "/api/v1/tasks/1" {
			t.Fatalf("block = %+v, headers = %v", op, location)
		}
	}
}

func TestAcceptNewHTTP(t *testing.T) {
	srv := testutil.StartServer(t)
	first := addHTTPTask(t, srv, `{"title":"first"}`)
	second := addHTTPTask(t, srv, `{"title":"second"}`)
	request := api.AcceptNewRequest{}
	// BF12: reversed input still returns ascending new-reason sequence order.
	for _, task := range []api.Task{second, first} {
		request.Items = append(request.Items, struct {
			ID   int64  `json:"id"`
			ETag string `json:"etag"`
		}{task.ID, task.ETag})
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	headers := http.Header{api.HeaderIdempotencyKey: {api.NewIdempotencyKey()}}
	for _, replayed := range []bool{false, true} {
		status, _, data := mutateAPI(t, srv, http.MethodPost, "/triage/accept-new", string(body), "", headers)
		op := taskOp(t, status, 200, data)
		if len(op.Tasks) != 2 || op.Tasks[0].ID != first.ID || op.Tasks[1].ID != second.ID || op.Task != nil || op.Replayed != replayed {
			t.Fatalf("accept = %+v", op)
		}
		for _, task := range op.Tasks {
			if task.View != "backlog" || len(task.Reasons) != 0 {
				t.Fatalf("accepted task = %+v", task)
			}
		}
	}
	status, _, data := mutateAPI(t, srv, http.MethodPost, "/triage/accept-new", `{"items":[{"id":1,"etag":"*"}]}`, "", nil)
	taskProblem(t, status, 400, data, domain.CodeMalformed, "")
}

func TestPRG18_NoPurgeRoutes(t *testing.T) {
	srv := testutil.StartServer(t)
	for _, path := range []string{"/tasks/1/purge", "/entries/1/purge"} {
		status, _, body := mutateAPI(t, srv, http.MethodPost, path, `{}`, "", nil)
		taskProblem(t, status, 404, body, domain.CodeNotFound, "")
	}
}

func TestTokenPlacementForbiddenHTTP(t *testing.T) {
	srv := testutil.StartServer(t)
	c := srv.TokenClient(t, "writer", domain.ScopeRead|domain.ScopeCapture|domain.ScopeWrite, 100, "api")
	_, err := c.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: "placed", Place: &api.AddPlacement{How: "top"}})
	requireReadProblem(t, err, 403, domain.CodeForbidden, "")
	op, err := c.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: "captured"})
	if err != nil || op.Task == nil || op.Task.View != "triage" {
		t.Fatalf("token capture = %+v, %v", op, err)
	}
}

func TestTaskMutationClients(t *testing.T) {
	srv := testutil.StartServer(t)
	ctx, c := t.Context(), srv.Client
	next := func(op *api.OpResponse, err error) *api.OpResponse {
		t.Helper()
		if err != nil || op == nil || op.Task == nil {
			t.Fatalf("mutation = %+v, %v", op, err)
		}
		return op
	}
	due := api.Moment{Date: civil.MustDate("2026-11-03"), Time: new(civil.Minute(13 * 60))}
	op := next(c.AddTask(ctx, client.Mut{}, api.AddTaskRequest{Title: "capture", Notes: "notes", Due: &due, Project: "work", Priority: "H", Tags: []string{"tag"}}))
	if !reflect.DeepEqual(op.Task.Due, &due) || op.Task.Notes != "notes" || op.Task.Project == nil || *op.Task.Project != "work" || op.Task.Priority == nil || *op.Task.Priority != "H" || !reflect.DeepEqual(op.Task.Tags, []string{"tag"}) {
		t.Fatalf("add mapping = %+v", op.Task)
	}
	id := op.Task.ID
	mut := func() client.Mut { return client.Mut{IfMatch: op.Task.ETag} }
	op = next(c.UpdateTask(ctx, mut(), id, api.UpdateTaskRequest{Title: api.Opt[string]{Set: true, V: "edited"}, Notes: api.Opt[string]{Set: true, V: "changed"}, Project: api.Opt[string]{Set: true, V: "home"}, Priority: api.Opt[string]{Set: true, V: "L"}}))
	if op.Task.Title != "edited" || op.Task.Notes != "changed" || op.Task.Project == nil || *op.Task.Project != "home" || op.Task.Priority == nil || *op.Task.Priority != "L" {
		t.Fatalf("update mapping = %+v", op.Task)
	}
	due.Time = nil
	op = next(c.SetDue(ctx, mut(), id, api.DueRequest{Due: api.Opt[api.Moment]{Set: true, V: due}}))
	if !reflect.DeepEqual(op.Task.Due, &due) {
		t.Fatalf("date-only due = %+v", op.Task.Due)
	}
	due.Time = new(civil.Minute(14 * 60))
	op = next(c.SetDue(ctx, mut(), id, api.DueRequest{Due: api.Opt[api.Moment]{Set: true, V: due}}))
	if !reflect.DeepEqual(op.Task.Due, &due) {
		t.Fatalf("timed due = %+v", op.Task.Due)
	}
	until := api.Moment{Date: civil.MustDate("2026-10-29"), Time: new(civil.Minute(9 * 60))}
	op = next(c.DeferTask(ctx, mut(), id, api.DeferRequest{Until: api.Opt[api.Moment]{Set: true, V: until}}))
	if !reflect.DeepEqual(op.Task.Defer, &until) || !op.Task.Deferred {
		t.Fatalf("defer = %+v", op.Task)
	}
	until.Time = nil
	op = next(c.DeferTask(ctx, mut(), id, api.DeferRequest{Until: api.Opt[api.Moment]{Set: true, V: until}}))
	if !reflect.DeepEqual(op.Task.Defer, &until) {
		t.Fatalf("date-only defer = %+v", op.Task.Defer)
	}
	ref := addHTTPTask(t, srv, `{"title":"reference","place":{"how":"bottom"}}`)
	op = next(c.PlaceTask(ctx, mut(), id, api.PlaceRequest{How: "before", Ref: ref.ID}))
	if op.Task.Defer != nil || op.Task.View != "backlog" {
		t.Fatalf("place = %+v", op.Task)
	}
	backlog, err := c.ListTasks(ctx, client.TaskQuery{View: "backlog"})
	if err != nil || len(backlog.Items) != 2 || backlog.Items[0].ID != id || backlog.Items[1].ID != ref.ID {
		t.Fatalf("placement order = %+v, %v", backlog, err)
	}
	span := api.BlockRequest{Date: civil.MustDate("2026-10-28"), Start: 11 * 60, End: 12 * 60}
	op = next(c.BlockTask(ctx, mut(), id, span))
	if op.Block == nil || op.Block.Date != span.Date || op.Block.Start != span.Start || op.Block.End != span.End || op.Block.TaskETag != op.Task.ETag {
		t.Fatalf("block = %+v", op)
	}
	op = next(c.MarkManual(ctx, mut(), id))
	if len(op.Task.Reasons) != 1 || op.Task.Reasons[0].Reason != "manual" {
		t.Fatalf("manual = %+v", op.Task)
	}
	date := civil.MustDate("2026-10-27")
	op = next(c.CompleteTask(ctx, mut(), id, api.CompleteRequest{As: new("final title"), Date: &date, Log: "completion", Notes: "log notes"}))
	if op.Task.Title != "final title" || op.Task.Status != "done" || op.Entry == nil || op.Entry.Title != "completion" || op.Entry.Notes != "log notes" || op.Entry.WorkDate != date || op.Entry.TaskTitle == nil || *op.Entry.TaskTitle != "final title" {
		t.Fatalf("complete = %+v", op)
	}
	entryID := op.Entry.ID
	op = next(c.ReopenTask(ctx, mut(), id, api.ReopenRequest{RemoveEntry: true}))
	if op.Task.Status != "open" {
		t.Fatalf("reopen = %+v", op.Task)
	}
	entry, err := c.GetEntry(ctx, entryID)
	if err != nil || entry.DeletedAt == nil {
		t.Fatalf("removed completion = %+v, %v", entry, err)
	}
	op = next(c.DropTask(ctx, mut(), id))
	if op.Task.Status != "dropped" {
		t.Fatalf("drop = %+v", op.Task)
	}
	op = next(c.DeleteTask(ctx, mut(), id))
	if op.Task.DeletedAt == nil {
		t.Fatalf("delete = %+v", op.Task)
	}
	op = next(c.UndeleteTask(ctx, mut(), id))
	if op.Task.DeletedAt != nil || op.Task.Status != "dropped" {
		t.Fatalf("undelete = %+v", op.Task)
	}
	op = next(c.AddTask(ctx, client.Mut{}, api.AddTaskRequest{Title: "accept"}))
	r := api.AcceptNewRequest{}
	r.Items = append(r.Items, struct {
		ID   int64  `json:"id"`
		ETag string `json:"etag"`
	}{op.Task.ID, op.Task.ETag})
	accepted, err := c.AcceptNew(ctx, client.Mut{IfMatch: "unused"}, r)
	if err != nil || len(accepted.Tasks) != 1 || accepted.Tasks[0].ID != op.Task.ID || accepted.Tasks[0].View != "backlog" || accepted.Task != nil {
		t.Fatalf("accept client = %+v, %v", accepted, err)
	}
}

func TestAddTaskPlacementMappings(t *testing.T) {
	srv := testutil.StartServer(t)
	ref := addHTTPTask(t, srv, `{"title":"reference","place":{"how":"bottom"}}`)
	until := api.Moment{Date: civil.MustDate("2026-10-29"), Time: new(civil.Minute(9 * 60))}
	span := api.BlockRequest{Date: civil.MustDate("2026-10-28"), Start: 11 * 60, End: 12 * 60}
	for _, place := range []api.AddPlacement{
		{How: "top"}, {How: "bottom"}, {How: "before", Ref: ref.ID}, {How: "after", Ref: ref.ID},
		{How: "defer", Defer: &until}, {How: "block", Block: &span}, {How: "now"},
	} {
		t.Run(place.How, func(t *testing.T) {
			op, err := srv.Client.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: place.How, Place: &place})
			if err != nil || op.Task == nil || len(op.Task.Reasons) != 0 {
				t.Fatalf("placement = %+v, %v", op, err)
			}
			if place.How == "defer" && !reflect.DeepEqual(op.Task.Defer, &until) {
				t.Fatalf("defer mapping = %+v", op.Task)
			}
			if place.How == "block" {
				detail, err := srv.Client.GetTask(t.Context(), op.Task.ID)
				if err != nil || len(detail.Blocks) != 1 || detail.Blocks[0].Date != span.Date || detail.Blocks[0].Start != span.Start || detail.Blocks[0].End != span.End {
					t.Fatalf("block mapping = %+v, %v", detail, err)
				}
			}
			if place.How == "now" {
				today, _, _, err := srv.Client.Today(t.Context(), "")
				if err != nil || today.Focus == nil || today.Focus.ID != op.Task.ID {
					t.Fatalf("focus placement = %+v, %v", today, err)
				}
			}
		})
	}
}
