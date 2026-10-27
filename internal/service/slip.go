package service

import (
	"cmp"
	"database/sql"
	"maps"
	"slices"
	"time"

	"taskronin/internal/domain"
	"taskronin/internal/store"
)

type blockChange struct {
	Status  domain.BlockStatus
	Source  domain.OutcomeSource // Empty for planned/cancelled.
	Cancel  domain.CancelReason
	Slipped *bool // Nil leaves the slip value unchanged.
	Resolve bool  // Refresh resolution metadata for an explicit user outcome.
	Restore bool  // Write Image verbatim, skipping every transition hook.
	Image   map[string]any
}

// AIDEV-NOTE: setBlock is the ONLY way block status/slipped change (A§4.5).
// Persist before running removal hooks so they inspect the post-change state.
// Restore bypasses all timestamps and hooks; revert runs removal checks later.
func (o *op) setBlock(b *domain.TimeBlock, ch blockChange) error {
	before, after := *b, *b
	if ch.Restore {
		if err := store.ApplyColumns(o.ctx, o.tx, domain.EntBlock, int64(b.ID), ch.Image); err != nil {
			return err
		}
		var err error
		after, err = store.GetBlock(o.ctx, o.tx, b.ID)
		if err != nil {
			return err
		}
	} else {
		after.Status, after.OutcomeSource, after.CancelReason = ch.Status, ch.Source, ch.Cancel
		if after.Status == domain.BlockPlanned || after.Status == domain.BlockCancelled {
			after.OutcomeSource = ""
		}
		// AIDEV-NOTE: SetOutcome resolves at now even when confirming the
		// current status (A§3.3). Other source-only changes retain metadata.
		if before.Status != after.Status || ch.Resolve {
			after.ResolvedAt = sql.Null[time.Time]{V: o.now, Valid: true}
			after.ResolvedSeq = sql.Null[domain.OpSeq]{V: o.rec.Seq, Valid: true}
			if after.Status == domain.BlockPlanned {
				after.ResolvedAt = sql.Null[time.Time]{}
				after.ResolvedSeq = sql.Null[domain.OpSeq]{}
			}
		}
		if ch.Slipped != nil {
			after.Slipped = *ch.Slipped
		}
		if !before.Slipped && after.Slipped {
			after.SlippedAt = sql.Null[time.Time]{V: o.now, Valid: true}
			after.SlippedSeq = sql.Null[domain.OpSeq]{V: o.rec.Seq, Valid: true}
		}
		if err := store.UpdateBlock(o.ctx, o.tx, after); err != nil {
			return err
		}
	}
	if err := o.recordUpdate(domain.EntBlock, int64(b.ID), blockCols(before), blockCols(after)); err != nil {
		return err
	}
	*b = after
	if ch.Restore || (before.Status == after.Status && before.Slipped == after.Slipped) {
		return nil
	}
	t, err := store.GetTask(o.ctx, o.tx, b.TaskID)
	if err != nil {
		return err
	}
	eligible := t.Status == domain.StatusOpen && !t.DeletedAt.Valid && !o.placed[t.ID]
	if !before.Slipped && b.Slipped && eligible {
		if err := o.addReason(t.ID, domain.ReasonSlipped); err != nil {
			return err
		}
	}
	if before.Slipped && !b.Slipped {
		if err := o.afterUnslip(t.ID); err != nil {
			return err
		}
	}
	if before.Status != domain.BlockWorked && b.Status == domain.BlockWorked && eligible {
		later, err := store.HasPlannedAfter(o.ctx, o.tx, t.ID, b.Span.Date)
		if err != nil {
			return err
		}
		if !later {
			if err := o.addReason(t.ID, domain.ReasonContinue); err != nil {
				return err
			}
		}
	}
	if before.Status == domain.BlockWorked && b.Status != domain.BlockWorked {
		return o.afterUnwork(t.ID)
	}
	return nil
}

func (o *op) resolveByDerivation(b *domain.TimeBlock) error {
	status, err := derive(o.ctx, o.tx, b.TaskID, b.Span.Date)
	if err != nil {
		return err
	}
	slipped := status == domain.BlockMissed
	return o.setBlock(b, blockChange{Status: status, Source: domain.OutcomeAuto, Slipped: &slipped})
}

// AIDEV-NOTE: Removal checks use operation sequences, never timestamps. A
// backward clock correction must not undo acknowledgement. Exclude ID 0 to
// inspect all live blocks after the write, including Task 30's restored rows.
func (o *op) afterUnslip(task domain.TaskID) error {
	t, err := store.GetTask(o.ctx, o.tx, task)
	if err != nil {
		return err
	}
	var placed int64
	if t.LastPlacedSeq.Valid {
		placed = int64(t.LastPlacedSeq.V)
	}
	other, err := store.OtherSlippedSince(o.ctx, o.tx, task, 0, placed)
	if err != nil || other {
		return err
	}
	return o.removeReason(task, domain.ReasonSlipped)
}

func (o *op) afterUnwork(task domain.TaskID) error {
	t, err := store.GetTask(o.ctx, o.tx, task)
	if err != nil {
		return err
	}
	var placed int64
	if t.LastPlacedSeq.Valid {
		placed = int64(t.LastPlacedSeq.V)
	}
	other, err := store.OtherWorkedSince(o.ctx, o.tx, task, 0, placed)
	if err != nil || other {
		return err
	}
	return o.removeReason(task, domain.ReasonContinue)
}

func (o *op) recompute() error {
	// AIDEV-NOTE: Block updates AND their triage hooks are derived consequences
	// of entry changes. Revert skips these events and recomputes the pairs.
	previous := o.derived
	o.derived = true
	defer func() { o.derived = previous }()
	pairs := slices.SortedFunc(maps.Keys(o.pairs), func(a, b pairKey) int {
		if c := cmp.Compare(a.Task, b.Task); c != 0 {
			return c
		}
		return a.Date.Compare(b.Date)
	})
	for _, pair := range pairs {
		blocks, err := store.AutoResolvedOn(o.ctx, o.tx, pair.Task, pair.Date)
		if err != nil {
			return err
		}
		if len(blocks) == 0 {
			continue
		}
		status, err := derive(o.ctx, o.tx, pair.Task, pair.Date)
		if err != nil {
			return err
		}
		slipped := status == domain.BlockMissed
		for _, b := range blocks {
			if b.Status != status {
				if err := o.setBlock(&b, blockChange{Status: status, Source: domain.OutcomeAuto, Slipped: &slipped}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
