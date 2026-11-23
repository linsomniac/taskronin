package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/store"
	"github.com/linsomniac/taskronin/internal/textsafe"
)

// AddEntryIn supplies work to log against an explicit task or standalone.
type AddEntryIn struct {
	TaskID   sql.Null[domain.TaskID] // Invalid means standalone.
	Title    string
	Notes    string
	WorkDate civil.Date             // Zero means today.
	At       sql.Null[civil.Minute] // Optional display/sort minute, 0..1439.
}

// UpdateEntryIn supplies optional changes to an entry's content and work date.
type UpdateEntryIn struct {
	ID       domain.EntryID
	Expect   domain.Expect
	Title    *string
	Notes    *string
	WorkDate *civil.Date
	At       *sql.Null[civil.Minute] // Non-nil invalid Null clears the time.
}

// RelinkEntryIn identifies an entry and its new task; invalid TaskID means standalone.
type RelinkEntryIn struct {
	ID     domain.EntryID
	Expect domain.Expect
	TaskID sql.Null[domain.TaskID]
}

// EntryRef identifies an entry and its expected version.
type EntryRef struct {
	ID     domain.EntryID
	Expect domain.Expect
}

// UpdateEntry edits work entries, or just the notes of completion entries.
func (s *Service) UpdateEntry(ctx context.Context, m Meta, in UpdateEntryIn) (*OpResult, error) {
	return s.run(ctx, m, opSpec{kind: domain.OpEntryUpdate, class: classForward, scopes: domain.ScopeDestroy, idem: true, revertible: true}, func(o *op) error {
		e, err := o.loadEntry(in.ID, &in.Expect, true)
		if err != nil {
			return err
		}
		if e.PurgedAt.Valid {
			return domain.Errorf(domain.CodeConflict, "entry #%d is purged", e.ID)
		}
		if e.DeletedAt.Valid {
			return domain.Errorf(domain.CodeDeleted, "entry #%d is deleted", e.ID)
		}
		if e.Kind == domain.EntryComplete && (in.Title != nil || in.WorkDate != nil || in.At != nil) {
			return domain.Errorf(domain.CodeValidation, "only notes of a complete entry are editable")
		}
		before := e
		if in.Title != nil {
			e.Title, err = textsafe.ValidateTitle(*in.Title)
			if err != nil {
				return validation("title", err)
			}
		}
		if in.Notes != nil {
			e.Notes, err = textsafe.ValidateNotes(*in.Notes)
			if err != nil {
				return validation("notes", err)
			}
		}
		if in.WorkDate != nil {
			e.WorkDate = *in.WorkDate
		}
		if in.At != nil {
			e.OccurredMin = *in.At
		}
		if in.WorkDate != nil || in.At != nil {
			if err := o.checkEntryDate(e.WorkDate, e.OccurredMin); err != nil {
				return err
			}
		}
		return o.saveEntryEdit(before, e, domain.ActUpdate)
	})
}

// RelinkEntry attaches a work entry to a task or makes it standalone.
func (s *Service) RelinkEntry(ctx context.Context, m Meta, in RelinkEntryIn) (*OpResult, error) {
	return s.run(ctx, m, opSpec{kind: domain.OpEntryRelink, class: classForward, scopes: domain.ScopeWrite, idem: true, revertible: true}, func(o *op) error {
		e, err := o.loadEntry(in.ID, &in.Expect, true)
		if err != nil {
			return err
		}
		if e.PurgedAt.Valid {
			return domain.Errorf(domain.CodeConflict, "entry #%d is purged", e.ID)
		}
		if e.DeletedAt.Valid {
			return domain.Errorf(domain.CodeDeleted, "entry #%d is deleted", e.ID)
		}
		if e.Kind == domain.EntryComplete {
			return domain.Errorf(domain.CodeValidation, "complete entries cannot be relinked")
		}
		if in.TaskID.Valid {
			if _, err := o.loadTask(in.TaskID.V, nil, false); err != nil {
				return err
			}
		}
		before := e
		e.TaskID = in.TaskID
		return o.saveEntryEdit(before, e, domain.ActUpdate)
	})
}

// DeleteEntry tombstones an entry unless it completes a currently done task.
func (s *Service) DeleteEntry(ctx context.Context, m Meta, in EntryRef) (*OpResult, error) {
	return s.run(ctx, m, opSpec{kind: domain.OpEntryDelete, class: classForward, scopes: domain.ScopeDestroy, idem: true, revertible: true}, func(o *op) error {
		e, err := o.loadEntry(in.ID, &in.Expect, false)
		if err != nil {
			return err
		}
		if e.Kind == domain.EntryComplete {
			t, err := o.loadTask(e.TaskID.V, nil, true)
			if err != nil {
				return err
			}
			if t.Status == domain.StatusDone {
				return domain.Errorf(domain.CodeConflict, "use reopen --remove-entry")
			}
		}
		before := e
		e.DeletedAt = sql.Null[time.Time]{V: o.now, Valid: true}
		return o.saveEntryEdit(before, e, domain.ActTombstone)
	})
}

// UndeleteEntry restores a tombstoned entry, leaving live entries unchanged.
func (s *Service) UndeleteEntry(ctx context.Context, m Meta, in EntryRef) (*OpResult, error) {
	return s.run(ctx, m, opSpec{kind: domain.OpEntryUndelete, class: classForward, scopes: domain.ScopeDestroy, idem: true, revertible: true}, func(o *op) error {
		e, err := o.loadEntry(in.ID, &in.Expect, true)
		if err != nil {
			return err
		}
		if e.PurgedAt.Valid {
			return domain.Errorf(domain.CodeConflict, "entry #%d is purged", e.ID)
		}
		if !e.DeletedAt.Valid {
			o.ref(domain.EntityRef{Entity: domain.EntEntry, ID: int64(e.ID)})
			return nil
		}
		before := e
		e.DeletedAt = sql.Null[time.Time]{}
		return o.saveEntryEdit(before, e, domain.ActUntombstone)
	})
}

func (o *op) saveEntryEdit(before, after domain.LogEntry, action domain.Action) error {
	if err := store.UpdateEntry(o.ctx, o.tx, after); err != nil {
		return err
	}
	ref := domain.EntityRef{Entity: domain.EntEntry, ID: int64(after.ID)}
	if action == domain.ActUpdate {
		if err := o.recordEntryUpdate(before, after); err != nil {
			return err
		}
	} else {
		bj, err := json.Marshal(map[string]any{"deleted_at": instantCol(before.DeletedAt)})
		if err != nil {
			return err
		}
		aj, err := json.Marshal(map[string]any{"deleted_at": instantCol(after.DeletedAt)})
		if err != nil {
			return err
		}
		if err := o.record(domain.Event{Entity: domain.EntEntry, EntityID: ref.ID, Action: action, Before: bj, After: aj}); err != nil {
			return err
		}
		o.markDirty(ref)
	}
	// AIDEV-NOTE: Recompute the union even for text edits and tombstones;
	// relinks and date changes must repair both the old and the new pair.
	o.addPair(before.TaskID, before.WorkDate)
	o.addPair(after.TaskID, after.WorkDate)
	o.ref(ref)
	return nil
}

// AddEntry records work, warning when the linked task is done or dropped.
func (s *Service) AddEntry(ctx context.Context, m Meta, in AddEntryIn) (*OpResult, error) {
	return s.run(ctx, m, opSpec{kind: domain.OpEntryAdd, class: classForward, scopes: domain.ScopeCapture, idem: true, revertible: true}, func(o *op) error {
		title, err := textsafe.ValidateTitle(in.Title)
		if err != nil {
			return validation("title", err)
		}
		notes, err := textsafe.ValidateNotes(in.Notes)
		if err != nil {
			return validation("notes", err)
		}
		date := in.WorkDate
		if date.IsZero() {
			date = o.today
		}
		if err := o.checkEntryDate(date, in.At); err != nil {
			return err
		}
		if in.TaskID.Valid {
			t, err := o.loadTask(in.TaskID.V, nil, false)
			if err != nil {
				return err
			}
			switch t.Status {
			case domain.StatusDone:
				o.warn("logged against a done task")
			case domain.StatusDropped:
				o.warn("logged against a dropped task")
			default:
			}
		}
		e := domain.LogEntry{TaskID: in.TaskID, Kind: domain.EntryWork, Title: title, Notes: notes,
			WorkDate: date, OccurredMin: in.At, CreatedAt: o.now, UpdatedAt: o.now}
		if err := o.insertEntry(&e); err != nil {
			return err
		}
		o.ref(domain.EntityRef{Entity: domain.EntEntry, ID: int64(e.ID)})
		o.created = true
		return nil
	})
}

func (o *op) insertEntry(e *domain.LogEntry) error {
	// AIDEV-NOTE: Work and completion entries share persistence and audit
	// bookkeeping; callers own response refs and Created semantics.
	if err := store.InsertEntry(o.ctx, o.tx, e); err != nil {
		return err
	}
	ref := domain.EntityRef{Entity: domain.EntEntry, ID: int64(e.ID)}
	o.markInserted(ref)
	o.markDirty(ref)
	after, err := json.Marshal(entryCols(*e))
	if err != nil {
		return err
	}
	if err := o.record(domain.Event{Entity: domain.EntEntry, EntityID: int64(e.ID), Action: domain.ActInsert, After: after}); err != nil {
		return err
	}
	o.addPair(e.TaskID, e.WorkDate)
	return nil
}

func (o *op) checkEntryDate(date civil.Date, at sql.Null[civil.Minute]) error {
	if _, err := civil.ParseDate(date.String()); err != nil {
		return validation("work_date", err)
	}
	if date.After(o.today) {
		return validation("work_date", errors.New("must not be in the future"))
	}
	// AIDEV-NOTE: At is display/sort data, never an instant (A20).
	// Accept DST gap minutes on past dates without resolving them in the zone.
	if at.Valid {
		if at.V < 0 || at.V > 1439 {
			return validation("time", errors.New("minute must be between 0 and 1439"))
		}
		if date == o.today && at.V > o.s.zone.MinuteOf(o.now) {
			return validation("time", errors.New("must not be in the future"))
		}
	}
	return nil
}

// GetEntry returns an entry, including a tombstone, with its linked task's title.
func (s *Service) GetEntry(ctx context.Context, a domain.Actor, id domain.EntryID) (domain.EntryView, error) {
	var view domain.EntryView
	err := s.read(ctx, a, domain.ScopeRead, false, func(q store.Q, _ time.Time, _ civil.Date) error {
		e, err := store.GetEntry(ctx, q, id)
		if errors.Is(err, store.ErrNotFound) {
			return domain.Errorf(domain.CodeNotFound, "entry #%d not found", id)
		}
		if err != nil {
			return err
		}
		view, err = s.entryView(ctx, q, e)
		return err
	})
	if err != nil {
		return domain.EntryView{}, err
	}
	return view, nil
}
