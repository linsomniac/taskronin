package cli

import (
	"strings"
	"testing"

	"taskronin/internal/offline"
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
