package cli

import (
	"strings"
	"testing"

	"taskronin/internal/client"
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
