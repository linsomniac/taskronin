package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/civil"
)

const plainLsFixture = "Triage\n  #1  [new] Keycloak  infra.keycloak +db\n\nBacklog\n  #2  Infrastructure  infrastructure\n"

func seedLs(t *testing.T) sysDeps {
	t.Helper()
	_, sys := captureServer(t)
	captureRun(t, sys, "", "add", "Keycloak", "-P", "infra.keycloak", "-t", "db")
	captureRun(t, sys, "", "add", "Infrastructure", "-P", "infrastructure", "--bottom")
	return sys
}

func TestLsFormat(t *testing.T) {
	sys := seedLs(t)
	want := plainLsFixture
	if out := captureRun(t, sys, "", "ls"); out != want {
		t.Fatalf("stdout=%q want=%q", out, want)
	}
	for _, flags := range [][]string{{"-P", "infra"}, {"-t", "DB"}, {"--view", "triage"}} {
		out := captureRun(t, sys, "", append([]string{"ls"}, flags...)...)
		if !strings.Contains(out, "#1  [new] Keycloak") || strings.Contains(out, "#2") {
			t.Fatalf("flags=%v stdout=%q", flags, out)
		}
	}
	captureRun(t, sys, "", "add", "Deferred", "--defer", "tomorrow")
	captureRun(t, sys, "", "add", "Deleted")
	captureRun(t, sys, "", "delete", "4", "-y")
	for _, tc := range []struct {
		flags []string
		ids   []int64
	}{
		{nil, []int64{1, 2}},
		{[]string{"--view", "backlog"}, []int64{2}},
		{[]string{"--all"}, []int64{1, 2, 3}},
		{[]string{"--view", "deleted"}, []int64{4}},
	} {
		out := captureRun(t, sys, "", append([]string{"ls", "--json"}, tc.flags...)...)
		var page api.Page[api.Task]
		if err := json.Unmarshal([]byte(out), &page); err != nil {
			t.Fatal(err)
		}
		var ids []int64
		for _, task := range page.Items {
			ids = append(ids, task.ID)
		}
		if !reflect.DeepEqual(ids, tc.ids) || page.NextCursor != nil {
			t.Fatalf("flags=%v ids=%v want=%v cursor=%v", tc.flags, ids, tc.ids, page.NextCursor)
		}
	}
}

func TestEmptyReadScreens(t *testing.T) {
	_, sys := captureServer(t)
	captureRun(t, sys, "ls_empty", "ls")
	captureRun(t, sys, "upcoming_empty", "upcoming")
	if out := captureRun(t, sys, "", "ls", "--view", "done"); out != "Done\n  none\n" {
		t.Errorf("empty done view = %q", out)
	}
	if out := captureRun(t, sys, "", "triage"); out != "Triage clear\n" {
		t.Errorf("empty triage = %q", out)
	}
}

func TestSearchNoMatches(t *testing.T) {
	sys, _ := testSys(t)
	socket, token := fakeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query().Get("q"); q != "bad\x1b[2J query" {
			t.Errorf("search query = %q", q)
		}
		writeJSON(t, w, api.Page[api.SearchHit]{})
	}))
	args := []string{"--socket", socket, "--token-file", token, "search", "bad\x1b[2J", "query"}
	const want = "No matches for 'bad�[2J query'"
	if out := captureRun(t, sys, "", args...); out != want+"\n" {
		t.Errorf("empty search = %q", out)
	}
	if out := captureRun(t, sys, "", append(args, "--color", "always")...); out != (paint{on: true}).dim(want)+"\n" {
		t.Errorf("colored empty search = %q", out)
	}
}

func TestShowMinimal(t *testing.T) {
	_, sys := captureServer(t)
	captureRun(t, sys, "", "add", "Unscheduled", "--bottom")
	captureRun(t, sys, "show_minimal", "show", "1")
	out := captureRun(t, sys, "", "show", "1", "--color", "always")
	for _, label := range []string{"Project", "Tags", "Priority", "Notes"} {
		if !strings.Contains(out, label+": "+(paint{on: true}).dim("none")+"\n") {
			t.Errorf("missing dim empty value for %s in %q", label, out)
		}
	}
	captureRun(t, sys, "", "due", "1", "2026-12-31")
	out = captureRun(t, sys, "", "show", "1", "--color", "always")
	if !strings.Contains(out, "Due phase: "+(paint{on: true}).dim("none")+"\n") {
		t.Errorf("missing dim empty due phase in %q", out)
	}
}

func TestShowSanitizes(t *testing.T) {
	sys, _ := testSys(t)
	unsafe := "x\x1b]52;c;AAAA\x07y"
	notes := "**raw markdown**\n\tsecond line\x1b\x07"
	task := api.TaskDetail{
		Task: api.Task{ID: 1, Title: unsafe, Notes: notes, Status: "open", View: "triage",
			Project: &unsafe, Tags: []string{unsafe}, Priority: new("H"), Origin: "cli",
			Reasons: []api.Reason{{Reason: "slipped"}}, Slips: api.Slips{Lifetime: 3, SinceTriage: 2},
			Due: &api.Moment{Date: civil.MustDate("2026-10-30")}},
		Blocks:  []api.Block{{ID: 7, TaskID: 1, TaskTitle: unsafe, Date: civil.MustDate("2026-10-28"), Start: 540, End: 600, Status: "missed", Slipped: true}},
		Entries: []api.Entry{{ID: 9, Title: unsafe, Notes: notes, WorkDate: civil.MustDate("2026-10-28"), Kind: "work", TaskID: new(int64(1)), TaskTitle: &unsafe}},
	}
	socket, token := fakeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/tasks/1":
			writeJSON(t, w, task)
		case "/api/v1/entries/9":
			writeJSON(t, w, task.Entries[0])
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	for _, args := range [][]string{{"show", "#1"}, {"entry", "show", "e9"}} {
		out := captureRun(t, sys, "", append([]string{"--socket", socket, "--token-file", token}, args...)...)
		if strings.ContainsAny(out, "\x1b\x07") || !strings.Contains(out, "x�]52;c;AAAA�y") || !strings.Contains(out, "**raw markdown**\n\tsecond line��") {
			t.Fatalf("args=%v unsafe or rendered output=%q", args, out)
		}
		if args[0] == "show" {
			for _, field := range []string{"open", "triage", "slipped", "3 lifetime", "2 since triage", "b7", "09:00–10:00", "e9", "2026-10-30", "Priority: H"} {
				if !strings.Contains(out, field) {
					t.Errorf("missing %q in %q", field, out)
				}
			}
		}
		colored := captureRun(t, sys, "", append([]string{"--socket", socket, "--token-file", token, "--color", "always"}, args...)...)
		p := paint{on: true}
		if !strings.Contains(colored, p.taskID(1)) || !strings.Contains(colored, p.entryID(9)) || colorSGR.ReplaceAllString(colored, "") != out {
			t.Errorf("%v with color = %q", args, colored)
		}
		out = captureRun(t, sys, "", append([]string{"--socket", socket, "--token-file", token, "--json", "--color", "always"}, args...)...)
		var entity struct{ Title, Notes string }
		if err := json.Unmarshal([]byte(out), &entity); err != nil || entity.Title != unsafe || entity.Notes != notes || strings.ContainsAny(out, "\x1b\x07") {
			t.Fatalf("JSON=%q err=%v", out, err)
		}
	}
}

func TestUpcomingAndSearch(t *testing.T) {
	_, sys := captureServer(t)
	captureRun(t, sys, "", "add", "keycloak cluster", "--defer", "fri 9:00")
	captureRun(t, sys, "", "add", "Due task", "--due", "tomorrow")
	captureRun(t, sys, "", "log", "1", "keycloak progress")
	out := captureRun(t, sys, "", "upcoming")
	for _, want := range []string{"Deferred", "#1", "keycloak cluster", "Fri 2026-10-30 09:00", "Due soon", "#2", "Due task"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	var upcoming api.Upcoming
	out = captureRun(t, sys, "", "upcoming", "--json")
	if err := json.Unmarshal([]byte(out), &upcoming); err != nil || len(upcoming.Deferred) != 1 || len(upcoming.DueSoon) != 1 {
		t.Fatalf("upcoming=%q err=%v", out, err)
	}
	for _, tc := range []struct {
		args         []string
		want, absent string
	}{
		{[]string{"search", "keycloak"}, "#1", "Due task"},
		{[]string{"search", "keycloak", "cluster", "--kind", "task"}, "#1", "e1"},
		{[]string{"search", "keycloak", "--kind", "entry"}, "e1", "keycloak cluster"},
	} {
		out = captureRun(t, sys, "", tc.args...)
		if !strings.Contains(out, tc.want) || strings.Contains(out, tc.absent) {
			t.Fatalf("args=%v stdout=%q", tc.args, out)
		}
	}
}

func TestHistory(t *testing.T) {
	_, sys := captureServer(t)
	if _, stderr, code := runCLI(t, sys, "", "history", "999"); code != 5 {
		t.Fatalf("unknown task history exit = %d, want 5; stderr = %q", code, stderr)
	}
	captureRun(t, sys, "", "add", "History task")
	captureRun(t, sys, "", "keep", "1")
	out := captureRun(t, sys, "", "history", "1")
	placed, added := strings.Index(out, "task.place"), strings.Index(out, "task.add")
	if placed < 0 || added <= placed || !strings.Contains(out, "owner") || !strings.Contains(out, "#1") {
		t.Fatalf("history order or metadata: %q", out)
	}
	out = captureRun(t, sys, "", "history", "#1", "--limit", "1", "--json")
	var page api.Page[api.EventView]
	if err := json.Unmarshal([]byte(out), &page); err != nil || len(page.Items) != 1 || page.Items[0].Op.Kind != "task.place" || page.NextCursor == nil {
		t.Fatalf("limited history=%q err=%v", out, err)
	}
}

func TestEntriesAndLink(t *testing.T) {
	srv, sys := captureServer(t)
	captureTask(t, srv, "Task one")
	captureTask(t, srv, "Task two")
	captureRun(t, sys, "", "log", "1", "Today's work", "-m", "Entry notes")
	captureRun(t, sys, "", "log", "1", "Friday work", "--date", "fri")
	for _, tc := range []struct {
		args []string
		ids  []int64
	}{
		{[]string{"--date", "today"}, []int64{1}},
		{nil, []int64{1}},
		{[]string{"--date", "fri"}, []int64{2}},
		{[]string{"--from", "fri", "--to", "today"}, []int64{1, 2}},
		{[]string{"--task", "#1"}, []int64{1, 2}},
		{[]string{"--task", "none", "--date", "today"}, []int64{}},
	} {
		out := captureRun(t, sys, "", append([]string{"entries", "--json"}, tc.args...)...)
		var page api.Page[api.Entry]
		if err := json.Unmarshal([]byte(out), &page); err != nil {
			t.Fatal(err)
		}
		ids := []int64{}
		for _, entry := range page.Items {
			ids = append(ids, entry.ID)
		}
		if !reflect.DeepEqual(ids, tc.ids) {
			t.Fatalf("args=%v ids=%v want=%v", tc.args, ids, tc.ids)
		}
	}
	if out := captureRun(t, sys, "", "entries", "--date", "today"); !strings.Contains(out, "e1") || !strings.Contains(out, "Today's work") || strings.Contains(out, "e2") {
		t.Fatalf("entries=%q", out)
	}
	for _, tc := range []struct {
		args            []string
		linked, deleted bool
	}{
		{[]string{"link", "e1", "none"}, false, false},
		{[]string{"link", "1", "#2"}, true, false},
		{[]string{"delete", "e1", "-y"}, true, true},
		{[]string{"undelete", "e1"}, true, false},
	} {
		out := captureRun(t, sys, "", append([]string{"entry"}, tc.args...)...)
		if !strings.Contains(out, "e1") {
			t.Fatalf("mutation output=%q", out)
		}
		entry, err := srv.Client.GetEntry(t.Context(), 1)
		if err != nil || (entry.TaskID != nil) != tc.linked || (entry.DeletedAt != nil) != tc.deleted || (tc.linked && *entry.TaskID != 2) {
			t.Fatalf("args=%v entry=%+v err=%v", tc.args, entry, err)
		}
	}
}

func TestReadInvalidArgs(t *testing.T) {
	_, sys := captureServer(t)
	for _, args := range [][]string{
		{"upcoming", "extra"}, {"ls", "--view", "bad"}, {"ls", "--all", "--view", "triage"},
		{"show", "e1"}, {"history", "1", "--limit", "0"}, {"history", "1", "--limit", "501"},
		{"search"}, {"search", "x", "--kind", "bad"},
		{"entries", "--date", "today", "--from", "yesterday", "--to", "today"},
		{"entries", "--from", "today"}, {"entries", "--to", "today"}, {"entries", "--date", "today 09:00"},
		{"entries", "--from", "today", "--to", "yesterday"}, {"entries", "--task", "e1"},
		{"entry", "show", "#1"}, {"entry", "link", "e1", "bad"}, {"entry", "delete", "e1"},
	} {
		out, stderr, code := runCLI(t, sys, "", args...)
		if code != 2 || out != "" || stderr == "" {
			t.Errorf("args=%v exit=%d stdout=%q stderr=%q", args, code, out, stderr)
		}
	}
}

func TestEntryDeleteConfirmation(t *testing.T) {
	srv, sys := captureServer(t)
	captureRun(t, sys, "", "log", "-N", "Keep until confirmed")
	sys.StdinTTY = true
	for _, answer := range []string{"n\n", "yes\n"} {
		out, stderr, code := runCLI(t, sys, answer, "entry", "delete", "e1")
		if code != 0 || !strings.Contains(stderr, "Delete e1 'Keep until confirmed'? [y/N]") || (answer == "n\n" && out != "") {
			t.Fatalf("answer=%q exit=%d stdout=%q stderr=%q", answer, code, out, stderr)
		}
		entry, err := srv.Client.GetEntry(t.Context(), 1)
		if err != nil || (entry.DeletedAt != nil) != (answer == "yes\n") {
			t.Fatalf("entry=%+v err=%v", entry, err)
		}
	}
}

func TestEntryEdit(t *testing.T) {
	for _, tc := range []struct {
		name, mode, title, notes string
		code                     int
	}{
		{"changed", "write:New title\n\nNew notes", "New title", "New notes", 0},
		{"clear notes", "write:Old title", "Old title", "", 0},
		{"unchanged", "keep", "Old title", "Old notes", 0},
		{"editor failure", "exit:1", "Old title", "Old notes", 1},
		{"missing editor", "missing", "Old title", "Old notes", 1},
		{"empty buffer", "write:", "Old title", "Old notes", 2},
		{"oversized notes", "write:New title\n\n" + strings.Repeat("n", 70*1024), "Old title", "Old notes", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, sys := captureServer(t)
			captureTask(t, srv, "Task title")
			captureRun(t, sys, "", "log", "1", "Old title", "-m", "Old notes", "--at", "09:30")
			before, err := srv.Client.GetEntry(t.Context(), 1)
			if err != nil {
				t.Fatal(err)
			}
			initial := editHelper(t, &sys, tc.mode)
			if tc.mode == "missing" {
				getenv := sys.Getenv
				sys.Getenv = func(key string) string {
					if key == "EDITOR" {
						return "/nonexistent"
					}
					return getenv(key)
				}
			}
			out, stderr, code := runCLI(t, sys, "", "entry", "edit", "e1", "--json")
			if code != tc.code {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
			data, err := os.ReadFile(initial)
			if tc.mode != "missing" && (err != nil || string(data) != "Old title\n\nOld notes") {
				t.Fatalf("initial=%q err=%v", data, err)
			}
			if tc.code == 0 {
				if stderr != "" || !json.Valid([]byte(out)) || len(editDrafts(t, srv.Paths.DraftsDir)) != 0 {
					t.Fatalf("stdout=%q stderr=%q", out, stderr)
				}
			} else {
				draft := "Old title\n\nOld notes"
				if strings.HasPrefix(tc.mode, "write:") {
					draft = strings.TrimPrefix(tc.mode, "write:")
				}
				assertEditDraft(t, srv.Paths.DraftsDir, stderr, draft)
			}
			after, err := srv.Client.GetEntry(t.Context(), 1)
			if err != nil || after.Title != tc.title || after.Notes != tc.notes || !reflect.DeepEqual(after.TaskID, before.TaskID) || after.WorkDate != before.WorkDate || !reflect.DeepEqual(after.Time, before.Time) {
				t.Fatalf("entry=%+v err=%v", after, err)
			}
			if (tc.code != 0 || tc.mode == "keep") && after.Version != before.Version {
				t.Fatal("failed or unchanged edit mutated entry")
			}
			if tc.code == 0 && tc.mode != "keep" && after.Version != before.Version+1 {
				t.Fatal("edit did not update the entry exactly once")
			}
		})
	}
}

func TestReadOutputSanitizes(t *testing.T) {
	sys, _ := testSys(t)
	unsafe := "x\x1b]52;c;AAAA\x07y\n"
	date := civil.MustDate("2026-10-28")
	socket, token := fakeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/search":
			writeJSON(t, w, api.Page[api.SearchHit]{Items: []api.SearchHit{{Kind: "task", ID: 1, Title: unsafe, Snippet: unsafe, Status: unsafe}}})
		case "/api/v1/tasks/1/history":
			writeJSON(t, w, api.Page[api.EventView]{Items: []api.EventView{{Entity: "task", EntityID: 1, Action: unsafe, Op: api.OpSummary{OpID: unsafe, Kind: unsafe, Actor: unsafe, State: unsafe}}}})
		case "/api/v1/entries":
			writeJSON(t, w, api.Page[api.Entry]{Items: []api.Entry{{ID: 1, Title: unsafe, TaskID: new(int64(1)), TaskTitle: &unsafe, WorkDate: date}}})
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	for _, args := range [][]string{{"search", "x"}, {"history", "1"}, {"entries", "--task", "1"}} {
		out := captureRun(t, sys, "", append([]string{"--socket", socket, "--token-file", token}, args...)...)
		if strings.ContainsAny(out, "\x1b\x07") || !strings.Contains(out, "x�]52;c;AAAA�y�") {
			t.Fatalf("args=%v unsafe output=%q", args, out)
		}
	}
}

func TestEntryEditPrecondition(t *testing.T) {
	sys, paths := testSys(t)
	editHelper(t, &sys, "write:New title\n\nOld notes")
	var patches atomic.Int32
	socket, token := fakeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/entries/1":
			writeJSON(t, w, api.Entry{ID: 1, Title: "Old title", Notes: "Old notes", WorkDate: civil.MustDate("2026-10-28"), ETag: `"test-generation:3"`})
		case "PATCH /api/v1/entries/1":
			patches.Add(1)
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !reflect.DeepEqual(body, map[string]string{"title": "New title"}) || r.Header.Get("If-Match") != `"test-generation:3"` {
				t.Errorf("body=%v If-Match=%q err=%v", body, r.Header.Get("If-Match"), err)
			}
			w.WriteHeader(http.StatusPreconditionFailed)
			writeJSON(t, w, api.Problem{Status: 412, Code: "precondition_failed", Detail: "entry changed"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	out, stderr, code := runCLI(t, sys, "", "--socket", socket, "--token-file", token, "entry", "edit", "1")
	if code != 6 || patches.Load() != 1 || out != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q patches=%d", code, out, stderr, patches.Load())
	}
	assertEditDraft(t, paths.DraftsDir, stderr, "New title\n\nOld notes")
}

func TestReadPagination(t *testing.T) {
	for _, verb := range []string{"ls", "search", "entries"} {
		t.Run(verb, func(t *testing.T) {
			sys, _ := testSys(t)
			var reads atomic.Int32
			socket, token := fakeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				page := reads.Add(1)
				if page > 2 || (page == 1 && r.URL.Query().Get("cursor") != "") || (page == 2 && r.URL.Query().Get("cursor") != "next") {
					t.Errorf("page=%d query=%v", page, r.URL.Query())
				}
				var cursor *string
				if page == 1 {
					cursor = new("next")
				}
				switch verb {
				case "ls":
					if r.URL.Query().Get("view") != "backlog" || r.URL.Query().Get("project") != "infra" {
						t.Errorf("query=%v", r.URL.Query())
					}
					writeJSON(t, w, api.Page[api.Task]{Items: []api.Task{{ID: int64(page), Title: fmt.Sprint(page)}}, NextCursor: cursor})
				case "search":
					if r.URL.Query().Get("q") != "two words" || r.URL.Query().Get("kind") != "task" {
						t.Errorf("query=%v", r.URL.Query())
					}
					writeJSON(t, w, api.Page[api.SearchHit]{Items: []api.SearchHit{{ID: int64(page), Kind: "task", Title: fmt.Sprint(page)}}, NextCursor: cursor})
				case "entries":
					if r.URL.Query().Get("task") != "1" {
						t.Errorf("query=%v", r.URL.Query())
					}
					writeJSON(t, w, api.Page[api.Entry]{Items: []api.Entry{{ID: int64(page), Title: fmt.Sprint(page), WorkDate: civil.MustDate("2026-10-28")}}, NextCursor: cursor})
				}
			}))
			args := map[string][]string{"ls": {"ls", "--view", "backlog", "-P", "infra"}, "search": {"search", "two", "words", "--kind", "task"}, "entries": {"entries", "--task", "1"}}[verb]
			out := captureRun(t, sys, "", append([]string{"--socket", socket, "--token-file", token, "--json"}, args...)...)
			var page api.Page[struct{ ID int64 }]
			if err := json.Unmarshal([]byte(out), &page); err != nil || len(page.Items) != 2 || page.Items[0].ID != 1 || page.Items[1].ID != 2 || page.NextCursor != nil || reads.Load() != 2 {
				t.Fatalf("stdout=%q reads=%d err=%v", out, reads.Load(), err)
			}
		})
	}
}
