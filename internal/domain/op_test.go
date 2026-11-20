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
