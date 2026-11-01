package cli

import (
	"errors"
	"io/fs"
	"os"
	"testing"
)

func TestPurgeCLI(t *testing.T) {
	srv, sys := captureServer(t)
	captureTask(t, srv, "secret")
	srv.Stop()

	// Usage errors exit 2 without touching anything.
	for _, args := range [][]string{
		{"purge"},
		{"purge", "task"},
		{"purge", "tag", "1"},
		{"purge", "task", "zero"},
		{"purge", "entry", "1", "--with-entries"},
		{"purge", "--repair-ledger", "task", "1"},
		{"purge", "--repair-ledger", "-y"},
	} {
		if out, stderr, code := runCLI(t, sys, "", args...); code != 2 || out != "" || stderr == "" {
			t.Fatalf("%v: %d %q %q", args, code, out, stderr)
		}
	}
	// No terminal: the password cannot be entered (-y never skips it) -> exit 4.
	for _, args := range [][]string{{"purge", "task", "1", "-y"}, {"purge", "--repair-ledger"}} {
		if _, stderr, code := runCLI(t, sys, "", args...); code != 4 || stderr == "" {
			t.Fatalf("%v: %d %q", args, code, stderr)
		}
	}
	if _, err := os.Lstat(srv.Paths.LedgerFile); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ledger file exists: %v", err)
	}
}
