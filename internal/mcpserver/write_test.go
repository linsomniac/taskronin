package mcpserver

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/client"
	"taskronin/internal/domain"
	"taskronin/internal/testutil"
)

var writeTools = []string{"add_task", "log_work", "complete_task", "reopen_task", "defer_task", "set_due", "block_time", "update_task", "place_task"}

func listTools(t *testing.T, cs *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()
	list, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*mcp.Tool{}
	for _, tool := range list.Tools {
		got[tool.Name] = tool
	}
	return got
}

// seedTask adds a task as the owner and returns its id.
func seedTask(t *testing.T, srv *testutil.Server, title string) int64 {
	t.Helper()
	r, err := srv.Client.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: title})
	if err != nil {
		t.Fatal(err)
	}
	return r.Task.ID
}

func okOp(t *testing.T, res *mcp.CallToolResult) api.OpResponse {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool error: %s", structured(t, res))
	}
	requireMirror(t, res)
	var op api.OpResponse
	if err := json.Unmarshal(structured(t, res), &op); err != nil {
		t.Fatal(err)
	}
	return op
}

// countRequests counts the token:ai requests the server logged for a route
// pattern such as "GET /api/v1/tasks/{id}".
func countRequests(log, route string) int {
	n := 0
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, `"route":"`+route+`"`) && strings.Contains(line, `"actor":"token:ai"`) {
			n++
		}
	}
	return n
}

func TestToolsList(t *testing.T) {
	_, cs := aiSession(t)
	got := listTools(t, cs)
	want := append(append([]string{}, readTools...), writeTools...)
	var names []string
	for n := range got {
		names = append(names, n)
	}
	slices.Sort(names)
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
	for name, tool := range got {
		a := tool.Annotations
		if a == nil || a.OpenWorldHint == nil || *a.OpenWorldHint {
			t.Errorf("%s: OpenWorldHint must be explicitly false: %+v", name, a)
			continue
		}
		if slices.Contains(readTools, name) {
			if !a.ReadOnlyHint {
				t.Errorf("%s: want ReadOnlyHint", name)
			}
			continue
		}
		if a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint {
			t.Errorf("%s: write tool must be non-destructive: %+v", name, a)
		}
		if wantIdem := name == "set_due"; a.IdempotentHint != wantIdem {
			t.Errorf("%s: IdempotentHint = %v, want %v", name, a.IdempotentHint, wantIdem)
		}
		if strings.Contains(name, "drop") || strings.Contains(name, "delete") || strings.Contains(name, "purge") || strings.Contains(name, "remove") {
			t.Errorf("forbidden tool registered: %s", name)
		}
	}
	// reopen_task has no remove_entry option.
	if _, ok := got["reopen_task"].InputSchema.(map[string]any)["properties"].(map[string]any)["remove_entry"]; ok {
		t.Error("reopen_task exposes remove_entry")
	}
}

func TestWriteSchemasGolden(t *testing.T) {
	_, cs := aiSession(t)
	got := listTools(t, cs)
	for _, name := range writeTools {
		tool := got[name]
		if tool == nil {
			t.Fatalf("missing tool %s", name)
		}
		for kind, schema := range map[string]any{"input": tool.InputSchema, "output": tool.OutputSchema} {
			b, err := json.MarshalIndent(schema, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			testutil.Golden(t, "mcp_"+name+"_"+kind, append(b, '\n'))
		}
	}
	in, _ := json.Marshal(got["log_work"].InputSchema)
	var s struct {
		Properties map[string]struct {
			Type any `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(in, &s); err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(s.Properties["task_id"].Type); string(b) != `["integer","null"]` {
		t.Errorf("log_work task_id type = %s", b)
	}
	if !slices.Contains(s.Required, "task_id") {
		t.Errorf("log_work required = %v, want task_id", s.Required)
	}
	for _, name := range []string{"add_task", "log_work"} {
		b, _ := json.Marshal(got[name].InputSchema)
		if strings.Contains(string(b), "expected_etag") {
			t.Errorf("%s input has expected_etag", name)
		}
	}
	for _, name := range writeTools[2:] {
		b, _ := json.Marshal(got[name].InputSchema)
		if !strings.Contains(string(b), "expected_etag") {
			t.Errorf("%s input lacks expected_etag", name)
		}
	}
}

func TestCreationToolsDoNotFetch(t *testing.T) {
	srv, cs := aiSession(t)
	id := seedTask(t, srv, "existing")
	before := countRequests(srv.Log.String(), "GET /api/v1/tasks/{id}")
	okOp(t, call(t, cs, "add_task", map[string]any{"title": "from ai"}))
	okOp(t, call(t, cs, "log_work", map[string]any{"task_id": id, "title": "did a thing"}))
	if after := countRequests(srv.Log.String(), "GET /api/v1/tasks/{id}"); after != before {
		t.Fatalf("creation tools fetched the task: %d -> %d GETs\n%s", before, after, srv.Log.String())
	}
	if countRequests(srv.Log.String(), "POST /api/v1/tasks") == 0 || countRequests(srv.Log.String(), "POST /api/v1/entries") == 0 {
		t.Fatalf("positive control: POSTs not visible in log:\n%s", srv.Log.String())
	}
	// A mutation of an existing task without expected_etag: exactly one GET, then the POST.
	okOp(t, call(t, cs, "complete_task", map[string]any{"id": id}))
	if got := countRequests(srv.Log.String(), "GET /api/v1/tasks/{id}") - before; got != 1 {
		t.Fatalf("complete_task GETs = %d, want 1\n%s", got, srv.Log.String())
	}
	log := srv.Log.String()
	if countRequests(log, "POST /api/v1/tasks/{id}/complete") != 1 {
		t.Fatalf("expected one POST /complete:\n%s", log)
	}
	// With a (fresh) expected_etag there is no fetch at all.
	d, err := srv.Client.GetTask(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	mark := countRequests(srv.Log.String(), "GET /api/v1/tasks/{id}")
	okOp(t, call(t, cs, "reopen_task", map[string]any{"id": id, "expected_etag": d.ETag}))
	if got := countRequests(srv.Log.String(), "GET /api/v1/tasks/{id}"); got != mark {
		t.Fatalf("reopen_task with expected_etag fetched: %d -> %d", mark, got)
	}
}

func TestAddTaskToolLandsInTriage(t *testing.T) {
	srv, cs := aiSession(t)
	tool := listTools(t, cs)["add_task"]
	props := tool.InputSchema.(map[string]any)["properties"].(map[string]any)
	if _, ok := props["place"]; ok {
		t.Fatal("add_task schema has a place property")
	}
	requireToolError(t, call(t, cs, "add_task", map[string]any{"title": "x", "place": map[string]any{"how": "top"}}), "validation")
	op := okOp(t, call(t, cs, "add_task", map[string]any{"title": "from ai", "tags": []string{"a"}, "priority": "H", "due": "+3d"}))
	if op.Task == nil || op.Task.View != "triage" {
		t.Fatalf("task = %+v, want view triage", op.Task)
	}
	if op.Task.Due == nil || op.Task.Due.Date != civil.MustDate("2026-10-31") {
		t.Fatalf("due = %+v", op.Task.Due)
	}
	// AIDEV-NOTE: SCP-03. The owner sees the op in AI changes attributed to the token.
	owner := srv.Client
	changes, err := owner.AIChanges(t.Context(), true, client.PageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range changes.Ops {
		if o.Actor == "token:ai" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no op by token:ai in AI changes: %+v", changes.Ops)
	}
}

func TestLogWorkTool(t *testing.T) {
	srv, cs := aiSession(t)
	id := seedTask(t, srv, "t")
	requireToolError(t, call(t, cs, "log_work", map[string]any{"title": "no task id"}), "validation")
	from, to := civil.MustDate("2026-10-01"), civil.MustDate("2026-10-31")
	if page, err := srv.Client.ListEntries(t.Context(), client.EntryQuery{From: &from, To: &to}); err != nil || len(page.Items) != 0 {
		t.Fatalf("entries after rejected call = %v, %v", page, err)
	}
	op := okOp(t, call(t, cs, "log_work", map[string]any{"task_id": nil, "title": "standalone"}))
	if op.Entry == nil || op.Entry.TaskID != nil {
		t.Fatalf("entry = %+v, want standalone", op.Entry)
	}
	op = okOp(t, call(t, cs, "log_work", map[string]any{"task_id": id, "title": "linked", "date": "mon", "time": "09:30"}))
	e := op.Entry
	if e == nil || e.TaskID == nil || *e.TaskID != id || e.WorkDate != civil.MustDate("2026-10-26") || e.Time == nil || e.Time.String() != "09:30" {
		t.Fatalf("entry = %+v", e)
	}
	requireToolError(t, call(t, cs, "log_work", map[string]any{"task_id": id, "title": "x", "time": "9am"}), "validation")
}

func TestDeferAndDueNone(t *testing.T) {
	srv, cs := aiSession(t)
	id := seedTask(t, srv, "t")
	op := okOp(t, call(t, cs, "defer_task", map[string]any{"id": id, "until": "fri"}))
	if op.Task.Defer == nil || op.Task.Defer.Date != civil.MustDate("2026-10-30") {
		t.Fatalf("defer = %+v", op.Task.Defer)
	}
	op = okOp(t, call(t, cs, "defer_task", map[string]any{"id": id, "until": "none"}))
	if op.Task.Defer != nil {
		t.Fatalf("defer = %+v, want cleared", op.Task.Defer)
	}
	op = okOp(t, call(t, cs, "set_due", map[string]any{"id": id, "due": "tomorrow 14:00"}))
	if op.Task.Due == nil || op.Task.Due.Date != civil.MustDate("2026-10-29") || op.Task.Due.Time == nil {
		t.Fatalf("due = %+v", op.Task.Due)
	}
	op = okOp(t, call(t, cs, "set_due", map[string]any{"id": id, "due": "none"}))
	if op.Task.Due != nil {
		t.Fatalf("due = %+v, want cleared", op.Task.Due)
	}
}

func TestBlockTimeDefaults(t *testing.T) {
	srv, cs := aiSession(t)
	id := seedTask(t, srv, "t")
	op := okOp(t, call(t, cs, "block_time", map[string]any{"task_id": id, "start": "13:00", "end": "15:00"}))
	b := op.Block
	if b == nil || b.Date != civil.MustDate("2026-10-28") || b.Start.String() != "13:00" || b.End.String() != "15:00" {
		t.Fatalf("block = %+v", b)
	}
	op = okOp(t, call(t, cs, "block_time", map[string]any{"task_id": id, "date": "fri", "start": "22:00", "end": "24:00"}))
	if op.Block == nil || op.Block.End.String() != "24:00" {
		t.Fatalf("block = %+v", op.Block)
	}
	requireToolError(t, call(t, cs, "block_time", map[string]any{"task_id": id, "start": "25:00", "end": "26:00"}), "validation")
}

func TestUpdateTaskNulls(t *testing.T) {
	srv, cs := aiSession(t)
	id := seedTask(t, srv, "t")
	op := okOp(t, call(t, cs, "update_task", map[string]any{"id": id, "project": "infra", "priority": "H", "tags_add": []string{"x"}}))
	if op.Task.Project == nil || *op.Task.Project != "infra" || op.Task.Priority == nil || *op.Task.Priority != "H" {
		t.Fatalf("task = %+v", op.Task)
	}
	op = okOp(t, call(t, cs, "update_task", map[string]any{"id": id, "project": nil, "priority": nil}))
	if op.Task.Project != nil || op.Task.Priority != nil {
		t.Fatalf("project/priority not cleared: %+v", op.Task)
	}
	// Absent keys leave fields alone.
	okOp(t, call(t, cs, "update_task", map[string]any{"id": id, "project": "infra"}))
	op = okOp(t, call(t, cs, "update_task", map[string]any{"id": id, "title": "renamed"}))
	if op.Task.Project == nil || op.Task.Title != "renamed" {
		t.Fatalf("task = %+v, want project kept", op.Task)
	}
	requireToolError(t, call(t, cs, "update_task", map[string]any{"id": id, "priority": "X"}), "validation")
	requireToolError(t, call(t, cs, "update_task", map[string]any{"id": id, "title": nil}), "validation")
}

func TestPlaceTask(t *testing.T) {
	srv, cs := aiSession(t)
	a, b := seedTask(t, srv, "a"), seedTask(t, srv, "b")
	okOp(t, call(t, cs, "place_task", map[string]any{"id": a, "how": "after", "ref_id": b}))
	requireToolError(t, call(t, cs, "place_task", map[string]any{"id": a, "how": "sideways"}), "validation")
}

func TestExpectedETagStale(t *testing.T) {
	srv, cs := aiSession(t)
	id := seedTask(t, srv, "t")
	d, err := srv.Client.GetTask(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Client.UpdateTask(t.Context(), client.Mut{IfMatch: d.ETag}, id, api.UpdateTaskRequest{Title: api.Opt[string]{Set: true, V: "owner edit"}}); err != nil {
		t.Fatal(err)
	}
	got := requireToolError(t, call(t, cs, "complete_task", map[string]any{"id": id, "expected_etag": d.ETag}), "precondition_failed")
	cur, ok := got["current"].(map[string]any)
	if !ok || cur["title"] != "owner edit" {
		t.Fatalf("current = %v, want the task as the owner edited it", got["current"])
	}
}

func TestSCP10_RateCapMessage(t *testing.T) {
	srv := testutil.StartServer(t)
	cs := connect(t, srv.TokenClient(t, "capped", domain.MCPDefaultScopes, 2, "mcp"))
	okOp(t, call(t, cs, "add_task", map[string]any{"title": "one"}))
	okOp(t, call(t, cs, "add_task", map[string]any{"title": "two"}))
	got := requireToolError(t, call(t, cs, "add_task", map[string]any{"title": "three"}), "rate_limited")
	if !regexp.MustCompile(`^rate limit reached; retry after [1-9][0-9]* s$`).MatchString(got["detail"].(string)) {
		t.Fatalf("detail = %q", got["detail"])
	}
	if ra, _ := got["retry_after"].(float64); ra <= 0 {
		t.Fatalf("retry_after = %v", got["retry_after"])
	}
}

func TestWriteToolsRejectLLMDates(t *testing.T) {
	srv, cs := aiSession(t)
	id := seedTask(t, srv, "t")
	before, err := srv.Client.GetTask(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"defer_task", map[string]any{"id": id, "until": "2026-10-30T09:00:00-06:00"}},
		{"set_due", map[string]any{"id": id, "due": "next friday"}},
		{"block_time", map[string]any{"task_id": id, "date": "tomorrow 9am", "start": "09:00", "end": "10:00"}},
		{"log_work", map[string]any{"task_id": id, "title": "x", "date": "2026-10-27T10:00:00Z"}},
		{"add_task", map[string]any{"title": "x", "due": "9am next week"}},
		{"complete_task", map[string]any{"id": id, "date": "last tuesday"}},
	} {
		got := requireToolError(t, call(t, cs, c.tool, c.args), "validation")
		if d, _ := got["detail"].(string); !strings.Contains(d, "YYYY-MM-DD") {
			t.Errorf("%s: detail %q lacks accepted forms", c.tool, d)
		}
	}
	after, err := srv.Client.GetTask(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != before.Version || len(after.Entries) != 0 || len(after.Blocks) != 0 {
		t.Fatalf("task changed: version %d -> %d, entries %d, blocks %d", before.Version, after.Version, len(after.Entries), len(after.Blocks))
	}
	if page, err := srv.Client.ListTasks(t.Context(), client.TaskQuery{View: "all"}); err != nil || len(page.Items) != 1 {
		t.Fatalf("tasks = %v, %v; want only the seed", page, err)
	}
}

func TestForbiddenMapped(t *testing.T) {
	srv := testutil.StartServer(t)
	cs := connect(t, srv.TokenClient(t, "reader", domain.ScopeRead, 60, "mcp"))
	requireToolError(t, call(t, cs, "add_task", map[string]any{"title": "x"}), "forbidden")
}

// TestNoDeleteToolRegistered proves MCP never deletes: no tool can drop,
// delete, purge or remove anything, whatever the token's scopes.
func TestNoDeleteToolRegistered(t *testing.T) {
	srv := testutil.StartServer(t)
	cs := connect(t, srv.TokenClient(t, "all", domain.AllScopes&^domain.ScopeAdmin, 60, "mcp"))
	for name := range listTools(t, cs) {
		for _, bad := range []string{"drop", "delete", "purge", "remove", "destroy", "undelete"} {
			if strings.Contains(name, bad) {
				t.Errorf("tool %q looks destructive", name)
			}
		}
	}
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "delete_task", Arguments: map[string]any{"id": 1}})
	if err == nil && !res.IsError {
		t.Fatal("delete_task call succeeded")
	}
}
