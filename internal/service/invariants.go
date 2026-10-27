package service

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/store"
)

// AIDEV-NOTE: Task 17 supplies structural and temporal invariant checks here;
// whole-store scans and touched-entity checks share the same rule implementations.
type invariantCheck struct {
	ctx        context.Context
	q          store.Q
	violations []string
	overlaps   map[[2]domain.BlockID]bool
}

func (c *invariantCheck) add(id, format string, args ...any) {
	c.violations = append(c.violations, "invariant "+id+" violated: "+fmt.Sprintf(format, args...))
}

func (c *invariantCheck) result() error {
	if len(c.violations) == 0 {
		return nil
	}
	slices.Sort(c.violations)
	return &domain.Error{Code: domain.CodeConflict, Detail: strings.Join(c.violations, "; ")}
}

// CheckStructural checks I1a, I2, I3, I4, I5, I7 and I8 across the whole store.
func CheckStructural(ctx context.Context, q store.Q) error {
	c := invariantCheck{ctx: ctx, q: q}
	ids, err := invariantIDs(ctx, q, `SELECT id FROM tasks ORDER BY id`)
	if err != nil {
		return serviceError(err)
	}
	for _, id := range ids {
		t, err := store.GetTask(ctx, q, domain.TaskID(id))
		if err != nil {
			return serviceError(err)
		}
		if err := c.task(t); err != nil {
			return serviceError(err)
		}
		if err := c.rank(t); err != nil {
			return serviceError(err)
		}
	}
	ids, err = invariantIDs(ctx, q, `SELECT id FROM time_blocks WHERE status = 'planned' AND deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return serviceError(err)
	}
	for _, id := range ids {
		b, err := store.GetBlock(ctx, q, domain.BlockID(id))
		if err != nil {
			return serviceError(err)
		}
		if err := c.block(b); err != nil {
			return serviceError(err)
		}
	}
	// AIDEV-NOTE: I8's historical kind immutability is enforced by the
	// log_entries_kind_fixed trigger; a state scan checks completion ownership.
	ids, err = invariantIDs(ctx, q, `SELECT id FROM log_entries WHERE kind = 'complete' AND task_id IS NULL ORDER BY id`)
	if err != nil {
		return serviceError(err)
	}
	for _, id := range ids {
		c.add("I8", "entry #%d is complete without a task", id)
	}
	return c.result()
}

// CheckTimeDependent checks I1b and I6 across the whole store at now in z.
func CheckTimeDependent(ctx context.Context, q store.Q, now time.Time, z civil.Zone) error {
	c := invariantCheck{ctx: ctx, q: q}
	ids, err := invariantIDs(ctx, q, `SELECT DISTINCT task_id FROM time_blocks WHERE status = 'planned' AND deleted_at IS NULL ORDER BY task_id`)
	if err != nil {
		return serviceError(err)
	}
	for _, id := range ids {
		t, err := store.GetTask(ctx, q, domain.TaskID(id))
		if err != nil {
			return serviceError(err)
		}
		c.deferred(t, now, z)
	}
	if err := c.focus(now, z); err != nil {
		return serviceError(err)
	}
	return c.result()
}

// CheckAll checks all structural and time-dependent invariants, joining violations.
func CheckAll(ctx context.Context, q store.Q, now time.Time, z civil.Zone) error {
	var details []string
	for _, check := range []func() error{
		func() error { return CheckStructural(ctx, q) },
		func() error { return CheckTimeDependent(ctx, q, now, z) },
	} {
		if err := check(); err != nil {
			de, ok := domain.AsError(err)
			if !ok || de.Code != domain.CodeConflict {
				return serviceError(err)
			}
			details = append(details, de.Detail)
		}
	}
	if len(details) == 0 {
		return nil
	}
	return &domain.Error{Code: domain.CodeConflict, Detail: strings.Join(details, "; ")}
}

// invariantIDs consumes and closes rows before callers issue more queries. This
// also permits whole-store checks on the single-connection writer or a transaction.
func invariantIDs(ctx context.Context, q store.Q, query string) ([]int64, error) {
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (c *invariantCheck) task(t domain.Task) error {
	// AIDEV-NOTE: I3 compares civil date/minute pairs without consulting now:
	// date-only defer is 00:00, while date-only due is 24:00 (D§5.7).
	var invalidDates, triage bool
	err := c.q.QueryRowContext(c.ctx, `SELECT
		defer_date IS NOT NULL AND due_date IS NOT NULL AND
		(defer_date > due_date OR (defer_date = due_date AND COALESCE(defer_min, 0) >= COALESCE(due_min, 1440))),
		EXISTS (SELECT 1 FROM task_triage WHERE task_id = tasks.id)
		FROM tasks WHERE id = ?`, t.ID).Scan(&invalidDates, &triage)
	if err != nil {
		return err
	}
	if invalidDates {
		c.add("I3", "task #%d defer must precede due and surface on or before its due date", t.ID)
	}
	if triage && (t.Status != domain.StatusOpen || t.DeletedAt.Valid) {
		c.add("I5", "task #%d has triage rows but is closed or deleted", t.ID)
	}
	if t.DeletedAt.Valid {
		return nil
	}
	entries, err := store.CompleteEntries(c.ctx, c.q, t.ID)
	if err != nil {
		return err
	}
	var live, purged int
	for _, e := range entries {
		if !e.DeletedAt.Valid && !e.Reopened {
			live++
		}
		if e.PurgedAt.Valid {
			purged++
		}
	}
	if t.Status == domain.StatusDone {
		if live > 1 || (live == 0 && purged == 0) {
			c.add("I4", "task #%d is done with %d live unreopened and %d purged completion entries", t.ID, live, purged)
		}
	} else if live != 0 {
		c.add("I4", "task #%d is open or dropped with %d live unreopened completion entries", t.ID, live)
	}
	return nil
}

func (c *invariantCheck) rank(t domain.Task) error {
	taken, err := store.RankTaken(c.ctx, c.q, t.ID, t.Rank)
	if err != nil {
		return err
	}
	if taken {
		c.add("I7", "task #%d shares rank %d with another task", t.ID, t.Rank)
	}
	return nil
}

func (c *invariantCheck) block(b domain.TimeBlock) error {
	if b.DeletedAt.Valid || b.Status != domain.BlockPlanned {
		return nil
	}
	t, err := store.GetTask(c.ctx, c.q, b.TaskID)
	if err != nil {
		return err
	}
	if t.DeletedAt.Valid || t.Status != domain.StatusOpen {
		c.add("I1a", "block #%d is planned on closed or deleted task #%d", b.ID, t.ID)
	}
	others, err := store.PlannedOverlapping(c.ctx, c.q, b.Span, b.ID)
	if err != nil {
		return err
	}
	if c.overlaps == nil {
		c.overlaps = make(map[[2]domain.BlockID]bool)
	}
	for _, other := range others {
		pair := [2]domain.BlockID{min(b.ID, other.ID), max(b.ID, other.ID)}
		if !c.overlaps[pair] {
			c.add("I2", "planned blocks #%d and #%d overlap", pair[0], pair[1])
			c.overlaps[pair] = true
		}
	}
	return nil
}

func (c *invariantCheck) deferred(t domain.Task, now time.Time, z civil.Zone) {
	if t.DeferDate.Valid && z.DeferMoment(t.DeferDate.V, t.DeferMin).After(now) {
		c.add("I1b", "task #%d has planned blocks while deferred", t.ID)
	}
}

func (c *invariantCheck) focus(now time.Time, z civil.Zone) error {
	f, err := store.GetFocus(c.ctx, c.q)
	if err != nil || !f.TaskID.Valid {
		return err
	}
	t, err := store.GetTask(c.ctx, c.q, f.TaskID.V)
	if err != nil {
		return err
	}
	if !f.Date.Valid || f.Date.V != z.DateOf(now) || t.Status != domain.StatusOpen || t.DeletedAt.Valid ||
		(t.DeferDate.Valid && z.DeferMoment(t.DeferDate.V, t.DeferMin).After(now)) {
		c.add("I6", "focus task #%d must be open, non-deleted, non-deferred and dated today", t.ID)
	}
	return nil
}

func (o *op) checkInvariants() error {
	c := invariantCheck{ctx: o.ctx, q: o.tx}
	tasks := make(map[domain.TaskID]bool)
	blocks := make(map[domain.BlockID]domain.TimeBlock)
	// AIDEV-NOTE: Dirty entries/blocks imply a touched parent task even when
	// that task's version is unchanged. Only rankMoved tasks need I7 queries.
	for ref, dirty := range o.dirty {
		if !dirty {
			continue
		}
		switch ref.Entity {
		case domain.EntTask:
			tasks[domain.TaskID(ref.ID)] = true
		case domain.EntBlock:
			b, err := store.GetBlock(o.ctx, o.tx, domain.BlockID(ref.ID))
			if err != nil {
				return err
			}
			blocks[b.ID], tasks[b.TaskID] = b, true
		case domain.EntEntry:
			e, err := store.GetEntry(o.ctx, o.tx, domain.EntryID(ref.ID))
			if err != nil {
				return err
			}
			if e.TaskID.Valid {
				tasks[e.TaskID.V] = true
			} else if e.Kind == domain.EntryComplete {
				c.add("I8", "entry #%d is complete without a task", e.ID)
			}
		default:
		}
	}
	for _, id := range slices.Sorted(maps.Keys(tasks)) {
		t, err := store.GetTask(o.ctx, o.tx, id)
		if err != nil {
			return err
		}
		if err := c.task(t); err != nil {
			return err
		}
		planned, err := store.PlannedOfTask(o.ctx, o.tx, id)
		if err != nil {
			return err
		}
		for _, b := range planned {
			blocks[b.ID] = b
		}
		if !o.spec.structuralOnly && len(planned) > 0 {
			c.deferred(t, o.now, o.s.zone)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(blocks)) {
		if err := c.block(blocks[id]); err != nil {
			return err
		}
	}
	for _, id := range slices.Sorted(maps.Keys(o.rankMoved)) {
		if !o.rankMoved[id] {
			continue
		}
		t, err := store.GetTask(o.ctx, o.tx, id)
		if err != nil {
			return err
		}
		if err := c.rank(t); err != nil {
			return err
		}
	}
	if !o.spec.structuralOnly {
		if err := c.focus(o.now, o.s.zone); err != nil {
			return err
		}
	}
	return c.result()
}
