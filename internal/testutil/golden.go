package testutil

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "update testdata/golden files")

// Golden compares got with testdata/golden/<name>.golden, relative to the
// calling package's working directory. Pass -update to rewrite the fixture.
func Golden(t testing.TB, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", "golden", name+".golden")
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatalf("create golden directory: %v", err)
		}
		if err := os.WriteFile(p, got, 0o600); err != nil {
			t.Fatalf("write golden %s: %v", p, err)
		}
	}
	want, err := os.ReadFile(p) //nolint:gosec // G304: golden file under the calling package's testdata
	if err != nil {
		t.Fatalf("read golden %s (use -update to create it): %v", p, err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("golden mismatch (use -update to accept):\n%s", goldenDiff(p, want, got))
	}
}

// goldenDiff emits a single unified hunk with three lines of context around
// the differing span. It needs no diff executable or additional dependency.
func goldenDiff(name string, want, got []byte) string {
	lines := func(b []byte) []string {
		if len(b) == 0 {
			return nil
		}
		result := strings.SplitAfter(string(b), "\n")
		if result[len(result)-1] == "" {
			result = result[:len(result)-1]
		}
		return result
	}
	a, b := lines(want), lines(got)
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	start := max(0, prefix-3)
	endA, endB := len(a)-max(0, suffix-3), len(b)-max(0, suffix-3)
	lineStart := func(count int) int {
		if count == 0 {
			return start
		}
		return start + 1
	}
	var diff strings.Builder
	fmt.Fprintf(&diff, "--- %s\n+++ got\n@@ -%d,%d +%d,%d @@\n", name,
		lineStart(endA-start), endA-start, lineStart(endB-start), endB-start)
	writeLine := func(sign byte, line string) {
		diff.WriteByte(sign)
		diff.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			diff.WriteString("\n\\ No newline at end of file\n")
		}
	}
	for _, line := range a[start:prefix] {
		writeLine(' ', line)
	}
	for _, line := range a[prefix : len(a)-suffix] {
		writeLine('-', line)
	}
	for _, line := range b[prefix : len(b)-suffix] {
		writeLine('+', line)
	}
	for _, line := range a[len(a)-suffix : endA] {
		writeLine(' ', line)
	}
	return diff.String()
}
