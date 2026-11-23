package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/linsomniac/taskronin/internal/domain"
)

// ApplyColumns restores allowed event-payload columns verbatim in one UPDATE.
// JSON integers retain their precision, booleans become 0/1, and nil writes NULL.
func ApplyColumns(ctx context.Context, q Q, ent domain.EntityKind, id int64, cols map[string]any) error {
	var table, allowed string
	switch ent {
	case domain.EntTask:
		table, allowed = "tasks", `title notes status rank last_placed_at last_placed_seq
			defer_date defer_min due_date due_min due_ack due_ack_date project priority
			completed_at completed_date deleted_at`
	case domain.EntEntry:
		table, allowed = "log_entries", `task_id title notes work_date occurred_min reopened deleted_at`
	case domain.EntBlock:
		table, allowed = "time_blocks", `date start_min end_min status outcome_source cancel_reason
			slipped slipped_at slipped_seq resolved_at resolved_seq deleted_at`
	case domain.EntInterruption:
		table, allowed = "interruptions", "deleted_at"
	case domain.EntFocus:
		table, allowed = "focus", "task_id date set_at"
	default:
		return domain.Errorf(domain.CodeValidation, "entity does not support column restoration")
	}
	allowlist := strings.Fields(allowed)
	var names []string
	for name := range cols {
		if !slices.Contains(allowlist, name) {
			return domain.Errorf(domain.CodeValidation, "column does not support restoration")
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil
	}
	slices.Sort(names)
	assignments := make([]string, 0, len(names))
	args := make([]any, 0, len(names)+1)
	for _, name := range names {
		value := cols[name]
		switch v := value.(type) {
		case json.Number:
			n, err := v.Int64()
			if err != nil {
				return MapErr(fmt.Errorf("store: restore integer column %s: %w", name, err))
			}
			value = n
		case bool:
			value = int64(0)
			if v {
				value = int64(1)
			}
		}
		assignments = append(assignments, name+" = ?")
		args = append(args, value)
	}
	args = append(args, id)
	// AIDEV-NOTE: Only allowlisted identifiers enter SQL. Validate and convert
	// every column before writing; coupled CHECK fields require one UPDATE.
	result, err := q.ExecContext(ctx, "UPDATE "+table+" SET "+strings.Join(assignments, ", ")+" WHERE id = ?", args...)
	return requireAffected(result, err)
}
