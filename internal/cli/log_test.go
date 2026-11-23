package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/client"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/testutil"
)

func TestFOC10_ParseLogArgs(t *testing.T) {
	for _, tc := range []struct {
		name           string
		args           []string
		noTask, editor bool
		id             domain.TaskID
		hasID          bool
		text, err      string
	}{
		{name: "missing", err: "missing text"},
		{name: "numeric only", args: []string{"42"}, err: `missing text after task id (did you mean: tkr log 42 "…")`},
		{name: "numeric text", args: []string{"42 things done"}, text: "42 things done"},
		{name: "id and text", args: []string{"42", "Fixed it"}, id: 42, hasID: true, text: "Fixed it"},
		{name: "prefixed id", args: []string{"#42", "x"}, id: 42, hasID: true, text: "x"},
		{name: "two words", args: []string{"abc", "x"}, err: `quote your text: tkr log "…"`},
		{name: "three words", args: []string{"42", "Fixed", "it"}, err: "quote your text"},
		{name: "conflict", args: []string{"42", "x"}, noTask: true, err: "-N conflicts with a task id"},
		{name: "editor id", args: []string{"42"}, editor: true, id: 42, hasID: true},
		{name: "editor empty", editor: true},
		{name: "editor text", args: []string{"Fixed it"}, editor: true, text: "Fixed it"},
		{name: "editor conflict", args: []string{"#42"}, editor: true, noTask: true, err: "-N conflicts with a task id"},
		{name: "standalone", args: []string{"x"}, noTask: true, text: "x"},
		{name: "zero is text", args: []string{"0"}, text: "0"},
		{name: "leading zero is text", args: []string{"01"}, text: "01"},
		{name: "max id", args: []string{"9223372036854775807", "x"}, id: 9223372036854775807, hasID: true, text: "x"},
		{name: "overflow", args: []string{"9223372036854775808", "x"}, err: `invalid task ID "9223372036854775808"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, hasID, text, err := parseLogArgs(tc.args, tc.noTask, tc.editor)
			if tc.err != "" {
				if exitCode(err) != 2 || err.Error() != tc.err {
					t.Fatalf("error = %v; want usage %q", err, tc.err)
				}
				return
			}
			if err != nil || id != tc.id || hasID != tc.hasID || text != tc.text {
				t.Fatalf("parseLogArgs = (%d, %t, %q, %v)", id, hasID, text, err)
			}
		})
	}
}

func FuzzParseLogArgs(f *testing.F) {
	for _, input := range []string{"", "42", "42\x00Fixed it", "#42\x00x", "42 things done", "42\x00Fixed\x00it", "9223372036854775808"} {
		f.Add(input, false, false)
		f.Add(input, true, true)
	}
	f.Fuzz(func(t *testing.T, input string, noTask, editor bool) {
		var args []string
		if input != "" {
			args = strings.Split(input, "\x00")
		}
		id, hasID, _, err := parseLogArgs(args, noTask, editor)
		if err == nil && hasID && (noTask || id <= 0) {
			t.Fatalf("invalid success: id=%d hasID=%t noTask=%t", id, hasID, noTask)
		}
		if err != nil && exitCode(err) != 2 {
			t.Fatalf("non-usage error: %v", err)
		}
	})
}

func logFixture(t *testing.T) (*testutil.Server, sysDeps) {
	t.Helper()
	srv := testutil.StartServer(t, testutil.WithClock("2026-10-28 13:30"))
	srv.Clock.Set(testutil.Local(t, "2026-10-28 10:00"))
	for i, title := range []string{"Set up keycloak", "Write report"} {
		req := api.AddTaskRequest{Title: title}
		if i == 1 {
			req.Place = &api.AddPlacement{How: "block", Block: &api.BlockRequest{
				Date: civil.MustDate("2026-10-28"), Start: 13 * 60, End: 15 * 60,
			}}
		}
		res, err := srv.Client.AddTask(t.Context(), client.Mut{}, req)
		if err != nil {
			t.Fatal(err)
		}
		if res.Task == nil || res.Task.ID != int64(i+1) {
			t.Fatalf("fixture task: %+v", res)
		}
	}
	srv.Clock.Set(testutil.Local(t, "2026-10-28 13:30"))
	return srv, sysDeps{StdinTTY: true, Getenv: os.Getenv}
}

func logFocus(t *testing.T, srv *testutil.Server) {
	t.Helper()
	_, err := srv.Client.SetFocus(t.Context(), client.Mut{}, api.FocusRequest{TaskID: api.Opt[int64]{Set: true, V: 1}})
	if err != nil {
		t.Fatal(err)
	}
}

func logEntries(t *testing.T, srv *testutil.Server) []api.Entry {
	t.Helper()
	from, to := civil.MustDate("2026-10-20"), civil.MustDate("2026-10-29")
	page, err := srv.Client.ListEntries(t.Context(), client.EntryQuery{From: &from, To: &to})
	if err != nil {
		t.Fatal(err)
	}
	return page.Items
}

func assertLogEntry(t *testing.T, srv *testutil.Server, task int64, date, title string) api.Entry {
	t.Helper()
	entries := logEntries(t, srv)
	if len(entries) == 0 {
		t.Fatal("no entry persisted")
	}
	entry := entries[0]
	if entry.Title != title || entry.WorkDate.String() != date ||
		(task == 0 && entry.TaskID != nil) || (task != 0 && (entry.TaskID == nil || *entry.TaskID != task)) {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	return entry
}

func TestFOC04_FocusWins(t *testing.T) {
	srv, sys := logFixture(t)
	logFocus(t, srv)
	out, stderr, code := runCLI(t, sys, "", "log", "x")
	if code != 0 || stderr != "" || out != "→ #1 (focus)\ne1 logged today on #1 'Set up keycloak'\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	assertLogEntry(t, srv, 1, "2026-10-28", "x")
}

func TestFOC05_CurrentBlock(t *testing.T) {
	srv, sys := logFixture(t)
	out, stderr, code := runCLI(t, sys, "", "log", "x")
	if code != 0 || stderr != "" || out != "→ #2 (current block)\ne1 logged today on #2 'Write report'\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	assertLogEntry(t, srv, 2, "2026-10-28", "x")
}

func TestFOC06_NoTask(t *testing.T) {
	srv, sys := logFixture(t)
	srv.Clock.Set(testutil.Local(t, "2026-10-28 16:00"))
	out, stderr, code := runCLI(t, sys, "", "log", "x")
	if code != 0 || stderr != "" || out != "→ no task\ne1 logged today (no task)\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	assertLogEntry(t, srv, 0, "2026-10-28", "x")
}

func TestFOC07_Backdated(t *testing.T) {
	for _, tc := range []struct {
		args []string
		task int64
		code int
	}{
		{[]string{"x", "--date", "yesterday"}, 0, 2},
		{[]string{"-N", "x", "--date", "yesterday"}, 0, 0},
		{[]string{"2", "x", "--date", "yesterday"}, 2, 0},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			srv, sys := logFixture(t)
			logFocus(t, srv)
			out, stderr, code := runCLI(t, sys, "", append([]string{"log"}, tc.args...)...)
			if code != tc.code {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
			if code == 2 {
				if stderr != "tkr: backdated entries need a task id or -N\n" || out != "" || len(logEntries(t, srv)) != 0 {
					t.Fatalf("rejected log: stdout=%q stderr=%q", out, stderr)
				}
				return
			}
			if stderr != "" || !strings.Contains(out, "logged yesterday") || strings.Contains(out, "→") {
				t.Fatalf("stdout=%q stderr=%q", out, stderr)
			}
			assertLogEntry(t, srv, tc.task, "2026-10-27", "x")
		})
	}
}

func TestFOC08_ExplicitWins(t *testing.T) {
	for _, tc := range []struct {
		arg  string
		task int64
	}{{"2", 2}, {"-N", 0}} {
		t.Run(tc.arg, func(t *testing.T) {
			srv, sys := logFixture(t)
			logFocus(t, srv)
			out, stderr, code := runCLI(t, sys, "", "log", tc.arg, "x")
			if code != 0 || stderr != "" || strings.Contains(out, "→") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
			assertLogEntry(t, srv, tc.task, "2026-10-28", "x")
		})
	}
}

func TestINT06_FocusBeatsBlockAfterInterrupt(t *testing.T) {
	srv, sys := logFixture(t)
	res, err := srv.Client.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{
		Title: "Restart stuck pg replica", Place: &api.AddPlacement{How: "now"},
	})
	if err != nil || res.Task.ID != 3 {
		t.Fatalf("interrupt: %+v, %v", res, err)
	}
	out, stderr, code := runCLI(t, sys, "", "log", "Restarted replica")
	if code != 0 || stderr != "" || !strings.HasPrefix(out, "→ #3 (focus)\n") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	assertLogEntry(t, srv, 3, "2026-10-28", "Restarted replica")
}

func TestFOC11_FocusClearedAtMidnight(t *testing.T) {
	srv, sys := logFixture(t)
	logFocus(t, srv)
	srv.Clock.Set(testutil.Local(t, "2026-10-29 00:10"))
	out, stderr, code := runCLI(t, sys, "", "log", "x")
	if code != 0 || stderr != "" || out != "→ no task\ne1 logged today (no task)\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	assertLogEntry(t, srv, 0, "2026-10-29", "x")
}

func TestLogEditorCrossesMidnight(t *testing.T) {
	for _, tc := range []struct {
		name, date, link, wantDate string
		wantTask                   int64
		wantCode                   int
	}{
		{"implicit past date", "2026-10-28", "", "", 0, 2},
		{"implicit today", "today", "", "2026-10-29", 2, 0},
		{"explicit past date", "2026-10-28", "1", "2026-10-28", 1, 0},
		{"standalone past date", "2026-10-28", "-N", "2026-10-28", 0, 0},
		{"explicit yesterday", "yesterday", "1", "2026-10-28", 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, sys := logFixture(t)
			srv.Clock.Set(testutil.Local(t, "2026-10-28 23:59"))
			logFocus(t, srv)
			command := filepath.Join(t.TempDir(), "editor")
			const buffer = "Edited at midnight\n\nRetained notes\n"
			script := "#!/bin/sh\nprintf 'Edited at midnight\\n\\nRetained notes\\n' > \"$1\"\nprintf 'editor ready\\n'\nread -r finished\n"
			if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			sys.Getenv = func(key string) string {
				if key == "VISUAL" || key == "EDITOR" {
					return command
				}
				return os.Getenv(key)
			}
			in, release, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			out := &promptOutput{prompt: "editor ready\n", seen: make(chan struct{})}
			var stderr bytes.Buffer
			args := []string{"log", "-e", "--date", tc.date}
			if tc.link != "" {
				args = append(args, tc.link)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			done := make(chan int, 1)
			go func() {
				defer close(done)
				done <- run(ctx, args, in, out, &stderr, sys)
			}()
			t.Cleanup(func() {
				cancel()
				_ = release.Close()
				_ = in.Close()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("editor command did not stop")
				}
			})
			select {
			case <-out.seen:
			case code := <-done:
				t.Fatalf("editor did not open: exit=%d stderr=%q", code, stderr.String())
			}
			// Advance only while the real editor process is blocked on stdin.
			srv.Clock.Set(testutil.Local(t, "2026-10-29 00:10"))
			_, err = srv.Client.SetFocus(t.Context(), client.Mut{}, api.FocusRequest{TaskID: api.Opt[int64]{Set: true, V: 2}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := release.Write([]byte("\n")); err != nil {
				t.Fatal(err)
			}
			code := <-done
			if code != tc.wantCode {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), stderr.String())
			}
			drafts, err := filepath.Glob(filepath.Join(srv.Paths.DraftsDir, "*.md"))
			if err != nil {
				t.Fatal(err)
			}
			if code != 0 {
				if len(logEntries(t, srv)) != 0 || len(drafts) != 1 || out.String() != "editor ready\n" ||
					stderr.String() != "tkr: backdated entries need a task id or -N\ntkr: your draft is at "+drafts[0]+"\n" {
					t.Fatalf("rejected log: drafts=%v stdout=%q stderr=%q", drafts, out.String(), stderr.String())
				}
				got, err := os.ReadFile(drafts[0])
				if err != nil || string(got) != buffer {
					t.Fatalf("retained draft=%q, %v", got, err)
				}
				return
			}
			entry := assertLogEntry(t, srv, tc.wantTask, tc.wantDate, "Edited at midnight")
			if len(drafts) != 0 || stderr.Len() != 0 || entry.Notes != "Retained notes\n" {
				t.Fatalf("successful log: drafts=%v stderr=%q entry=%+v", drafts, stderr.String(), entry)
			}
			if tc.link == "" && !strings.Contains(out.String(), "→ #2 (focus)\n") {
				t.Fatalf("missing new day's focus: %q", out.String())
			}
		})
	}
}

func TestFOC12_NonInteractive(t *testing.T) {
	srv, sys := logFixture(t)
	sys.StdinTTY = false
	out, stderr, code := runCLI(t, sys, "", "log", "x")
	if code != 2 || out != "" || stderr != "tkr: a task id or -N is required when stdin is not a terminal\n" || len(logEntries(t, srv)) != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	for _, arg := range []string{"-N", "2"} {
		out, stderr, code = runCLI(t, sys, "", "log", arg, "x")
		if code != 0 || stderr != "" || strings.Contains(out, "→") {
			t.Fatalf("%s: exit=%d stdout=%q stderr=%q", arg, code, out, stderr)
		}
	}
}

func TestLogAtValidation(t *testing.T) {
	srv, sys := logFixture(t)
	for _, at := range []string{"24:00", "9:5", ""} {
		out, stderr, code := runCLI(t, sys, "", "log", "-N", "x", "--at", at)
		if code != 2 || out != "" || !strings.Contains(stderr, "tkr: ") || len(logEntries(t, srv)) != 0 {
			t.Fatalf("%q: exit=%d stdout=%q stderr=%q", at, code, out, stderr)
		}
	}
	out, stderr, code := runCLI(t, sys, "", "log", "-N", "x", "--at", "13:00")
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	entry := assertLogEntry(t, srv, 0, "2026-10-28", "x")
	if entry.Time == nil || *entry.Time != 13*60 {
		t.Fatalf("entry time = %v", entry.Time)
	}
}

func TestLogWarnsClosedTask(t *testing.T) {
	srv, sys := logFixture(t)
	task, err := srv.Client.GetTask(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Client.CompleteTask(t.Context(), client.Mut{IfMatch: task.ETag}, 1, api.CompleteRequest{}); err != nil {
		t.Fatal(err)
	}
	res, err := srv.Client.AddEntry(t.Context(), client.Mut{}, api.AddEntryRequest{TaskID: api.Opt[int64]{Set: true, V: 1}, Title: "warning reference"})
	if err != nil || len(res.Warnings) == 0 {
		t.Fatalf("server warning: %+v, %v", res, err)
	}
	out, stderr, code := runCLI(t, sys, "", "log", "1", "x")
	want := ""
	for _, warning := range res.Warnings {
		want += "tkr: warning: " + warning + "\n"
	}
	if code != 0 || stderr != want || !strings.Contains(out, "on #1 'Set up keycloak'") {
		t.Fatalf("exit=%d stdout=%q stderr=%q; want stderr=%q", code, out, stderr, want)
	}
}

func TestLogDatesAndNotes(t *testing.T) {
	srv, sys := logFixture(t)
	logFocus(t, srv)
	for _, date := range []string{"today", "wed", "+0d", "2026-10-28"} {
		out, stderr, code := runCLI(t, sys, "", "log", "x", "--date", date, "-m", "notes\nmore")
		if code != 0 || stderr != "" || !strings.HasPrefix(out, "→ #1 (focus)\n") {
			t.Fatalf("%s: exit=%d stdout=%q stderr=%q", date, code, out, stderr)
		}
		entry := assertLogEntry(t, srv, 1, "2026-10-28", "x")
		if entry.Notes != "notes\nmore" || entry.Time != nil {
			t.Fatalf("unexpected entry content: %+v", entry)
		}
	}
	for _, date := range []string{"last tuesday", "", "today 13:00"} {
		out, stderr, code := runCLI(t, sys, "", "log", "-N", "x", "--date", date)
		if code != 2 || out != "" || stderr == "" || len(logEntries(t, srv)) != 4 {
			t.Fatalf("%q: exit=%d stdout=%q stderr=%q", date, code, out, stderr)
		}
	}
}

func TestLogJSON(t *testing.T) {
	srv, sys := logFixture(t)
	logFocus(t, srv)
	out, stderr, code := runCLI(t, sys, "", "log", "x", "--json")
	var res api.OpResponse
	if code != 0 || stderr != "" || json.Unmarshal([]byte(out), &res) != nil || res.Entry == nil || res.Entry.TaskID == nil || *res.Entry.TaskID != 1 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
}

func TestLogEditor(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		args          []string
		code          int
	}{
		{"prefill", "", []string{"2", "Initial", "-m", "Original notes"}, 0},
		{"first non-empty line", "\n \nEdited title\n\nEdited notes\n", []string{"-N"}, 0},
		{"id only", "Edited title\n\nEdited notes\n", []string{"2"}, 0},
		{"missing executable", "", []string{"-N", "Initial"}, 1},
		{"editor failure", "", []string{"-N", "Initial"}, 1},
		{"empty buffer", "\n \n", []string{"-N", "Initial"}, 1},
		{"notes too large", "Edited title\n\n" + strings.Repeat("x", 65537), []string{"-N", "Initial"}, 1},
		{"server refusal", "Edited title\n\nEdited notes", []string{"999", "Initial"}, 5},
		{"JSON refusal", "Edited title\n\nEdited notes", []string{"999", "Initial", "--json"}, 5},
		{"unreachable", "Edited title\n\nEdited notes", []string{"-N", "Initial"}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, sys := logFixture(t)
			dir := t.TempDir()
			command := filepath.Join(dir, "editor")
			initial := filepath.Join(dir, "initial")
			content := filepath.Join(dir, "content")
			t.Setenv("TKR_LOG_INITIAL", initial)
			t.Setenv("TKR_LOG_CONTENT", content)
			if err := os.WriteFile(content, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			script := "#!/bin/sh\ncp \"$1\" \"$TKR_LOG_INITIAL\"\n"
			if tc.content != "" {
				script += "cp \"$TKR_LOG_CONTENT\" \"$1\"\n"
			}
			if tc.name == "editor failure" {
				script += "exit 3\n"
			}
			if tc.name != "missing executable" {
				if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			sys.Getenv = func(key string) string {
				if key == "VISUAL" || key == "EDITOR" {
					return command
				}
				return os.Getenv(key)
			}
			if tc.name == "unreachable" {
				srv.Stop()
			}
			out, stderr, code := runCLI(t, sys, "", append([]string{"log", "-e"}, tc.args...)...)
			if tc.name == "unreachable" {
				srv.Start(t)
			}
			if code != tc.code {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
			drafts, err := filepath.Glob(filepath.Join(srv.Paths.DraftsDir, "*.md"))
			if err != nil {
				t.Fatal(err)
			}
			if code != 0 {
				if tc.name == "JSON refusal" {
					var problem api.Problem
					if err := json.Unmarshal([]byte(out), &problem); err != nil || problem.Code != "not_found" {
						t.Fatalf("problem=%q, %v", out, err)
					}
				}
				if len(drafts) != 1 || !strings.Contains(stderr, "tkr: your draft is at "+drafts[0]) || len(logEntries(t, srv)) != 0 {
					t.Fatalf("failure lost draft or saved entry: drafts=%v stdout=%q stderr=%q", drafts, out, stderr)
				}
				info, err := os.Stat(drafts[0])
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatalf("draft permissions: %v, %v", info, err)
				}
				want := tc.content
				if want == "" {
					want = "Initial\n\n"
				}
				got, err := os.ReadFile(drafts[0])
				if err != nil || string(got) != want {
					t.Fatalf("retained draft content differs: %v", err)
				}
				return
			}
			if len(drafts) != 0 || stderr != "" {
				t.Fatalf("successful edit: drafts=%v stderr=%q", drafts, stderr)
			}
			if tc.name == "prefill" {
				got, err := os.ReadFile(initial)
				if err != nil || string(got) != "Initial\n\nOriginal notes" {
					t.Fatalf("initial buffer=%q, %v", got, err)
				}
				entry := assertLogEntry(t, srv, 2, "2026-10-28", "Initial")
				if entry.Notes != "Original notes" {
					t.Fatal(entry.Notes)
				}
			} else {
				var task int64
				if tc.name == "id only" {
					task = 2
				}
				entry := assertLogEntry(t, srv, task, "2026-10-28", "Edited title")
				if entry.Notes != "Edited notes\n" {
					t.Fatalf("notes=%q", entry.Notes)
				}
			}
		})
	}
}
