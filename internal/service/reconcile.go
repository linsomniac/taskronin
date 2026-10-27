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

// Surface precedes due so a just-surfaced task receives both reasons in one op.
var reconcileSteps = []reconcileStep{
	{name: "surface", need: needSurface, run: (*op).reconcileSurface},
	{name: "due", need: needDue, run: (*op).reconcileDue},
	{name: "overdue", need: needOverdue, run: (*op).reconcileOverdue},
	{name: "resolve", need: needResolve, run: (*op).reconcileBlocks},
	{name: "focus", need: needFocus, run: (*op).reconcileFocus},
}

func needSurface(ctx context.Context, q store.Q, now time.Time, today civil.Date) (bool, error) {
	z, err := civil.LoadZone(now.Location().String())
	if err != nil {
		return false, err
	}
	ids, err := surfaceTasks(ctx, q, now, today, z, true)
	return len(ids) > 0, err
}

func (o *op) reconcileSurface() error {
	ids, err := surfaceTasks(o.ctx, o.tx, o.now, o.today, o.s.zone, false)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := o.setDefer(id, nil); err != nil {
			return err
		}
		if err := o.addReason(id, domain.ReasonSurfaced); err != nil {
			return err
		}
	}
	return nil
}

func surfaceTasks(ctx context.Context, q store.Q, now time.Time, today civil.Date, z civil.Zone, firstOnly bool) ([]domain.TaskID, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, defer_date, defer_min FROM tasks
		WHERE status = 'open' AND deleted_at IS NULL AND defer_date <= ? ORDER BY id`, today)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []domain.TaskID
	for rows.Next() {
		var id domain.TaskID
		var date civil.Date
		var minute sql.Null[civil.Minute]
		if err := rows.Scan(&id, &date, &minute); err != nil {
			return nil, err
		}
		// AIDEV-NOTE: The date filter only narrows candidates. Civil instants
		// handle timed defers, DST gaps and folds in both need and run paths.
		if !z.DeferMoment(date, minute).After(now) {
			ids = append(ids, id)
			if firstOnly {
				break
			}
		}
	}
	return ids, rows.Err()
}

func needFocus(ctx context.Context, q store.Q, _ time.Time, today civil.Date) (bool, error) {
	var needed bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM focus f JOIN tasks t ON t.id = f.task_id
		WHERE f.date != ? OR t.status != 'open' OR t.deleted_at IS NOT NULL OR t.defer_date IS NOT NULL)`, today).Scan(&needed)
	return needed, err
}

func (o *op) reconcileFocus() error {
	needed, err := needFocus(o.ctx, o.tx, o.now, o.today)
	if err != nil || !needed {
		return err
	}
	return o.setFocus(sql.Null[domain.TaskID]{})
}

func needResolve(ctx context.Context, q store.Q, _ time.Time, today civil.Date) (bool, error) {
	var needed bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM time_blocks
		WHERE deleted_at IS NULL AND status = 'planned' AND date < ?)`, today).Scan(&needed)
	return needed, err
}

func (o *op) reconcileBlocks() error {
	blocks, err := store.PlannedBefore(o.ctx, o.tx, o.today)
	if err != nil {
		return err
	}
	for _, b := range blocks {
		if err := o.resolveByDerivation(&b); err != nil {
			return err
		}
	}
	return nil
}

func needDue(ctx context.Context, q store.Q, _ time.Time, today civil.Date) (bool, error) {
	ids, err := dueTasks(ctx, q, today, 1)
	return len(ids) > 0, err
}

func (o *op) reconcileDue() error {
	ids, err := dueTasks(o.ctx, o.tx, o.today, -1)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := o.addReason(id, domain.ReasonDue); err != nil {
			return err
		}
	}
	return nil
}

func dueTasks(ctx context.Context, q store.Q, today civil.Date, limit int) ([]domain.TaskID, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM tasks
		WHERE status = 'open' AND deleted_at IS NULL AND defer_date IS NULL
		AND due_date <= ? AND due_ack = 'none'
		AND NOT EXISTS (SELECT 1 FROM task_triage WHERE task_id = tasks.id AND reason = 'due')
		AND NOT EXISTS (SELECT 1 FROM time_blocks WHERE task_id = tasks.id
			AND date = ? AND status = 'planned' AND deleted_at IS NULL)
		ORDER BY id LIMIT ?`, today, today, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []domain.TaskID
	for rows.Next() {
		var id domain.TaskID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func needOverdue(ctx context.Context, q store.Q, now time.Time, today civil.Date) (bool, error) {
	z, err := civil.LoadZone(now.Location().String())
	if err != nil {
		return false, err
	}
	ids, err := overdueTasks(ctx, q, now, today, z, true)
	return len(ids) > 0, err
}

func (o *op) reconcileOverdue() error {
	ids, err := overdueTasks(o.ctx, o.tx, o.now, o.today, o.s.zone, false)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := o.addReason(id, domain.ReasonOverdue); err != nil {
			return err
		}
	}
	return nil
}

func overdueTasks(ctx context.Context, q store.Q, now time.Time, today civil.Date, z civil.Zone, firstOnly bool) ([]domain.TaskID, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, due_date, due_min FROM tasks
		WHERE status = 'open' AND deleted_at IS NULL AND due_date <= ?
		AND NOT EXISTS (SELECT 1 FROM task_triage WHERE task_id = tasks.id AND reason = 'overdue')
		AND (due_ack != 'overdue' OR due_ack_date IS NULL OR due_ack_date != ?)
		ORDER BY id`, today, today)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []domain.TaskID
	for rows.Next() {
		var id domain.TaskID
		var date civil.Date
		var minute sql.Null[civil.Minute]
		if err := rows.Scan(&id, &date, &minute); err != nil {
			return nil, err
		}
		// AIDEV-NOTE: SQL narrows the candidates; only civil instants decide
		// overdue, including date-only midnight and the earlier DST fold.
		if !z.DueMoment(date, minute).After(now) {
			ids = append(ids, id)
			if firstOnly {
				break
			}
		}
	}
	return ids, rows.Err()
}

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
	// AIDEV-NOTE: Step need callbacks receive the configured IANA location
	// through now, regardless of the clock's location. The instant is unchanged.
	// This preserves their shared interface while allowing civil moment checks.
	now = now.In(s.zone.Location())
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
