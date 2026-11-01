package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/client"
	"taskronin/internal/datespec"
	"taskronin/internal/domain"
	"taskronin/internal/testutil"
)

var readTools = []string{"today", "list_tasks", "get_task", "search", "work_report"}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// connect builds an MCP server over c and returns a connected client session.
func connect(t *testing.T, c *client.Client) *mcp.ClientSession {
	t.Helper()
	srv, err := New(t.Context(), Options{Client: c, Logger: quietLogger()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func aiSession(t *testing.T) (*testutil.Server, *mcp.ClientSession) {
	t.Helper()
	srv := testutil.StartServer(t)
	ai := srv.TokenClient(t, "ai", domain.MCPDefaultScopes, 60, "mcp")
	return srv, connect(t, ai)
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	return res
}

func structured(t *testing.T, res *mcp.CallToolResult) []byte {
	t.Helper()
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// requireMirror checks that Content[0] is a TextContent carrying the same JSON as StructuredContent.
func requireMirror(t *testing.T, res *mcp.CallToolResult) {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("no content")
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("Content[0] is %T, want *mcp.TextContent", res.Content[0])
	}
	var fromText, fromStructured any
	if err := json.Unmarshal([]byte(text.Text), &fromText); err != nil {
		t.Fatalf("text content is not JSON: %v", err)
	}
	if err := json.Unmarshal(structured(t, res), &fromStructured); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromText, fromStructured) {
		t.Fatalf("text %v != structured %v", fromText, fromStructured)
	}
}

func requireToolError(t *testing.T, res *mcp.CallToolResult, code string) map[string]any {
	t.Helper()
	if !res.IsError {
		t.Fatalf("expected IsError, got %s", structured(t, res))
	}
	requireMirror(t, res)
	var got map[string]any
	if err := json.Unmarshal(structured(t, res), &got); err != nil {
		t.Fatal(err)
	}
	if got["code"] != code {
		t.Fatalf("code = %v, want %s (%v)", got["code"], code, got)
	}
	return got
}

func TestSCP13_OwnerTokenRefused(t *testing.T) {
	srv := testutil.StartServer(t)
	// The owner client on channel "cli" sees owner:true.
	if _, err := New(t.Context(), Options{Client: srv.Client, Logger: quietLogger()}); err == nil || !strings.Contains(err.Error(), "needs its own token") {
		t.Fatalf("cli channel owner: err = %v", err)
	}
	// The same owner token on channel "mcp" is refused by the server itself.
	cfg := srv.Paths.OwnerToken
	mcpOwner, err := client.New(client.Options{Socket: srv.Socket, TokenFile: cfg, Channel: "mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(t.Context(), Options{Client: mcpOwner, Logger: quietLogger()}); err == nil || !strings.Contains(err.Error(), "needs its own token") {
		t.Fatalf("mcp channel owner: err = %v", err)
	}
	// Raw request: owner token + channel mcp → 403 forbidden.
	token, err := readFileTrim(cfg)
	if err != nil {
		t.Fatal(err)
	}
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", srv.Socket)
	}}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://tkr/api/v1/today", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Taskronin-Channel", "mcp")
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	var p api.Problem
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	if p.Code != string(domain.CodeForbidden) {
		t.Fatalf("code = %q", p.Code)
	}
}

func TestReadToolsList(t *testing.T) {
	_, cs := aiSession(t)
	list, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*mcp.Tool{}
	for _, tool := range list.Tools {
		got[tool.Name] = tool
	}
	for _, name := range readTools {
		tool := got[name]
		if tool == nil {
			t.Errorf("missing tool %s", name)
			continue
		}
		a := tool.Annotations
		if a == nil || !a.ReadOnlyHint || a.OpenWorldHint == nil || *a.OpenWorldHint {
			t.Errorf("%s annotations = %+v, want readOnly and openWorld=false", name, a)
		}
	}
	if len(got) != len(readTools) {
		t.Errorf("tools = %d, want %d", len(got), len(readTools))
	}
}

func TestReadSchemasGolden(t *testing.T) {
	_, cs := aiSession(t)
	list, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range list.Tools {
		for kind, schema := range map[string]any{"input": tool.InputSchema, "output": tool.OutputSchema} {
			b, err := json.MarshalIndent(schema, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			testutil.Golden(t, "mcp_"+tool.Name+"_"+kind, append(b, '\n'))
		}
	}
	// civil.Date fields are plain date strings.
	today := toolByName(list.Tools, "work_report")
	out, _ := json.Marshal(today.OutputSchema)
	if !strings.Contains(string(out), `"format":"date"`) {
		t.Errorf("work_report output schema lacks date format: %s", out)
	}
}

func toolByName(tools []*mcp.Tool, name string) *mcp.Tool {
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	return nil
}

func TestStructuredErrorThroughSession(t *testing.T) {
	_, cs := aiSession(t)
	res := call(t, cs, "get_task", map[string]any{"id": 999})
	got := requireToolError(t, res, "not_found")
	if _, ok := got["detail"]; !ok {
		t.Fatalf("no detail: %v", got)
	}
}

func TestTextMirrorOnSuccess(t *testing.T) {
	_, cs := aiSession(t)
	res := call(t, cs, "today", map[string]any{})
	if res.IsError {
		t.Fatalf("error: %s", structured(t, res))
	}
	requireMirror(t, res)
}

func TestTodayTool(t *testing.T) {
	_, cs := aiSession(t)
	res := call(t, cs, "today", map[string]any{})
	var today api.Today
	if err := json.Unmarshal(structured(t, res), &today); err != nil {
		t.Fatal(err)
	}
	if want := civil.MustDate("2026-10-28"); today.Clock.Today != want {
		t.Fatalf("today = %v, want %v", today.Clock.Today, want)
	}
}

func TestListTasksLimit(t *testing.T) {
	srv, cs := aiSession(t)
	res := call(t, cs, "list_tasks", map[string]any{"limit": 201})
	requireToolError(t, res, "validation")
	if hitTasks(srv.Log.String()) {
		t.Fatalf("limit 201 reached the API:\n%s", srv.Log.String())
	}
	for _, bad := range []any{0, -1} {
		requireToolError(t, call(t, cs, "list_tasks", map[string]any{"limit": bad}), "validation")
	}
	res = call(t, cs, "list_tasks", map[string]any{})
	if res.IsError {
		t.Fatalf("error: %s", structured(t, res))
	}
	var page api.Page[api.Task]
	if err := json.Unmarshal(structured(t, res), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) > 50 {
		t.Fatalf("items = %d, want <= 50", len(page.Items))
	}
	requireMirror(t, res)
	// Positive control: the log format really does record task list calls.
	if !hitTasks(srv.Log.String()) {
		t.Fatalf("valid list_tasks call not visible in server log:\n%s", srv.Log.String())
	}
}

// hitTasks reports whether the server log records a GET of the task list route.
func hitTasks(log string) bool {
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "GET") && strings.Contains(line, "/api/v1/tasks") {
			return true
		}
	}
	return false
}

func TestWorkReportDefaults(t *testing.T) {
	_, cs := aiSession(t)
	res := call(t, cs, "work_report", map[string]any{})
	if res.IsError {
		t.Fatalf("error: %s", structured(t, res))
	}
	var rep api.Report
	if err := json.Unmarshal(structured(t, res), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.From != civil.MustDate("2026-10-26") || rep.To != civil.MustDate("2026-10-28") {
		t.Fatalf("range = %v..%v, want 2026-10-26..2026-10-28", rep.From, rep.To)
	}
	requireMirror(t, res)
}

func TestWorkReportRejectsRFC3339(t *testing.T) {
	_, cs := aiSession(t)
	for _, from := range []string{"2026-10-26T00:00:00Z", "last monday", "9am"} {
		res := call(t, cs, "work_report", map[string]any{"from": from})
		got := requireToolError(t, res, "validation")
		if detail, _ := got["detail"].(string); !strings.Contains(detail, "YYYY-MM-DD") {
			t.Errorf("from %q: detail %q lacks YYYY-MM-DD", from, detail)
		}
	}
}

func TestSearchAndGetTask(t *testing.T) {
	srv, cs := aiSession(t)
	if _, err := srv.Client.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: "write the quarterly report"}); err != nil {
		t.Fatal(err)
	}
	res := call(t, cs, "search", map[string]any{"query": "quarterly"})
	if res.IsError {
		t.Fatalf("search: %s", structured(t, res))
	}
	var page api.Page[api.SearchHit]
	if err := json.Unmarshal(structured(t, res), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("hits = %d, want 1", len(page.Items))
	}
	res = call(t, cs, "get_task", map[string]any{"id": page.Items[0].ID})
	if res.IsError {
		t.Fatalf("get_task: %s", structured(t, res))
	}
	var detail api.TaskDetail
	if err := json.Unmarshal(structured(t, res), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Title != "write the quarterly report" {
		t.Fatalf("title = %q", detail.Title)
	}
	// Unknown arguments are rejected as validation errors, not protocol errors.
	requireToolError(t, call(t, cs, "get_task", map[string]any{"id": 1, "bogus": true}), "validation")
	requireToolError(t, call(t, cs, "search", map[string]any{"query": "x", "kind": "nope"}), "validation")
}

func readFileTrim(path string) (string, error) {
	b, err := os.ReadFile(path)
	return strings.TrimSpace(string(b)), err
}

func TestResolveDate(t *testing.T) {
	srv := testutil.StartServer(t)
	ai := srv.TokenClient(t, "ai", domain.MCPDefaultScopes, 60, "mcp")
	for _, tc := range []struct {
		in   string
		mode datespec.Mode
		want string
	}{
		{"today", datespec.Past, "2026-10-28"},
		{"friday", datespec.Past, "2026-10-23"},
		{"+3d", datespec.Future, "2026-10-31"},
		{"2026-11-02 09:30", datespec.Future, "2026-11-02"},
	} {
		m, err := resolveDate(t.Context(), ai, tc.in, tc.mode)
		if err != nil || m.Date.String() != tc.want {
			t.Errorf("resolveDate(%q) = %v, %v; want %s", tc.in, m.Date, err, tc.want)
		}
	}
	_, err := resolveDate(t.Context(), ai, "next friday", datespec.Future)
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeValidation || !strings.HasPrefix(de.Detail, `invalid date "next friday": use today, yesterday, tomorrow, a weekday (mon…sun), +3d/-2d/+2w or YYYY-MM-DD, optionally followed by HH:MM`) {
		t.Errorf("err = %v", err)
	}
}

func TestToolErrorRateLimited(t *testing.T) {
	res := toolError(&client.ProblemError{Status: 429, Problem: api.Problem{Code: "rate_limited", Detail: "x", RetryAfter: 42}})
	got := requireToolError(t, res, "rate_limited")
	if got["detail"] != "rate limit reached; retry after 42 s" || got["retry_after"] != float64(42) {
		t.Fatalf("got %v", got)
	}
}
