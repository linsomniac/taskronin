package service

import (
	"context"
	"database/sql"
	"time"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/store"
)

type reconcileStep struct {
	name string
	need func(context.Context, store.Q, time.Time, civil.Date) (bool, error)
	run  func(*op) error
}

// Tasks 27, 20, 23 and 26 register surface, due, overdue, resolve and focus,
// in that order, before concurrent use of the service begins.
var reconcileSteps []reconcileStep

// Reconcile performs time-dependent maintenance only when the reader finds work.
func (s *Service) Reconcile(ctx context.Context) error {
	leave, err := s.enter()
	if err != nil {
		return err
	}
	defer leave()
	var needed bool
	err = s.read(ctx, domain.SystemActor(), 0, false, func(q store.Q, now time.Time, _ civil.Date) error {
		var err error
		needed, err = s.needsReconcile(ctx, q, now)
		return err
	})
	if err != nil || !needed {
		return err
	}
	return s.writeTxEntered(ctx, func(tx *sql.Tx, now time.Time) error { return s.reconcileTx(ctx, tx, now) })
}

func (s *Service) needsReconcile(ctx context.Context, q store.Q, now time.Time) (bool, error) {
	today := s.zone.DateOf(now)
	var needed bool
	for _, step := range reconcileSteps {
		need, err := step.need(ctx, q, now, today)
		if err != nil {
			return false, err
		}
		needed = needed || need
	}
	return needed, nil
}

// AIDEV-NOTE: reconcileTx owns a complete system op and finalizes its versions
// before the user savepoint is opened (A§4.4). Run all steps in D§5.8 order:
// an earlier step can create work for a later step in this same transaction.
func (s *Service) reconcileTx(ctx context.Context, tx *sql.Tx, now time.Time) error {
	need, err := s.needsReconcile(ctx, tx, now)
	if err != nil || !need {
		return err
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT reconcile`); err != nil {
		return store.MapErr(err)
	}
	o := s.newOp(ctx, tx, domain.SystemActor(), opSpec{kind: domain.OpSystemReconcile, class: classForward}, now)
	if err := store.InsertOp(ctx, tx, &o.rec); err != nil {
		return err
	}
	for _, step := range reconcileSteps {
		if err := step.run(o); err != nil {
			return err
		}
	}
	if err := o.checkInvariants(); err != nil {
		return err
	}
	if err := o.bumpVersions(); err != nil {
		return err
	}
	if len(o.events) == 0 {
		if _, err := tx.ExecContext(ctx, `ROLLBACK TO reconcile`); err != nil {
			return store.MapErr(err)
		}
	}
	_, err = tx.ExecContext(ctx, `RELEASE reconcile`)
	return store.MapErr(err)
}
