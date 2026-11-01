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
	// Final review B-M9: purge takes the CLI's own ID forms (A§9.1), like
	// every other verb; signs, leading zeros and the wrong prefix are refused.
	for _, args := range [][]string{
		{"purge", "task", "+1"},
		{"purge", "task", "01"},
		{"purge", "task", "e1"},
		{"purge", "entry", "#1"},
		{"purge", "entry", "e"},
	} {
		if out, stderr, code := runCLI(t, sys, "", args...); code != 2 || out != "" || stderr == "" {
			t.Fatalf("%v: %d %q %q", args, code, out, stderr)
		}
	}
	// No terminal: the password cannot be entered (-y never skips it) -> exit 4.
	// Reaching the password check proves the ID form was accepted.
	for _, args := range [][]string{{"purge", "task", "1", "-y"}, {"purge", "task", "#1", "-y"}, {"purge", "entry", "e1", "-y"}, {"purge", "entry", "1", "-y"}, {"purge", "--repair-ledger"}} {
		if _, stderr, code := runCLI(t, sys, "", args...); code != 4 || stderr == "" {
			t.Fatalf("%v: %d %q", args, code, stderr)
		}
	}
	if _, err := os.Lstat(srv.Paths.LedgerFile); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ledger file exists: %v", err)
	}
}
