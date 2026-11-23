package cli

import (
	"strings"
	"testing"

	"github.com/linsomniac/taskronin/internal/offline"
)

func TestScrubCheckResult(t *testing.T) {
	for _, tc := range []struct {
		name    string
		results []offline.SetResult
		want    int
	}{
		{"no sets", nil, 0},
		{"all clean", []offline.SetResult{{Path: "a", Status: "clean"}, {Path: "b", Status: "clean"}}, 0},
		{"one contains", []offline.SetResult{{Path: "a", Status: "clean"}, {Path: "b", Status: "contains"}}, 8},
		{"two contain", []offline.SetResult{{Path: "a", Status: "contains"}, {Path: "b", Status: "contains"}}, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := scrubCheckResult(tc.results)
			if got := exitCode(err); got != tc.want {
				t.Fatalf("exit = %d (%v), want %d", got, err, tc.want)
			}
			if tc.want == 8 && !strings.Contains(err.Error(), "still contain purged content") {
				t.Fatalf("detail = %q", err.Error())
			}
		})
	}
	if err := scrubCheckResult([]offline.SetResult{{Path: "a", Status: "contains"}, {Path: "b", Status: "contains"}}); !strings.HasPrefix(err.Error(), "2 backup set(s)") {
		t.Fatalf("count = %q", err.Error())
	}
}

// TestScrubCheckHelp is final review B-M7: --check does not "change nothing"
// (it finishes an interrupted rewrite and replays the ledger on the live DB),
// and it exits 8 when a set still holds purged content.
func TestScrubCheckHelp(t *testing.T) {
	s, _ := testSys(t)
	out, stderr, code := runCLI(t, s, "", "scrub-backups", "--help")
	if code != 0 || stderr != "" {
		t.Fatalf("%d %q", code, stderr)
	}
	flag := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "--check") {
			flag += line
		}
	}
	if strings.Contains(flag, "change nothing") || !strings.Contains(flag, "exit 8") || !strings.Contains(flag, "rewrite") {
		t.Fatalf("--check help = %q", flag)
	}
	if !strings.Contains(out, "live database") {
		t.Fatalf("long help does not say what --check still changes: %q", out)
	}
}
