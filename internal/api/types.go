package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/linsomniac/taskronin/internal/civil"
)

// Time encodes an instant with millisecond precision in its existing location.
type Time struct{ time.Time }

// MarshalJSON retains the instant's zone offset and always emits three fractional digits.
func (t Time) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.Format("2006-01-02T15:04:05.000Z07:00"))
}

// UnmarshalJSON decodes an RFC 3339 timestamp, including optional fractional seconds.
func (t *Time) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return err
	}
	t.Time = v
	return nil
}

// Moment is a civil date with an optional minute of day; null time means date-only.
type Moment struct {
	Date civil.Date    `json:"date"`
	Time *civil.Minute `json:"time"`
}

// Opt distinguishes an absent field, an explicit null, and a value.
type Opt[T any] struct {
	Set, Null bool
	V         T
}

// IsZero reports whether the field was absent.
func (o Opt[T]) IsZero() bool { return !o.Set }

// MarshalJSON emits null for unset or explicitly null fields, otherwise V.
func (o Opt[T]) MarshalJSON() ([]byte, error) {
	// AIDEV-NOTE: Optional request fields must use json:",omitzero" so absent
	// fields are omitted before this method runs. This preserves R2-3's distinct
	// canonical idempotency bodies for absent fields and explicit nulls.
	if !o.Set || o.Null {
		return []byte("null"), nil
	}
	return json.Marshal(o.V)
}

// UnmarshalJSON marks a present field as either null or a decoded value.
func (o *Opt[T]) UnmarshalJSON(b []byte) error {
	value := Opt[T]{Set: true}
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		value.Null = true
	} else {
		// AIDEV-NOTE: An outer decoder's DisallowUnknownFields does not reach
		// custom unmarshalers. Enforce A§6.3 here for struct values such as Moment.
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&value.V); err != nil {
			return err
		}
		if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
			if err != nil {
				return err
			}
			return errors.New("api: trailing data after option value")
		}
	}
	*o = value
	return nil
}

// Reason records why a task needs triage and when the reason arose.
type Reason struct {
	Reason string `json:"reason"`
	Since  Time   `json:"since"`
}

// Slips counts lifetime slips and slips since the last triage.
type Slips struct {
	Lifetime    int `json:"lifetime"`
	SinceTriage int `json:"since_triage"`
}

// Task is the wire representation of a task and its derived state.
type Task struct {
	ID            int64       `json:"id"`
	Title         string      `json:"title"`
	Notes         string      `json:"notes"`
	Status        string      `json:"status"`
	View          string      `json:"view"`
	Reasons       []Reason    `json:"reasons"`
	Defer         *Moment     `json:"defer"`
	Deferred      bool        `json:"deferred"`
	Due           *Moment     `json:"due"`
	DuePhase      string      `json:"due_phase"`
	DueSoon       bool        `json:"due_soon"`
	Project       *string     `json:"project"`
	Tags          []string    `json:"tags"`
	Priority      *string     `json:"priority"`
	Origin        string      `json:"origin"`
	Slips         Slips       `json:"slips"`
	LastPlacedAt  *Time       `json:"last_placed_at"`
	CompletedAt   *Time       `json:"completed_at"`
	CompletedDate *civil.Date `json:"completed_date"`
	CreatedAt     Time        `json:"created_at"`
	UpdatedAt     Time        `json:"updated_at"`
	DeletedAt     *Time       `json:"deleted_at"`
	Version       int64       `json:"version"`
	ETag          string      `json:"etag"`
}

// TaskDetail includes a task and its associated entries, blocks and interruptions.
type TaskDetail struct {
	Task
	Entries       []Entry        `json:"entries"`
	Blocks        []Block        `json:"blocks"`
	Interruptions []Interruption `json:"interruptions"`
}

// Entry is a work or completion entry with optional linked task metadata.
type Entry struct {
	ID        int64         `json:"id"`
	TaskID    *int64        `json:"task_id"`
	TaskTitle *string       `json:"task_title"`
	Kind      string        `json:"kind"`
	Title     string        `json:"title"`
	Notes     string        `json:"notes"`
	WorkDate  civil.Date    `json:"work_date"`
	Time      *civil.Minute `json:"time"`
	Reopened  bool          `json:"reopened"`
	CreatedAt Time          `json:"created_at"`
	UpdatedAt Time          `json:"updated_at"`
	DeletedAt *Time         `json:"deleted_at"`
	Version   int64         `json:"version"`
	ETag      string        `json:"etag"`
}

// Block is a scheduled interval with task and outcome metadata.
type Block struct {
	ID            int64           `json:"id"`
	TaskID        int64           `json:"task_id"`
	TaskTitle     string          `json:"task_title"`
	TaskETag      string          `json:"task_etag"` // send as ReblockRequest.task_etag / web `task_etag`
	Date          civil.Date      `json:"date"`
	Start         civil.Minute    `json:"start"`
	End           civil.EndMinute `json:"end"`
	Status        string          `json:"status"`
	OutcomeSource *string         `json:"outcome_source"`
	CancelReason  *string         `json:"cancel_reason"`
	Slipped       bool            `json:"slipped"`
	SlippedAt     *Time           `json:"slipped_at"`
	Current       bool            `json:"current"`
	Started       bool            `json:"started"`
	Ended         bool            `json:"ended"`
	NeedsReblock  bool            `json:"needs_reblock"`
	CreatedAt     Time            `json:"created_at"`
	ResolvedAt    *Time           `json:"resolved_at"`
	Version       int64           `json:"version"`
	ETag          string          `json:"etag"`
}

// Interruption records a switch of focus to another task.
type Interruption struct {
	ID          int64      `json:"id"`
	At          Time       `json:"at"`
	Date        civil.Date `json:"date"`
	FromTaskID  *int64     `json:"from_task_id"`
	FromBlockID *int64     `json:"from_block_id"`
	ToTaskID    int64      `json:"to_task_id"`
}

// Effect describes an observable consequence of an operation.
type Effect struct {
	Kind    string `json:"kind"`
	TaskID  int64  `json:"task_id,omitempty"`
	BlockID int64  `json:"block_id,omitempty"`
	EntryID int64  `json:"entry_id,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Via     string `json:"via,omitempty"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
}

// OpResponse contains an operation result and its current entity representations.
type OpResponse struct {
	OpID     *string  `json:"op_id"`
	Replayed bool     `json:"replayed"`
	Task     *Task    `json:"task,omitempty"`
	Entry    *Entry   `json:"entry,omitempty"`
	Block    *Block   `json:"block,omitempty"`
	Tasks    []Task   `json:"tasks,omitempty"`
	Effects  []Effect `json:"effects"`
	Warnings []string `json:"warnings"`
}

// Clock contains the server clock and instance settings.
type Clock struct {
	Now           Time       `json:"now"`
	Today         civil.Date `json:"today"`
	Timezone      string     `json:"timezone"`
	WeekStart     string     `json:"week_start"`
	DueSoonDays   int        `json:"due_soon_days"`
	ServerVersion string     `json:"server_version"`
	Generation    string     `json:"generation"` // db_generation; send as Taskronin-Generation
}

// Banner describes a notice displayed by clients.
type Banner struct{ Kind, Message string } // kinds: backup_failed, backup_stale, ai_changes, purge_incomplete, purge_ledger_damaged, tz_changed

// BackupHealth summarizes the most recent backup attempt.
type BackupHealth struct {
	LastSuccessAt *Time  `json:"last_success_at"`
	LastError     string `json:"last_error"`
	Healthy       bool   `json:"healthy"`
}

// Today contains the current dashboard view.
type Today struct {
	Clock         Clock          `json:"clock"`
	Focus         *Task          `json:"focus"`
	CurrentBlock  *Block         `json:"current_block"`
	Blocks        []Block        `json:"blocks"`
	TriageCount   int            `json:"triage_count"`
	TriageTop     []Task         `json:"triage_top"` // first 5 in triage order
	Overdue       []Task         `json:"overdue"`
	DueSoon       []Task         `json:"due_soon"`
	Entries       []Entry        `json:"entries"`
	Interruptions []Interruption `json:"interruptions"`
	AIChanges     int            `json:"ai_changes"`
	Backup        BackupHealth   `json:"backup"`
	Banners       []Banner       `json:"banners"`
}

// Status contains the compact current status view.
type Status struct {
	Focus        *StatusTask  `json:"focus"`         // {id, title}
	CurrentBlock *StatusBlock `json:"current_block"` // {id, task_id, title, end}
	Triage       int          `json:"triage"`
	Due          int          `json:"due"` // due-phase + overdue open tasks
	AI           int          `json:"ai"`
	BackupWarn   bool         `json:"backup_warning"`
	PurgeWarning bool         `json:"purge_warning"` // purge_incomplete or purge_ledger_damaged
}

// Day groups scheduled and recorded activity for a civil date.
type Day struct {
	Date          civil.Date     `json:"date"`
	Blocks        []Block        `json:"blocks"`
	Entries       []Entry        `json:"entries"`
	Unlinked      []Entry        `json:"unlinked"`
	Interruptions []Interruption `json:"interruptions"`
}

// Report contains work over a date range and optional task lists.
type Report struct {
	From     civil.Date            `json:"from"`
	To       civil.Date            `json:"to"`
	Tag      *string               `json:"tag"`
	Project  *string               `json:"project"`
	Days     []ReportDay           `json:"days"`  // ascending; days without entries omitted
	Tasks    map[string]ReportTask `json:"tasks"` // key = task id
	Todo     *ReportTodo           `json:"todo,omitempty"`
	Upcoming []Task                `json:"upcoming,omitempty"`
}

// ReportDay groups report lines by civil date.
type ReportDay struct {
	Date  civil.Date   `json:"date"`
	Lines []ReportLine `json:"lines"`
}

// ReportLine is one work or completion entry in a report.
type ReportLine struct {
	EntryID  int64         `json:"entry_id"`
	Kind     string        `json:"kind"`
	Title    string        `json:"title"`
	Notes    string        `json:"notes"`
	TaskID   *int64        `json:"task_id"`
	Time     *civil.Minute `json:"time"`
	Reopened bool          `json:"reopened"`
}

// ReportTask contains task metadata referenced by report lines.
type ReportTask struct {
	Title   string `json:"title"`
	Status  string `json:"status"`
	Deleted bool   `json:"deleted"`
}

// ReportTodo contains the current triage and backlog lists.
type ReportTodo struct {
	Triage  []Task `json:"triage"`
	Backlog []Task `json:"backlog"`
}

// SearchHit identifies a matching task or entry.
type SearchHit struct {
	Kind     string      `json:"kind"` // task|entry
	ID       int64       `json:"id"`
	TaskID   *int64      `json:"task_id"`
	Title    string      `json:"title"`
	Snippet  string      `json:"snippet"`
	Status   string      `json:"status,omitempty"`
	WorkDate *civil.Date `json:"work_date,omitempty"`
}

// Page contains one page of items and an optional continuation cursor.
type Page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// TagCount counts open, non-deleted tasks for a tag.
type TagCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
} // open, non-deleted tasks

// StatusTask identifies the focused task.
type StatusTask struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

// StatusBlock identifies the current block and its end time.
type StatusBlock struct {
	ID     int64           `json:"id"`
	TaskID int64           `json:"task_id"`
	Title  string          `json:"title"`
	End    civil.EndMinute `json:"end"`
}

// Upcoming contains deferred and soon-due tasks.
type Upcoming struct {
	Deferred []Task `json:"deferred"`
	DueSoon  []Task `json:"due_soon"`
}

// OpSummary describes an operation and its effects.
type OpSummary struct {
	Seq        int64    `json:"seq"`
	OpID       string   `json:"op_id"`
	Kind       string   `json:"kind"`
	Actor      string   `json:"actor"`
	Channel    string   `json:"channel"`
	At         Time     `json:"at"`
	State      string   `json:"state"`
	Revertible bool     `json:"revertible"`
	Via        *string  `json:"via"`
	TaskIDs    []int64  `json:"task_ids"`
	Effects    []Effect `json:"effects"`
}

// EventView contains an entity change and its operation metadata.
type EventView struct {
	ID       int64           `json:"id"`
	Op       OpSummary       `json:"op"` // Effects omitted
	Entity   string          `json:"entity"`
	EntityID int64           `json:"entity_id"`
	Action   string          `json:"action"`
	Before   json.RawMessage `json:"before"`
	After    json.RawMessage `json:"after"`
}

// OpDetail contains an operation and its events.
type OpDetail struct {
	Op     OpSummary   `json:"op"`
	Events []EventView `json:"events"`
}

// AIChanges contains review progress and operations by non-owner tokens.
type AIChanges struct {
	ReviewedThrough int64       `json:"reviewed_through"`
	Unreviewed      int         `json:"unreviewed"`
	Ops             []OpSummary `json:"ops"`
	NextCursor      *string     `json:"next_cursor"`
}

// WhoAmI describes the authenticated actor and database generation.
type WhoAmI struct {
	Actor      string `json:"actor"`
	Scopes     string `json:"scopes"`
	Owner      bool   `json:"owner"`
	Generation string `json:"generation"`
}

// TokenInfo contains token metadata without a secret or secret hash.
type TokenInfo struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	Scopes           string `json:"scopes"`
	Owner            bool   `json:"owner"`
	MutationsPerHour int    `json:"mutations_per_hour"`
	CreatedAt        Time   `json:"created_at"`
	ExpiresAt        *Time  `json:"expires_at"`
	LastUsedAt       *Time  `json:"last_used_at"`
	RevokedAt        *Time  `json:"revoked_at"`
}

// TokenCreated returns a newly created token with its one-time secret.
type TokenCreated struct {
	Token  TokenInfo `json:"token"`
	Secret string    `json:"secret"`
} // shown once

// Settings contains mutable instance settings.
type Settings struct {
	BackupKeepDaily  int `json:"backup_keep_daily"`
	BackupKeepWeekly int `json:"backup_keep_weekly"`
}

// Snapshot describes a backup snapshot.
type Snapshot struct {
	Name          string `json:"name"`
	Path          string `json:"path"`
	Kind          string `json:"kind"`
	At            Time   `json:"at"`
	Size          int64  `json:"size"`
	SchemaVersion int    `json:"schema_version"`
}

// BackupStatus contains backup health and available snapshots.
type BackupStatus struct {
	BackupHealth
	Dir       string     `json:"dir"`
	Snapshots []Snapshot `json:"snapshots"`
}
