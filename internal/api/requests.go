package api

import (
	"log/slog"

	"github.com/linsomniac/taskronin/internal/civil"
)

// AddTaskRequest supplies a new task and optional placement.
type AddTaskRequest struct {
	Title    string        `json:"title"`
	Notes    string        `json:"notes,omitempty"`
	Due      *Moment       `json:"due,omitempty"`
	Tags     []string      `json:"tags,omitempty"`
	Project  string        `json:"project,omitempty"`
	Priority string        `json:"priority,omitempty"`
	Place    *AddPlacement `json:"place,omitempty"`
}

// AddPlacement specifies the placement of a new task.
type AddPlacement struct {
	How   string        `json:"how"`           // top|bottom|before|after|defer|block|now
	Ref   int64         `json:"ref,omitempty"` // before/after
	Defer *Moment       `json:"defer,omitempty"`
	Block *BlockRequest `json:"block,omitempty"`
}

// UpdateTaskRequest supplies optional task changes.
type UpdateTaskRequest struct {
	Title      Opt[string] `json:"title,omitzero"`    // null rejected
	Notes      Opt[string] `json:"notes,omitzero"`    // null rejected
	Project    Opt[string] `json:"project,omitzero"`  // null clears
	Priority   Opt[string] `json:"priority,omitzero"` // null clears
	TagsAdd    []string    `json:"tags_add,omitempty"`
	TagsRemove []string    `json:"tags_remove,omitempty"`
}

// DueRequest sets or clears a task due moment.
type DueRequest struct {
	Due Opt[Moment] `json:"due"`
} // key required; null clears

// DeferRequest sets or clears a task deferral.
type DeferRequest struct {
	Until Opt[Moment] `json:"until"`
} // key required; null clears the defer

// PlaceRequest specifies a task position.
type PlaceRequest struct {
	How string `json:"how"`
	Ref int64  `json:"ref,omitempty"`
}

// BlockRequest specifies a scheduled civil interval.
type BlockRequest struct {
	Date  civil.Date      `json:"date"`
	Start civil.Minute    `json:"start"`
	End   civil.EndMinute `json:"end"`
}

// ReblockRequest reschedules a block with a task precondition.
type ReblockRequest struct {
	BlockRequest
	TaskETag string `json:"task_etag"`
}

// OutcomeRequest selects a block outcome policy.
type OutcomeRequest struct {
	Outcome string `json:"outcome"`
} // worked|missed|auto

// CompleteRequest supplies optional completion details.
type CompleteRequest struct {
	As    *string     `json:"as,omitempty"`
	Date  *civil.Date `json:"date,omitempty"`
	Log   string      `json:"log,omitempty"`
	Notes string      `json:"notes,omitempty"`
}

// ReopenRequest controls whether reopening removes the completion entry.
type ReopenRequest struct {
	RemoveEntry bool `json:"remove_entry"`
}

// AcceptNewRequest identifies new tasks to accept with their preconditions.
type AcceptNewRequest struct {
	Items []struct {
		ID   int64  `json:"id"`
		ETag string `json:"etag"`
	} `json:"items"`
}

// AddEntryRequest supplies a work entry with an explicit task or null.
type AddEntryRequest struct {
	TaskID   Opt[int64]    `json:"task_id"` // MUST be present: number or null (D§5.2)
	Title    string        `json:"title"`
	Notes    string        `json:"notes,omitempty"`
	WorkDate *civil.Date   `json:"work_date,omitempty"`
	Time     *civil.Minute `json:"time,omitempty"`
}

// UpdateEntryRequest supplies optional entry changes.
type UpdateEntryRequest struct {
	Title    Opt[string]       `json:"title,omitzero"`
	Notes    Opt[string]       `json:"notes,omitzero"`
	WorkDate Opt[civil.Date]   `json:"work_date,omitzero"`
	Time     Opt[civil.Minute] `json:"time,omitzero"` // null clears
}

// RelinkRequest links an entry to a task or makes it standalone.
type RelinkRequest struct {
	TaskID Opt[int64] `json:"task_id"`
} // present; null = standalone

// FocusRequest sets or clears focus and optionally records an interruption.
type FocusRequest struct {
	TaskID    Opt[int64] `json:"task_id"`
	Interrupt bool       `json:"interrupt,omitempty"`
}

// ReviewRequest acknowledges AI changes through an operation sequence.
type ReviewRequest struct {
	ThroughSeq int64 `json:"through_seq"`
}

// PasswordRequest supplies current and replacement passwords.
type PasswordRequest struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

// SettingsRequest supplies settings changes and optional password verification.
type SettingsRequest struct {
	BackupKeepDaily  *int   `json:"backup_keep_daily,omitempty"`
	BackupKeepWeekly *int   `json:"backup_keep_weekly,omitempty"`
	Password         string `json:"password,omitempty"`
}

// CreateTokenRequest supplies token metadata and an explicit expiry choice.
type CreateTokenRequest struct {
	Name             string    `json:"name"`
	Scopes           string    `json:"scopes"`
	ExpiresAt        Opt[Time] `json:"expires_at"` // key required; null = never (explicit choice, D§10.3)
	MutationsPerHour int       `json:"mutations_per_hour"`
}

// LogValue redacts all password request fields.
// AIDEV-NOTE: Use a value receiver so both values and pointers are redacted by
// slog. Password-bearing requests must never be idempotency-hashed or persisted.
func (PasswordRequest) LogValue() slog.Value { return slog.StringValue("[redacted]") }

// LogValue redacts settings requests, which may carry a password.
func (SettingsRequest) LogValue() slog.Value { return slog.StringValue("[redacted]") }
