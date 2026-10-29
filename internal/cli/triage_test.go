package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/client"
)

func TestTaskLine(t *testing.T) {
	today := civil.MustDate("2026-10-28")
	project := "infra.keycloak"
	task := api.Task{
		ID: 42, Title: "Set up keycloak postgres cluster", Reasons: []api.Reason{{Reason: "slipped"}},
		Slips: api.Slips{Lifetime: 2, SinceTriage: 1}, Project: &project, Tags: []string{"db"},
		Due: &api.Moment{Date: civil.MustDate("2026-10-30")},
	}
	want := "#42  [slipped×1] Set up keycloak postgres cluster  infra.keycloak +db  due Fri"
	if got := taskLine(task, today); got != want {
		t.Fatalf("taskLine=%q; want %q", got, want)
	}
	for _, tc := range []struct {
		name string
		task api.Task
		want string
	}{
		{"plain", api.Task{ID: 1, Title: "Plain"}, "#1  Plain"},
		{"reasons", api.Task{ID: 1, Title: "Review", Reasons: []api.Reason{{Reason: "due"}, {Reason: "slipped"}, {Reason: "manual"}}, Slips: api.Slips{Lifetime: 9, SinceTriage: 2}}, "#1  [due,slipped×2,manual] Review"},
		{"tags", api.Task{ID: 1, Title: "Tags", Tags: []string{"db", "infra"}}, "#1  Tags  +db +infra"},
		{"today", api.Task{ID: 1, Title: "Today", Due: &api.Moment{Date: today}}, "#1  Today  due Wed"},
		{"six days", api.Task{ID: 1, Title: "Soon", Due: &api.Moment{Date: today.AddDays(6)}}, "#1  Soon  due Tue"},
		{"seven days", api.Task{ID: 1, Title: "Later", Due: &api.Moment{Date: today.AddDays(7)}}, "#1  Later  due 2026-11-04"},
		{"far date", api.Task{ID: 1, Title: "Later", Due: &api.Moment{Date: civil.MustDate("2026-11-30")}}, "#1  Later  due 2026-11-30"},
		{"overdue today", api.Task{ID: 1, Title: "Late", DuePhase: "overdue", Due: &api.Moment{Date: today}}, "#1  Late  overdue"},
		{"past date", api.Task{ID: 1, Title: "Closed", DuePhase: "none", Due: &api.Moment{Date: today.AddDays(-1)}}, "#1  Closed  due 2026-10-27"},
		{"width boundary", api.Task{ID: 1, Title: strings.Repeat("x", 40)}, "#1  " + strings.Repeat("x", 40)},
		{"wide title", api.Task{ID: 1, Title: strings.Repeat("界", 21)}, "#1  " + strings.Repeat("界", 19) + "…"},
		{"unsafe text", api.Task{ID: 1, Title: "Title\n\x1b", Project: new("p\tq"), Tags: []string{"t\x1b"}, Reasons: []api.Reason{{Reason: "new\n"}}}, "#1  [new�] Title��  p�q +t�"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := taskLine(tc.task, today); got != tc.want {
				t.Errorf("taskLine=%q; want %q", got, tc.want)
			}
		})
	}
}

func TestTriageList(t *testing.T) {
	srv, sys := captureServer(t)
	captureTask(t, srv, "First new")
	task := scheduleTask(t, srv, "Manual")
	if _, err := srv.Client.MarkManual(t.Context(), client.Mut{IfMatch: task.ETag}, task.ID); err != nil {
		t.Fatal(err)
	}
	scheduleTask(t, srv, "Backlog is excluded")
	want := "#2  [manual] Manual\n#1  [new] First new\n"
	if out := captureRun(t, sys, "", "triage"); out != want {
		t.Fatalf("stdout=%q; want %q", out, want)
	}
	out := captureRun(t, sys, "", "triage", "--json")
	var page api.Page[api.Task]
	if err := json.Unmarshal([]byte(out), &page); err != nil || len(page.Items) != 2 || page.Items[0].ID != 2 || page.Items[1].ID != 1 {
		t.Fatalf("page=%+v err=%v stdout=%q", page, err, out)
	}
}

func TestTriageAcceptNew(t *testing.T) {
	srv, sys := captureServer(t)
	captureTask(t, srv, "First")
	captureTask(t, srv, "Second")
	task := captureTask(t, srv, "Needs manual attention")
	if _, err := srv.Client.MarkManual(t.Context(), client.Mut{IfMatch: task.ETag}, task.ID); err != nil {
		t.Fatal(err)
	}
	if out := captureRun(t, sys, "", "triage", "--accept-new"); out != "accepted 2 new tasks into the backlog\n" {
		t.Fatalf("stdout=%q", out)
	}
	page, err := srv.Client.ListTasks(t.Context(), client.TaskQuery{View: "backlog"})
	if err != nil || len(page.Items) != 2 || page.Items[0].ID != 1 || page.Items[1].ID != 2 {
		t.Fatalf("backlog=%+v err=%v", page, err)
	}
	manual, err := srv.Client.GetTask(t.Context(), task.ID)
	if err != nil || manual.View != "triage" || len(manual.Reasons) != 2 {
		t.Fatalf("manual=%+v err=%v", manual, err)
	}
	if out := captureRun(t, sys, "", "triage", "--accept-new"); out != "accepted 0 new tasks into the backlog\n" {
		t.Fatalf("empty acceptance stdout=%q", out)
	}
}

func TestTriageManual(t *testing.T) {
	srv, sys := captureServer(t)
	scheduleTask(t, srv, "First")
	if out := captureRun(t, sys, "", "triage", "--manual", "#1"); out != "#1 sent to triage (manual)\n" {
		t.Fatalf("stdout=%q", out)
	}
	task, err := srv.Client.GetTask(t.Context(), 1)
	if err != nil || task.View != "triage" || len(task.Reasons) != 1 || task.Reasons[0].Reason != "manual" {
		t.Fatalf("task=%+v err=%v", task, err)
	}
	captureRun(t, sys, "", "defer", "1", "tomorrow")
	if out := captureRun(t, sys, "", "triage", "--manual", "1"); out != "#1 sent to triage (manual)\n#1 no longer deferred\n" {
		t.Fatalf("deferral consequence stdout=%q", out)
	}
}

func TestTUIHookDispatch(t *testing.T) {
	sys, _ := testSys(t)
	previous := launchTUI
	t.Cleanup(func() { launchTUI = previous })
	for _, tc := range []struct {
		args []string
		want TUIRequest
	}{
		{[]string{"triage", "-i"}, TUIRequest{Mode: "triage"}},
		{[]string{"plan"}, TUIRequest{Mode: "plan"}},
		{[]string{"recap"}, TUIRequest{Mode: "recap"}},
		{[]string{"recap", "--date", "mon"}, TUIRequest{Mode: "recap", RecapDate: "mon"}},
		{[]string{"recap", "--date", "raw date for Plan C"}, TUIRequest{Mode: "recap", RecapDate: "raw date for Plan C"}},
		{[]string{"tui"}, TUIRequest{Mode: "tabs"}},
		{[]string{"tui", "--tab", "backlog"}, TUIRequest{Mode: "tabs", Tab: "backlog"}},
		{[]string{"tui", "--tab", "raw tab for Plan C"}, TUIRequest{Mode: "tabs", Tab: "raw tab for Plan C"}},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			launchTUI = nil
			out, stderr, code := runCLI(t, sys, "", tc.args...)
			if code != 2 || out != "" || stderr != "tkr: the TUI is not built in\n" {
				t.Fatalf("nil hook: exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
			var got []TUIRequest
			launchTUI = func(cmd *cobra.Command, req TUIRequest) error {
				if envFrom(cmd) == nil {
					t.Error("missing command environment")
				}
				got = append(got, req)
				return nil
			}
			out, stderr, code = runCLI(t, sys, "", tc.args...)
			if code != 0 || out != "" || stderr != "" || len(got) != 1 || got[0] != tc.want {
				t.Fatalf("hook: exit=%d stdout=%q stderr=%q got=%v", code, out, stderr, got)
			}
		})
	}
}

func TestTagProjectPriority(t *testing.T) {
	srv, sys := captureServer(t)
	captureTask(t, srv, "Metadata")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"tag", "1", "+infra", "+db"}, "#1 tags: db, infra\n"},
		{[]string{"tag", "#1", "-infra", "+ops"}, "#1 tags: db, ops\n"},
		{[]string{"project", "1", "infra.keycloak"}, "#1 project infra.keycloak\n"},
		{[]string{"project", "1", "none"}, "#1 project cleared\n"},
		{[]string{"priority", "1", "H"}, "#1 priority H\n"},
		{[]string{"priority", "1", "M"}, "#1 priority M\n"},
		{[]string{"priority", "1", "L"}, "#1 priority L\n"},
		{[]string{"priority", "1", "none"}, "#1 priority cleared\n"},
	} {
		if out := captureRun(t, sys, "", tc.args...); out != tc.want {
			t.Errorf("%v: stdout=%q; want %q", tc.args, out, tc.want)
		}
	}
	task, err := srv.Client.GetTask(t.Context(), 1)
	if err != nil || task.Project != nil || task.Priority != nil || strings.Join(task.Tags, ",") != "db,ops" || task.Title != "Metadata" || task.View != "triage" {
		t.Fatalf("metadata=%+v err=%v", task, err)
	}
	for _, args := range [][]string{
		{"tag", "1", "infra"}, {"tag", "1", "+"}, {"tag", "1", "-"}, {"tag", "1"},
		{"project", "1"}, {"priority", "1", "X"}, {"priority", "1", "h"}, {"priority", "bad", "H"},
	} {
		out, stderr, code := runCLI(t, sys, "", args...)
		if code != 2 || out != "" || stderr == "" {
			t.Errorf("%v: exit=%d stdout=%q stderr=%q", args, code, out, stderr)
		}
	}
}

func TestTriageUsage(t *testing.T) {
	sys, _ := testSys(t)
	for _, args := range [][]string{
		{"triage", "extra"}, {"triage", "--manual", "0"}, {"triage", "--manual", ""},
		{"triage", "--manual", "1", "--accept-new"}, {"triage", "-i", "--manual", "1"},
		{"triage", "-i", "--accept-new"}, {"plan", "extra"}, {"recap", "extra"}, {"tui", "extra"},
	} {
		out, stderr, code := runCLI(t, sys, "", args...)
		if code != 2 || out != "" || stderr == "" {
			t.Errorf("%v: exit=%d stdout=%q stderr=%q", args, code, out, stderr)
		}
	}
}

func TestTriageMetadataJSON(t *testing.T) {
	srv, sys := captureServer(t)
	captureTask(t, srv, "JSON")
	for _, args := range [][]string{
		{"tag", "1", "+db"}, {"project", "1", "infra"}, {"priority", "1", "H"},
		{"triage", "--accept-new"}, {"triage", "--manual", "1"},
	} {
		out := captureRun(t, sys, "", append([]string{"--json"}, args...)...)
		var res api.OpResponse
		if err := json.Unmarshal([]byte(out), &res); err != nil || res.OpID == nil || len(res.Effects) == 0 {
			t.Fatalf("%v: response=%q err=%v", args, out, err)
		}
	}
}

func TestTriagePagination(t *testing.T) {
	for _, accept := range []bool{false, true} {
		t.Run(fmt.Sprintf("accept=%t", accept), func(t *testing.T) {
			sys, _ := testSys(t)
			var pages, posts atomic.Int32
			socket, token := fakeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET /api/v1/clock":
					writeJSON(t, w, api.Clock{Today: civil.MustDate("2026-10-28")})
				case "GET /api/v1/tasks":
					pages.Add(1)
					if r.URL.Query().Get("view") != "triage" {
						t.Error("wrong task view")
					}
					page := api.Page[api.Task]{Items: []api.Task{{ID: 1, Title: "First", ETag: "etag-1", Reasons: []api.Reason{{Reason: "new"}}}}, NextCursor: new("next")}
					if cursor := r.URL.Query().Get("cursor"); cursor != "" {
						if cursor != "next" {
							t.Errorf("cursor=%q", cursor)
						}
						page.Items[0].ID, page.Items[0].Title, page.Items[0].ETag = 2, "Second", "etag-2"
						page.NextCursor = nil
					}
					writeJSON(t, w, page)
				case "POST /api/v1/triage/accept-new":
					posts.Add(1)
					var req api.AcceptNewRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Error(err)
					}
					if pages.Load() != 2 || len(req.Items) != 2 || req.Items[0].ID != 1 || req.Items[0].ETag != "etag-1" || req.Items[1].ID != 2 || req.Items[1].ETag != "etag-2" {
						t.Errorf("pages=%d request=%+v", pages.Load(), req)
					}
					writeJSON(t, w, api.OpResponse{Tasks: []api.Task{{ID: 1}, {ID: 2}}})
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			args := []string{"--socket", socket, "--token-file", token, "triage"}
			want := "#1  [new] First\n#2  [new] Second\n"
			if accept {
				args = append(args, "--accept-new")
				want = "accepted 2 new tasks into the backlog\n"
			}
			out, stderr, code := runCLI(t, sys, "", args...)
			if code != 0 || out != want || stderr != "" || pages.Load() != 2 || (posts.Load() == 1) != accept {
				t.Fatalf("exit=%d stdout=%q stderr=%q pages=%d posts=%d", code, out, stderr, pages.Load(), posts.Load())
			}
		})
	}
}

func TestTriageMetadataPreconditions(t *testing.T) {
	for _, args := range [][]string{{"triage", "--manual", "1"}, {"tag", "1", "+db"}, {"project", "1", "none"}, {"priority", "1", "H"}} {
		t.Run(args[0], func(t *testing.T) {
			sys, _ := testSys(t)
			var gets, mutations atomic.Int32
			socket, token := fakeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && r.URL.Path == "/api/v1/tasks/1" {
					gets.Add(1)
					writeJSON(t, w, api.TaskDetail{Task: api.Task{ID: 1, ETag: `"test-generation:3"`}})
					return
				}
				mutations.Add(1)
				method, path := http.MethodPatch, "/api/v1/tasks/1"
				if args[0] == "triage" {
					method, path = http.MethodPost, path+"/triage"
				}
				if r.Method != method || r.URL.Path != path || gets.Load() != 1 || r.Header.Get("If-Match") != `"test-generation:3"` {
					t.Errorf("request=%s %s GETs=%d If-Match=%q", r.Method, r.URL.Path, gets.Load(), r.Header.Get("If-Match"))
				}
				w.WriteHeader(http.StatusPreconditionFailed)
				writeJSON(t, w, api.Problem{Status: 412, Code: "precondition_failed", Detail: "task changed", Current: json.RawMessage(`{"id":1,"version":4}`)})
			}))
			out, stderr, code := runCLI(t, sys, "", append([]string{"--socket", socket, "--token-file", token}, args...)...)
			if code != 6 || out != "" || !strings.Contains(stderr, "current: #1 version 4") || mutations.Load() != 1 {
				t.Fatalf("exit=%d stdout=%q stderr=%q mutations=%d", code, out, stderr, mutations.Load())
			}
		})
	}
}
