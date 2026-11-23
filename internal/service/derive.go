package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/store"
)

func derive(ctx context.Context, q store.Q, task domain.TaskID, d civil.Date) (domain.BlockStatus, error) {
	work, complete, err := store.CountEntriesOn(ctx, q, task, d)
	if err != nil {
		return "", err
	}
	switch {
	case complete > 0:
		return domain.BlockDone, nil
	case work > 0:
		return domain.BlockWorked, nil
	default:
		return domain.BlockMissed, nil
	}
}

func duePhase(t domain.Task, now time.Time, today civil.Date, z civil.Zone) domain.DuePhase {
	if !t.DueDate.Valid {
		return domain.DueNone
	}
	if !z.DueMoment(t.DueDate.V, t.DueMin).After(now) {
		return domain.DueOverdue
	}
	if !t.DueDate.V.After(today) {
		return domain.DueDue
	}
	return domain.DueNone
}

func (o *op) addReason(task domain.TaskID, r domain.TriageReason) error {
	row := domain.TriageRow{TaskID: task, Reason: r, Since: o.now, Seq: o.rec.Seq}
	added, err := store.AddReason(o.ctx, o.tx, row)
	if err != nil || !added {
		return err
	}
	after, err := reasonImage(row)
	if err != nil {
		return err
	}
	if err := o.record(domain.Event{Entity: domain.EntTriage, EntityID: int64(task), Action: domain.ActAdd, After: after}); err != nil {
		return err
	}
	o.markTaskDirty(task)
	return nil
}

func (o *op) removeReason(task domain.TaskID, r domain.TriageReason) error {
	removed, err := store.RemoveReason(o.ctx, o.tx, task, r)
	if err != nil || !removed.Valid {
		return err
	}
	before, err := reasonImage(removed.V)
	if err != nil {
		return err
	}
	if err := o.record(domain.Event{Entity: domain.EntTriage, EntityID: int64(task), Action: domain.ActRemove, Before: before}); err != nil {
		return err
	}
	o.markTaskDirty(task)
	return nil
}

func (o *op) clearReasons(task domain.TaskID) error {
	rows, err := store.Reasons(o.ctx, o.tx, task)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := o.removeReason(task, row.Reason); err != nil {
			return err
		}
	}
	return nil
}

func reasonImage(row domain.TriageRow) (json.RawMessage, error) {
	return json.Marshal(map[string]any{"reason": string(row.Reason), "since": row.Since.UnixMilli(), "seq": int64(row.Seq)})
}
