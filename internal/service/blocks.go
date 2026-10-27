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

// BlockRef identifies a block and its expected version.
type BlockRef struct {
	BlockID domain.BlockID
	Expect  domain.Expect
}

// ReblockIn supplies a new span and preconditions for both the block and its task.
type ReblockIn struct {
	BlockID            domain.BlockID
	Expect, TaskExpect domain.Expect
	Span               domain.BlockSpan
}

// SetOutcomeIn identifies a block, its expected version, and an outcome policy.
type SetOutcomeIn struct {
	BlockID domain.BlockID
	Expect  domain.Expect
	Outcome domain.UserOutcome
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

// Unblock ends a planned block without acknowledging the task's triage reasons.
func (s *Service) Unblock(ctx context.Context, m Meta, in BlockRef) (*OpResult, error) {
	return s.run(ctx, m, opSpec{kind: domain.OpBlockUnblock, class: classForward, scopes: domain.ScopeWrite, idem: true, revertible: true}, func(o *op) error {
		b, err := o.loadBlock(in.BlockID, &in.Expect)
		if err != nil {
			return err
		}
		if b.Status != domain.BlockPlanned {
			return domain.Errorf(domain.CodeConflict, "block #%d is not planned", b.ID)
		}
		if err := o.endEarly(&b, domain.CancelUser); err != nil {
			return err
		}
		o.ref(domain.EntityRef{Entity: domain.EntBlock, ID: int64(b.ID)})
		return nil
	})
}

// Reblock reschedules a block and acknowledges its task's triage reasons.
func (s *Service) Reblock(ctx context.Context, m Meta, in ReblockIn) (*OpResult, error) {
	return s.run(ctx, m, opSpec{kind: domain.OpBlockReblock, class: classForward, scopes: domain.ScopeWrite, idem: true, revertible: true}, func(o *op) error {
		b, err := o.loadBlock(in.BlockID, &in.Expect)
		if err != nil {
			return err
		}
		t, err := o.loadTask(b.TaskID, &in.TaskExpect, false)
		if err != nil {
			return err
		}
		if t.Status != domain.StatusOpen {
			return domain.Errorf(domain.CodeConflict, "task #%d is not open", t.ID)
		}
		// AIDEV-NOTE: Reblock is a placement throughout the op (A§3.1).
		// Suppress reason additions while ending the old block; place below
		// still clears existing reasons and persists the acknowledgement.
		o.placed[t.ID] = true
		var inPlace bool
		if b.Status == domain.BlockPlanned {
			started, err := o.started(b)
			if err != nil {
				return err
			}
			inPlace = !started && in.Span.Date == b.Span.Date
		}
		if inPlace {
			if err := o.checkSpan(in.Span, b.ID); err != nil {
				return err
			}
			before := b
			b.Span = in.Span
			if err := store.UpdateBlock(o.ctx, o.tx, b); err != nil {
				return err
			}
			if err := o.recordUpdate(domain.EntBlock, int64(b.ID), blockCols(before), blockCols(b)); err != nil {
				return err
			}
			o.ref(domain.EntityRef{Entity: domain.EntBlock, ID: int64(b.ID)})
		} else {
			// AIDEV-NOTE: End the old planned row before checking overlap for its
			// replacement. The runner rolls both changes back if creation fails.
			if b.Status == domain.BlockPlanned {
				if err := o.endEarly(&b, domain.CancelRescheduled); err != nil {
					return err
				}
			}
			if _, err := o.createBlock(t, in.Span); err != nil {
				return err
			}
		}
		if err := o.place(t.ID); err != nil {
			return err
		}
		o.ref(domain.EntityRef{Entity: domain.EntTask, ID: int64(t.ID)})
		return nil
	})
}

// SetOutcome overrides a block's outcome or restores automatic resolution.
func (s *Service) SetOutcome(ctx context.Context, m Meta, in SetOutcomeIn) (*OpResult, error) {
	return s.run(ctx, m, opSpec{kind: domain.OpBlockOutcome, class: classForward, scopes: domain.ScopeWrite, idem: true, revertible: true}, func(o *op) error {
		if !in.Outcome.Valid() {
			return domain.Errorf(domain.CodeValidation, "unsupported block outcome")
		}
		b, err := o.loadBlock(in.BlockID, &in.Expect)
		if err != nil {
			return err
		}
		o.ref(domain.EntityRef{Entity: domain.EntBlock, ID: int64(b.ID)})
		if b.Status == domain.BlockCancelled {
			return domain.Errorf(domain.CodeConflict, "cannot set a cancelled block's outcome; use unblock for cancellation")
		}
		if in.Outcome == domain.SetAuto {
			if b.Status == domain.BlockPlanned {
				return nil
			}
			if b.Span.Date.Before(o.today) {
				return o.resolveByDerivation(&b)
			}
			if b.Span.Date == o.today {
				slipped := false
				return o.setBlock(&b, blockChange{Status: domain.BlockPlanned, Slipped: &slipped})
			}
			return domain.Errorf(domain.CodeConflict, "cannot reset an outcome on a future block")
		}
		if b.Status == domain.BlockPlanned {
			started, err := o.started(b)
			if err != nil {
				return err
			}
			if b.Span.Date != o.today || !started {
				return domain.Errorf(domain.CodeConflict, "block has not started today; use unblock")
			}
		}
		slipped := in.Outcome == domain.SetMissed
		return o.setBlock(&b, blockChange{Status: domain.BlockStatus(in.Outcome), Source: domain.OutcomeUser, Slipped: &slipped, Resolve: true})
	})
}

func (o *op) started(b domain.TimeBlock) (bool, error) {
	start, err := o.s.zone.Instant(b.Span.Date, b.Span.Start, civil.GapReject)
	return !start.After(o.now), err
}

func (o *op) slipOnCancel(b domain.TimeBlock, c domain.CancelReason) bool {
	if c == domain.CancelTaskClosed || b.Span.Date != o.today || o.now.Sub(b.CreatedAt) < o.s.slipGrace {
		return false
	}
	// Persisted spans are valid; endEarly propagates any instant error first.
	started, err := o.started(b)
	return err == nil && !started
}

func (o *op) endEarly(b *domain.TimeBlock, c domain.CancelReason) error {
	started, err := o.started(*b)
	if err != nil {
		return err
	}
	if started {
		return o.resolveByDerivation(b)
	}
	slipped := o.slipOnCancel(*b, c)
	return o.setBlock(b, blockChange{Status: domain.BlockCancelled, Cancel: c, Slipped: &slipped})
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
