package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/store"
)

// SetFocusIn supplies an optional focus target and an explicit interruption request.
type SetFocusIn struct {
	TaskID    sql.Null[domain.TaskID]
	Interrupt bool
}

// SetFocus changes or clears today's focus without placing the target task.
func (s *Service) SetFocus(ctx context.Context, m Meta, in SetFocusIn) (*OpResult, error) {
	return s.run(ctx, m, opSpec{kind: domain.OpFocusSet, class: classForward, scopes: domain.ScopeWrite, idem: true, revertible: true}, func(o *op) error {
		if in.TaskID.Valid {
			t, err := o.loadTask(in.TaskID.V, nil, false)
			if err != nil {
				return err
			}
			if t.Status != domain.StatusOpen || t.DeferDate.Valid {
				return domain.Errorf(domain.CodeConflict, "focus requires an open, non-deferred task")
			}
			if err := o.interrupt(t.ID, false, in.Interrupt); err != nil {
				return err
			}
		}
		return o.setFocus(in.TaskID)
	})
}

func (o *op) setFocus(task sql.Null[domain.TaskID]) error {
	before, err := store.GetFocus(o.ctx, o.tx)
	if err != nil {
		return err
	}
	after := domain.Focus{SetAt: sql.Null[time.Time]{V: o.now, Valid: true}}
	if task.Valid {
		after.TaskID = task
		after.Date = sql.Null[civil.Date]{V: o.today, Valid: true}
	}
	if before.TaskID == after.TaskID && before.Date == after.Date {
		return nil
	}
	if err := store.PutFocus(o.ctx, o.tx, after); err != nil {
		return err
	}
	b, err := json.Marshal(map[string]any{"task_id": integerCol(before.TaskID), "date": dateCol(before.Date)})
	if err != nil {
		return err
	}
	a, err := json.Marshal(map[string]any{"task_id": integerCol(after.TaskID), "date": dateCol(after.Date)})
	if err != nil {
		return err
	}
	if err := o.record(domain.Event{Entity: domain.EntFocus, EntityID: 1, Action: domain.ActSet, Before: b, After: a}); err != nil {
		return err
	}
	o.markDirty(domain.EntityRef{Entity: domain.EntFocus, ID: 1})
	return nil
}

func (o *op) clearFocusIf(task domain.TaskID) error {
	f, err := store.GetFocus(o.ctx, o.tx)
	if err != nil {
		return err
	}
	if f.TaskID.Valid && f.TaskID.V == task {
		return o.setFocus(sql.Null[domain.TaskID]{})
	}
	return nil
}

func (o *op) currentBlock() (sql.Null[domain.TimeBlock], error) {
	return currentBlockAt(o.ctx, o.tx, o.s.zone, o.now)
}

func currentBlockAt(ctx context.Context, q store.Q, z civil.Zone, now time.Time) (sql.Null[domain.TimeBlock], error) {
	blocks, err := store.PlannedOn(ctx, q, z.DateOf(now))
	if err != nil {
		return sql.Null[domain.TimeBlock]{}, err
	}
	for _, b := range blocks {
		// AIDEV-NOTE: Compare instants, not wall minutes: the second DST fold
		// can be inside a block even when its wall minute precedes the start.
		start, err := z.Instant(b.Span.Date, b.Span.Start, civil.GapReject)
		if err != nil {
			return sql.Null[domain.TimeBlock]{}, err
		}
		end, err := z.Instant(b.Span.Date, civil.Minute(b.Span.End), civil.GapReject)
		if err != nil {
			return sql.Null[domain.TimeBlock]{}, err
		}
		if !now.Before(start) && now.Before(end) {
			return sql.Null[domain.TimeBlock]{V: b, Valid: true}, nil
		}
	}
	return sql.Null[domain.TimeBlock]{}, nil
}

func (o *op) interrupt(to domain.TaskID, viaAddNow, explicit bool) error {
	// AIDEV-NOTE: Call before setFocus so the fallback is the previous focus.
	// A current block wins over that focus, including suppressing interruption
	// when returning to the block's own task (D35).
	b, err := o.currentBlock()
	if err != nil {
		return err
	}
	in := domain.Interruption{At: o.now, Date: o.today, ToTaskID: to, OpID: o.rec.ID}
	if b.Valid {
		if b.V.TaskID == to {
			return nil
		}
		in.FromTaskID = sql.Null[domain.TaskID]{V: b.V.TaskID, Valid: true}
		in.FromBlockID = sql.Null[domain.BlockID]{V: b.V.ID, Valid: true}
	} else {
		if !viaAddNow && !explicit {
			return nil
		}
		f, err := store.GetFocus(o.ctx, o.tx)
		if err != nil {
			return err
		}
		if !f.TaskID.Valid || f.TaskID.V == to {
			return nil
		}
		in.FromTaskID = f.TaskID
	}
	if err := store.InsertInterruption(o.ctx, o.tx, &in); err != nil {
		return err
	}
	after, err := json.Marshal(map[string]any{
		"at": in.At.UnixMilli(), "date": in.Date.String(), "from_task_id": integerCol(in.FromTaskID),
		"from_block_id": integerCol(in.FromBlockID), "to_task_id": int64(in.ToTaskID),
		"op_id": in.OpID.String(), "deleted_at": instantCol(in.DeletedAt),
	})
	if err != nil {
		return err
	}
	return o.record(domain.Event{Entity: domain.EntInterruption, EntityID: int64(in.ID), Action: domain.ActInsert, After: after})
}
