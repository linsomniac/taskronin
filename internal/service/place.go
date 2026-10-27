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

// DeferIn identifies a task and its defer moment; nil clears the defer.
type DeferIn struct {
	ID     domain.TaskID
	Expect domain.Expect
	Until  *domain.Moment
}

// Defer acknowledges triage and sets or clears a task's defer moment.
func (s *Service) Defer(ctx context.Context, m Meta, in DeferIn) (*OpResult, error) {
	return s.run(ctx, m, opSpec{kind: domain.OpTaskDefer, class: classForward, scopes: domain.ScopeWrite, idem: true, revertible: true}, func(o *op) error {
		t, err := o.loadTask(in.ID, &in.Expect, false)
		if err != nil {
			return err
		}
		if in.Until != nil {
			if t.Status != domain.StatusOpen {
				return domain.Errorf(domain.CodeConflict, "task #%d is not open", t.ID)
			}
			if err := o.checkDefer(t, *in.Until); err != nil {
				return err
			}
			// AIDEV-NOTE: Defer is a placement throughout the op. Suppress
			// slipped/continue additions before closing blocks, as in Reblock.
			o.placed[t.ID] = true
			if err := o.closeForDefer(t); err != nil {
				return err
			}
		}
		if err := o.place(t.ID); err != nil {
			return err
		}
		if in.Until != nil {
			if err := o.setDefer(t.ID, in.Until); err != nil {
				return err
			}
			if err := o.clearFocusIf(t.ID); err != nil {
				return err
			}
		}
		o.ref(domain.EntityRef{Entity: domain.EntTask, ID: int64(t.ID)})
		return nil
	})
}

func (o *op) checkDefer(t domain.Task, until domain.Moment) error {
	// AIDEV-NOTE: The task's due fields are validated before reaching here.
	// Overdue takes precedence over past-defer and ordering errors (DUE-09).
	if duePhase(t, o.now, o.today, o.s.zone) == domain.DueOverdue {
		return domain.Errorf(domain.CodeValidation, "change the due date first")
	}
	if err := validateMoment("defer", &until); err != nil {
		return err
	}
	if !o.s.zone.DeferMoment(until.Date, until.Min).After(o.now) {
		return domain.Errorf(domain.CodeValidation, "defer must be in the future")
	}
	var due *domain.Moment
	if t.DueDate.Valid {
		due = &domain.Moment{Date: t.DueDate.V, Min: t.DueMin}
	}
	return o.checkDeferDue(&until, due)
}

func (o *op) closeForDefer(t domain.Task) error {
	blocks, err := store.PlannedOfTask(o.ctx, o.tx, t.ID)
	if err != nil {
		return err
	}
	for _, b := range blocks {
		if err := o.endEarly(&b, domain.CancelDeferred); err != nil {
			return err
		}
	}
	return nil
}

func (o *op) setDefer(id domain.TaskID, until *domain.Moment) error {
	// Reload to preserve placement fields written earlier in this op.
	t, err := store.GetTask(o.ctx, o.tx, id)
	if err != nil {
		return err
	}
	before := t
	t.DeferDate, t.DeferMin = sql.Null[civil.Date]{}, sql.Null[civil.Minute]{}
	if until != nil {
		t.DeferDate, t.DeferMin = sql.Null[civil.Date]{V: until.Date, Valid: true}, until.Min
	}
	if err := store.UpdateTask(o.ctx, o.tx, t); err != nil {
		return err
	}
	return o.recordTaskUpdate(before, t)
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
