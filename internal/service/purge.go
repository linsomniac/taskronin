package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/linsomniac/taskronin/internal/backup"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/store"
)

// ReplayMode selects whether ledger replay reconciles time-dependent state.
type ReplayMode uint8

const (
	// ReplayLive reconciles the live database before replaying purge intents.
	ReplayLive ReplayMode = iota + 1
	// ReplayHistorical preserves a snapshot's point-in-time state.
	ReplayHistorical ReplayMode = iota + 1
)

// PurgeLiveIn identifies an offline purge target and whether to include all linked entries.
type PurgeLiveIn struct {
	Target      string // "task" or "entry"
	ID          int64
	WithEntries bool
}

// PurgeLiveResult reports the selected set, excluded linked entries and any committed change.
type PurgeLiveResult struct {
	TaskIDs, EntryIDs, LinkedNotIncluded []int64
	Changed                              bool
}

type purgeSet struct {
	tasks   []domain.TaskID
	entries []domain.EntryID
	dirty   []domain.EntityRef // Only rows whose content has not already been scrubbed.
}

// ReplayLedger applies the union of valid ledger records as one system operation.
// Missing IDs are skipped. Historical replay cancels target tasks' planned blocks
// without deriving outcomes or reconciling unrelated historical state.
func (s *Service) ReplayLedger(ctx context.Context, recs []backup.LedgerRecord, mode ReplayMode) (*OpResult, error) {
	if mode != ReplayLive && mode != ReplayHistorical {
		return nil, validation("mode", errors.New("must be live or historical"))
	}
	return s.run(ctx, s.systemMeta(), opSpec{kind: domain.OpSystemLedgerReplay, class: classForward,
		noReconcile: mode == ReplayHistorical, structuralOnly: mode == ReplayHistorical}, func(o *op) error {
		var tasks []domain.TaskID
		var entries []domain.EntryID
		for _, rec := range recs {
			tasks = append(tasks, rec.Tasks...)
			entries = append(entries, rec.Entries...)
		}
		var set purgeSet
		for _, id := range purgeIDs(tasks) {
			t, err := store.GetTask(o.ctx, o.tx, id)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			set.addTask(t)
			if !t.DeletedAt.Valid {
				if err := o.deleteForReplay(&t); err != nil {
					return err
				}
			}
		}
		for _, id := range purgeIDs(entries) {
			e, err := store.GetEntry(o.ctx, o.tx, id)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			set.addEntry(e)
		}
		_, err := o.scrubPurgeSet(set, false)
		return err
	})
}

// ReapplyLedger replays the configured ledger in live mode without appending records.
func (s *Service) ReapplyLedger(ctx context.Context) error {
	if s.ledger == nil {
		return domain.Errorf(domain.CodeInternal, "purge ledger is not configured")
	}
	recs, err := s.ledger.Records()
	if err != nil {
		return serviceError(fmt.Errorf("read purge ledger: %w", err))
	}
	_, err = s.ReplayLedger(ctx, recs, ReplayLive)
	return err
}

// PurgeLive durably records an intent and scrubs its exact set in one writer transaction.
// The offline caller holds the instance lock and verifies the owner's password;
// it must finish checkpointing, backup scrubbing and recovery-file cleanup afterwards.
func (s *Service) PurgeLive(ctx context.Context, m Meta, in PurgeLiveIn) (PurgeLiveResult, error) {
	kind := domain.OpPurgeTask
	switch in.Target {
	case "task":
	case "entry":
		kind = domain.OpPurgeEntry
	default:
		return PurgeLiveResult{}, validation("target", errors.New("must be task or entry"))
	}
	var result PurgeLiveResult
	_, err := s.run(ctx, m, opSpec{kind: kind, class: classForward, scopes: domain.ScopeDestroy,
		ownerOnly: true, notWeb: true}, func(o *op) error {
		set, linked, err := o.selectPurgeSet(in)
		if err != nil {
			return err
		}
		result.LinkedNotIncluded = linked
		for _, id := range set.tasks {
			result.TaskIDs = append(result.TaskIDs, int64(id))
		}
		for _, id := range set.entries {
			result.EntryIDs = append(result.EntryIDs, int64(id))
		}
		if s.ledger == nil {
			return domain.Errorf(domain.CodeInternal, "purge ledger is not configured")
		}
		recs, err := s.ledger.Records()
		if err != nil {
			return fmt.Errorf("read purge ledger: %w", err)
		}
		if len(set.dirty) == 0 {
			for _, rec := range recs {
				if slices.Equal(purgeIDs(rec.Tasks), set.tasks) && slices.Equal(purgeIDs(rec.Entries), set.entries) {
					return nil
				}
			}
		}
		// AIDEV-NOTE: Ledger first, under the writer lock: an append can survive
		// rollback, but no scrub may commit without its durable recovery intent.
		if err := s.ledger.Append(backup.LedgerRecord{V: 1, Rec: domain.NewOpID().String(), At: o.now.UnixMilli(),
			State: backup.LedgerPending, Tasks: set.tasks, Entries: set.entries}); err != nil {
			return fmt.Errorf("append purge intent: %w", err)
		}
		if s.purgeAfterAppendHook != nil {
			if err := s.purgeAfterAppendHook(); err != nil {
				return err
			}
		}
		result.Changed, err = o.scrubPurgeSet(set, true)
		return err
	})
	if err != nil {
		return PurgeLiveResult{}, err
	}
	return result, nil
}

func (o *op) selectPurgeSet(in PurgeLiveIn) (purgeSet, []int64, error) {
	var set purgeSet
	if in.Target == "entry" {
		e, err := o.loadEntry(domain.EntryID(in.ID), nil, true)
		if err != nil {
			return set, nil, err
		}
		if !e.DeletedAt.Valid {
			if e.TaskID.Valid {
				t, err := o.loadTask(e.TaskID.V, nil, true)
				if err != nil {
					return set, nil, err
				}
				if t.DeletedAt.Valid {
					return set, nil, domain.Errorf(domain.CodeConflict, "delete it first; use tkr purge task %d --with-entries", t.ID)
				}
			}
			return set, nil, domain.Errorf(domain.CodeConflict, "delete it first")
		}
		set.addEntry(e)
		return set, nil, nil
	}
	t, err := o.loadTask(domain.TaskID(in.ID), nil, true)
	if err != nil {
		return set, nil, err
	}
	if !t.DeletedAt.Valid {
		return set, nil, domain.Errorf(domain.CodeConflict, "delete it first")
	}
	set.addTask(t)
	task := sql.Null[domain.TaskID]{V: t.ID, Valid: true}
	entries, err := store.ListEntries(o.ctx, o.tx, store.EntryQuery{Task: &task, IncludeDeleted: true, Limit: -1})
	if err != nil {
		return set, nil, err
	}
	var linked []int64
	for _, e := range entries {
		if in.WithEntries || e.Kind == domain.EntryComplete {
			set.addEntry(e)
		} else {
			linked = append(linked, int64(e.ID))
		}
	}
	slices.Sort(set.entries)
	slices.Sort(linked)
	return set, linked, nil
}

func (set *purgeSet) addTask(t domain.Task) {
	set.tasks = append(set.tasks, t.ID)
	if !t.PurgedAt.Valid {
		set.dirty = append(set.dirty, domain.EntityRef{Entity: domain.EntTask, ID: int64(t.ID)})
	}
}

func (set *purgeSet) addEntry(e domain.LogEntry) {
	set.entries = append(set.entries, e.ID)
	if !e.PurgedAt.Valid {
		set.dirty = append(set.dirty, domain.EntityRef{Entity: domain.EntEntry, ID: int64(e.ID)})
	}
}

func purgeIDs[T ~int64](ids []T) []T {
	ids = slices.Clone(ids)
	slices.Sort(ids)
	return slices.Compact(ids)
}

func (o *op) deleteForReplay(t *domain.Task) error {
	blocks, err := store.PlannedOfTask(o.ctx, o.tx, t.ID)
	if err != nil {
		return err
	}
	unslipped := false
	for _, b := range blocks {
		if err := o.setBlock(&b, blockChange{Status: domain.BlockCancelled, Cancel: domain.CancelTaskClosed, Slipped: &unslipped}); err != nil {
			return err
		}
	}
	if err := o.clearReasons(t.ID); err != nil {
		return err
	}
	if err := o.clearFocusIf(t.ID); err != nil {
		return err
	}
	return o.setTaskDeleted(t, true)
}

func (o *op) scrubPurgeSet(set purgeSet, newIntent bool) (bool, error) {
	result, err := store.ScrubEntities(o.ctx, o.tx, set.tasks, set.entries, o.now)
	// A newly appended exact intent gets its audit event even if a previous
	// superset already scrubbed the rows. Replay records only actual changes.
	if err != nil || (!result.Changed() && !newIntent) {
		return false, err
	}
	// AIDEV-NOTE: Purge does not add recompute pairs. Resolved block outcomes
	// are historical facts even after their supporting entries are scrubbed.
	for _, ref := range set.dirty {
		o.markDirty(ref)
	}
	ids := make([]int64, 0, len(set.tasks)+len(set.entries))
	for _, id := range set.tasks {
		ids = append(ids, int64(id))
	}
	for _, id := range set.entries {
		ids = append(ids, int64(id))
	}
	entity := domain.EntEntry
	if len(set.tasks) != 0 {
		entity = domain.EntTask
	}
	after, err := json.Marshal(map[string]any{"ids": ids})
	if err != nil {
		return false, err
	}
	if err := o.record(domain.Event{Entity: entity, EntityID: ids[0], Action: domain.ActPurge, After: after}); err != nil {
		return false, err
	}
	// AIDEV-NOTE: Include the newly recorded purge op in the store's purge
	// marking. Otherwise every replay would mark its predecessor and emit a
	// new event forever. Rows/content are already scrubbed; this seals audit only.
	_, err = store.ScrubEntities(o.ctx, o.tx, set.tasks, set.entries, o.now)
	return err == nil, err
}
