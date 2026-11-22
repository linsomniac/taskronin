package domain

import "testing"

func TestEffectImplied(t *testing.T) {
	for _, tc := range []struct {
		kind                string
		onDelete, onRestore bool
	}{
		{"placed", true, true},
		{"moved", true, true},
		{"updated", true, true},
		{"reason_removed", true, false},
		{"undeferred", true, false},
		{"deleted", false, false},
		{"undeleted", false, false},
		{"reason_added", false, false},
		{"deferred", false, false},
		{"due_set", false, false},
		{"focus_cleared", false, false},
		{"block_cancelled", false, false},
		{"entry_deleted", false, false},
	} {
		for _, deleted := range []bool{false, true} {
			for _, undeleted := range []bool{false, true} {
				want := deleted && tc.onDelete || undeleted && tc.onRestore
				if got := EffectImplied(tc.kind, deleted, undeleted); got != want {
					t.Errorf("EffectImplied(%q, %t, %t) = %t, want %t", tc.kind, deleted, undeleted, got, want)
				}
			}
		}
	}
}
