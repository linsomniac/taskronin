package domain

import (
	"database/sql"
	"time"

	"taskronin/internal/civil"
)

// BlockSpan is the half-open interval [Start, End) on Date,
// with 0 <= Start < End <= 1440.
type BlockSpan struct {
	Date  civil.Date
	Start civil.Minute
	End   civil.EndMinute
}

// String returns YYYY-MM-DD HH:MM-HH:MM, allowing 24:00 as the end.
func (b BlockSpan) String() string {
	return b.Date.String() + " " + b.Start.String() + "-" + b.End.String()
}

// TimeBlock is a scheduled task interval and its resolution state.
type TimeBlock struct {
	ID            BlockID
	TaskID        TaskID
	Span          BlockSpan
	Status        BlockStatus
	OutcomeSource OutcomeSource // empty while planned or cancelled
	CancelReason  CancelReason  // empty unless cancelled
	Slipped       bool
	SlippedAt     sql.Null[time.Time] // retained after unslip
	SlippedSeq    sql.Null[OpSeq]     // operation that last slipped it
	CreatedAt     time.Time
	ResolvedAt    sql.Null[time.Time] // set on every status change; null while planned
	ResolvedSeq   sql.Null[OpSeq]
	DeletedAt     sql.Null[time.Time] // set only by reverting the creating operation
	Version       Version
}

// BlockView adds the associated task and derived interval state to a block.
type BlockView struct {
	TimeBlock
	TaskTitle    string
	TaskVersion  Version // task precondition for reblock (R4-7)
	Current      bool    // planned and [start,end) contains now
	Started      bool    // start instant <= now
	Ended        bool    // end instant <= now
	NeedsReblock bool    // planned, ended, no non-deleted entry for (task, date)
}
