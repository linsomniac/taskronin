package tui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"taskronin/internal/api"
	"taskronin/internal/client"
)

func stubExec(s string, exitErr error, runs *int) func(*exec.Cmd, tea.ExecCallback) tea.Cmd {
	return func(cmd *exec.Cmd, cb tea.ExecCallback) tea.Cmd {
		return func() tea.Msg {
			(*runs)++
			if err := os.WriteFile(cmd.Args[len(cmd.Args)-1], []byte(s), 0o600); err != nil {
				return cb(err)
			}
			return cb(exitErr)
		}
	}
}

func editFixture() *fakeAPI {
	f := newFake()
	d := api.TaskDetail{Task: task(42, "Old")}
	d.Notes = "n1"
	f.details[42] = d
	return f
}

func draftFiles(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestEditTaskPatchesChanges(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       api.UpdateTaskRequest
	}{
		{"both", "New title\n\nn2\n", api.UpdateTaskRequest{Title: api.Opt[string]{Set: true, V: "New title"}, Notes: api.Opt[string]{Set: true, V: "n2\n"}}},
		{"title only", "New title\n\nn1", api.UpdateTaskRequest{Title: api.Opt[string]{Set: true, V: "New title"}}},
		{"clear notes", "Old\n\n", api.UpdateTaskRequest{Notes: api.Opt[string]{Set: true, V: ""}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, runs := editFixture(), 0
			a := newTestApp(t, f, func(c *Config) { c.Editor.Setting = "vi"; c.Editor.Exec = stubExec(tc.text, nil, &runs) })
			cmd := a.editTask(42)
			eq(t, f.count("GetTask"), 0)
			eq(t, len(draftFiles(t, a.cfg.Editor.DraftsDir)), 0)
			drain(t, a, cmd)
			eq(t, runs, 1)
			eq(t, f.mustLast(t, "GetTask").ID, int64(42))
			eq(t, f.mustLast(t, "UpdateTask"), call{Method: "UpdateTask", ID: 42, Mut: client.Mut{IfMatch: "g-42"}, Req: tc.want})
			eq(t, len(draftFiles(t, a.cfg.Editor.DraftsDir)), 0)
			eq(t, a.inflight, false)
		})
	}
}

func TestEditorFailuresKeepDraft(t *testing.T) { // Review Focus 5.
	for _, tc := range []struct {
		name, setting, write          string
		exitErr, patchErr             error
		wantRun, wantPatch, wantDraft bool
		banner                        string
	}{
		{"bad setting", `vi "unclosed`, "", nil, nil, false, false, false, "bad editor setting"},
		{"draft not creatable", "vi", "", nil, nil, false, false, false, "cannot create draft"},
		{"exit 1", "vi", "X\n\nY", errors.New("exit status 1"), nil, true, false, true, "editor failed — draft kept at "},
		{"unchanged", "vi", "Old\n\nn1", nil, nil, true, false, false, "no changes"},
		{"empty title", "vi", "\n\nnotes", nil, nil, true, false, true, "title required — draft kept at "},
		{"server refuses", "vi", "T2\n\nn", nil, problem("validation", 422, "too long"), true, true, true, "too long"},
		{"stale", "vi", "T2\n\nn", nil, problem("precondition_failed", 412, "stale"), true, true, true, "changed elsewhere — refreshed"},
		{"restore", "vi", "T2\n\nn", nil, problem("generation_mismatch", 412, "restored"), true, true, true, "database was restored — refreshed"},
		{"unknown", "vi", "T2\n\nn", nil, outcomeUnknown("edit-key"), true, true, true, "outcome unknown — refreshed; check before repeating"},
		{"not sent", "vi", "T2\n\nn", nil, notSent(), true, true, true, "nothing was saved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, runs := editFixture(), 0
			f.errs["UpdateTask"] = tc.patchErr
			dir := t.TempDir()
			if tc.name == "draft not creatable" {
				dir = filepath.Join(dir, "regular-file")
				if err := os.WriteFile(dir, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			a := newTestApp(t, f, func(c *Config) {
				c.Editor.Setting, c.Editor.DraftsDir = tc.setting, dir
				c.Editor.Exec = stubExec(tc.write, tc.exitErr, &runs)
			})
			drain(t, a, a.editTask(42))
			eq(t, runs == 1, tc.wantRun)
			eq(t, f.count("UpdateTask") == 1, tc.wantPatch)
			eq(t, a.inflight, false)
			if !strings.Contains(a.banner.text, tc.banner) {
				t.Fatal(a.banner.text)
			}
			files := draftFiles(t, dir)
			eq(t, len(files) == 1, tc.wantDraft)
			if tc.wantDraft {
				if !strings.Contains(a.banner.text, "draft kept at "+files[0]) {
					t.Fatal(a.banner.text)
				}
				if tc.wantPatch && !strings.HasSuffix(a.banner.text, " · draft kept at "+files[0]) {
					t.Fatal("draft suffix must have one separator:", a.banner.text)
				}
				b, err := os.ReadFile(files[0])
				if err != nil {
					t.Fatal(err)
				}
				eq(t, string(b), tc.write)
			}
		})
	}
}

func TestEditEntry(t *testing.T) { // LIF-15 / D32 (TUI side).
	for _, tc := range []struct {
		name, kind, notes, initial, edited string
		want                               api.UpdateEntryRequest
	}{
		{"work title", "work", "n", "T\n\nn", "T2\n\nn", api.UpdateEntryRequest{Title: api.Opt[string]{Set: true, V: "T2"}}},
		{"work notes", "work", "n", "T\n\nn", "T\n\nn2", api.UpdateEntryRequest{Notes: api.Opt[string]{Set: true, V: "n2"}}},
		{"complete", "complete", "n", "n", "n2", api.UpdateEntryRequest{Notes: api.Opt[string]{Set: true, V: "n2"}}},
		{"blank first line", "complete", "\nn", "\nn", "\nn2", api.UpdateEntryRequest{Notes: api.Opt[string]{Set: true, V: "\nn2"}}},
		{"clear complete notes", "complete", "n", "n", "", api.UpdateEntryRequest{Notes: api.Opt[string]{Set: true, V: ""}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, runs := newFake(), 0
			var initial []byte
			var readErr error
			a := newTestApp(t, f, func(c *Config) {
				c.Editor.Exec = func(cmd *exec.Cmd, cb tea.ExecCallback) tea.Cmd {
					initial, readErr = os.ReadFile(cmd.Args[len(cmd.Args)-1])
					if readErr != nil {
						return func() tea.Msg { return cb(readErr) }
					}
					return stubExec(tc.edited, nil, &runs)(cmd, cb)
				}
			})
			drain(t, a, a.editEntry(api.Entry{ID: 9, ETag: "e-9", Kind: tc.kind, Title: "T", Notes: tc.notes}))
			if readErr != nil {
				t.Fatal(readErr)
			}
			eq(t, string(initial), tc.initial)
			eq(t, f.mustLast(t, "UpdateEntry"), call{Method: "UpdateEntry", ID: 9, Mut: client.Mut{IfMatch: "e-9"}, Req: tc.want})
			eq(t, f.count("GetTask"), 0)
			eq(t, runs, 1)
			eq(t, len(draftFiles(t, a.cfg.Editor.DraftsDir)), 0)
		})
	}
}

func TestEditTaskReadFailureAndGuard(t *testing.T) {
	for _, err := range []error{notSent(), outcomeUnknown("")} {
		f, runs := editFixture(), 0
		a := newTestApp(t, f, func(c *Config) { c.Editor.Exec = stubExec("New\n\nn1", nil, &runs) })
		f.errs["GetTask"] = err
		cmd := a.editTask(42)
		eq(t, a.editTask(42) == nil, true)
		eq(t, a.editEntry(api.Entry{}) == nil, true)
		drain(t, a, cmd)
		eq(t, runs, 0)
		eq(t, f.count("GetTask"), 1)
		eq(t, f.count("Today"), 1)
		eq(t, f.count("UpdateTask"), 0)
		eq(t, a.inflight, false)
		if strings.Contains(a.banner.text, "saved") {
			t.Fatal(a.banner.text)
		}
		drain(t, a, a.editTask(42))
		eq(t, runs, 1)
		eq(t, f.count("UpdateTask"), 1)
	}
}

func TestEditorCallbackReadsBufferAndDefersDiscard(t *testing.T) { // CF08, CF13.
	for _, changed := range []bool{false, true} {
		f := editFixture()
		var path string
		var done tea.Msg
		var args []string
		var cancellable bool
		var writeErr error
		a := newTestApp(t, f, func(c *Config) {
			c.Editor.Setting = `vi -f 'two words'`
			c.Editor.Exec = func(cmd *exec.Cmd, cb tea.ExecCallback) tea.Cmd {
				return func() tea.Msg {
					args, cancellable = cmd.Args, cmd.Cancel != nil
					path = cmd.Args[len(cmd.Args)-1]
					if changed {
						writeErr = os.WriteFile(path, []byte("New\n\nn1"), 0o600)
					}
					done = cb(writeErr)
					return nil // Deliver the callback message ourselves below.
				}
			}
		})
		read := a.editTask(42)
		_, prepare := a.Update(read())
		eq(t, len(draftFiles(t, a.cfg.Editor.DraftsDir)), 0)
		drain(t, a, prepare)
		if writeErr != nil {
			t.Fatal(writeErr)
		}
		eq(t, len(args), 4)
		eq(t, args[:3], []string{"vi", "-f", "two words"})
		eq(t, cancellable, true)
		// If Update reads the file, it will incorrectly save this later text.
		if err := os.WriteFile(path, []byte("Wrong\n\nlate"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, cmd := a.Update(done)
		if changed {
			op := cmd()
			eq(t, f.mustLast(t, "UpdateTask").Req, any(api.UpdateTaskRequest{Title: api.Opt[string]{Set: true, V: "New"}}))
			_, cmd = a.Update(op)
		} else {
			eq(t, f.count("UpdateTask"), 0)
			eq(t, a.banner.text, "no changes")
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal("Update discarded the draft synchronously:", err)
		}
		drain(t, a, cmd)
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("draft not discarded:", err)
		}
	}
}

func TestEditEntryUnchangedAndRefused(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		f, runs := newFake(), 0
		text := "\nnotes\n"
		if refuse {
			text = "\nchanged\n"
			f.errs["UpdateEntry"] = problem("validation", 422, "refused")
		}
		a := newTestApp(t, f, func(c *Config) { c.Editor.Exec = stubExec(text, nil, &runs) })
		drain(t, a, a.editEntry(api.Entry{ID: 9, ETag: "e-9", Kind: "complete", Title: "T", Notes: "\nnotes\n"}))
		eq(t, f.count("UpdateEntry") == 1, refuse)
		eq(t, len(draftFiles(t, a.cfg.Editor.DraftsDir)) == 1, refuse)
		if !refuse {
			eq(t, a.banner.text, "no changes")
		}
	}
}

func TestEditorTestDefaultsDoNotLaunchProcess(t *testing.T) { // CF07.
	f := editFixture()
	a := newTestApp(t, f)
	drain(t, a, a.editTask(42))
	eq(t, a.banner.text, "no changes")
	eq(t, f.count("UpdateTask"), 0)
	eq(t, len(draftFiles(t, a.cfg.Editor.DraftsDir)), 0)
}

func TestEditorReadFailureKeepsDraftPath(t *testing.T) {
	f := editFixture()
	var path string
	var setupErr error
	a := newTestApp(t, f, func(c *Config) {
		c.Editor.Exec = func(cmd *exec.Cmd, cb tea.ExecCallback) tea.Cmd {
			return func() tea.Msg {
				path = cmd.Args[len(cmd.Args)-1]
				setupErr = os.Remove(path)
				if setupErr == nil {
					setupErr = os.Mkdir(path, 0o700)
				}
				return cb(setupErr)
			}
		}
	})
	drain(t, a, a.editTask(42))
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	eq(t, f.count("UpdateTask"), 0)
	eq(t, a.banner.text, "editor failed — draft kept at "+path)
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
