package domain

import "testing"

func TestOpKindVerb(t *testing.T) {
	for kind, want := range map[OpKind]string{
		OpTaskAdd: "add", OpTaskUpdate: "edit", OpTaskDue: "due", OpTaskPlace: "place",
		OpTaskAcceptNew: "accept new", OpTaskDefer: "defer", OpTaskManual: "send to triage",
		OpTaskComplete: "complete", OpTaskReopen: "reopen", OpTaskDrop: "drop",
		OpTaskDelete: "delete", OpTaskUndelete: "undelete", OpBlockCreate: "block",
		OpBlockReblock: "reblock", OpBlockUnblock: "unblock", OpBlockOutcome: "outcome",
		OpEntryAdd: "log", OpEntryUpdate: "edit entry", OpEntryRelink: "relink entry",
		OpEntryDelete: "delete entry", OpEntryUndelete: "restore entry", OpFocusSet: "focus",
		OpRevert: "revert", OpSystemImport: "system.import",
	} {
		if got := kind.Verb(); got != want {
			t.Errorf("%s.Verb() = %q, want %q", kind, got, want)
		}
	}
}

func TestReversalText(t *testing.T) {
	for _, tc := range []struct {
		via  string
		kind OpKind
		want string
	}{
		{"undo", OpTaskComplete, "undid complete (op 12345678)"},
		{"redo", OpTaskComplete, "redid complete (op 12345678)"},
		{"revert", OpTaskComplete, "reverted complete (op 12345678)"},
		{"", OpTaskComplete, "reverted complete (op 12345678)"},
		{"undo", "", "undone (op 12345678)"},
		{"redo", "", "redone (op 12345678)"},
		{"revert", "", "reverted op 12345678"},
		{"", "", "reverted op 12345678"},
	} {
		if got := tc.kind.ReversalText(tc.via, "12345678"); got != tc.want {
			t.Errorf("kind=%q via=%q: got %q, want %q", tc.kind, tc.via, got, tc.want)
		}
	}
}
