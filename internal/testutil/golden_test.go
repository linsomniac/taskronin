package testutil_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"taskronin/internal/testutil"
)

func TestGolden(t *testing.T) {
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	run := func(got string, update bool) (string, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		args := []string{"-test.run=^TestGoldenProcessHelper$"}
		if update {
			args = append(args, "-update")
		}
		cmd := exec.CommandContext(ctx, exe, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "TASKRONIN_GOLDEN_TEST=1", "TASKRONIN_GOLDEN_GOT="+got)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run("first\n", false); err == nil || !strings.Contains(out, "sample.golden") {
		t.Fatalf("missing golden: err=%v\n%s", err, out)
	}
	if out, err := run("first\n", true); err != nil {
		t.Fatalf("create golden: %v\n%s", err, out)
	}
	path := filepath.Join(dir, "testdata", "golden", "sample.golden")
	for p, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s permissions = %04o; want %04o", p, info.Mode().Perm(), want)
		}
	}
	if out, err := run("first\n", false); err != nil {
		t.Fatalf("matching golden: %v\n%s", err, out)
	}
	out, err := run("second\n", false)
	if err == nil {
		t.Fatalf("mismatch passed: %s", out)
	}
	for _, want := range []string{"--- ", "+++ ", "@@ -1,1 +1,1 @@", "-first\n", "+second\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("diff missing %q:\n%s", want, out)
		}
	}
	if out, err := run("second\n", true); err != nil {
		t.Fatalf("update golden: %v\n%s", err, out)
	}
	if out, err := run("second\n", false); err != nil {
		t.Fatalf("updated golden: %v\n%s", err, out)
	}
	if out, err := run("second", false); err == nil || !strings.Contains(out, "\\ No newline at end of file") {
		t.Fatalf("missing final newline: err=%v\n%s", err, out)
	}
	for _, tc := range []struct{ name, want, got, diff string }{
		{"empty_before", "", "added\n", "@@ -0,0 +1,1 @@\n+added\n"},
		{"empty_after", "deleted\n", "", "@@ -1,1 +0,0 @@\n-deleted\n"},
		{"context", "1\n2\n3\n4\nold\n6\n7\n8\n9\n", "1\n2\n3\n4\nnew\n6\n7\n8\n9\n",
			"@@ -2,7 +2,7 @@\n 2\n 3\n 4\n-old\n+new\n 6\n 7\n 8\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.want), 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := run(tc.got, false)
			// testing indents continuation lines of the helper's failure message.
			out = strings.ReplaceAll(out, "\n        ", "\n")
			if err == nil || !strings.Contains(out, tc.diff) {
				t.Fatalf("unified diff: err=%v\n%s", err, out)
			}
		})
	}
}

func TestGoldenProcessHelper(t *testing.T) {
	if os.Getenv("TASKRONIN_GOLDEN_TEST") == "" {
		return
	}
	testutil.Golden(t, "sample", []byte(os.Getenv("TASKRONIN_GOLDEN_GOT")))
}
