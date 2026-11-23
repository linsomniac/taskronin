package api

import (
	"database/sql"
	"time"

	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/domain"
)

// Conv converts domain values using the instance generation and server location.
// Loc must be supplied by the caller; conversion performs no I/O.
type Conv struct {
	Gen domain.Generation
	Loc *time.Location
}

// Time represents an instant in the configured server location.
func (c Conv) Time(t time.Time) Time { return Time{t.In(c.Loc)} }

// OptTime converts a nullable instant to a wire timestamp or nil.
func (c Conv) OptTime(t sql.Null[time.Time]) *Time {
	if !t.Valid {
		return nil
	}
	return new(c.Time(t.V))
}

// ETag formats an entity version with the instance generation.
func (c Conv) ETag(v domain.Version) string { return FormatETag(c.Gen, v) }

// Task converts a task view, preserving reason and tag order.
func (c Conv) Task(v domain.TaskView) Task {
	reasons := make([]Reason, len(v.Reasons))
	for i, reason := range v.Reasons {
		reasons[i] = Reason{Reason: string(reason.Reason), Since: c.Time(reason.Since)}
	}
	return Task{
		ID: int64(v.ID), Title: v.Title, Notes: v.Notes, Status: string(v.Status), View: string(v.View),
		Reasons: reasons, Defer: optMoment(v.DeferDate, v.DeferMin), Deferred: v.Deferred,
		Due: optMoment(v.DueDate, v.DueMin), DuePhase: string(v.DuePhase), DueSoon: v.DueSoon,
		Project: optString(v.Project), Tags: append([]string{}, v.Tags...), Priority: optString(string(v.Priority)),
		Origin: v.Origin, Slips: Slips{Lifetime: v.SlipsLifetime, SinceTriage: v.SlipsSinceTriage},
		LastPlacedAt: c.OptTime(v.LastPlacedAt), CompletedAt: c.OptTime(v.CompletedAt), CompletedDate: nullPtr(v.CompletedDate),
		CreatedAt: c.Time(v.CreatedAt), UpdatedAt: c.Time(v.UpdatedAt), DeletedAt: c.OptTime(v.DeletedAt),
		Version: int64(v.Version), ETag: c.ETag(v.Version),
	}
}

// TaskDetail converts a task and its associated records, including empty arrays.
func (c Conv) TaskDetail(v domain.TaskDetail) TaskDetail {
	result := TaskDetail{
		Task: c.Task(v.TaskView), Entries: make([]Entry, len(v.Entries)),
		Blocks: make([]Block, len(v.Blocks)), Interruptions: make([]Interruption, len(v.Interruptions)),
	}
	for i, entry := range v.Entries {
		result.Entries[i] = c.Entry(entry)
	}
	for i, block := range v.Blocks {
		result.Blocks[i] = c.Block(block)
	}
	for i, interruption := range v.Interruptions {
		result.Interruptions[i] = c.Interruption(interruption)
	}
	return result
}

// Entry converts an entry view including the linked task's current title.
func (c Conv) Entry(v domain.EntryView) Entry { return c.LogEntry(v.LogEntry, v.TaskTitle) }

// LogEntry converts an entry with a task title supplied by the caller.
func (c Conv) LogEntry(e domain.LogEntry, taskTitle string) Entry {
	return Entry{
		ID: int64(e.ID), TaskID: nullID(e.TaskID), TaskTitle: optString(taskTitle),
		Kind: string(e.Kind), Title: e.Title, Notes: e.Notes, WorkDate: e.WorkDate,
		Time: nullPtr(e.OccurredMin), Reopened: e.Reopened,
		CreatedAt: c.Time(e.CreatedAt), UpdatedAt: c.Time(e.UpdatedAt), DeletedAt: c.OptTime(e.DeletedAt),
		Version: int64(e.Version), ETag: c.ETag(e.Version),
	}
}

// Block converts a block view with separate block and task preconditions.
func (c Conv) Block(v domain.BlockView) Block {
	return Block{
		ID: int64(v.ID), TaskID: int64(v.TaskID), TaskTitle: v.TaskTitle,
		// AIDEV-NOTE: Reblock checks both entities; task_etag must use the
		// linked task's version, never the block's version (R4-7).
		TaskETag: c.ETag(v.TaskVersion), Date: v.Span.Date, Start: v.Span.Start, End: v.Span.End,
		Status: string(v.Status), OutcomeSource: optString(string(v.OutcomeSource)), CancelReason: optString(string(v.CancelReason)),
		Slipped: v.Slipped, SlippedAt: c.OptTime(v.SlippedAt), Current: v.Current, Started: v.Started,
		Ended: v.Ended, NeedsReblock: v.NeedsReblock, CreatedAt: c.Time(v.CreatedAt), ResolvedAt: c.OptTime(v.ResolvedAt),
		Version: int64(v.Version), ETag: c.ETag(v.Version),
	}
}

// Interruption converts a focus switch and its optional source IDs.
func (c Conv) Interruption(i domain.Interruption) Interruption {
	return Interruption{
		ID: int64(i.ID), At: c.Time(i.At), Date: i.Date,
		FromTaskID: nullID(i.FromTaskID), FromBlockID: nullID(i.FromBlockID), ToTaskID: int64(i.ToTaskID),
	}
}

// Token converts only public token metadata, never the secret hash.
func (c Conv) Token(t domain.Token) TokenInfo {
	return TokenInfo{
		ID: int64(t.ID), Name: t.Name, Scopes: t.Scopes.String(), Owner: t.IsOwner, MutationsPerHour: t.MutationsPerHour,
		CreatedAt: c.Time(t.CreatedAt), ExpiresAt: c.OptTime(t.ExpiresAt), LastUsedAt: c.OptTime(t.LastUsedAt), RevokedAt: c.OptTime(t.RevokedAt),
	}
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullPtr[T any](v sql.Null[T]) *T {
	if !v.Valid {
		return nil
	}
	return &v.V
}

func nullID[T ~int64](v sql.Null[T]) *int64 {
	if !v.Valid {
		return nil
	}
	return new(int64(v.V))
}

func optMoment(date sql.Null[civil.Date], minute sql.Null[civil.Minute]) *Moment {
	if !date.Valid {
		return nil
	}
	return &Moment{Date: date.V, Time: nullPtr(minute)}
}
