package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/linsomniac/taskronin/internal/domain"
)

// ScrubResult counts changed task/entry rows, events and operations.
type ScrubResult struct{ Rows, Events, Ops int }

// Changed reports whether any row, event or operation changed.
func (r ScrubResult) Changed() bool { return r.Rows != 0 || r.Events != 0 || r.Ops != 0 }

const scrubEventSet = `((entity IN ('task', 'task_tags') AND entity_id IN (SELECT value FROM json_each(?1)))
	OR (entity = 'entry' AND entity_id IN (SELECT value FROM json_each(?2))))`

const scrubEventContent = `(
	COALESCE(json_extract(before_json, '$.title'), '[purged]') <> '[purged]'
	OR COALESCE(json_extract(before_json, '$.notes'), '') <> ''
	OR json_extract(before_json, '$.project') IS NOT NULL
	OR COALESCE(json_array_length(before_json, '$.tags'), 0) > 0
	OR COALESCE(json_extract(after_json, '$.title'), '[purged]') <> '[purged]'
	OR COALESCE(json_extract(after_json, '$.notes'), '') <> ''
	OR json_extract(after_json, '$.project') IS NOT NULL
	OR COALESCE(json_array_length(after_json, '$.tags'), 0) > 0)`

// ScrubEntities tombstones and removes content from the listed tasks and entries,
// scrubs their event payloads and disables reversal of affected operations.
// The caller supplies a transaction when the scrub must be atomic.
func ScrubEntities(ctx context.Context, q Q, tasks []domain.TaskID, entries []domain.EntryID, now time.Time) (ScrubResult, error) {
	var result ScrubResult
	if len(tasks) == 0 && len(entries) == 0 {
		return result, nil
	}
	// Bind ID sets as JSON arrays so large ledger records need no variable-sized
	// SQL or parameter-limit batching. IN also makes duplicate IDs harmless.
	taskJSON, err := json.Marshal(tasks)
	if err != nil {
		return result, MapErr(err)
	}
	entryJSON, err := json.Marshal(entries)
	if err != nil {
		return result, MapErr(err)
	}
	taskIDs, entryIDs := string(taskJSON), string(entryJSON)
	// AIDEV-NOTE: Scrub content and tombstone in ONE UPDATE so FTS triggers see
	// the original live text. Event scrubbing must not depend on ops.purged:
	// sequential purges can affect different entities in the same op (R3-1).
	n, err := scrubExec(ctx, q, `UPDATE tasks SET deleted_at = COALESCE(deleted_at, ?1),
		purged_at = ?1, title = '[purged]', notes = '', project = NULL
		WHERE purged_at IS NULL AND id IN (SELECT value FROM json_each(?2))`, now.UnixMilli(), taskIDs)
	result.Rows = n
	if err != nil {
		return result, err
	}
	n, err = scrubExec(ctx, q, `UPDATE log_entries SET deleted_at = COALESCE(deleted_at, ?1),
		purged_at = ?1, title = '[purged]', notes = ''
		WHERE purged_at IS NULL AND id IN (SELECT value FROM json_each(?2))`, now.UnixMilli(), entryIDs)
	result.Rows += n
	if err != nil {
		return result, err
	}
	if len(tasks) != 0 {
		if _, err := q.ExecContext(ctx, `DELETE FROM task_tags WHERE task_id IN (SELECT value FROM json_each(?))`, taskIDs); err != nil {
			return result, MapErr(err)
		}
		if _, err := q.ExecContext(ctx, `DELETE FROM tags WHERE NOT EXISTS (SELECT 1 FROM task_tags WHERE tag_id = tags.id)`); err != nil {
			return result, MapErr(err)
		}
	}
	// json_replace changes only existing keys (including JSON null), just as
	// json_type(j, '$.key') IS NOT NULL requires; it never adds absent keys.
	result.Events, err = scrubExec(ctx, q, `UPDATE events SET
		before_json = json_replace(before_json, '$.title', '[purged]', '$.notes', '', '$.project', NULL, '$.tags', json('[]')),
		after_json = json_replace(after_json, '$.title', '[purged]', '$.notes', '', '$.project', NULL, '$.tags', json('[]'))
		WHERE `+scrubEventSet+` AND `+scrubEventContent, taskIDs, entryIDs)
	if err != nil {
		return result, err
	}
	result.Ops, err = scrubExec(ctx, q, `UPDATE ops SET purged = 1, revertible = 0
		WHERE purged = 0 AND op_id IN (SELECT op_id FROM events WHERE `+scrubEventSet+`
		OR (entity = 'triage' AND entity_id IN (SELECT value FROM json_each(?1))))`, taskIDs, entryIDs)
	return result, err
}

// scrubExec accepts constant SQL and counts only rows selected for a real change.
func scrubExec(ctx context.Context, q Q, query string, args ...any) (int, error) {
	result, err := q.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, MapErr(err)
	}
	n, err := result.RowsAffected()
	return int(n), MapErr(err)
}
