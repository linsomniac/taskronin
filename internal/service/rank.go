package service

import (
	"database/sql"
	"encoding/json"

	"taskronin/internal/domain"
	"taskronin/internal/store"
)

// Gap is the default spacing between task ranks.
const Gap int64 = 1 << 16

const rankLimit int64 = 1 << 60

func rankInRange(rank int64) bool { return rank >= -rankLimit && rank <= rankLimit }

func (o *op) rankTop() (int64, error) { return o.rankEdge(true) }

func (o *op) rankBottom() (int64, error) { return o.rankEdge(false) }

func (o *op) rankEdge(top bool) (int64, error) {
	for {
		lo, hi, err := store.RankBounds(o.ctx, o.tx)
		if err != nil {
			return 0, err
		}
		if !lo.Valid {
			return Gap, nil
		}
		// AIDEV-NOTE: Bound operands before arithmetic to avoid int64 overflow;
		// renormalization is storage-only and never marks unrelated tasks dirty.
		if rankInRange(lo.V) && rankInRange(hi.V) {
			rank := hi.V + Gap
			if top {
				rank = lo.V - Gap
			}
			if rankInRange(rank) {
				return rank, nil
			}
		}
		if err := store.RenormalizeRanks(o.ctx, o.tx); err != nil {
			return 0, err
		}
	}
}

func (o *op) rankBefore(ref domain.Task) (int64, error) { return o.rankBeside(ref.ID, true) }

func (o *op) rankAfter(ref domain.Task) (int64, error) { return o.rankBeside(ref.ID, false) }

func (o *op) rankBeside(id domain.TaskID, before bool) (int64, error) {
	for {
		// AIDEV-NOTE: A retry must reload the reference after renormalization.
		ref, err := store.GetTask(o.ctx, o.tx, id)
		if err != nil {
			return 0, err
		}
		pos := store.RankPos{ID: ref.ID, Rank: ref.Rank}
		var neighbor sql.Null[store.RankPos]
		if before {
			neighbor, err = store.RankPrev(o.ctx, o.tx, pos)
		} else {
			neighbor, err = store.RankNext(o.ctx, o.tx, pos)
		}
		if err != nil {
			return 0, err
		}
		if rankInRange(ref.Rank) && (!neighbor.Valid || rankInRange(neighbor.V.Rank)) {
			rank := ref.Rank + Gap
			if before {
				rank = ref.Rank - Gap
			}
			if neighbor.Valid {
				lo, hi := ref.Rank, neighbor.V.Rank
				if before {
					lo, hi = neighbor.V.Rank, ref.Rank
				}
				if hi-lo >= 2 {
					return lo + (hi-lo)/2, nil
				}
			} else if rankInRange(rank) {
				return rank, nil
			}
		}
		if err := store.RenormalizeRanks(o.ctx, o.tx); err != nil {
			return 0, err
		}
	}
}

func (o *op) placementRank(how domain.PlaceHow, ref domain.TaskID) (int64, error) {
	switch how {
	case domain.PlaceTop:
		return o.rankTop()
	case domain.PlaceBottom:
		return o.rankBottom()
	case domain.PlaceBefore, domain.PlaceAfter:
		t, err := o.loadTask(ref, nil, false)
		if err != nil {
			return 0, err
		}
		if how == domain.PlaceBefore {
			return o.rankBefore(t)
		}
		return o.rankAfter(t)
	default:
		return 0, domain.Errorf(domain.CodeValidation, "unsupported placement")
	}
}

func (o *op) moveTask(t *domain.Task, rank int64) error {
	// AIDEV-NOTE: Computing rank may renormalize every task, including t.
	// Read its current position so the before-image names the true predecessor.
	current, err := store.GetTask(o.ctx, o.tx, t.ID)
	if err != nil {
		return err
	}
	before, err := o.predecessorImage(current)
	if err != nil {
		return err
	}
	if err := store.SetRank(o.ctx, o.tx, t.ID, rank); err != nil {
		return err
	}
	t.Rank = rank
	after, err := o.predecessorImage(*t)
	if err != nil {
		return err
	}
	if err := o.record(domain.Event{Entity: domain.EntTask, EntityID: int64(t.ID), Action: domain.ActMove, Before: before, After: after}); err != nil {
		return err
	}
	o.rankMoved[t.ID] = true
	o.markTaskDirty(t.ID)
	return nil
}

func (o *op) predecessorImage(t domain.Task) (json.RawMessage, error) {
	prev, err := store.RankPrev(o.ctx, o.tx, store.RankPos{ID: t.ID, Rank: t.Rank})
	if err != nil {
		return nil, err
	}
	var id any
	if prev.Valid {
		id = int64(prev.V.ID)
	}
	return json.Marshal(map[string]any{"after_id": id})
}
