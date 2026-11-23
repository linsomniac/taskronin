package domain

import (
	"database/sql"
	"time"

	"github.com/linsomniac/taskronin/internal/civil"
)

// LogEntry records work or completion, optionally linked to a task.
type LogEntry struct {
	ID          EntryID
	TaskID      sql.Null[TaskID]
	Kind        EntryKind
	Title       string
	Notes       string
	WorkDate    civil.Date
	OccurredMin sql.Null[civil.Minute]
	Reopened    bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   sql.Null[time.Time]
	PurgedAt    sql.Null[time.Time]
	Version     Version
}

// EntryView adds the linked task's title to a log entry.
type EntryView struct {
	LogEntry
	TaskTitle string // empty for standalone entries
}
