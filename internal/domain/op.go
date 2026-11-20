package domain

import (
	"database/sql"
	"encoding/json"
	"time"
)

// OpKind names an operation as <area>.<verb>.
type OpKind string

const (
	// OpTaskAdd captures a task.
	OpTaskAdd OpKind = "task.add"
	// OpTaskUpdate updates task fields.
	OpTaskUpdate OpKind = "task.update"
	// OpTaskDue changes a task's due moment.
	OpTaskDue OpKind = "task.due"
	// OpTaskPlace places a task in the task order.
	OpTaskPlace OpKind = "task.place"
	// OpTaskAcceptNew acknowledges a new task.
	OpTaskAcceptNew OpKind = "task.accept_new"
	// OpTaskDefer changes a task's deferral.
	OpTaskDefer OpKind = "task.defer"
	// OpTaskManual requests manual triage.
	OpTaskManual OpKind = "task.manual"
	// OpTaskComplete completes a task.
	OpTaskComplete OpKind = "task.complete"
	// OpTaskReopen reopens a task.
	OpTaskReopen OpKind = "task.reopen"
	// OpTaskDrop drops a task.
	OpTaskDrop OpKind = "task.drop"
	// OpTaskDelete tombstones a task.
	OpTaskDelete OpKind = "task.delete"
	// OpTaskUndelete restores a tombstoned task.
	OpTaskUndelete OpKind = "task.undelete"
	// OpBlockCreate creates a time block.
	OpBlockCreate OpKind = "block.create"
	// OpBlockReblock reschedules a block.
	OpBlockReblock OpKind = "block.reblock"
	// OpBlockUnblock cancels a block.
	OpBlockUnblock OpKind = "block.unblock"
	// OpBlockOutcome changes a block's outcome policy.
	OpBlockOutcome OpKind = "block.outcome"
	// OpEntryAdd adds a log entry.
	OpEntryAdd OpKind = "entry.add"
	// OpEntryUpdate updates a log entry.
	OpEntryUpdate OpKind = "entry.update"
	// OpEntryRelink changes an entry's task link.
	OpEntryRelink OpKind = "entry.relink"
	// OpEntryDelete tombstones a log entry.
	OpEntryDelete OpKind = "entry.delete"
	// OpEntryUndelete restores a tombstoned log entry.
	OpEntryUndelete OpKind = "entry.undelete"
	// OpFocusSet changes the current focus.
	OpFocusSet OpKind = "focus.set"
	// OpRevert reverts an operation.
	OpRevert OpKind = "op.revert"
	// OpUndo undoes an operation on the actor's stack.
	OpUndo OpKind = "op.undo"
	// OpRedo redoes an operation on the actor's stack.
	OpRedo OpKind = "op.redo"
	// OpAIReview acknowledges AI changes.
	OpAIReview OpKind = "ai.review"
	// OpTokenCreate creates an authentication token.
	OpTokenCreate OpKind = "token.create"
	// OpTokenRevoke revokes an authentication token.
	OpTokenRevoke OpKind = "token.revoke"
	// OpSettingsUpdate updates settings.
	OpSettingsUpdate OpKind = "settings.update"
	// OpSettingsRetentionLowered records a retention reduction.
	OpSettingsRetentionLowered OpKind = "settings.retention_lowered"
	// OpAuthPassword changes the owner's password.
	OpAuthPassword OpKind = "auth.password"
	// OpPurgeTask purges task content.
	OpPurgeTask OpKind = "purge.task"
	// OpPurgeEntry purges entry content.
	OpPurgeEntry OpKind = "purge.entry"
	// OpSystemReconcile reconciles time-dependent state.
	OpSystemReconcile OpKind = "system.reconcile"
	// OpSystemLedgerReplay reapplies purge-ledger records.
	OpSystemLedgerReplay OpKind = "system.ledger_replay"
	// OpSystemImport records an import.
	OpSystemImport OpKind = "system.import"
	// OpSystemRestore records a restore.
	OpSystemRestore OpKind = "system.restore"
)

// Verb is the human verb for messages (J§4.2); the mirrors in cli, web and tui all use it.
func (k OpKind) Verb() string {
	switch k {
	case OpTaskAdd:
		return "add"
	case OpTaskUpdate:
		return "edit"
	case OpTaskDue:
		return "due"
	case OpTaskPlace:
		return "place"
	case OpTaskAcceptNew:
		return "accept new"
	case OpTaskDefer:
		return "defer"
	case OpTaskManual:
		return "send to triage"
	case OpTaskComplete:
		return "complete"
	case OpTaskReopen:
		return "reopen"
	case OpTaskDrop:
		return "drop"
	case OpTaskDelete:
		return "delete"
	case OpTaskUndelete:
		return "undelete"
	case OpBlockCreate:
		return "block"
	case OpBlockReblock:
		return "reblock"
	case OpBlockUnblock:
		return "unblock"
	case OpBlockOutcome:
		return "outcome"
	case OpEntryAdd:
		return "log"
	case OpEntryUpdate:
		return "edit entry"
	case OpEntryRelink:
		return "relink entry"
	case OpEntryDelete:
		return "delete entry"
	case OpEntryUndelete:
		return "restore entry"
	case OpFocusSet:
		return "focus"
	case OpRevert:
		return "revert"
	default:
		return string(k)
	}
}

// ReversalText describes a reversal of the root operation. A caller can omit
// the operation ID for a compact banner.
func (k OpKind) ReversalText(via, opID string) string {
	// AIDEV-NOTE: J§4.2 uses revert wording when Via is missing or unknown.
	action, past := "reverted", "reverted"
	switch via {
	case "undo":
		action, past = "undid", "undone"
	case "redo":
		action, past = "redid", "redone"
	}
	line := past
	if k != "" {
		line = action + " " + k.Verb()
	}
	if opID == "" {
		return line
	}
	if k == "" && action == "reverted" {
		return line + " op " + opID
	}
	return line + " (op " + opID + ")"
}

// OpState is an operation's current reversal state.
type OpState string

const (
	// OpActive denotes an active operation.
	OpActive OpState = "active"
	// OpReverted denotes a reverted operation.
	OpReverted OpState = "reverted"
)

// OpRecord is the persisted metadata for an operation.
type OpRecord struct {
	Seq         OpSeq
	ID          OpID
	Kind        OpKind
	Actor       string // Actor.Key()
	Channel     Channel
	At          time.Time
	Scopes      Scopes // permissions required by this operation and its revert
	Revertible  bool
	State       OpState
	RevertsOpID sql.Null[OpID] // set on revert, undo, and redo
	Via         string         // empty, revert, undo, or redo
}

// EntityKind identifies the entity affected by an event.
type EntityKind string

const (
	// EntTask denotes a task.
	EntTask EntityKind = "task"
	// EntTriage denotes a task triage reason.
	EntTriage EntityKind = "triage"
	// EntTaskTags denotes a task's tag set.
	EntTaskTags EntityKind = "task_tags"
	// EntEntry denotes a log entry.
	EntEntry EntityKind = "entry"
	// EntBlock denotes a time block.
	EntBlock EntityKind = "block"
	// EntFocus denotes the singleton focus row.
	EntFocus EntityKind = "focus"
	// EntInterruption denotes an interruption.
	EntInterruption EntityKind = "interruption"
	// EntToken denotes an authentication token.
	EntToken EntityKind = "token"
	// EntSetting denotes a setting.
	EntSetting EntityKind = "setting"
	// EntAuth denotes authentication configuration.
	EntAuth EntityKind = "auth"
)

// Action identifies the change recorded in an event.
type Action string

const (
	// ActInsert inserts an entity.
	ActInsert Action = "insert"
	// ActUpdate updates entity fields.
	ActUpdate Action = "update"
	// ActMove changes task ordering.
	ActMove Action = "move"
	// ActTombstone marks an entity deleted.
	ActTombstone Action = "tombstone"
	// ActUntombstone restores a deleted entity.
	ActUntombstone Action = "untombstone"
	// ActAdd adds a member to a collection.
	ActAdd Action = "add"
	// ActRemove removes a member from a collection.
	ActRemove Action = "remove"
	// ActSet replaces a value or set.
	ActSet Action = "set"
	// ActRevoke revokes a token.
	ActRevoke Action = "revoke"
	// ActPurge purges entity content.
	ActPurge Action = "purge"
)

// Event records changed fields for an operation's effect on one entity.
type Event struct {
	ID       EventID
	OpID     OpID
	Entity   EntityKind
	EntityID int64            // task ID for triage/tags; 1 for focus; 0 for setting
	TaskID   sql.Null[TaskID] // denormalized for task history
	Action   Action
	Before   json.RawMessage // changed columns only; null for insert/add
	After    json.RawMessage // changed columns only; null for tombstone-less removes
	Derived  bool            // recompute effects; revert re-derives them
}

// EntityRef identifies an entity by kind and ID.
type EntityRef struct {
	Entity EntityKind
	ID     int64
}
