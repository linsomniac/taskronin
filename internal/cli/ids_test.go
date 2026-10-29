package cli

import "testing"

func TestIDs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		parse   func(string) (int64, error)
		valid   map[string]int64
		invalid []string
	}{
		{"task", parseTaskID, map[string]int64{"42": 42, "#42": 42, "9223372036854775807": 9223372036854775807}, []string{"#0", "-1", "4a", "", "#", "01", "+1", " 1", "1\n", "9223372036854775808", "12345678901234567890", "b7"}},
		{"block", parseBlockID, map[string]int64{"b7": 7, "7": 7}, []string{"b", "b0", "b01", "#7", "bb7"}},
		{"entry", parseEntryID, map[string]int64{"e9": 9, "9": 9}, []string{"e", "e0", "e01", "#9", "ee9"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for input, want := range tc.valid {
				if got, err := tc.parse(input); err != nil || got != want {
					t.Errorf("%q = %d, %v", input, got, err)
				}
			}
			for _, input := range tc.invalid {
				if _, err := tc.parse(input); exitCode(err) != 2 {
					t.Errorf("%q error = %v", input, err)
				}
			}
		})
	}
}
