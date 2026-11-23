package domain

import (
	"database/sql"
	"strings"
	"time"

	"github.com/linsomniac/taskronin/internal/civil"
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

// BlockSpanText uses an en dash for the clock range in a block effect payload.
// An optional ISO date is preserved for the caller to format.
func BlockSpanText(text string) string {
	span := text
	if date, clocks, ok := strings.Cut(text, " "); ok {
		if _, err := civil.ParseDate(date); err != nil {
			return text
		}
		span = clocks
	}
	start, end, ok := strings.Cut(span, "-")
	if !ok || len(start) != 5 || len(end) != 5 {
		return text
	}
	if _, err := civil.ParseHHMM(start, false); err != nil {
		return text
	}
	if _, err := civil.ParseHHMM(end, true); err != nil {
		return text
	}
	return strings.TrimSuffix(text, "-"+end) + "–" + end
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
