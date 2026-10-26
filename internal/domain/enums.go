package domain

// TaskStatus is the lifecycle state of a task.
type TaskStatus string

const (
	// StatusOpen marks an open task.
	StatusOpen TaskStatus = "open"
	// StatusDone marks a completed task.
	StatusDone TaskStatus = "done"
	// StatusDropped marks a dropped task.
	StatusDropped TaskStatus = "dropped"
)

// Valid reports whether s is a declared task status.
func (s TaskStatus) Valid() bool {
	switch s {
	case StatusOpen, StatusDone, StatusDropped:
		return true
	default:
		return false
	}
}

// TriageReason explains why a task needs triage.
type TriageReason string

const (
	// ReasonSlipped marks a slipped task.
	ReasonSlipped TriageReason = "slipped"
	// ReasonOverdue marks an overdue task.
	ReasonOverdue TriageReason = "overdue"
	// ReasonDue marks a due task.
	ReasonDue TriageReason = "due"
	// ReasonContinue marks work to continue.
	ReasonContinue TriageReason = "continue"
	// ReasonSurfaced marks an elapsed deferral.
	ReasonSurfaced TriageReason = "surfaced"
	// ReasonReopened marks a reopened task.
	ReasonReopened TriageReason = "reopened"
	// ReasonManual marks an explicit triage request.
	ReasonManual TriageReason = "manual"
	// ReasonNew marks a newly captured task.
	ReasonNew TriageReason = "new"
)

// TriageOrder lists reasons from highest to lowest priority.
var TriageOrder = [...]TriageReason{
	ReasonSlipped, ReasonOverdue, ReasonDue, ReasonContinue,
	ReasonSurfaced, ReasonReopened, ReasonManual, ReasonNew,
}

// Rank returns the reason's priority (8 through 1), or zero for an unknown reason.
func (r TriageReason) Rank() int {
	for i, reason := range TriageOrder {
		if r == reason {
			return len(TriageOrder) - i
		}
	}
	return 0
}

// Valid reports whether r is a declared triage reason.
func (r TriageReason) Valid() bool { return r.Rank() != 0 }

// EntryKind distinguishes work from completion log entries.
type EntryKind string

const (
	// EntryWork records work on a task or standalone activity.
	EntryWork EntryKind = "work"
	// EntryComplete records task completion.
	EntryComplete EntryKind = "complete"
)

// Valid reports whether k is a declared entry kind.
func (k EntryKind) Valid() bool { return k == EntryWork || k == EntryComplete }

// BlockStatus is the lifecycle state of a time block.
type BlockStatus string

const (
	// BlockPlanned marks a scheduled block.
	BlockPlanned BlockStatus = "planned"
	// BlockDone marks a block with task completion.
	BlockDone BlockStatus = "done"
	// BlockWorked marks a block with work recorded.
	BlockWorked BlockStatus = "worked"
	// BlockMissed marks a block without work.
	BlockMissed BlockStatus = "missed"
	// BlockCancelled marks a cancelled block.
	BlockCancelled BlockStatus = "cancelled"
)

// Valid reports whether s is a declared block status.
func (s BlockStatus) Valid() bool {
	switch s {
	case BlockPlanned, BlockDone, BlockWorked, BlockMissed, BlockCancelled:
		return true
	default:
		return false
	}
}

// OutcomeSource identifies how a resolved block outcome was selected.
type OutcomeSource string

const (
	// OutcomeAuto denotes an automatically derived outcome.
	OutcomeAuto OutcomeSource = "auto"
	// OutcomeUser denotes a user-selected outcome.
	OutcomeUser OutcomeSource = "user"
)

// Valid reports whether s is a declared outcome source.
func (s OutcomeSource) Valid() bool { return s == OutcomeAuto || s == OutcomeUser }

// CancelReason explains a block cancellation.
type CancelReason string

const (
	// CancelUser denotes an explicit cancellation.
	CancelUser CancelReason = "user"
	// CancelTaskClosed denotes cancellation when a task closes.
	CancelTaskClosed CancelReason = "task_closed"
	// CancelDeferred denotes cancellation when a task is deferred.
	CancelDeferred CancelReason = "deferred"
	// CancelRescheduled denotes cancellation when a block is rescheduled.
	CancelRescheduled CancelReason = "rescheduled"
)

// Valid reports whether r is a declared cancellation reason.
func (r CancelReason) Valid() bool {
	switch r {
	case CancelUser, CancelTaskClosed, CancelDeferred, CancelRescheduled:
		return true
	default:
		return false
	}
}

// DuePhase is a task's due state or acknowledged due state.
type DuePhase string

const (
	// DueNone denotes neither due nor overdue.
	DueNone DuePhase = "none"
	// DueDue denotes a due task.
	DueDue DuePhase = "due"
	// DueOverdue denotes an overdue task.
	DueOverdue DuePhase = "overdue"
)

// Valid reports whether p is a declared due phase.
func (p DuePhase) Valid() bool { return p == DueNone || p == DueDue || p == DueOverdue }

// Priority is an optional task priority.
type Priority string

const (
	// PriorityNone denotes no priority.
	PriorityNone Priority = ""
	// PriorityH denotes high priority.
	PriorityH Priority = "H"
	// PriorityM denotes medium priority.
	PriorityM Priority = "M"
	// PriorityL denotes low priority.
	PriorityL Priority = "L"
)

// Valid reports whether p is a declared priority, including no priority.
func (p Priority) Valid() bool {
	switch p {
	case PriorityNone, PriorityH, PriorityM, PriorityL:
		return true
	default:
		return false
	}
}

// View selects a task classification for a list.
type View string

const (
	// ViewTriage selects tasks needing triage.
	ViewTriage View = "triage"
	// ViewBacklog selects backlog tasks.
	ViewBacklog View = "backlog"
	// ViewUpcoming selects deferred tasks.
	ViewUpcoming View = "upcoming"
	// ViewDone selects completed tasks.
	ViewDone View = "done"
	// ViewDropped selects dropped tasks.
	ViewDropped View = "dropped"
	// ViewDeleted selects deleted tasks.
	ViewDeleted View = "deleted"
	// ViewAll selects all tasks.
	ViewAll View = "all"
)

// Valid reports whether v is a declared task view.
func (v View) Valid() bool {
	switch v {
	case ViewTriage, ViewBacklog, ViewUpcoming, ViewDone, ViewDropped, ViewDeleted, ViewAll:
		return true
	default:
		return false
	}
}

// UserOutcome is a requested block outcome policy.
type UserOutcome string

const (
	// SetWorked explicitly marks a block worked.
	SetWorked UserOutcome = "worked"
	// SetMissed explicitly marks a block missed.
	SetMissed UserOutcome = "missed"
	// SetAuto restores automatic outcome derivation.
	SetAuto UserOutcome = "auto"
)

// Valid reports whether o is a declared user outcome.
func (o UserOutcome) Valid() bool { return o == SetWorked || o == SetMissed || o == SetAuto }

// PlaceHow selects how a task is positioned in the global order.
type PlaceHow string

const (
	// PlaceKeep retains the task's position.
	PlaceKeep PlaceHow = "keep"
	// PlaceTop places the task at the top.
	PlaceTop PlaceHow = "top"
	// PlaceBottom places the task at the bottom.
	PlaceBottom PlaceHow = "bottom"
	// PlaceBefore places the task before another task.
	PlaceBefore PlaceHow = "before"
	// PlaceAfter places the task after another task.
	PlaceAfter PlaceHow = "after"
)

// Valid reports whether h is a declared placement mode.
func (h PlaceHow) Valid() bool {
	switch h {
	case PlaceKeep, PlaceTop, PlaceBottom, PlaceBefore, PlaceAfter:
		return true
	default:
		return false
	}
}
