package service

import (
	"cmp"
	"context"
	"database/sql"
	"slices"
	"time"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/store"
)

// PlaceIn identifies a task, its expected version and its requested position.
type PlaceIn struct {
	ID     domain.TaskID
	Expect domain.Expect
	How    domain.PlaceHow
	Ref    domain.TaskID
}

// AcceptItem identifies a new task and the version seen by the caller.
type AcceptItem struct {
	ID     domain.TaskID
	Expect domain.Expect
}

// AcceptNewIn supplies the tasks to accept atomically from triage.
type AcceptNewIn struct{ Items []AcceptItem }

// Place acknowledges all triage reasons and optionally changes a task's rank.
func (s *Service) Place(ctx context.Context, m Meta, in PlaceIn) (*OpResult, error) {
	return s.run(ctx, m, opSpec{kind: domain.OpTaskPlace, class: classForward, scopes: domain.ScopeWrite, idem: true, revertible: true}, func(o *op) error {
		if !in.How.Valid() {
			return domain.Errorf(domain.CodeValidation, "unsupported placement")
		}
		if (in.How == domain.PlaceBefore || in.How == domain.PlaceAfter) && (in.Ref == 0 || in.Ref == in.ID) {
			return domain.Errorf(domain.CodeValidation, "placement reference must name another task")
		}
		t, err := o.loadTask(in.ID, &in.Expect, false)
		if err != nil {
			return err
		}
		if in.How != domain.PlaceKeep {
			rank, err := o.placementRank(in.How, in.Ref)
			if err != nil {
				return err
			}
			if err := o.moveTask(&t, rank); err != nil {
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

// AcceptNew places tasks whose only triage reason is new at the bottom in new-sequence order.
func (s *Service) AcceptNew(ctx context.Context, m Meta, in AcceptNewIn) (*OpResult, error) {
	r, err := s.run(ctx, m, opSpec{kind: domain.OpTaskAcceptNew, class: classForward, scopes: domain.ScopeWrite, idem: true, revertible: true}, func(o *op) error {
		if len(in.Items) == 0 {
			return domain.Errorf(domain.CodeValidation, "accept new requires at least one item")
		}
		seen := make(map[domain.TaskID]bool, len(in.Items))
		for _, item := range in.Items {
			if seen[item.ID] {
				return domain.Errorf(domain.CodeValidation, "duplicate task #%d", item.ID)
			}
			seen[item.ID] = true
		}
		type candidate struct {
			task domain.Task
			seq  domain.OpSeq
		}
		tasks := make([]candidate, 0, len(in.Items))
		for _, item := range in.Items {
			t, err := o.loadTask(item.ID, &item.Expect, false)
			if err != nil {
				return err
			}
			reasons, err := store.Reasons(o.ctx, o.tx, t.ID)
			if err != nil {
				return err
			}
			if len(reasons) != 1 || reasons[0].Reason != domain.ReasonNew {
				return domain.Errorf(domain.CodeConflict, "task #%d must have exactly the new reason", t.ID)
			}
			tasks = append(tasks, candidate{task: t, seq: reasons[0].Seq})
		}
		slices.SortStableFunc(tasks, func(a, b candidate) int { return cmp.Compare(a.seq, b.seq) })
		for _, c := range tasks {
			rank, err := o.rankBottom()
			if err != nil {
				return err
			}
			if err := o.moveTask(&c.task, rank); err != nil {
				return err
			}
			if err := o.place(c.task.ID); err != nil {
				return err
			}
			o.ref(domain.EntityRef{Entity: domain.EntTask, ID: int64(c.task.ID)})
		}
		return nil
	})
	// The shared loader uses Task for one ref; bulk responses always use Tasks,
	// including a replay, which also passes through this result normalization.
	if err == nil && r.Task != nil {
		r.Tasks, r.Task = []domain.TaskView{*r.Task}, nil
	}
	return r, err
}

func (o *op) place(id domain.TaskID) error {
	t, err := store.GetTask(o.ctx, o.tx, id)
	if err != nil {
		return err
	}
	before := t
	if err := o.clearReasons(id); err != nil {
		return err
	}
	t.LastPlacedAt = sql.Null[time.Time]{V: o.now, Valid: true}
	t.LastPlacedSeq = sql.Null[domain.OpSeq]{V: o.rec.Seq, Valid: true}
	t.DueAck = duePhase(t, o.now, o.today, o.s.zone)
	t.DueAckDate = sql.Null[civil.Date]{V: o.today, Valid: true}
	t.DeferDate, t.DeferMin = sql.Null[civil.Date]{}, sql.Null[civil.Minute]{}
	if err := store.UpdateTask(o.ctx, o.tx, t); err != nil {
		return err
	}
	if err := o.recordTaskUpdate(before, t); err != nil {
		return err
	}
	o.placed[id] = true
	return nil
}
