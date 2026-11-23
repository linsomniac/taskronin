package service

import (
	"database/sql"
	"reflect"
	"time"

	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/domain"
)

// AIDEV-NOTE: Event images use SQLite values, never domain JSON encodings.
// In particular Minute implements MarshalText, but events require integers.
func taskCols(t domain.Task) map[string]any {
	return map[string]any{
		"title": t.Title, "notes": t.Notes, "status": string(t.Status), "rank": t.Rank,
		"last_placed_at": instantCol(t.LastPlacedAt), "last_placed_seq": integerCol(t.LastPlacedSeq),
		"defer_date": dateCol(t.DeferDate), "defer_min": integerCol(t.DeferMin),
		"due_date": dateCol(t.DueDate), "due_min": integerCol(t.DueMin),
		"due_ack": string(t.DueAck), "due_ack_date": dateCol(t.DueAckDate),
		"project": optionalText(t.Project), "priority": optionalText(t.Priority), "origin": t.Origin,
		"completed_at": instantCol(t.CompletedAt), "completed_date": dateCol(t.CompletedDate),
		"deleted_at": instantCol(t.DeletedAt),
	}
}

func entryCols(e domain.LogEntry) map[string]any {
	return map[string]any{
		"task_id": integerCol(e.TaskID), "kind": string(e.Kind), "title": e.Title, "notes": e.Notes,
		"work_date": e.WorkDate.String(), "occurred_min": integerCol(e.OccurredMin),
		"reopened": boolCol(e.Reopened), "deleted_at": instantCol(e.DeletedAt),
	}
}

func blockCols(b domain.TimeBlock) map[string]any {
	return map[string]any{
		"task_id": int64(b.TaskID), "date": b.Span.Date.String(), "start_min": int64(b.Span.Start), "end_min": int64(b.Span.End),
		"status": string(b.Status), "outcome_source": optionalText(b.OutcomeSource), "cancel_reason": optionalText(b.CancelReason),
		"slipped": boolCol(b.Slipped), "slipped_at": instantCol(b.SlippedAt), "slipped_seq": integerCol(b.SlippedSeq),
		"resolved_at": instantCol(b.ResolvedAt), "resolved_seq": integerCol(b.ResolvedSeq), "deleted_at": instantCol(b.DeletedAt),
	}
}

// AIDEV-NOTE: A§2.7's companion-column ruling makes each op's effects
// reconstructable from its own events. Unchanged companions are copied verbatim
// into both images; reverting them restores the same values. Only columns in
// the entity's storage map participate, so task status changes add no block fields.
var columnCompanions = []struct {
	changed string
	include []string
}{
	{"due_date", []string{"due_min"}},
	{"due_min", []string{"due_date"}},
	{"defer_date", []string{"defer_min"}},
	{"defer_min", []string{"defer_date"}},
	{"status", []string{"outcome_source", "resolved_at"}},
}

func diffCols(before, after map[string]any) (b, a map[string]any) {
	b, a = make(map[string]any), make(map[string]any)
	for k, v := range before {
		if !reflect.DeepEqual(v, after[k]) {
			b[k], a[k] = v, after[k]
		}
	}
	for k, v := range after {
		if _, ok := before[k]; !ok && v != nil {
			b[k], a[k] = nil, v
		}
	}
	for _, rule := range columnCompanions {
		if reflect.DeepEqual(before[rule.changed], after[rule.changed]) {
			continue
		}
		for _, k := range rule.include {
			bv, inBefore := before[k]
			av, inAfter := after[k]
			if inBefore || inAfter {
				b[k], a[k] = bv, av
			}
		}
	}
	return b, a
}

func instantCol(v sql.Null[time.Time]) any {
	if !v.Valid {
		return nil
	}
	return v.V.UnixMilli()
}

func dateCol(v sql.Null[civil.Date]) any {
	if !v.Valid {
		return nil
	}
	return v.V.String()
}

func integerCol[T ~int16 | ~int64](v sql.Null[T]) any {
	if !v.Valid {
		return nil
	}
	return int64(v.V)
}

func optionalText[T ~string](v T) any {
	if v == "" {
		return nil
	}
	return string(v)
}

func boolCol(v bool) int64 {
	if v {
		return 1
	}
	return 0
}
