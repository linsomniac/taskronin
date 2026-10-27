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

// AddEntryIn supplies work to log against an explicit task or standalone.
type AddEntryIn struct {
	TaskID   sql.Null[domain.TaskID] // Invalid means standalone.
	Title    string
	Notes    string
	WorkDate civil.Date             // Zero means today.
	At       sql.Null[civil.Minute] // Optional display/sort minute, 0..1439.
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
		if _, err := civil.ParseDate(date.String()); err != nil {
			return validation("work_date", err)
		}
		if date.After(o.today) {
			return validation("work_date", errors.New("must not be in the future"))
		}
		// AIDEV-NOTE: At is display/sort data, never an instant (A20).
		// Accept DST gap minutes on past dates without resolving them in the zone.
		if in.At.Valid {
			if in.At.V < 0 || in.At.V > 1439 {
				return validation("time", errors.New("minute must be between 0 and 1439"))
			}
			if date == o.today && in.At.V > s.zone.MinuteOf(o.now) {
				return validation("time", errors.New("must not be in the future"))
			}
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
		o.addPair(e.TaskID, e.WorkDate)
		o.ref(ref)
		o.created = true
		return nil
	})
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
