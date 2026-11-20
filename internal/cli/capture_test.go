package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/client"
	"taskronin/internal/service"
	"taskronin/internal/testutil"
)

func captureServer(t *testing.T) (*testutil.Server, sysDeps) {
	t.Helper()
	return testutil.StartServer(t, testutil.WithClock("2026-10-28 10:00")), sysDeps{Getenv: os.Getenv}
}

func captureTask(t *testing.T, srv *testutil.Server, title string) *api.Task {
	t.Helper()
	res, err := srv.Client.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: title})
	if err != nil {
		t.Fatal(err)
	}
	return res.Task
}

func captureRun(t *testing.T, sys sysDeps, golden string, args ...string) string {
	t.Helper()
	out, stderr, code := runCLI(t, sys, "", args...)
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	if golden != "" {
		testutil.Golden(t, golden, []byte(out))
	}
	return out
}

func TestAddVariants(t *testing.T) {
	srv, sys := captureServer(t)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"add_triage", []string{"Set up keycloak"}},
		{"add_top", []string{"B", "--top"}},
		{"add_block", []string{"C", "--block", "13:00-15:00"}},
		{"add_bottom", []string{"D", "--bottom"}},
		{"add_after", []string{"E", "--after", "#2"}},
		{"add_before", []string{"F", "--before", "2"}},
		{"add_defer", []string{"G", "--defer", "tomorrow"}},
		{"add_defer_time", []string{"H", "--defer", "fri 9:00"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			captureRun(t, sys, tc.name, append([]string{"add"}, tc.args...)...)
		})
	}
	task, err := srv.Client.GetTask(t.Context(), 8)
	if err != nil || task.Defer == nil || task.Defer.Date.String() != "2026-10-30" || task.Defer.Time == nil || *task.Defer.Time != 540 {
		t.Fatalf("defer: %+v, %v", task, err)
	}
	page, err := srv.Client.ListTasks(t.Context(), client.TaskQuery{View: "backlog"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, task := range page.Items {
		ids = append(ids, task.ID)
	}
	if fmt.Sprint(ids) != "[6 2 5 3 4]" {
		t.Fatalf("backlog order = %v", ids)
	}
}

func TestAddUnrecognisedDateMessage(t *testing.T) {
	_, sys := captureServer(t)
	out, stderr, code := runCLI(t, sys, "", "add", "X", "--defer", "next monday")
	want := "tkr: --defer: unrecognised date \"next\": use today, yesterday, tomorrow, a weekday (mon…sun), +Nd/-Nd, +Nw/-Nw, or YYYY-MM-DD\n"
	if code != 2 || out != "" || stderr != want {
		t.Fatalf("exit=%d stdout=%q stderr=%q; want exit 2 and %q", code, out, stderr, want)
	}
}

func TestTRI09_OnePlacementFlag(t *testing.T) {
	srv, sys := captureServer(t)
	for _, args := range [][]string{{"add", "x", "--top", "--bottom"}, {"add", "x", "--defer", "tomorrow", "--now"}} {
		out, stderr, code := runCLI(t, sys, "", args...)
		if code != 2 || out != "" || !strings.Contains(stderr, "at most one placement flag") {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
	}
	if task := captureTask(t, srv, "first saved task"); task.ID != 1 {
		t.Fatalf("invalid command saved a task: %+v", task)
	}
}

func TestAddFalsePlacementFlags(t *testing.T) {
	for _, tc := range []struct {
		name, action, view string
		flags              []string
	}{
		{"top false", "added to triage (new)", "triage", []string{"--top=false"}},
		{"bottom false", "added to triage (new)", "triage", []string{"--bottom=false"}},
		{"now false", "added to triage (new)", "triage", []string{"--now=false"}},
		{"all false", "added to triage (new)", "triage", []string{"--top=false", "--bottom=false", "--now=false"}},
		{"false with true", "added to backlog (top)", "backlog", []string{"--top=true", "--bottom=false", "--now=false"}},
		{"false with date", "added, deferred to tomorrow", "upcoming", []string{"--top=false", "--bottom=false", "--now=false", "--defer", "tomorrow"}},
		{"true then false", "added to triage (new)", "triage", []string{"--now", "--now=false"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, sys := captureServer(t)
			captureTask(t, srv, "Existing focus")
			logFocus(t, srv)
			out := captureRun(t, sys, "", append([]string{"add", "New task"}, tc.flags...)...)
			if want := "#2 'New task' " + tc.action + "\n"; out != want {
				t.Errorf("stdout=%q; want %q", out, want)
			}
			task, err := srv.Client.GetTask(t.Context(), 2)
			if err != nil || task.View != tc.view || len(task.Interruptions) != 0 {
				t.Fatalf("task=%+v, err=%v", task, err)
			}
			status, _, _, err := srv.Client.Status(t.Context(), "")
			if err != nil || status.Focus == nil || status.Focus.ID != 1 {
				t.Fatalf("focus changed: status=%+v, err=%v", status, err)
			}
		})
	}
}

func TestAddQuoteTitle(t *testing.T) {
	sys, _ := testSys(t)
	out, stderr, code := runCLI(t, sys, "", "add", "a", "b")
	if code != 2 || out != "" || !strings.Contains(stderr, "quote your title") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
}

func TestAddNowInterrupts(t *testing.T) {
	srv, sys := captureServer(t)
	captureTask(t, srv, "Set up keycloak")
	logFocus(t, srv)
	captureRun(t, sys, "add_now", "add", "Restart stuck pg replica", "--now")
	status, _, _, err := srv.Client.Status(t.Context(), "")
	if err != nil || status.Focus == nil || status.Focus.ID != 2 {
		t.Fatalf("focus = %+v, %v", status, err)
	}
}

func TestAddMetadata(t *testing.T) {
	srv, sys := captureServer(t)
	captureRun(t, sys, "add_metadata", "add", "Metadata", "--due", "wed 16:00", "-t", "db", "-t", "infra", "-P", "infra.keycloak", "-p", "H")
	task, err := srv.Client.GetTask(t.Context(), 1)
	if err != nil || task.Due == nil || task.Due.Date.String() != "2026-11-04" || task.Due.Time == nil || *task.Due.Time != 960 ||
		task.Project == nil || *task.Project != "infra.keycloak" || task.Priority == nil || *task.Priority != "H" || strings.Join(task.Tags, ",") != "db,infra" {
		t.Fatalf("metadata = %+v, %v", task, err)
	}
}

func TestDoneEcho(t *testing.T) {
	srv, sys := captureServer(t)
	captureTask(t, srv, "Set up keycloak")
	captureRun(t, sys, "done", "done", "1")
	out, stderr, code := runCLI(t, sys, "", "done", "1", "--date", "tomorrow")
	if code != 1 || out != "" || stderr == "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
}

func TestDoneFlags(t *testing.T) {
	srv, sys := captureServer(t)
	captureTask(t, srv, "Set up keycloak")
	captureTask(t, srv, "Second task")
	captureRun(t, sys, "done_flags", "done", "#1", "--as", "Add certs", "--log", "Failover tested", "-m", "notes")
	task, err := srv.Client.GetTask(t.Context(), 1)
	if err != nil || task.Title != "Add certs" || task.Status != "done" {
		t.Fatalf("task = %+v, %v", task, err)
	}
	entry := assertLogEntry(t, srv, 1, "2026-10-28", "Failover tested")
	if entry.Kind != "complete" || entry.Notes != "notes" {
		t.Fatalf("entry = %+v", entry)
	}
	captureRun(t, sys, "done_yesterday", "done", "2", "--date", "yesterday")
}

func TestLIF13_ReopenHint(t *testing.T) {
	srv, sys := captureServer(t)
	captureTask(t, srv, "Set up keycloak")
	captureRun(t, sys, "", "done", "1")
	captureRun(t, sys, "reopen", "reopen", "1")
}

func TestReopenRemoveEntry(t *testing.T) {
	srv, sys := captureServer(t)
	captureTask(t, srv, "First")
	captureTask(t, srv, "Second")
	// AIDEV-NOTE: BF21 requires creating the block before it ends; advance
	// through real operations instead of constructing an invalid past block.
	srv.Clock.Set(testutil.Local(t, "2026-10-28 08:00"))
	_, err := srv.Client.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: "Third", Place: &api.AddPlacement{
		How: "block", Block: &api.BlockRequest{Date: civil.MustDate("2026-10-28"), Start: 540, End: 570},
	}})
	if err != nil {
		t.Fatal(err)
	}
	srv.Clock.Set(testutil.Local(t, "2026-10-28 10:00"))
	captureRun(t, sys, "", "done", "3")
	entry := assertLogEntry(t, srv, 3, "2026-10-28", "Third")
	captureRun(t, sys, "reopen_remove_entry", "reopen", "3", "--remove-entry")
	deleted, err := srv.Client.GetEntry(t.Context(), entry.ID)
	if err != nil || deleted.DeletedAt == nil {
		t.Fatalf("entry = %+v, %v", deleted, err)
	}
	task, err := srv.Client.GetTask(t.Context(), 3)
	if err != nil || task.Status != "open" || len(task.Blocks) != 1 || !task.Blocks[0].Slipped {
		t.Fatalf("task = %+v, %v", task, err)
	}
}

func TestDeleteConfirm(t *testing.T) {
	srv, sys := captureServer(t)
	captureTask(t, srv, "Set up keycloak")
	out, stderr, code := runCLI(t, sys, "", "delete", "1")
	if code != 2 || out != "" || !strings.Contains(stderr, "use -y to confirm") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	captureRun(t, sys, "delete", "delete", "1", "-y")
	captureRun(t, sys, "undelete", "undelete", "1")
}

func TestDeleteTTY(t *testing.T) {
	for _, answer := range []string{"", "n\n", "yes\n", "y\n"} {
		t.Run(fmt.Sprintf("answer=%q", answer), func(t *testing.T) {
			srv, sys := captureServer(t)
			sys.StdinTTY = true
			captureTask(t, srv, "Set up keycloak")
			out, stderr, code := runCLI(t, sys, answer, "delete", "1")
			if code != 0 || stderr != "Delete #1 'Set up keycloak'? [y/N] " {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
			task, err := srv.Client.GetTask(t.Context(), 1)
			confirmed := strings.HasPrefix(answer, "y")
			if err != nil || (task.DeletedAt != nil) != confirmed || (out != "") != confirmed {
				t.Fatalf("task = %+v, %v; stdout=%q", task, err, out)
			}
		})
	}
}

func TestDrop(t *testing.T) {
	srv, sys := captureServer(t)
	captureTask(t, srv, "Set up keycloak")
	captureRun(t, sys, "drop", "drop", "1")
	task, err := srv.Client.GetTask(t.Context(), 1)
	if err != nil || task.Status != "dropped" || len(logEntries(t, srv)) != 0 {
		t.Fatalf("task = %+v, %v", task, err)
	}
}

var captureOpID = regexp.MustCompile(`\b[0-9a-f]{8}\b`)

func TestUndoRedo(t *testing.T) {
	srv, sys := captureServer(t)
	out, stderr, code := runCLI(t, sys, "", "undo")
	if code != 6 || out != "" || stderr != "tkr: nothing to undo\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	captureRun(t, sys, "", "add", "Set up keycloak")
	for _, verb := range []string{"undo", "redo"} {
		out := captureRun(t, sys, "", verb)
		if !strings.HasPrefix(out, map[string]string{"undo": "undid add", "redo": "redid add"}[verb]+" (op ") || !captureOpID.MatchString(out) {
			t.Fatalf("%s: %q", verb, out)
		}
		testutil.Golden(t, verb, []byte(captureOpID.ReplaceAllString(out, "<opid>")))
		task, err := srv.Client.GetTask(t.Context(), 1)
		if err != nil || (task.DeletedAt != nil) != (verb == "undo") {
			t.Fatalf("task = %+v, %v", task, err)
		}
	}
}

func TestUndoJSONCarriesVia(t *testing.T) {
	srv, sys := captureServer(t)
	captureTask(t, srv, "Set up keycloak")
	out := captureRun(t, sys, "", "undo", "--json")
	var res struct {
		Effects []map[string]any `json:"effects"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Effects) == 0 || res.Effects[0]["kind"] != "reverted" || res.Effects[0]["via"] != "undo" {
		t.Fatalf("undo --json effects = %v", res.Effects)
	}
}

func TestRevert(t *testing.T) {
	srv, sys := captureServer(t)
	res, err := srv.Client.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: "Revert me"})
	if err != nil {
		t.Fatal(err)
	}
	out := captureRun(t, sys, "", "revert", *res.OpID)
	testutil.Golden(t, "revert", []byte(captureOpID.ReplaceAllString(out, "<opid>")))
	task, err := srv.Client.GetTask(t.Context(), 1)
	if err != nil || task.DeletedAt == nil {
		t.Fatalf("task = %+v, %v", task, err)
	}
}

func TestRevertPrefix(t *testing.T) {
	srv, sys := captureServer(t)
	const adds = 3*16 + 1 // pigeonhole: four ops share a first hex digit
	for i := range adds {
		if _, err := srv.Client.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: fmt.Sprintf("t%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	listOps := func() []api.OpSummary {
		t.Helper()
		page, err := srv.Client.ListOps(t.Context(), client.OpsQuery{PageQuery: client.PageQuery{Limit: 500}})
		if err != nil || page.NextCursor != nil {
			t.Fatalf("ops: %+v, %v", page, err)
		}
		return page.Items
	}
	ops := listOps()
	byFirst := map[byte][]string{}
	for _, op := range ops {
		byFirst[op.OpID[0]] = append(byFirst[op.OpID[0]], op.OpID)
	}
	var shared []string // newest first, as ListOps orders them
	for _, ids := range byFirst {
		if len(ids) > 3 {
			shared = ids
			break
		}
	}
	out, stderr, code := runCLI(t, sys, "", "revert", shared[0][:1])
	if code != 2 || out != "" || !strings.Contains(stderr, "matches more than 3 operations") ||
		!strings.Contains(stderr, shared[0]) || !strings.Contains(stderr, shared[1]) || !strings.Contains(stderr, shared[2]) ||
		strings.Contains(stderr, shared[3]) || !strings.Contains(stderr, "use more characters or the full ID") {
		t.Fatalf("ambiguous: exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	missing := strings.Repeat("0", 16)
	for _, op := range ops {
		for strings.HasPrefix(op.OpID, missing) {
			missing = strings.Repeat(string(missing[0]+1), 16)
		}
	}
	if out, stderr, code := runCLI(t, sys, "", "revert", missing); code != 5 || out != "" || !strings.Contains(stderr, missing) {
		t.Fatalf("missing: exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	for _, bad := range []string{"XYZ", "c0ffee!", ops[0].OpID + "0"} {
		if out, stderr, code := runCLI(t, sys, "", "revert", bad); code != 2 || out != "" {
			t.Fatalf("invalid %q: exit=%d stdout=%q stderr=%q", bad, code, out, stderr)
		}
	}
	for _, op := range listOps() {
		if op.State != "active" {
			t.Fatalf("a rejected prefix reverted %+v", op)
		}
	}
	target := ops[0] // the newest add, of the last task
	prefix := target.OpID[:8]
	for _, op := range ops[1:] {
		for strings.HasPrefix(op.OpID, prefix) {
			prefix = target.OpID[:len(prefix)+1]
		}
	}
	if out := captureRun(t, sys, "", "revert", prefix); !strings.Contains(out, "reverted") {
		t.Fatalf("unique prefix revert = %q", out)
	}
	if task, err := srv.Client.GetTask(t.Context(), adds); err != nil || task.DeletedAt == nil {
		t.Fatalf("task = %+v, %v", task, err)
	}
}

func TestFirstRunBeforeInit(t *testing.T) {
	sys, paths := testSys(t)
	out, stderr, code := runCLI(t, sys, "", "add", "x")
	if code != 4 || out != "" || !strings.Contains(stderr, paths.OwnerToken) || !strings.Contains(stderr, "not found — run `tkr init`") ||
		!strings.Contains(stderr, "nothing was saved. Your command was:\n  tkr add x\n") || strings.Contains(stderr, "panic") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
}

func TestUnreachableEcho(t *testing.T) {
	srv, sys := captureServer(t)
	srv.Stop()
	out, stderr, code := runCLI(t, sys, "", "add", "Restart stuck pg replica", "--now")
	want := "tkr: cannot reach the Taskronin server at " + srv.Socket + ": no such file or directory\n" +
		"tkr: nothing was saved. Your command was:\n  tkr add 'Restart stuck pg replica' --now\n" +
		"tkr: start it with: systemctl --user start taskronin.socket\n"
	if code != 3 || out != "" || stderr != want {
		t.Fatalf("exit=%d stdout=%q stderr=%q; want stderr=%q", code, out, stderr, want)
	}
}

func TestCaptureJSON(t *testing.T) {
	_, sys := captureServer(t)
	for _, args := range [][]string{
		{"add", "JSON"}, {"done", "1"}, {"reopen", "1"}, {"drop", "1"},
		{"delete", "1", "-y"}, {"undelete", "1"}, {"undo"}, {"redo"},
	} {
		out := captureRun(t, sys, "", append(args, "--json")...)
		var res api.OpResponse
		if err := json.Unmarshal([]byte(out), &res); err != nil || res.OpID == nil || len(res.Effects) == 0 {
			t.Fatalf("response = %q, %v", out, err)
		}
	}
}

func TestAddEditor(t *testing.T) {
	for _, name := range []string{"success", "editor failure", "missing editor", "too large", "server refusal", "unreachable"} {
		t.Run(name, func(t *testing.T) {
			srv, sys := captureServer(t)
			dir := t.TempDir()
			command := filepath.Join(dir, "editor")
			content := "Notes from editor\nMore notes\n"
			if name == "too large" {
				content = strings.Repeat("x", 65537)
			}
			buffer := filepath.Join(dir, "buffer")
			if err := os.WriteFile(buffer, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TKR_CAPTURE_NOTES", buffer)
			script := "#!/bin/sh\ncp \"$TKR_CAPTURE_NOTES\" \"$1\"\n"
			if name == "editor failure" {
				script += "exit 3\n"
			}
			if name != "missing editor" {
				if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				content = ""
			}
			sys.Getenv = func(key string) string {
				if key == "VISUAL" || key == "EDITOR" {
					return command
				}
				return os.Getenv(key)
			}
			args := []string{"add", "Title stays", "-e"}
			if name == "server refusal" {
				args = append(args, "--before", "999")
			}
			if name == "unreachable" {
				srv.Stop()
			}
			out, stderr, code := runCLI(t, sys, "", args...)
			if name == "unreachable" {
				srv.Start(t)
			}
			drafts, err := filepath.Glob(filepath.Join(srv.Paths.DraftsDir, "*.md"))
			if err != nil {
				t.Fatal(err)
			}
			if name == "success" {
				task, err := srv.Client.GetTask(t.Context(), 1)
				if code != 0 || stderr != "" || len(drafts) != 0 || err != nil || task.Title != "Title stays" || task.Notes != content {
					t.Fatalf("exit=%d stdout=%q stderr=%q drafts=%v task=%+v err=%v", code, out, stderr, drafts, task, err)
				}
				return
			}
			if code == 0 || out != "" || len(drafts) != 1 || !strings.Contains(stderr, "tkr: your draft is at "+drafts[0]) {
				t.Fatalf("exit=%d stdout=%q stderr=%q drafts=%v", code, out, stderr, drafts)
			}
			got, err := os.ReadFile(drafts[0])
			if err != nil || string(got) != content {
				t.Fatalf("draft content lost: %v", err)
			}
			if task := captureTask(t, srv, "first saved"); task.ID != 1 {
				t.Fatal("failed command saved task")
			}
		})
	}
}

func TestCaptureUsage(t *testing.T) {
	sys, _ := testSys(t)
	for _, args := range [][]string{
		{"add"}, {"add", "x", "--before", "0"}, {"add", "x", "--after", "b1"},
		{"done"}, {"done", "1", "2"}, {"done", "0"}, {"reopen", "e1"},
		{"drop", "-1"}, {"delete", "01", "-y"}, {"undelete", "#0"},
		{"undo", "1"}, {"redo", "1"}, {"revert"}, {"revert", "invalid"},
	} {
		out, stderr, code := runCLI(t, sys, "", args...)
		if code != 2 || out != "" || stderr == "" {
			t.Fatalf("%v: exit=%d stdout=%q stderr=%q", args, code, out, stderr)
		}
	}
}

func TestCaptureDateValidation(t *testing.T) {
	srv, sys := captureServer(t)
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"add", "x", "--defer", "next friday"}, 2},
		{[]string{"add", "x", "--due", ""}, 2},
		{[]string{"add", "x", "--block", "15:00-13:00"}, 2},
		{[]string{"add", "x", "--defer", "09:00"}, 1},
		{[]string{"add", "x", "--due", "yesterday"}, 1},
		{[]string{"done", "1", "--date", "today 09:00"}, 2},
	} {
		out, stderr, code := runCLI(t, sys, "", tc.args...)
		if code != tc.code || out != "" || stderr == "" {
			t.Fatalf("%v: exit=%d stdout=%q stderr=%q", tc.args, code, out, stderr)
		}
	}
	if task := captureTask(t, srv, "Weekday completion"); task.ID != 1 {
		t.Fatal("invalid command saved a task")
	}
	out := captureRun(t, sys, "", "done", "1", "--date", "wed")
	if !strings.Contains(out, "logged today") {
		t.Fatalf("Past mode resolved weekday incorrectly: %q", out)
	}
}

type captureInputFunc func([]byte) (int, error)

func (f captureInputFunc) Read(p []byte) (int, error) { return f(p) }

func TestDeletePrecondition(t *testing.T) {
	srv, sys := captureServer(t)
	sys.StdinTTY = true
	task := captureTask(t, srv, "Original")
	answer := strings.NewReader("y\n")
	changed := false
	in := captureInputFunc(func(p []byte) (int, error) {
		if !changed {
			// The CLI already fetched the ETag and displayed the prompt. Change
			// the real task before confirming to exercise the stale precondition.
			_, err := srv.Client.UpdateTask(t.Context(), client.Mut{IfMatch: task.ETag}, task.ID,
				api.UpdateTaskRequest{Title: api.Opt[string]{Set: true, V: "Changed elsewhere"}})
			if err != nil {
				t.Fatal(err)
			}
			changed = true
		}
		return answer.Read(p)
	})
	var out, stderr bytes.Buffer
	code := run(t.Context(), []string{"delete", "1"}, in, &out, &stderr, sys)
	if code != 6 || out.Len() != 0 || !strings.Contains(stderr.String(), "current: #1 version 2") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), stderr.String())
	}
	current, err := srv.Client.GetTask(t.Context(), 1)
	if err != nil || current.Title != "Changed elsewhere" || current.DeletedAt != nil || current.Version != 2 {
		t.Fatalf("stale delete was retried: %+v, %v", current, err)
	}
}

func TestCaptureEffects(t *testing.T) {
	var out bytes.Buffer
	e := &env{out: &out, clockLoaded: true, serverClock: &api.Clock{Today: civil.MustDate("2026-10-28")}}
	res := &api.OpResponse{Effects: []api.Effect{
		{Kind: "deferred", TaskID: 42, To: "2026-10-30 09:00"},
		{Kind: "undeferred", TaskID: 42},
		{Kind: "due_set", TaskID: 42, To: "2026-10-31"},
		{Kind: "due_cleared", TaskID: 42},
		{Kind: "blocked", BlockID: 7, To: "2026-10-28 13:00-15:00"},
		{Kind: "block_moved", BlockID: 7, From: "13:00", To: "14:00"},
		{Kind: "block_cancelled", BlockID: 7, To: "task_closed"},
		{Kind: "block_cancelled", BlockID: 7},
		{Kind: "block_resolved", BlockID: 7, To: "done"},
		{Kind: "block_resolved", BlockID: 7},
		{Kind: "outcome_set", BlockID: 7, To: "worked"},
		{Kind: "slipped", BlockID: 7},
		{Kind: "unslipped", BlockID: 7},
		{Kind: "reason_added", TaskID: 42, Reason: "slipped"},
		{Kind: "reason_removed", TaskID: 42, Reason: "new"},
		{Kind: "focus_set", TaskID: 42},
		{Kind: "focus_cleared"},
		{Kind: "interrupted", From: "41"},
		{Kind: "retitled", TaskID: 42, From: "Old", To: "New"},
		{Kind: "entry_logged", EntryID: 9},
		{Kind: "entry_updated", EntryID: 9},
		{Kind: "entry_relinked", EntryID: 9},
		{Kind: "entry_deleted", EntryID: 9},
		{Kind: "entry_restored", EntryID: 9},
		{Kind: "reverted", Via: "undo", From: "abcdef12", To: "task.complete"},
		{Kind: "reverted", Via: "redo", From: "abcdef13", To: "entry.add"},
		{Kind: "reverted", Via: "revert", From: "abcdef14"},
		{Kind: "created", TaskID: 42},
		{Kind: "updated", TaskID: 42},
		{Kind: "placed", TaskID: 42},
		{Kind: "moved", TaskID: 42},
		{Kind: "completed", TaskID: 42},
		{Kind: "reopened", TaskID: 42},
		{Kind: "dropped", TaskID: 42},
		{Kind: "deleted", TaskID: 42},
		{Kind: "undeleted", TaskID: 42},
		{Kind: "reviewed", To: "123"},
		{Kind: "purged", TaskID: 42},
	}}
	covered := make(map[service.EffectKind]bool)
	for _, effect := range res.Effects {
		covered[service.EffectKind(effect.Kind)] = true
	}
	for _, kind := range service.EffectKinds() {
		if !covered[kind] {
			t.Errorf("missing CLI wording for %q", kind)
		}
	}
	res.Effects = append(res.Effects, api.Effect{Kind: "future_kind", TaskID: 42})
	e.printOp(res, plainHeadline("Effects"))
	testutil.Golden(t, "effects", out.Bytes())
	plain := out.String()
	out.Reset()
	e.color, e.colorSet = "always", true
	e.printOp(res, plainHeadline("Effects"))
	p := paint{on: true}
	for _, token := range []string{p.taskID(42), p.blockID(7), p.entryID(9), p.id("op abcdef12"), p.warn("due Sat 2026-10-31"), p.bad("slipped"), p.ok("worked")} {
		if !strings.Contains(out.String(), token) {
			t.Errorf("missing effect token %q", token)
		}
	}
	if colorSGR.ReplaceAllString(out.String(), "") != plain {
		t.Errorf("color changed plain effects: %q", out.String())
	}
}

func TestCaptureEffectsSanitized(t *testing.T) {
	var out bytes.Buffer
	e := &env{out: &out}
	res := &api.OpResponse{Effects: []api.Effect{{Kind: "retitled", TaskID: 1, From: "Old\nline", To: "New\tname\x1b[31m"}}}
	e.printOp(res, plainHeadline("Headline\r\x1b[2J"))
	want := "Headline��[2J\n#1 retitled 'Old�line' → 'New�name�[31m'\n"
	if out.String() != want {
		t.Fatalf("unsanitized output: %q; want %q", out.String(), want)
	}
	// A title that mentions another effect must not suppress it.
	out.Reset()
	res.Task = &api.Task{ID: 1, Title: "completed; ✓ logged b7 slipped"}
	res.Effects = []api.Effect{{Kind: "created", TaskID: 1}, {Kind: "slipped", TaskID: 1, BlockID: 7}, {Kind: "reason_added", TaskID: 2, Reason: "new"}}
	e.printOp(res, func(p paint) string { return p.taskHeadline(res.Task, "added to triage (new)") })
	if !strings.HasSuffix(out.String(), "b7 slipped\n#2 needs triage (new)\n") || strings.Contains(out.String(), "#1 created") {
		t.Fatalf("incorrect effect suppression: %q", out.String())
	}
}

type captureFailWriter struct {
	bytes.Buffer
	remaining, writes int
	err               error
}

func (w *captureFailWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.remaining == 0 {
		return 0, w.err
	}
	w.remaining--
	return w.Buffer.Write(p)
}

func TestCaptureOutputFailure(t *testing.T) {
	for _, tc := range []struct {
		name      string
		remaining int
	}{
		{"headline", 0}, {"effect", 1}, {"tally", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, sys := captureServer(t)
			captureTask(t, srv, "Set up keycloak")
			logFocus(t, srv)
			writeErr := errors.New("output destination is full")
			out := &captureFailWriter{remaining: tc.remaining, err: writeErr}
			var stderr bytes.Buffer
			code := run(t.Context(), []string{"done", "1"}, strings.NewReader(""), out, &stderr, sys)
			if code != 1 || !strings.Contains(stderr.String(), "write operation output: "+writeErr.Error()) || strings.Contains(stderr.String(), "nothing was saved") {
				t.Errorf("exit=%d stdout=%q stderr=%q", code, out.String(), stderr.String())
			}
			if out.writes != tc.remaining+1 {
				t.Errorf("writes=%d; want %d (stop on failure)", out.writes, tc.remaining+1)
			}
			if tc.remaining == 1 && out.String() != "✓ #1 'Set up keycloak' done · logged today\n" {
				t.Errorf("headline=%q", out.String())
			}
			task, err := srv.Client.GetTask(t.Context(), 1)
			if err != nil || task.Status != "done" || len(logEntries(t, srv)) != 1 {
				t.Fatalf("confirmed mutation must stay applied: task=%+v, err=%v", task, err)
			}
		})
	}
}

func TestCapturePrintOpPreservesError(t *testing.T) {
	first := errors.New("first output failure")
	out := &captureFailWriter{err: first}
	e := &env{out: out}
	res := &api.OpResponse{Effects: []api.Effect{{Kind: "slipped", BlockID: 7}}}
	e.printOp(res, plainHeadline("Headline"))
	out.err = errors.New("later output failure")
	if err := e.outputOp(res, plainHeadline("Next headline")); !errors.Is(err, first) {
		t.Fatalf("outputOp error=%v; want original write error", err)
	}
	if out.writes != 1 {
		t.Fatalf("writes=%d; want no more writes after the first failure", out.writes)
	}
}

func TestDoneTally(t *testing.T) {
	srv, sys := captureServer(t)
	captureTask(t, srv, "First")
	captureTask(t, srv, "Second")
	captureRun(t, sys, "", "log", "-N", "Standalone work")
	if out := captureRun(t, sys, "", "done", "1"); !strings.HasSuffix(out, "one done today\n") {
		t.Errorf("first completion: %q", out)
	}
	if out := captureRun(t, sys, "", "done", "2"); !strings.HasSuffix(out, "2 done today\n") {
		t.Errorf("second completion: %q", out)
	}
	captureRun(t, sys, "", "reopen", "1")
	if out := captureRun(t, sys, "", "done", "1"); !strings.HasSuffix(out, "2 done today\n") {
		t.Errorf("after reopening and completing again: %q", out)
	}
}

func TestDoneTallyOmittedOnTimeout(t *testing.T) {
	srv, sys := captureServer(t)
	captureTask(t, srv, "First")
	var out, stderr bytes.Buffer
	e := &env{ctx: t.Context(), apiClient: srv.Client, out: &out, err: &stderr, sys: sys}
	root := newRoot(e)
	e.tallyTimeout = 0
	root.SetArgs([]string{"done", "1"})
	if err := root.ExecuteContext(e.ctx); err != nil {
		t.Fatal(err)
	}
	if out.String() != "✓ #1 'First' done · logged today\n" || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", out.String(), stderr.String())
	}
}

func TestDoneBackdatedHasNoTally(t *testing.T) {
	srv, sys := captureServer(t)
	captureTask(t, srv, "First")
	if out := captureRun(t, sys, "", "done", "1", "--date", "yesterday"); out != "✓ #1 'First' done · logged yesterday\n" {
		t.Fatalf("stdout=%q", out)
	}
}

func TestDoneOnDeferredTaskPrintsNoDeferLine(t *testing.T) {
	_, sys := captureServer(t)
	captureRun(t, sys, "", "add", "First", "--defer", "tomorrow")
	if out := captureRun(t, sys, "", "done", "1"); out != "✓ #1 'First' done · logged today\none done today\n" {
		t.Fatalf("stdout=%q", out)
	}
}

func captureRootOp(t *testing.T, sys sysDeps, args ...string) string {
	t.Helper()
	out := captureRun(t, sys, "", append(args, "--json")...)
	var res api.OpResponse
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.OpID == nil || len(*res.OpID) < 8 {
		t.Fatalf("expected operation ID, got %q (%v)", out, err)
	}
	return (*res.OpID)[:8]
}

func TestRedoPrintsVerbAndRootOp(t *testing.T) {
	srv, sys := captureServer(t)
	captureTask(t, srv, "First")
	id := captureRootOp(t, sys, "done", "1")
	captureRun(t, sys, "", "undo")
	out := captureRun(t, sys, "", "redo")
	if !strings.HasPrefix(out, "redid complete (op "+id+")\n") || strings.Count(out, "(op ") != 1 {
		t.Fatalf("redo of %s: %q", id, out)
	}
}

func TestKeepPrintsOneLine(t *testing.T) {
	srv, sys := captureServer(t)
	for _, title := range []string{"First", "Second", "Third"} {
		captureTask(t, srv, title)
	}
	if out := captureRun(t, sys, "", "keep", "3"); out != "#3 kept in place\n" {
		t.Fatalf("stdout=%q", out)
	}
}

func TestUndoKeepPrintsNeedsTriage(t *testing.T) {
	srv, sys := captureServer(t)
	for _, title := range []string{"First", "Second", "Third"} {
		captureTask(t, srv, title)
	}
	id := captureRootOp(t, sys, "keep", "3")
	if out := captureRun(t, sys, "", "undo"); out != "undid place (op "+id+")\n#3 placed\n#3 needs triage (new)\n" {
		t.Fatalf("undo of %s: %q", id, out)
	}
}

func TestAddMetadataHeadline(t *testing.T) {
	_, sys := captureServer(t)
	if out := captureRun(t, sys, "", "add", "Fix", "--top", "-t", "ci", "-P", "infra.ci", "-p", "H"); out != "#1 'Fix' added to backlog (top) · infra.ci +ci prio H\n" {
		t.Fatalf("stdout=%q", out)
	}
}

func TestPlacementSuppressesOnlyAffectedTasks(t *testing.T) {
	e := &env{}
	resp := &api.OpResponse{Tasks: []api.Task{{ID: 3}, {ID: 7}}}
	headline := "accepted 2 new tasks into the backlog"
	for _, tc := range []struct {
		kind   string
		taskID int64
		hidden bool
	}{
		{"reason_removed", 3, true}, {"undeferred", 7, true},
		{"reason_removed", 8, false}, {"undeferred", 8, false},
		{"reason_added", 3, false}, {"deferred", 7, false},
	} {
		effect := api.Effect{Kind: tc.kind, TaskID: tc.taskID, Reason: "manual"}
		if hidden := e.effectInHeadline(resp, effect, headline); hidden != tc.hidden {
			t.Errorf("%s on #%d: hidden=%t, want %t", tc.kind, tc.taskID, hidden, tc.hidden)
		}
	}
}

var colorSGR = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestColorNeverMatchesPlain(t *testing.T) {
	for _, mode := range []string{"never", "auto (non-TTY)", "NO_COLOR"} {
		t.Run(mode, func(t *testing.T) { checkColorOutputs(t, mode) })
	}
}

func TestColorAlwaysStripsToPlain(t *testing.T) {
	checkColorOutputs(t, "always")
}

func checkColorOutputs(t *testing.T, mode string) {
	t.Helper()
	for _, command := range []string{"ls", "today", "done"} {
		t.Run(command, func(t *testing.T) {
			var sys sysDeps
			var want string
			args := []string{command}
			switch command {
			case "ls":
				sys = seedLs(t)
				want = plainLsFixture
			case "today":
				sys = seedDashboard(t)
			case "done":
				srv, deps := captureServer(t)
				sys = deps
				captureTask(t, srv, "Set up keycloak")
				args = append(args, "1")
			}
			if command != "ls" {
				name := map[string]string{"today": "dashboard", "done": "done"}[command]
				b, err := os.ReadFile(filepath.Join("testdata", "golden", name+".golden"))
				if err != nil {
					t.Fatal(err)
				}
				want = string(b)
			}
			switch mode {
			case "NO_COLOR":
				t.Setenv("NO_COLOR", "1")
				sys.StdoutTTY = true
			case "always", "never":
				args = append(args, "--color", mode)
			}
			out := captureRun(t, sys, "", args...)
			plain := out
			if mode == "always" {
				plain = colorSGR.ReplaceAllString(out, "")
				if plain == out {
					t.Fatal("--color always emitted no SGR sequences")
				}
				testutil.Golden(t, command+"_color", []byte(out))
			}
			if plain != want {
				t.Fatalf("plain=%q want=%q", plain, want)
			}
		})
	}
}

func TestPrintOpUnsafeHeadline(t *testing.T) {
	for _, mode := range []string{"always", "never"} {
		t.Run(mode, func(t *testing.T) {
			var out bytes.Buffer
			e := &env{out: &out, color: mode, colorSet: true}
			e.printOp(&api.OpResponse{}, func(p paint) string { return p.id("#1") + " Bad\x1b[31mtitle" })
			if got, want := out.String(), "#1 Bad�[31mtitle\n"; got != want {
				t.Fatalf("unsafe headline = %q, want %q", got, want)
			}
		})
	}
}

func TestColorSanitizesBeforeStyling(t *testing.T) {
	palette := regexp.MustCompile(`\x1b\[(0|1|31|32|33|36|90)?m`)
	for _, tc := range []struct{ name, title, clean string }{
		{"palette", "Bad\x1b[31mtitle", "Bad�[31mtitle"},
		{"non-palette", "Bad\x1b]52;c;AAAA\x07\x1b[2Jtitle", "Bad�]52;c;AAAA��[2Jtitle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sys, _ := testSys(t)
			socket, token := fakeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/tasks" {
					t.Errorf("unexpected %s %s", r.Method, r.URL)
				}
				writeJSON(t, w, api.OpResponse{Task: &api.Task{ID: 1, Title: tc.title}})
			}))
			out := captureRun(t, sys, "", "--socket", socket, "--token-file", token, "add", "requested title", "--color", "always")
			if !strings.Contains(out, "'"+(paint{on: true}).strong(tc.clean)+"'") {
				t.Errorf("expected a sanitized, bold title inside plain quotes: %q", out)
			}
			if strings.Contains(out, tc.title) || strings.ContainsAny(palette.ReplaceAllString(out, ""), "\x1b\x07") {
				t.Fatalf("unexpected control bytes in %q", out)
			}
		})
	}
}

func TestLipglossOnlyInPaint(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file == "paint.go" || strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			if strings.Trim(imp.Path.Value, "\"`") == "charm.land/lipgloss/v2" {
				t.Errorf("%s imports lipgloss directly", file)
			}
		}
	}
}

func TestSchedulingJSONClockReads(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		requests string
	}{
		{"add", []string{"add", "x", "--defer", "tomorrow"}, "GET /clock, POST /tasks"},
		{"due", []string{"due", "1", "tomorrow"}, "GET /clock, GET /tasks/1, POST /tasks/1/due"},
		{"defer", []string{"defer", "1", "tomorrow"}, "GET /clock, GET /tasks/1, POST /tasks/1/defer"},
		{"block", []string{"block", "1", "13:00-14:00"}, "GET /clock, GET /tasks/1, POST /tasks/1/block"},
		{"clear due", []string{"due", "1", "none"}, "GET /tasks/1, POST /tasks/1/due"},
		{"clear defer", []string{"defer", "1", "none"}, "GET /tasks/1, POST /tasks/1/defer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sys, _ := testSys(t)
			var mu sync.Mutex
			var requests []string
			socket, token := fakeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				path := strings.TrimPrefix(r.URL.Path, "/api/v1")
				requests = append(requests, r.Method+" "+path)
				switch path {
				case "/clock":
					writeJSON(t, w, api.Clock{Today: civil.MustDate("2026-10-28"), WeekStart: "monday"})
				case "/tasks/1":
					writeJSON(t, w, api.TaskDetail{Task: api.Task{ID: 1, ETag: "etag"}})
				case "/tasks/1/block":
					// Omit the task metadata used in the text headline.
					writeJSON(t, w, map[string]any{"block": map[string]any{"id": 2, "date": "2026-10-29", "end": "14:00"}})
				case "/tasks", "/tasks/1/due", "/tasks/1/defer":
					task := &api.Task{ID: 1}
					// Omit the moment for date-setting commands. For "none",
					// supply a date so an eager headline would read /clock.
					if tc.args[2] == "none" {
						date := &api.Moment{Date: civil.MustDate("2026-10-29")}
						task.Due, task.Defer = date, date
					}
					writeJSON(t, w, api.OpResponse{Task: task})
				default:
					t.Errorf("unexpected request %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			out := captureRun(t, sys, "", append([]string{"--socket", socket, "--token-file", token, "--json"}, tc.args...)...)
			mu.Lock()
			defer mu.Unlock()
			if !json.Valid([]byte(out)) || strings.Join(requests, ", ") != tc.requests {
				t.Errorf("requests=%v output=%q", requests, out)
			}
		})
	}
}

func TestNewTaskPlacementHeadlineOnly(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"block", "1", "13:00-14:00"}, "b1 today 13:00–14:00 #1 'First'"},
		{[]string{"move", "1", "top"}, "#1 moved top"},
		{[]string{"defer", "1", "tomorrow"}, "#1 deferred to tomorrow"},
	} {
		t.Run(tc.args[0], func(t *testing.T) {
			srv, sys := captureServer(t)
			captureTask(t, srv, "First")
			if got := captureRun(t, sys, "", tc.args...); got != tc.want+"\n" {
				t.Fatalf("output=%q", got)
			}
		})
	}
}

func TestOutputReversalFallback(t *testing.T) {
	var out bytes.Buffer
	e := &env{out: &out}
	if err := e.outputReversal(&api.OpResponse{OpID: new("0123456789abcdef")}, "undone"); err != nil || out.String() != "undone (op 01234567)\n" {
		t.Fatalf("output=%q err=%v", out.String(), err)
	}
}
