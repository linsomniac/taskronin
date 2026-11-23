package domain

import (
	"database/sql"
	"time"

	"github.com/linsomniac/taskronin/internal/civil"
)

// Moment is a due or deferral date with an optional minute (0..1439).
type Moment struct {
	Date civil.Date
	Min  sql.Null[civil.Minute]
}

// String returns YYYY-MM-DD, followed by HH:MM when the minute is present.
func (m Moment) String() string {
	if m.Min.Valid {
		return m.Date.String() + " " + m.Min.V.String()
	}
	return m.Date.String()
}

// Task is the persisted state of a task.
type Task struct {
	ID            TaskID
	Title, Notes  string
	Status        TaskStatus
	Rank          int64
	LastPlacedAt  sql.Null[time.Time]
	LastPlacedSeq sql.Null[OpSeq] // ordering and acknowledgement use sequence numbers
	DeferDate     sql.Null[civil.Date]
	DeferMin      sql.Null[civil.Minute]
	DueDate       sql.Null[civil.Date]
	DueMin        sql.Null[civil.Minute]
	DueAck        DuePhase
	DueAckDate    sql.Null[civil.Date]
	Project       string // empty means none
	Priority      Priority
	Origin        string // creator's Actor.Key()
	CompletedAt   sql.Null[time.Time]
	CompletedDate sql.Null[civil.Date]
	CreatedAt     time.Time
	UpdatedAt     time.Time
	DeletedAt     sql.Null[time.Time]
	PurgedAt      sql.Null[time.Time] // set by ScrubEntities
	Version       Version
}

// TriageRow records one reason a task needs triage.
type TriageRow struct {
	TaskID TaskID
	Reason TriageReason
	Since  time.Time // informational
	Seq    OpSeq     // operation that added the reason; used for ordering
}

// Focus is the singleton current-focus row.
type Focus struct {
	TaskID  sql.Null[TaskID]
	Date    sql.Null[civil.Date]
	SetAt   sql.Null[time.Time]
	Version Version
}

// Interruption records a switch of focus away from a task or block.
type Interruption struct {
	ID          InterruptionID
	At          time.Time
	Date        civil.Date
	FromTaskID  sql.Null[TaskID]
	FromBlockID sql.Null[BlockID]
	ToTaskID    TaskID
	OpID        OpID
	DeletedAt   sql.Null[time.Time]
}

// TaskView adds derived state and display metadata to a task.
type TaskView struct {
	Task
	Reasons          []TriageRow // highest first
	Tags             []string    // sorted, case-insensitive
	View             View
	Deferred         bool // defer moment is after now
	DuePhase         DuePhase
	DueSoon          bool // due date is within the configured due-soon window
	SlipsLifetime    int
	SlipsSinceTriage int
}

// TaskDetail adds associated live entries, blocks, and interruptions to a task view.
type TaskDetail struct {
	TaskView
	Entries       []EntryView // non-deleted, newest first
	Blocks        []BlockView // all non-deleted
	Interruptions []Interruption
}
