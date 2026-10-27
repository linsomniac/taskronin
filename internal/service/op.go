package service

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/store"
)

type opClass uint8

const (
	classForward opClass = iota + 1
	classRevert  opClass = iota + 1
	classUndo    opClass = iota + 1
	classRedo    opClass = iota + 1
)

type opSpec struct {
	kind                                domain.OpKind
	class                               opClass
	scopes                              domain.Scopes
	idem, revertible, ownerOnly, notWeb bool
	noReconcile, structuralOnly         bool
}

type pairKey struct {
	Task domain.TaskID
	Date civil.Date
}

type op struct {
	s                 *Service
	ctx               context.Context
	tx                *sql.Tx
	spec              opSpec
	rec               domain.OpRecord
	actor             domain.Actor
	now               time.Time
	today             civil.Date
	gen               domain.Generation
	target            *domain.OpRecord
	placed, rankMoved map[domain.TaskID]bool
	dirty, inserted   map[domain.EntityRef]bool
	pairs             map[pairKey]struct{}
	events            []domain.Event
	warnings          []string
	refs              []domain.EntityRef
	created, derived  bool
}

func (s *Service) newOp(ctx context.Context, tx *sql.Tx, a domain.Actor, spec opSpec, now time.Time) *op {
	return &op{s: s, ctx: ctx, tx: tx, spec: spec, actor: a, now: now, today: s.zone.DateOf(now), gen: s.gen,
		rec: domain.OpRecord{ID: domain.NewOpID(), Kind: spec.kind, Actor: a.Key(), Channel: a.Channel,
			At: now, Scopes: spec.scopes, Revertible: spec.revertible, State: domain.OpActive},
		placed: make(map[domain.TaskID]bool), rankMoved: make(map[domain.TaskID]bool),
		dirty: make(map[domain.EntityRef]bool), inserted: make(map[domain.EntityRef]bool), pairs: make(map[pairKey]struct{})}
}

// AIDEV-NOTE: run is the single write path for domain operations (A§4.3).
// Reconcile finishes before user_op, so failed/no-change user work preserves it.
func (s *Service) run(ctx context.Context, m Meta, spec opSpec, fn func(*op) error) (*OpResult, error) {
	if (spec.class == classForward && !m.Actor.Scopes.Has(spec.scopes)) ||
		(spec.ownerOnly && m.Actor.Kind != domain.ActorOwner) || (spec.notWeb && m.Actor.Channel == domain.ChanWeb) {
		return nil, domain.Errorf(domain.CodeForbidden, "operation is not permitted")
	}
	// AIDEV-TODO: Task 18 adds request identity, lifecycle, replay and rate checks;
	// Tasks 30–31 add reversal target selection and stack transitions.
	tx, err := s.store.Writer.BeginTx(ctx, nil)
	if err != nil {
		return nil, serviceError(err)
	}
	defer func() { _ = tx.Rollback() }()
	now := s.clock.Now().Truncate(time.Millisecond)
	if !spec.noReconcile {
		if err := s.reconcileTx(ctx, tx, now); err != nil {
			return nil, serviceError(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT user_op`); err != nil {
		return nil, serviceError(err)
	}
	fail := func(cause error) (*OpResult, error) {
		if _, err := tx.ExecContext(ctx, `ROLLBACK TO user_op`); err != nil {
			return nil, serviceError(errors.Join(cause, err))
		}
		if _, err := tx.ExecContext(ctx, `RELEASE user_op`); err != nil {
			return nil, serviceError(errors.Join(cause, err))
		}
		if err := tx.Commit(); err != nil {
			return nil, &domain.Error{Code: domain.CodeInternal, Err: errors.Join(cause, err)}
		}
		return nil, serviceError(cause)
	}
	o := s.newOp(ctx, tx, m.Actor, spec, now)
	if err := store.InsertOp(ctx, tx, &o.rec); err != nil {
		return fail(err)
	}
	if err := fn(o); err != nil {
		return fail(err)
	}
	if err := o.recompute(); err != nil {
		return fail(err)
	}
	if err := o.checkInvariants(); err != nil {
		return fail(err)
	}
	if err := o.bumpVersions(); err != nil {
		return fail(err)
	}
	noChange := spec.class == classForward && len(o.events) == 0
	if noChange {
		if _, err := tx.ExecContext(ctx, `ROLLBACK TO user_op`); err != nil {
			return fail(err)
		}
	} else if spec.class == classForward && spec.revertible {
		if err := store.PushStack(ctx, tx, m.Actor.Key(), store.StackUndo, o.rec.ID); err != nil {
			return fail(err)
		}
		if err := store.ClearStack(ctx, tx, m.Actor.Key(), store.StackRedo); err != nil {
			return fail(err)
		}
		if err := store.TrimStack(ctx, tx, m.Actor.Key(), store.StackUndo, s.undoDepth); err != nil {
			return fail(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `RELEASE user_op`); err != nil {
		return fail(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, &domain.Error{Code: domain.CodeInternal, Err: err}
	}
	r := &OpResult{OpID: o.rec.ID, Created: o.created, NoChange: noChange, Refs: o.refs,
		Effects: effectsFromEvents(o.rec, o.events), Warnings: o.warnings}
	if noChange {
		r.OpID = domain.OpID{}
		r.Created = false
	}
	if err := s.loadResult(ctx, r); err != nil {
		return nil, err
	}
	return r, nil
}

func (o *op) bumpVersions() error {
	for ref, dirty := range o.dirty {
		if dirty && !o.inserted[ref] {
			if err := store.BumpVersion(o.ctx, o.tx, ref, o.now); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) loadResult(ctx context.Context, r *OpResult) error {
	if len(r.Refs) == 0 {
		return nil
	}
	return s.read(ctx, domain.SystemActor(), 0, false, func(q store.Q, _ time.Time, _ civil.Date) error {
		var tasks []domain.TaskView
		for _, ref := range r.Refs {
			switch ref.Entity {
			case domain.EntTask:
				t, err := store.GetTask(ctx, q, domain.TaskID(ref.ID))
				if err != nil {
					return err
				}
				// AIDEV-TODO: Task 16b enriches these persisted rows with taskView/blockView.
				tasks = append(tasks, domain.TaskView{Task: t})
			case domain.EntEntry:
				e, err := store.GetEntry(ctx, q, domain.EntryID(ref.ID))
				if err != nil {
					return err
				}
				r.Entry = &e
			case domain.EntBlock:
				b, err := store.GetBlock(ctx, q, domain.BlockID(ref.ID))
				if err != nil {
					return err
				}
				r.Block = &domain.BlockView{TimeBlock: b}
			default:
				// Settings and other event-only refs have no result representation.
			}
		}
		if len(tasks) == 1 {
			r.Task = &tasks[0]
		} else if len(tasks) > 1 {
			r.Tasks = tasks
		}
		return nil
	})
}
