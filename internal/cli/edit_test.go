package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/client"
	"github.com/linsomniac/taskronin/internal/config"
	"github.com/linsomniac/taskronin/internal/testutil"
)

func editFixture(t *testing.T) (*testutil.Server, sysDeps, *api.Task) {
	t.Helper()
	srv, sys := captureServer(t)
	res, err := srv.Client.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{
		Title: "Old title", Notes: "Old notes", Tags: []string{"keep"}, Project: "project", Priority: "H",
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, sys, res.Task
}

func editHelper(t *testing.T, sys *sysDeps, mode string) string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	initial := filepath.Join(t.TempDir(), "initial")
	t.Setenv("TKR_HELPER_EDITOR", mode)
	t.Setenv("TKR_HELPER_INITIAL", initial)
	command := quoteArgs([]string{binary, "-test.run=^TestEditHelperProcess$", "--"})
	getenv := sys.Getenv
	sys.Getenv = func(key string) string {
		switch key {
		case "VISUAL":
			return ""
		case "EDITOR":
			return command
		default:
			return getenv(key)
		}
	}
	return initial
}

func TestEditHelperProcess(t *testing.T) {
	mode := os.Getenv("TKR_HELPER_EDITOR")
	if mode == "" {
		return
	}
	path := os.Args[len(os.Args)-1]
	initial, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("TKR_HELPER_INITIAL"), initial, 0o600); err != nil {
		t.Fatal(err)
	}
	switch {
	case strings.HasPrefix(mode, "write:"):
		if err := os.WriteFile(path, []byte(strings.TrimPrefix(mode, "write:")), 0o600); err != nil {
			t.Fatal(err)
		}
	case mode == "exit:1":
		syscall.Exit(1)
	case mode == "keep":
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
	// AIDEV-NOTE: exit only the editor subprocess, without the test harness's
	// PASS line contaminating the command's output. Production returns errors.
	syscall.Exit(0)
}

func editDrafts(t *testing.T, dir string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func assertEditDraft(t *testing.T, dir, stderr, want string) {
	t.Helper()
	paths := editDrafts(t, dir)
	if len(paths) != 1 || !strings.Contains(stderr, "tkr: draft kept at "+paths[0]+"\n") {
		t.Fatalf("drafts=%v stderr=%q", paths, stderr)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil || string(data) != want {
		t.Fatalf("retained draft differs: length=%d want=%d err=%v", len(data), len(want), err)
	}
	for path, mode := range map[string]os.FileMode{dir: 0o700, paths[0]: 0o600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("permissions for %s: %v, %v", path, info, err)
		}
	}
}

func TestMissingEditor(t *testing.T) {
	srv, sys := captureServer(t)
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "nosuch")
	t.Setenv("PATH", t.TempDir())
	out, stderr, code := runCLI(t, sys, "", "add", "x", "-e")
	drafts := editDrafts(t, srv.Paths.DraftsDir)
	if len(drafts) != 1 {
		t.Fatalf("drafts = %v", drafts)
	}
	want := "tkr: editor \"nosuch\" not found in $PATH\ntkr: your draft is at " + drafts[0] + "\n"
	if code != 1 || out != "" || stderr != want {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
}

func TestEditSuccess(t *testing.T) {
	for _, tc := range []struct {
		name, title, notes string
		json, configured   bool
	}{
		{name: "title and notes", title: "New title", notes: "New notes"},
		{name: "title only", title: "New title", notes: "Old notes"},
		{name: "notes only", title: "Old title", notes: "New notes\n"},
		{name: "clear notes", title: "Old title"},
		{name: "JSON", title: "New title", notes: "New notes", json: true},
		{name: "configured editor", title: "New title", notes: "New notes", configured: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, sys, before := editFixture(t)
			initial := editHelper(t, &sys, "write:"+tc.title+"\n\n"+tc.notes)
			if tc.configured {
				cfg, err := config.LoadClient(srv.Paths.ClientConfig, srv.Paths)
				if err != nil {
					t.Fatal(err)
				}
				cfg.Editor = sys.Getenv("EDITOR")
				if err := config.WriteClient(srv.Paths.ClientConfig, cfg); err != nil {
					t.Fatal(err)
				}
				sys.Getenv = os.Getenv
				t.Setenv("VISUAL", "/nonexistent")
				t.Setenv("EDITOR", "/nonexistent")
			}
			args := []string{"edit", "#1"}
			if tc.json {
				args = append(args, "--json")
			}
			out, stderr, code := runCLI(t, sys, "", args...)
			if code != 0 || stderr != "" || len(editDrafts(t, srv.Paths.DraftsDir)) != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
			if tc.json {
				var res api.OpResponse
				if err := json.Unmarshal([]byte(out), &res); err != nil || res.Task == nil || res.Task.Title != tc.title || res.Task.Notes != tc.notes {
					t.Fatalf("response=%q err=%v", out, err)
				}
			} else {
				want := "#1 updated\n"
				if tc.title != before.Title {
					want += "#1 retitled 'Old title' → 'New title'\n"
				}
				if out != want {
					t.Fatalf("stdout=%q want=%q", out, want)
				}
			}
			data, err := os.ReadFile(initial)
			if err != nil || string(data) != "Old title\n\nOld notes" {
				t.Fatalf("initial buffer=%q err=%v", data, err)
			}
			after, err := srv.Client.GetTask(t.Context(), 1)
			if err != nil || after.Title != tc.title || after.Notes != tc.notes || after.Version != before.Version+1 {
				t.Fatalf("task=%+v err=%v", after, err)
			}
			if !reflect.DeepEqual(after.Tags, before.Tags) || !reflect.DeepEqual(after.Project, before.Project) || !reflect.DeepEqual(after.Priority, before.Priority) {
				t.Fatalf("unrelated metadata changed: %+v", after)
			}
		})
	}
}

func TestEditUnchanged(t *testing.T) {
	for _, format := range []string{"text", "JSON"} {
		t.Run(format, func(t *testing.T) {
			srv, sys, before := editFixture(t)
			editHelper(t, &sys, "keep")
			args := []string{"edit", "1"}
			if format == "JSON" {
				args = append(args, "--json")
			}
			out, stderr, code := runCLI(t, sys, "", args...)
			if code != 0 || stderr != "" || len(editDrafts(t, srv.Paths.DraftsDir)) != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
			if format == "JSON" {
				var task api.TaskDetail
				if err := json.Unmarshal([]byte(out), &task); err != nil || !reflect.DeepEqual(task.Task, *before) {
					t.Fatalf("unchanged JSON=%q err=%v", out, err)
				}
			} else if out != "#1 unchanged\n" {
				t.Fatalf("stdout=%q", out)
			}
			after, err := srv.Client.GetTask(t.Context(), 1)
			if err != nil || !reflect.DeepEqual(after.Task, *before) {
				t.Fatalf("unchanged edit mutated task: %+v err=%v", after, err)
			}
		})
	}
}

func TestEditFailureKeepsDraft(t *testing.T) {
	for _, tc := range []struct {
		name, mode, detail, draft string
		code                      int
	}{
		{"editor exits", "exit:1", "editor exited with status 1", "Old title\n\nOld notes", 1},
		{"empty buffer", "write:", "empty title", "", 2},
		{"empty first line", "write:\nTitle\n\nNotes", "empty title", "\nTitle\n\nNotes", 2},
		{"oversized notes", "write:New title\n\n" + strings.Repeat("n", 70*1024), "notes", "New title\n\n" + strings.Repeat("n", 70*1024), 1},
		{"missing executable", "missing", "/nonexistent", "Old title\n\nOld notes", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, sys, before := editFixture(t)
			editHelper(t, &sys, tc.mode)
			if tc.mode == "missing" {
				getenv := sys.Getenv
				sys.Getenv = func(key string) string {
					if key == "EDITOR" {
						return "/nonexistent"
					}
					return getenv(key)
				}
			}
			out, stderr, code := runCLI(t, sys, "", "edit", "1")
			if code != tc.code || out != "" || !strings.Contains(stderr, tc.detail) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
			assertEditDraft(t, srv.Paths.DraftsDir, stderr, tc.draft)
			after, err := srv.Client.GetTask(t.Context(), 1)
			if err != nil || !reflect.DeepEqual(after.Task, *before) {
				t.Fatalf("failed edit mutated task: %+v err=%v", after, err)
			}
		})
	}
}

// The real-server tests above cover persistence; this transport fixture checks
// omitted PATCH fields and the original GET's ETag even when the server refuses.
func TestEditPatchPrecondition(t *testing.T) {
	for _, tc := range []struct {
		name, buffer string
		want         map[string]string
		json         bool
	}{
		{"title", "New title\n\nOld notes", map[string]string{"title": "New title"}, false},
		{"notes", "Old title\n\nNew notes", map[string]string{"notes": "New notes"}, false},
		{"clear notes JSON", "Old title", map[string]string{"notes": ""}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sys, paths := testSys(t)
			editHelper(t, &sys, "write:"+tc.buffer)
			var gets, patches atomic.Int32
			etag := `"test-generation:3"`
			socket, token := fakeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET /api/v1/tasks/1":
					gets.Add(1)
					writeJSON(t, w, api.TaskDetail{Task: api.Task{ID: 1, Title: "Old title", Notes: "Old notes", ETag: etag}})
				case "PATCH /api/v1/tasks/1":
					patches.Add(1)
					if gets.Load() != 1 || r.Header.Get("If-Match") != etag {
						t.Errorf("GETs=%d If-Match=%q", gets.Load(), r.Header.Get("If-Match"))
					}
					var body map[string]string
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !reflect.DeepEqual(body, tc.want) {
						t.Errorf("PATCH=%v want=%v err=%v", body, tc.want, err)
					}
					w.WriteHeader(http.StatusPreconditionFailed)
					writeJSON(t, w, api.Problem{Status: 412, Code: "precondition_failed", Detail: "task changed", Current: json.RawMessage(`{"id":1,"version":4}`)})
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			args := []string{"--socket", socket, "--token-file", token, "edit", "1"}
			if tc.json {
				args = append(args, "--json")
			}
			out, stderr, code := runCLI(t, sys, "", args...)
			if code != 6 || gets.Load() != 1 || patches.Load() != 1 {
				t.Fatalf("exit=%d stdout=%q stderr=%q GETs=%d PATCHes=%d", code, out, stderr, gets.Load(), patches.Load())
			}
			if tc.json {
				var problem api.Problem
				if err := json.Unmarshal([]byte(out), &problem); err != nil || problem.Code != "precondition_failed" {
					t.Fatalf("problem=%q err=%v", out, err)
				}
			} else if out != "" || !strings.Contains(stderr, "current: #1 version 4") {
				t.Fatalf("stdout=%q stderr=%q", out, stderr)
			}
			assertEditDraft(t, paths.DraftsDir, stderr, tc.buffer)
		})
	}
}

func TestEditNoDraftBeforeGet(t *testing.T) {
	srv, sys, _ := editFixture(t)
	initial := editHelper(t, &sys, "keep")
	out, stderr, code := runCLI(t, sys, "", "edit", "999")
	if code != 5 || out != "" || stderr == "" || len(editDrafts(t, srv.Paths.DraftsDir)) != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	if _, err := os.Stat(initial); !os.IsNotExist(err) {
		t.Fatalf("editor ran before GET succeeded: %v", err)
	}
}
