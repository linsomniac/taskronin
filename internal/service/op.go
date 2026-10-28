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
	targetID                            domain.OpID // Explicit Revert target; selected after replay lookup.
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
	// AIDEV-NOTE: Task 18 supplies identity, lifecycle, replay and rate checks below.
	// AIDEV-NOTE: Reversal target selection and stack transitions (formerly an
	// AIDEV-TODO for Tasks 30-31) are implemented below.
	if spec.idem {
		if err := validateIdemKey(m.Idem.Key); err != nil {
			return nil, err
		}
	}
	if m.Gen == "" {
		return nil, domain.Errorf(domain.CodePreconditionReq, "database generation is required")
	}
	if m.Gen != s.gen {
		return nil, domain.Errorf(domain.CodeGenerationMismatch, "database generation does not match")
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), OpBudget)
	defer cancel()
	leave, err := s.enter()
	if err != nil {
		return nil, err
	}
	defer leave()
	tx, done, err := beginTx(ctx, s.store.Writer, nil)
	if err != nil {
		return nil, writerAdmissionError(err)
	}
	defer done()
	now := s.clock.Now().Truncate(time.Millisecond)
	// AIDEV-NOTE: Idempotency lookup precedes target selection (R2-5),
	// reconcile and the rate cap; a retry must never act on the next target.
	var hash [32]byte
	if spec.idem {
		hash = idemHash(s.idemHMACKey, m.Actor.Key(), m.Idem)
		row, err := s.lookupIdem(ctx, tx, m, hash, now)
		if err != nil {
			return nil, serviceError(err)
		}
		if row != nil {
			if err := tx.Rollback(); err != nil {
				return nil, serviceError(err)
			}
			done() // free the writer before the replay loads results on the reader
			return s.replayIdem(ctx, *row)
		}
	}
	savepoint := false
	fail := func(cause error) (*OpResult, error) {
		if savepoint {
			if _, err := tx.ExecContext(ctx, `ROLLBACK TO user_op`); err != nil {
				return nil, serviceError(errors.Join(cause, err))
			}
			if _, err := tx.ExecContext(ctx, `RELEASE user_op`); err != nil {
				return nil, serviceError(errors.Join(cause, err))
			}
		}
		var committed *committedError
		if errors.As(cause, &committed) {
			// AIDEV-NOTE: Select/replay precede reconcile. A committed pop keeps
			// no attempted op or events, and atomically remembers its terminal 409.
			row := committed.StackRow
			if err := store.DeleteStackRow(ctx, tx, row.Actor, row.Stack, row.Seq); err != nil {
				return nil, serviceError(err)
			}
			if spec.idem {
				problem := idemProblem{Code: committed.Err.Code, Detail: committed.Err.Detail}
				for _, conflict := range committed.Err.Conflicts {
					problem.Conflicts = append(problem.Conflicts, idemConflict(conflict))
				}
				body, err := json.Marshal(problem)
				if err != nil {
					return nil, serviceError(err)
				}
				if err := store.PutIdem(ctx, tx, store.IdemRow{Actor: m.Actor.Key(), Key: m.Idem.Key, Hash: hash,
					Status: 409, ErrorJSON: body, CreatedAt: now}); err != nil {
					return nil, serviceError(err)
				}
			}
			cause = committed.Err
		} else if !savepoint {
			return nil, serviceError(cause)
		}
		if err := tx.Commit(); err != nil {
			return nil, &domain.Error{Code: domain.CodeInternal, Err: errors.Join(cause, err)}
		}
		return nil, serviceError(cause)
	}
	var target *domain.OpRecord
	var stackRow undoRowKey
	var stackOpID domain.OpID
	scopes := spec.scopes
	switch spec.class {
	case classRevert:
		r, err := store.GetOp(ctx, tx, spec.targetID)
		if err != nil {
			return fail(err)
		}
		target = &r
	case classUndo, classRedo:
		target, stackRow, err = selectStackOp(ctx, tx, m.Actor.Key(), spec.class)
		if err != nil {
			return fail(err)
		}
	default:
		// Forward operations have no reversal target.
	}
	if target != nil {
		if !m.Actor.Scopes.Has(target.Scopes) || (m.Actor.Kind != domain.ActorOwner && m.Actor.Key() != target.Actor) {
			return fail(domain.Errorf(domain.CodeForbidden, "operation is not permitted"))
		}
		scopes = target.Scopes
		if spec.class == classUndo || spec.class == classRedo {
			stackOpID = target.ID
			target, err = resolveStackTarget(ctx, tx, target, stackRow)
			if err != nil {
				return fail(err)
			}
		}
	}
	if !spec.noReconcile {
		if err := s.reconcileTx(ctx, tx, now); err != nil {
			return nil, serviceError(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT user_op`); err != nil {
		return nil, serviceError(err)
	}
	savepoint = true
	if m.Actor.MutationsPerHour > 0 {
		n, oldest, err := store.CountOpsSince(ctx, tx, m.Actor.Key(), now.Add(-time.Hour))
		if err != nil {
			return fail(err)
		}
		if n >= m.Actor.MutationsPerHour {
			return fail(&domain.Error{Code: domain.CodeRateLimited, Detail: "hourly mutation limit reached",
				RetryAfter: oldest.V.Add(time.Hour).Sub(now)})
		}
	}
	o := s.newOp(ctx, tx, m.Actor, spec, now)
	if target != nil {
		o.target = target
		o.rec.Scopes = scopes
		o.rec.Via = "revert"
		switch spec.class {
		case classUndo:
			o.rec.Via = "undo"
		case classRedo:
			o.rec.Via = "redo"
		default:
			// Explicit Revert uses via="revert".
		}
		o.rec.RevertsOpID = sql.Null[domain.OpID]{V: target.ID, Valid: true}
	}
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
	switch {
	case noChange:
		if _, err := tx.ExecContext(ctx, `ROLLBACK TO user_op`); err != nil {
			return fail(err)
		}
	case (spec.class == classForward || spec.class == classRevert) && spec.revertible:
		if err := store.PushStack(ctx, tx, m.Actor.Key(), store.StackUndo, o.rec.ID); err != nil {
			return fail(err)
		}
		if err := store.ClearStack(ctx, tx, m.Actor.Key(), store.StackRedo); err != nil {
			return fail(err)
		}
		if err := store.TrimStack(ctx, tx, m.Actor.Key(), store.StackUndo, s.undoDepth); err != nil {
			return fail(err)
		}
	case spec.class == classUndo || spec.class == classRedo:
		if err := s.moveStackOp(ctx, tx, stackRow, stackOpID); err != nil {
			return fail(err)
		}
	}
	if spec.idem {
		row := store.IdemRow{Actor: m.Actor.Key(), Key: m.Idem.Key, Hash: hash,
			Status: 200, Refs: o.refs, CreatedAt: now}
		if !noChange {
			row.OpID = sql.Null[domain.OpID]{V: o.rec.ID, Valid: true}
			if o.created {
				row.Status = 201
			}
		}
		if err := store.PutIdem(ctx, tx, row); err != nil {
			return fail(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `RELEASE user_op`); err != nil {
		return fail(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, &domain.Error{Code: domain.CodeInternal, Err: err}
	}
	// AIDEV-NOTE: Release the writer connection before loadResult reads on the
	// reader pool; done is idempotent so the deferred call is harmless.
	done()
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
	return s.read(ctx, domain.SystemActor(), 0, false, func(q store.Q, now time.Time, today civil.Date) error {
		var tasks []domain.TaskView
		for _, ref := range r.Refs {
			switch ref.Entity {
			case domain.EntTask:
				t, err := store.GetTask(ctx, q, domain.TaskID(ref.ID))
				if err != nil {
					return err
				}
				v, err := s.taskView(ctx, q, t, now, today)
				if err != nil {
					return err
				}
				tasks = append(tasks, v)
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
				v, err := s.blockView(ctx, q, b, now)
				if err != nil {
					return err
				}
				r.Block = &v
			case domain.EntToken:
				// AIDEV-NOTE: Token results, including replays, share run's
				// original deadline and shutdown registration. Return no hash.
				token, err := store.GetToken(ctx, q, domain.TokenID(ref.ID))
				if err != nil {
					return err
				}
				token.SecretHash = [32]byte{}
				r.Token = &token
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
