package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"time"

	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/store"
)

// RevertIn identifies the operation to reverse.
type RevertIn struct{ OpID domain.OpID }

// Revert reverses one operation and records a new undoable operation.
func (s *Service) Revert(ctx context.Context, m Meta, in RevertIn) (*OpResult, error) {
	return s.run(ctx, m, opSpec{kind: domain.OpRevert, class: classRevert, idem: true, revertible: true, targetID: in.OpID}, func(o *op) error {
		return o.revertTx(*o.target)
	})
}

type reversalChecks struct {
	unslipped, unworked map[domain.TaskID]bool
	removed             []store.EventKey
}

// AIDEV-NOTE: Conflict keys exclude derived events and focus. Later events still
// conflict unless their ops form net-zero reversal pairs. Derived images are
// never replayed: entry pairs are recomputed against the restored live state.
func (o *op) revertTx(target domain.OpRecord) error {
	if !target.Revertible {
		return domain.Errorf(domain.CodeNotRevertible, "operation is not revertible")
	}
	if target.State != domain.OpActive {
		// AIDEV-NOTE: The runner has already inserted this attempted reversal.
		// Exclude it when naming the actor of the previous successful reversal.
		var actor string
		if err := o.tx.QueryRowContext(o.ctx, `SELECT actor FROM ops
			WHERE reverts_op_id = ? AND state = 'active' AND seq < ?
			ORDER BY seq DESC LIMIT 1`, target.ID, o.rec.Seq).Scan(&actor); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// No active earlier reversal: report a conflict, not not_found.
				return domain.Errorf(domain.CodeRevertConflict, "operation is not active")
			}
			return err
		}
		return domain.Errorf(domain.CodeRevertConflict, "already reverted by %s", actor)
	}
	events, err := store.EventsOfOp(o.ctx, o.tx, target.ID)
	if err != nil {
		return err
	}
	if err := o.revertConflicts(events); err != nil {
		return err
	}
	if err := o.revertBlockPrecheck(events); err != nil {
		return err
	}
	checks := reversalChecks{unslipped: make(map[domain.TaskID]bool), unworked: make(map[domain.TaskID]bool)}
	var focus []domain.Event
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Derived {
			if e.Entity == domain.EntBlock {
				b, err := store.GetBlock(o.ctx, o.tx, domain.BlockID(e.EntityID))
				if err != nil {
					return err
				}
				o.addPair(sql.Null[domain.TaskID]{V: b.TaskID, Valid: true}, b.Span.Date)
			}
			continue
		}
		if e.Entity == domain.EntFocus {
			focus = append(focus, e)
			continue
		}
		if err := o.revertEvent(e, &checks); err != nil {
			return err
		}
	}
	if err := o.revertFocus(focus); err != nil {
		return err
	}
	if err := o.recompute(); err != nil {
		return err
	}
	// AIDEV-NOTE: Restore bypasses transition hooks. Run R4-6 removal checks
	// after all writes/recomputation, then P-2's converse for removed additions.
	for _, id := range slices.Sorted(maps.Keys(checks.unslipped)) {
		if err := o.afterUnslip(id); err != nil {
			return err
		}
	}
	for _, id := range slices.Sorted(maps.Keys(checks.unworked)) {
		if err := o.afterUnwork(id); err != nil {
			return err
		}
	}
	for _, key := range checks.removed {
		justified, err := o.reasonJustified(domain.TaskID(key.ID), key.Reason)
		if err != nil {
			return err
		}
		if justified {
			if err := o.addReason(domain.TaskID(key.ID), key.Reason); err != nil {
				return err
			}
		}
	}
	if err := o.checkInvariants(); err != nil {
		return err
	}
	state := domain.OpReverted
	for {
		if err := store.SetOpState(o.ctx, o.tx, target.ID, state); err != nil {
			return err
		}
		if !target.RevertsOpID.Valid {
			break
		}
		target, err = store.GetOp(o.ctx, o.tx, target.RevertsOpID.V)
		if err != nil {
			return err
		}
		if state == domain.OpReverted {
			state = domain.OpActive
		} else {
			state = domain.OpReverted
		}
	}
	return nil
}

func revertKey(e domain.Event) (store.EventKey, error) {
	k := store.EventKey{Entity: e.Entity, ID: e.EntityID}
	if e.Entity == domain.EntTriage {
		image := e.Before
		if e.Action == domain.ActAdd {
			image = e.After
		}
		var row struct {
			Reason domain.TriageReason `json:"reason"`
		}
		if err := json.Unmarshal(image, &row); err != nil {
			return k, err
		}
		k.Reason = row.Reason
	}
	return k, nil
}

func (o *op) revertConflicts(events []domain.Event) error {
	var maxID domain.EventID
	for _, e := range events {
		if e.ID > maxID {
			maxID = e.ID
		}
	}
	keys := make(map[store.EventKey]bool)
	conflicts := make([]domain.RevertConflict, 0)
	seen := make(map[struct {
		op     domain.OpID
		entity domain.EntityKind
		id     int64
	}]bool)
	for _, e := range events {
		if e.Derived || e.Entity == domain.EntFocus {
			continue
		}
		k, err := revertKey(e)
		if err != nil {
			return err
		}
		if keys[k] {
			continue
		}
		keys[k] = true
		later, err := store.EventsAfter(o.ctx, o.tx, k, maxID)
		if err != nil {
			return err
		}
		for _, change := range later {
			zero, err := o.netZeroOp(change.Op)
			if err != nil {
				return err
			}
			if zero {
				continue
			}
			key := struct {
				op     domain.OpID
				entity domain.EntityKind
				id     int64
			}{change.Op.ID, k.Entity, k.ID}
			if seen[key] {
				continue
			}
			seen[key] = true
			conflicts = append(conflicts, domain.RevertConflict{OpID: change.Op.ID, Kind: change.Op.Kind, Actor: change.Op.Actor, At: change.Op.At, Entity: k.Entity, EntityID: k.ID})
		}
	}
	if len(conflicts) > 0 {
		return &domain.Error{Code: domain.CodeRevertConflict, Detail: "later operations changed the same entities", Conflicts: conflicts}
	}
	return nil
}

func (o *op) netZeroOp(r domain.OpRecord) (bool, error) {
	if r.State == domain.OpReverted {
		reversal, err := store.ActiveReversal(o.ctx, o.tx, r.ID)
		return reversal.Valid, err
	}
	if r.RevertsOpID.Valid {
		target, err := store.GetOp(o.ctx, o.tx, r.RevertsOpID.V)
		if err != nil {
			return false, err
		}
		return target.State == domain.OpReverted, nil
	}
	return false, nil
}

func eventColumns(image json.RawMessage) (map[string]any, error) {
	cols := make(map[string]any)
	if len(image) == 0 {
		return cols, nil
	}
	dec := json.NewDecoder(bytes.NewReader(image))
	dec.UseNumber()
	err := dec.Decode(&cols)
	return cols, err
}

func (o *op) revertBlockPrecheck(events []domain.Event) error {
	touched := make([]domain.BlockID, 0)
	for _, e := range events {
		if !e.Derived && e.Entity == domain.EntBlock {
			touched = append(touched, domain.BlockID(e.EntityID))
		}
	}
	for _, e := range events {
		if e.Derived || e.Entity != domain.EntBlock || e.Action == domain.ActInsert {
			continue
		}
		b, err := store.GetBlock(o.ctx, o.tx, domain.BlockID(e.EntityID))
		if err != nil {
			return err
		}
		// Images contain changed columns only: merge with the current block for
		// status-only changes and reschedules whose status remains planned.
		cols := blockCols(b)
		before, err := eventColumns(e.Before)
		if err != nil {
			return err
		}
		maps.Copy(cols, before)
		if cols["status"] != string(domain.BlockPlanned) || cols["deleted_at"] != nil {
			continue
		}
		var image struct {
			Date     civil.Date `json:"date"`
			StartMin int64      `json:"start_min"`
			EndMin   int64      `json:"end_min"`
		}
		raw, err := json.Marshal(cols)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &image); err != nil {
			return err
		}
		// Stored event minutes satisfy the schema's 0..1440 bounds.
		var start civil.Minute
		if err := start.Scan(image.StartMin); err != nil {
			return err
		}
		var end civil.EndMinute
		if err := end.Scan(image.EndMin); err != nil {
			return err
		}
		overlap, err := store.PlannedOverlapping(o.ctx, o.tx, domain.BlockSpan{Date: image.Date, Start: start, End: end}, touched...)
		if err != nil {
			return err
		}
		if len(overlap) > 0 {
			return domain.Errorf(domain.CodeConflict, "restoring block #%d would overlap block #%d", b.ID, overlap[0].ID)
		}
	}
	return nil
}

func (o *op) revertEvent(e domain.Event, checks *reversalChecks) error {
	switch e.Entity {
	case domain.EntTriage:
		k, err := revertKey(e)
		if err != nil {
			return err
		}
		if e.Action == domain.ActAdd {
			if err := o.removeReason(domain.TaskID(e.EntityID), k.Reason); err != nil {
				return err
			}
			if k.Reason == domain.ReasonSlipped || k.Reason == domain.ReasonContinue {
				checks.removed = append(checks.removed, k)
			}
			return nil
		}
		var image struct {
			Reason domain.TriageReason `json:"reason"`
			Since  int64               `json:"since"`
			Seq    domain.OpSeq        `json:"seq"`
		}
		if err := json.Unmarshal(e.Before, &image); err != nil {
			return err
		}
		added, err := store.AddReason(o.ctx, o.tx, domain.TriageRow{TaskID: domain.TaskID(e.EntityID), Reason: image.Reason, Since: time.UnixMilli(image.Since), Seq: image.Seq})
		if err != nil || !added {
			return err
		}
		o.markTaskDirty(domain.TaskID(e.EntityID))
		return o.record(domain.Event{Entity: e.Entity, EntityID: e.EntityID, Action: domain.ActAdd, After: e.Before})
	case domain.EntTaskTags:
		var image struct {
			Tags []string `json:"tags"`
		}
		if len(e.Before) > 0 {
			if err := json.Unmarshal(e.Before, &image); err != nil {
				return err
			}
		}
		current, err := store.TaskTags(o.ctx, o.tx, domain.TaskID(e.EntityID))
		if err != nil {
			return err
		}
		if err := store.SetTaskTags(o.ctx, o.tx, domain.TaskID(e.EntityID), image.Tags, o.now); err != nil {
			return err
		}
		before, err := json.Marshal(map[string]any{"tags": append([]string{}, current...)})
		if err != nil {
			return err
		}
		after, err := json.Marshal(map[string]any{"tags": append([]string{}, image.Tags...)})
		if err != nil {
			return err
		}
		o.markTaskDirty(domain.TaskID(e.EntityID))
		return o.record(domain.Event{Entity: e.Entity, EntityID: e.EntityID, Action: domain.ActSet, Before: before, After: after})
	case domain.EntTask:
		if e.Action == domain.ActMove {
			var image struct {
				AfterID *domain.TaskID `json:"after_id"`
			}
			if err := json.Unmarshal(e.Before, &image); err != nil {
				return err
			}
			t, err := store.GetTask(o.ctx, o.tx, domain.TaskID(e.EntityID))
			if err != nil {
				return err
			}
			var rank int64
			if image.AfterID == nil {
				rank, err = o.rankTop()
			} else {
				rank, err = o.rankBeside(*image.AfterID, false)
			}
			if err != nil {
				return err
			}
			return o.moveTask(&t, rank)
		}
	default:
		// Row-backed entities use column restoration below.
	}
	cols, err := eventColumns(e.Before)
	if err != nil {
		return err
	}
	action := domain.ActUpdate
	switch e.Action {
	case domain.ActInsert:
		cols = map[string]any{"deleted_at": o.now.UnixMilli()}
		action = domain.ActTombstone
	case domain.ActTombstone:
		action = domain.ActUntombstone
	case domain.ActUntombstone:
		action = domain.ActTombstone
	default:
		// Ordinary updates restore their before-image.
	}
	if e.Entity == domain.EntBlock {
		b, err := store.GetBlock(o.ctx, o.tx, domain.BlockID(e.EntityID))
		if err != nil {
			return err
		}
		before := b
		if err := o.setBlock(&b, blockChange{Restore: true, Image: cols}); err != nil {
			return err
		}
		if before.Slipped && (!b.Slipped || b.DeletedAt.Valid) {
			checks.unslipped[b.TaskID] = true
		}
		if before.Status == domain.BlockWorked && (b.Status != domain.BlockWorked || b.DeletedAt.Valid) {
			checks.unworked[b.TaskID] = true
		}
		return nil
	}
	before, err := o.revertColumns(e.Entity, e.EntityID)
	if err != nil {
		return err
	}
	if err := store.ApplyColumns(o.ctx, o.tx, e.Entity, e.EntityID, cols); err != nil {
		return err
	}
	after, err := o.revertColumns(e.Entity, e.EntityID)
	if err != nil {
		return err
	}
	if action == domain.ActUpdate {
		return o.recordUpdate(e.Entity, e.EntityID, before, after)
	}
	bj, err := json.Marshal(selectColumns(before, cols))
	if err != nil {
		return err
	}
	aj, err := json.Marshal(selectColumns(after, cols))
	if err != nil {
		return err
	}
	if e.Entity != domain.EntInterruption {
		o.markDirty(domain.EntityRef{Entity: e.Entity, ID: e.EntityID})
	}
	return o.record(domain.Event{Entity: e.Entity, EntityID: e.EntityID, Action: action, Before: bj, After: aj})
}

func selectColumns(image, keys map[string]any) map[string]any {
	result := make(map[string]any, len(keys))
	for k := range keys {
		result[k] = image[k]
	}
	return result
}

// revertColumns also registers both sides of entry relinks/date edits for recompute.
func (o *op) revertColumns(entity domain.EntityKind, id int64) (map[string]any, error) {
	switch entity {
	case domain.EntTask:
		t, err := store.GetTask(o.ctx, o.tx, domain.TaskID(id))
		return taskCols(t), err
	case domain.EntEntry:
		e, err := store.GetEntry(o.ctx, o.tx, domain.EntryID(id))
		if err != nil {
			return nil, err
		}
		o.addPair(e.TaskID, e.WorkDate)
		return entryCols(e), nil
	case domain.EntInterruption:
		i, err := store.GetInterruption(o.ctx, o.tx, domain.InterruptionID(id))
		return map[string]any{"deleted_at": instantCol(i.DeletedAt)}, err
	default:
		return nil, domain.Errorf(domain.CodeInternal, "unsupported reversal event")
	}
}

func (o *op) validRevertFocus(task sql.Null[domain.TaskID], date sql.Null[civil.Date]) (bool, error) {
	if !task.Valid {
		return true, nil
	}
	if !date.Valid || date.V != o.today {
		return false, nil
	}
	t, err := store.GetTask(o.ctx, o.tx, task.V)
	if err != nil {
		return false, err
	}
	return t.Status == domain.StatusOpen && !t.DeletedAt.Valid && (!t.DeferDate.Valid || !o.s.zone.DeferMoment(t.DeferDate.V, t.DeferMin).After(o.now)), nil
}

func (o *op) revertFocus(events []domain.Event) error {
	for _, e := range events {
		var image struct {
			TaskID *domain.TaskID `json:"task_id"`
			Date   *civil.Date    `json:"date"`
		}
		if err := json.Unmarshal(e.Before, &image); err != nil {
			return err
		}
		var task sql.Null[domain.TaskID]
		var date sql.Null[civil.Date]
		if image.TaskID != nil {
			task = sql.Null[domain.TaskID]{V: *image.TaskID, Valid: true}
		}
		if image.Date != nil {
			date = sql.Null[civil.Date]{V: *image.Date, Valid: true}
		}
		valid, err := o.validRevertFocus(task, date)
		if err != nil {
			return err
		}
		if valid {
			if err := o.setFocus(task); err != nil {
				return err
			}
		}
	}
	f, err := store.GetFocus(o.ctx, o.tx)
	if err != nil {
		return err
	}
	valid, err := o.validRevertFocus(f.TaskID, f.Date)
	if err != nil {
		return err
	}
	if !valid {
		return o.setFocus(sql.Null[domain.TaskID]{})
	}
	return nil
}

func (o *op) reasonJustified(task domain.TaskID, r domain.TriageReason) (bool, error) {
	t, err := store.GetTask(o.ctx, o.tx, task)
	if err != nil {
		return false, err
	}
	if t.Status != domain.StatusOpen || t.DeletedAt.Valid {
		return false, nil
	}
	var placed int64
	if t.LastPlacedSeq.Valid {
		placed = int64(t.LastPlacedSeq.V)
	}
	if r == domain.ReasonSlipped {
		return store.OtherSlippedSince(o.ctx, o.tx, task, 0, placed)
	}
	if r != domain.ReasonContinue {
		return false, nil
	}
	blocks, err := store.BlocksOfTask(o.ctx, o.tx, task)
	if err != nil {
		return false, err
	}
	for _, b := range blocks {
		if b.Status != domain.BlockWorked || !b.ResolvedSeq.Valid || int64(b.ResolvedSeq.V) <= placed {
			continue
		}
		later, err := store.HasPlannedAfter(o.ctx, o.tx, task, b.Span.Date)
		if err != nil {
			return false, err
		}
		if !later {
			return true, nil
		}
	}
	return false, nil
}
