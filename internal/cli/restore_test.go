package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/linsomniac/taskronin/internal/client"
)

func TestRestoreCLI(t *testing.T) {
	srv, sys := captureServer(t)
	captureTask(t, srv, "kept")
	snap, err := srv.Client.RunBackup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	captureTask(t, srv, "lost")
	srv.Stop()

	// Usage errors exit 2 without touching anything.
	for _, args := range [][]string{
		{"restore"},
		{"restore", snap.Name}, // not a terminal, no --yes
		{"restore", "--resume", "--rollback"},
		{"restore", "--resume", snap.Name},
	} {
		if out, stderr, code := runCLI(t, sys, "", args...); code != 2 || out != "" || stderr == "" {
			t.Fatalf("%v: %d %q %q", args, code, out, stderr)
		}
	}
	// No interrupted restore: resume and rollback say so.
	for _, flag := range []string{"--resume", "--rollback"} {
		if _, stderr, code := runCLI(t, sys, "", "restore", flag); code == 0 || !strings.Contains(stderr, "no interrupted restore") {
			t.Fatalf("%s: %d %q", flag, code, stderr)
		}
	}
	out, stderr, code := runCLI(t, sys, "", "restore", "-y", snap.Name)
	if code != 0 || !strings.HasPrefix(out, "restored "+snap.Name+"; the previous database is in ") {
		t.Fatalf("restore: %d %q %q", code, out, stderr)
	}
	srv.Start(t)
	page, err := srv.Client.ListTasks(t.Context(), client.TaskQuery{View: "all"})
	if err != nil || len(page.Items) != 1 || page.Items[0].Title != "kept" {
		t.Fatalf("tasks = %+v, %v", page, err)
	}
}

func TestImportCLI(t *testing.T) {
	src, srcSys := captureServer(t)
	captureTask(t, src, "carried")
	file := filepath.Join(t.TempDir(), "export.json")
	if out, stderr, code := runCLI(t, srcSys, "", "export", "-o", file); code != 0 {
		t.Fatalf("export: %d %q %q", code, out, stderr)
	}
	dst, sys := captureServer(t)
	dst.Stop()
	for _, args := range [][]string{{"import"}, {"import", file}} { // no FILE; not a terminal and no --yes
		if out, stderr, code := runCLI(t, sys, "", args...); code != 2 || out != "" || stderr == "" {
			t.Fatalf("%v: %d %q %q", args, code, out, stderr)
		}
	}
	out, stderr, code := runCLI(t, sys, "", "import", "-y", file)
	if code != 0 || !strings.HasPrefix(out, "imported 1 tasks, 0 entries and 0 blocks; the previous database is in ") {
		t.Fatalf("import: %d %q %q", code, out, stderr)
	}
	dst.Start(t)
	page, err := dst.Client.ListTasks(t.Context(), client.TaskQuery{View: "all"})
	if err != nil || len(page.Items) != 1 || page.Items[0].Title != "carried" {
		t.Fatalf("tasks = %+v, %v", page, err)
	}
}
