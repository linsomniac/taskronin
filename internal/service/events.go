package service

import (
	"database/sql"
	"encoding/json"
	"slices"

	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/store"
)

func (o *op) record(e domain.Event) error {
	e.OpID, e.Derived = o.rec.ID, o.derived
	if err := o.eventTaskID(&e); err != nil {
		return err
	}
	if err := store.InsertEvent(o.ctx, o.tx, &e); err != nil {
		return err
	}
	o.events = append(o.events, e)
	return nil
}

func (o *op) eventTaskID(e *domain.Event) error {
	if e.Action == domain.ActPurge && e.EntityID == 0 {
		// Aggregate purge events identify their rows in the ids payload.
		e.TaskID = sql.Null[domain.TaskID]{}
		return nil
	}
	switch e.Entity {
	case domain.EntTask, domain.EntTriage, domain.EntTaskTags:
		e.TaskID = sql.Null[domain.TaskID]{V: domain.TaskID(e.EntityID), Valid: e.EntityID != 0}
	case domain.EntFocus:
		var after, before struct {
			TaskID *domain.TaskID `json:"task_id"`
		}
		if len(e.After) > 0 {
			if err := json.Unmarshal(e.After, &after); err != nil {
				return err
			}
		}
		if len(e.Before) > 0 {
			if err := json.Unmarshal(e.Before, &before); err != nil {
				return err
			}
		}
		e.TaskID = sql.Null[domain.TaskID]{}
		if after.TaskID != nil {
			e.TaskID = sql.Null[domain.TaskID]{V: *after.TaskID, Valid: true}
		} else if before.TaskID != nil {
			e.TaskID = sql.Null[domain.TaskID]{V: *before.TaskID, Valid: true}
		}
	case domain.EntEntry:
		entry, err := store.GetEntry(o.ctx, o.tx, domain.EntryID(e.EntityID))
		if err != nil {
			return err
		}
		e.TaskID = entry.TaskID
	case domain.EntBlock:
		block, err := store.GetBlock(o.ctx, o.tx, domain.BlockID(e.EntityID))
		if err != nil {
			return err
		}
		e.TaskID = sql.Null[domain.TaskID]{V: block.TaskID, Valid: true}
	case domain.EntInterruption:
		interruption, err := store.GetInterruption(o.ctx, o.tx, domain.InterruptionID(e.EntityID))
		if err != nil {
			return err
		}
		e.TaskID = sql.Null[domain.TaskID]{V: interruption.ToTaskID, Valid: true}
	case domain.EntSetting, domain.EntToken, domain.EntAuth:
		e.TaskID = sql.Null[domain.TaskID]{}
	default:
	}
	return nil
}

func (o *op) recordTaskUpdate(before, after domain.Task) error {
	b, a := taskCols(before), taskCols(after)
	delete(b, "rank")
	delete(a, "rank")
	return o.recordUpdate(domain.EntTask, int64(after.ID), b, a)
}

func (o *op) recordEntryUpdate(before, after domain.LogEntry) error {
	return o.recordUpdate(domain.EntEntry, int64(after.ID), entryCols(before), entryCols(after))
}

func (o *op) recordUpdate(entity domain.EntityKind, id int64, before, after map[string]any) error {
	b, a := diffCols(before, after)
	if len(a) == 0 {
		return nil
	}
	bj, err := json.Marshal(b)
	if err != nil {
		return err
	}
	aj, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if err := o.record(domain.Event{Entity: entity, EntityID: id, Action: domain.ActUpdate, Before: bj, After: aj}); err != nil {
		return err
	}
	o.markDirty(domain.EntityRef{Entity: entity, ID: id})
	return nil
}

func (o *op) markDirty(ref domain.EntityRef) { o.dirty[ref] = true }
func (o *op) markTaskDirty(id domain.TaskID) {
	o.markDirty(domain.EntityRef{Entity: domain.EntTask, ID: int64(id)})
}
func (o *op) markInserted(ref domain.EntityRef) { o.inserted[ref] = true }
func (o *op) addPair(task sql.Null[domain.TaskID], d civil.Date) {
	if task.Valid {
		o.pairs[pairKey{Task: task.V, Date: d}] = struct{}{}
	}
}
func (o *op) ref(r domain.EntityRef) {
	if !slices.Contains(o.refs, r) {
		o.refs = append(o.refs, r)
	}
}
func (o *op) warn(msg string) { o.warnings = append(o.warnings, msg) }
