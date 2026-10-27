package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/store"
)

// BlockIn identifies a task, its expected version, and the interval to schedule.
type BlockIn struct {
	TaskID domain.TaskID
	Expect domain.Expect
	Span   domain.BlockSpan
}

// Block schedules an open task and acknowledges its triage reasons.
func (s *Service) Block(ctx context.Context, m Meta, in BlockIn) (*OpResult, error) {
	return s.run(ctx, m, opSpec{kind: domain.OpBlockCreate, class: classForward, scopes: domain.ScopeWrite, idem: true, revertible: true}, func(o *op) error {
		t, err := o.loadTask(in.TaskID, &in.Expect, false)
		if err != nil {
			return err
		}
		if _, err := o.createBlock(t, in.Span); err != nil {
			return err
		}
		if err := o.place(t.ID); err != nil {
			return err
		}
		o.ref(domain.EntityRef{Entity: domain.EntTask, ID: int64(t.ID)})
		o.created = true
		return nil
	})
}

// GetBlock returns a block, including a tombstone, with its task and interval state.
func (s *Service) GetBlock(ctx context.Context, a domain.Actor, id domain.BlockID) (domain.BlockView, error) {
	var view domain.BlockView
	err := s.read(ctx, a, domain.ScopeRead, false, func(q store.Q, now time.Time, _ civil.Date) error {
		b, err := store.GetBlock(ctx, q, id)
		if errors.Is(err, store.ErrNotFound) {
			return domain.Errorf(domain.CodeNotFound, "block #%d not found", id)
		}
		if err != nil {
			return err
		}
		view, err = s.blockView(ctx, q, b, now)
		return err
	})
	if err != nil {
		return domain.BlockView{}, err
	}
	return view, nil
}

func (o *op) createBlock(t domain.Task, span domain.BlockSpan) (domain.TimeBlock, error) {
	if t.Status != domain.StatusOpen {
		return domain.TimeBlock{}, domain.Errorf(domain.CodeConflict, "task #%d is not open", t.ID)
	}
	if err := o.checkSpan(span); err != nil {
		return domain.TimeBlock{}, err
	}
	b := domain.TimeBlock{TaskID: t.ID, Span: span, Status: domain.BlockPlanned, CreatedAt: o.now}
	if err := store.InsertBlock(o.ctx, o.tx, &b); err != nil {
		return domain.TimeBlock{}, err
	}
	ref := domain.EntityRef{Entity: domain.EntBlock, ID: int64(b.ID)}
	o.markInserted(ref)
	o.markDirty(ref)
	after, err := json.Marshal(blockCols(b))
	if err != nil {
		return domain.TimeBlock{}, err
	}
	if err := o.record(domain.Event{Entity: domain.EntBlock, EntityID: int64(b.ID), Action: domain.ActInsert, After: after}); err != nil {
		return domain.TimeBlock{}, err
	}
	o.ref(ref)
	return b, nil
}

// AIDEV-NOTE: Keep span validation independent of insertion (F10). Reblock
// also uses it to validate an in-place move while excluding its existing row.
func (o *op) checkSpan(span domain.BlockSpan, exclude ...domain.BlockID) error {
	if span.Start < 0 || civil.Minute(span.End) <= span.Start || span.End > 1440 {
		return domain.Errorf(domain.CodeValidation, "block requires 0 <= start < end <= 1440 on the same day")
	}
	if _, err := civil.ParseDate(span.Date.String()); err != nil {
		return validation("date", err)
	}
	if span.Date.Before(o.today) {
		return domain.Errorf(domain.CodeValidation, "block date must not be in the past")
	}
	_, end, err := o.spanInstants(span)
	if err != nil {
		return err
	}
	if !end.After(o.now) {
		return domain.Errorf(domain.CodeValidation, "block end must be in the future")
	}
	overlaps, err := store.PlannedOverlapping(o.ctx, o.tx, span, exclude...)
	if err != nil {
		return err
	}
	if len(overlaps) > 0 {
		b := overlaps[0]
		return domain.Errorf(domain.CodeConflict, "overlaps b%d %s-%s (#%d)", b.ID, b.Span.Start, b.Span.End, b.TaskID)
	}
	return nil
}

func (o *op) spanInstants(span domain.BlockSpan) (start, end time.Time, err error) {
	// AIDEV-NOTE: Resolve both endpoints with GapReject, including an end at
	// the gap's start. Zone.Instant also handles the earlier fold and 24:00.
	instant := func(minute civil.Minute) (time.Time, error) {
		at, err := o.s.zone.Instant(span.Date, minute, civil.GapReject)
		if errors.Is(err, civil.ErrNonexistent) {
			next, nextErr := o.s.zone.Instant(span.Date, minute, civil.GapForward)
			if nextErr != nil {
				return time.Time{}, nextErr
			}
			return time.Time{}, domain.Errorf(domain.CodeValidation, "%s does not exist on %s; the clocks jump to %s",
				minute, span.Date, o.s.zone.MinuteOf(next))
		}
		return at, err
	}
	start, err = instant(span.Start)
	if err != nil {
		return start, end, err
	}
	end, err = instant(civil.Minute(span.End))
	return start, end, err
}
