package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/store"
	"taskronin/internal/textsafe"
)

// CompleteIn supplies a task completion, optional retitle and completion log text.
type CompleteIn struct {
	ID       domain.TaskID
	Expect   domain.Expect
	As       *string
	Date     civil.Date // Zero means today; never future.
	LogTitle string     // Empty means the final task title.
	LogNotes string
}

// ReopenIn identifies a closed task and whether to remove its completion entry.
type ReopenIn struct {
	ID          domain.TaskID
	Expect      domain.Expect
	RemoveEntry bool
}

// Complete closes and places an open task, logging its completion on Date.
func (s *Service) Complete(ctx context.Context, m Meta, in CompleteIn) (*OpResult, error) {
	return s.run(ctx, m, opSpec{kind: domain.OpTaskComplete, class: classForward, scopes: domain.ScopeWrite, idem: true, revertible: true}, func(o *op) error {
		t, err := o.loadTask(in.ID, &in.Expect, false)
		if err != nil {
			return err
		}
		if t.Status != domain.StatusOpen {
			return domain.Errorf(domain.CodeConflict, "task #%d is not open", t.ID)
		}
		date := in.Date
		if date.IsZero() {
			date = o.today
		}
		if _, err := civil.ParseDate(date.String()); err != nil {
			return validation("date", err)
		}
		if date.After(o.today) {
			return validation("date", errors.New("must not be in the future"))
		}
		if in.As != nil {
			before := t
			t.Title, err = textsafe.ValidateTitle(*in.As)
			if err != nil {
				return validation("title", err)
			}
			if err := store.UpdateTask(o.ctx, o.tx, t); err != nil {
				return err
			}
			if err := o.recordTaskUpdate(before, t); err != nil {
				return err
			}
		}
		title := in.LogTitle
		if title == "" {
			title = t.Title
		}
		title, err = textsafe.ValidateTitle(title)
		if err != nil {
			return validation("log_title", err)
		}
		notes, err := textsafe.ValidateNotes(in.LogNotes)
		if err != nil {
			return validation("log_notes", err)
		}
		before := t
		t.Status = domain.StatusDone
		t.CompletedAt = sql.Null[time.Time]{V: o.now, Valid: true}
		t.CompletedDate = sql.Null[civil.Date]{V: date, Valid: true}
		if err := store.UpdateTask(o.ctx, o.tx, t); err != nil {
			return err
		}
		if err := o.recordTaskUpdate(before, t); err != nil {
			return err
		}
		e := domain.LogEntry{TaskID: sql.Null[domain.TaskID]{V: t.ID, Valid: true}, Kind: domain.EntryComplete,
			Title: title, Notes: notes, WorkDate: date, CreatedAt: o.now, UpdatedAt: o.now}
		if err := store.InsertEntry(o.ctx, o.tx, &e); err != nil {
			return err
		}
		ref := domain.EntityRef{Entity: domain.EntEntry, ID: int64(e.ID)}
		o.markInserted(ref)
		o.markDirty(ref)
		after, err := json.Marshal(entryCols(e))
		if err != nil {
			return err
		}
		if err := o.record(domain.Event{Entity: domain.EntEntry, EntityID: int64(e.ID), Action: domain.ActInsert, After: after}); err != nil {
			return err
		}
		o.addPair(e.TaskID, date)
		if err := o.closeForDone(t, date); err != nil {
			return err
		}
		if err := o.place(t.ID); err != nil {
			return err
		}
		if err := o.clearFocusIf(t.ID); err != nil {
			return err
		}
		o.ref(domain.EntityRef{Entity: domain.EntTask, ID: int64(t.ID)})
		o.ref(ref)
		return nil
	})
}

func (o *op) closeForDone(t domain.Task, d civil.Date) error {
	// AIDEV-NOTE: The done status is written before blocks close so hooks see
	// a non-open task and cannot add slipped/continue reasons (A§4.5).
	blocks, err := store.PlannedOfTask(o.ctx, o.tx, t.ID)
	if err != nil {
		return err
	}
	unslipped := false
	for _, b := range blocks {
		if b.Span.Date.Before(d) {
			continue
		}
		if b.Span.Date == d {
			started, err := o.started(b)
			if err != nil {
				return err
			}
			if started {
				if err := o.resolveByDerivation(&b); err != nil {
					return err
				}
				continue
			}
		}
		if err := o.setBlock(&b, blockChange{Status: domain.BlockCancelled, Cancel: domain.CancelTaskClosed, Slipped: &unslipped}); err != nil {
			return err
		}
	}
	blocks, err = store.BlocksOfTask(o.ctx, o.tx, t.ID)
	if err != nil {
		return err
	}
	for _, b := range blocks {
		if b.Span.Date.After(d) && b.Status == domain.BlockMissed && b.OutcomeSource == domain.OutcomeAuto {
			if err := o.setBlock(&b, blockChange{Status: domain.BlockCancelled, Cancel: domain.CancelTaskClosed, Slipped: &unslipped}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Reopen opens a done or dropped task without restoring blocks or placing it.
func (s *Service) Reopen(ctx context.Context, m Meta, in ReopenIn) (*OpResult, error) {
	scopes := domain.ScopeWrite
	if in.RemoveEntry {
		scopes |= domain.ScopeDestroy
	}
	return s.run(ctx, m, opSpec{kind: domain.OpTaskReopen, class: classForward, scopes: scopes, idem: true, revertible: true}, func(o *op) error {
		t, err := o.loadTask(in.ID, &in.Expect, false)
		if err != nil {
			return err
		}
		if t.Status != domain.StatusDone && t.Status != domain.StatusDropped {
			return domain.Errorf(domain.CodeConflict, "task #%d is not done or dropped", t.ID)
		}
		before := t
		t.Status, t.CompletedAt, t.CompletedDate = domain.StatusOpen, sql.Null[time.Time]{}, sql.Null[civil.Date]{}
		t.DueAck, t.DueAckDate = domain.DueNone, sql.Null[civil.Date]{}
		if err := store.UpdateTask(o.ctx, o.tx, t); err != nil {
			return err
		}
		if err := o.recordTaskUpdate(before, t); err != nil {
			return err
		}
		entries, err := store.CompleteEntries(o.ctx, o.tx, t.ID)
		if err != nil {
			return err
		}
		var completion *domain.LogEntry
		// Prefer a live completion even when a purged entry has a newer ID.
		for i := len(entries) - 1; i >= 0; i-- {
			e := &entries[i]
			if e.Reopened {
				continue
			}
			if !e.DeletedAt.Valid {
				completion = e
				break
			}
			if completion == nil && e.PurgedAt.Valid {
				completion = e
			}
		}
		if completion != nil {
			e := *completion
			e.Reopened = true
			if err := store.UpdateEntry(o.ctx, o.tx, e); err != nil {
				return err
			}
			if err := o.recordEntryUpdate(*completion, e); err != nil {
				return err
			}
			o.addPair(e.TaskID, e.WorkDate)
			if in.RemoveEntry && !e.DeletedAt.Valid {
				e.DeletedAt = sql.Null[time.Time]{V: o.now, Valid: true}
				if err := store.UpdateEntry(o.ctx, o.tx, e); err != nil {
					return err
				}
				after, err := json.Marshal(map[string]any{"deleted_at": o.now.UnixMilli()})
				if err != nil {
					return err
				}
				if err := o.record(domain.Event{Entity: domain.EntEntry, EntityID: int64(e.ID), Action: domain.ActTombstone,
					Before: json.RawMessage(`{"deleted_at":null}`), After: after}); err != nil {
					return err
				}
			}
		}
		if err := o.addReason(t.ID, domain.ReasonReopened); err != nil {
			return err
		}
		o.ref(domain.EntityRef{Entity: domain.EntTask, ID: int64(t.ID)})
		return nil
	})
}
