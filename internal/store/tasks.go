package store

import (
	"context"
	"database/sql"
	"time"

	"taskronin/internal/domain"
)

const taskColumns = `id, title, notes, status, rank, last_placed_at, last_placed_seq,
	defer_date, defer_min, due_date, due_min, due_ack, due_ack_date, project, priority,
	origin, completed_at, completed_date, created_at, updated_at, deleted_at, purged_at, version`

type rowScanner interface{ Scan(...any) error }

func scanTask(row rowScanner) (domain.Task, error) {
	var t domain.Task
	var placed, completed, deleted, purged sql.Null[int64]
	var created, updated int64
	var project sql.Null[string]
	var priority sql.Null[domain.Priority]
	err := row.Scan(&t.ID, &t.Title, &t.Notes, &t.Status, &t.Rank, &placed, &t.LastPlacedSeq,
		&t.DeferDate, &t.DeferMin, &t.DueDate, &t.DueMin, &t.DueAck, &t.DueAckDate, &project, &priority,
		&t.Origin, &completed, &t.CompletedDate, &created, &updated, &deleted, &purged, &t.Version)
	if err != nil {
		return domain.Task{}, MapErr(err)
	}
	t.Project, t.Priority = project.V, priority.V
	t.CreatedAt, t.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
	t.LastPlacedAt, t.CompletedAt = nullTime(placed), nullTime(completed)
	t.DeletedAt, t.PurgedAt = nullTime(deleted), nullTime(purged)
	return t, nil
}

func nullString[T ~string](v T) sql.Null[string] {
	return sql.Null[string]{V: string(v), Valid: v != ""}
}

func nullMillis(v sql.Null[time.Time]) sql.Null[int64] {
	if !v.Valid {
		return sql.Null[int64]{}
	}
	return sql.Null[int64]{V: v.V.UnixMilli(), Valid: true}
}

func nullTime(v sql.Null[int64]) sql.Null[time.Time] {
	if !v.Valid {
		return sql.Null[time.Time]{}
	}
	return sql.Null[time.Time]{V: time.UnixMilli(v.V).UTC(), Valid: true}
}

// InsertTask inserts t with a generated ID and version 1, assigning both on success.
func InsertTask(ctx context.Context, q Q, t *domain.Task) error {
	var id domain.TaskID
	err := q.QueryRowContext(ctx, `INSERT INTO tasks (
		title, notes, status, rank, last_placed_at, last_placed_seq, defer_date, defer_min,
		due_date, due_min, due_ack, due_ack_date, project, priority, origin, completed_at,
		completed_date, created_at, updated_at, deleted_at, purged_at, version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1) RETURNING id`,
		t.Title, t.Notes, t.Status, t.Rank, nullMillis(t.LastPlacedAt), t.LastPlacedSeq,
		t.DeferDate, t.DeferMin, t.DueDate, t.DueMin, t.DueAck, t.DueAckDate,
		nullString(t.Project), nullString(t.Priority), t.Origin, nullMillis(t.CompletedAt),
		t.CompletedDate, t.CreatedAt.UnixMilli(), t.UpdatedAt.UnixMilli(), nullMillis(t.DeletedAt), nullMillis(t.PurgedAt)).Scan(&id)
	if err != nil {
		return MapErr(err)
	}
	t.ID, t.Version = id, 1
	return nil
}

// GetTask returns a task, including tombstoned or purged rows.
func GetTask(ctx context.Context, q Q, id domain.TaskID) (domain.Task, error) {
	return scanTask(q.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id))
}

// UpdateTask replaces mutable task fields, leaving rank, creation/update times,
// version and purged_at to their dedicated writers.
func UpdateTask(ctx context.Context, q Q, t domain.Task) error {
	result, err := q.ExecContext(ctx, `UPDATE tasks SET title = ?, notes = ?, status = ?,
		last_placed_at = ?, last_placed_seq = ?, defer_date = ?, defer_min = ?, due_date = ?, due_min = ?,
		due_ack = ?, due_ack_date = ?, project = ?, priority = ?, origin = ?, completed_at = ?,
		completed_date = ?, deleted_at = ? WHERE id = ?`,
		t.Title, t.Notes, t.Status, nullMillis(t.LastPlacedAt), t.LastPlacedSeq, t.DeferDate, t.DeferMin,
		t.DueDate, t.DueMin, t.DueAck, t.DueAckDate, nullString(t.Project), nullString(t.Priority),
		t.Origin, nullMillis(t.CompletedAt), t.CompletedDate, nullMillis(t.DeletedAt), t.ID)
	return requireAffected(result, err)
}

func requireAffected(result sql.Result, err error) error {
	if err != nil {
		return MapErr(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return MapErr(err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// BumpVersion increments a task, entry, block or focus version. Only tasks and
// entries have updated_at, which is set to now without changing other fields.
func BumpVersion(ctx context.Context, q Q, ref domain.EntityRef, now time.Time) error {
	var query string
	var args []any
	switch ref.Entity {
	case domain.EntTask:
		query, args = `UPDATE tasks SET version = version + 1, updated_at = ? WHERE id = ?`, []any{now.UnixMilli(), ref.ID}
	case domain.EntEntry:
		query, args = `UPDATE log_entries SET version = version + 1, updated_at = ? WHERE id = ?`, []any{now.UnixMilli(), ref.ID}
	case domain.EntBlock:
		query, args = `UPDATE time_blocks SET version = version + 1 WHERE id = ?`, []any{ref.ID}
	case domain.EntFocus:
		query, args = `UPDATE focus SET version = version + 1 WHERE id = ?`, []any{ref.ID}
	default:
		return MapErr(domain.Errorf(domain.CodeValidation, "entity has no version"))
	}
	result, err := q.ExecContext(ctx, query, args...)
	return requireAffected(result, err)
}

// TaskQuery selects a filtered task view and an optional keyset page.
// AfterKeys contains that view's sort keys; AfterID breaks ties.
type TaskQuery struct {
	View         domain.View
	Tag, Project string
	AfterKeys    []any
	AfterID      int64
	Limit        int
}

// AIDEV-NOTE: Keep top_rank identical to TriageReason.Rank. top_seq belongs to
// that reason's row, not the oldest or newest reason attached to the task.
const taskListSelect = `WITH ranked_reasons AS (
	SELECT task_id, seq, ` + triageRank + ` AS top_rank,
		ROW_NUMBER() OVER (PARTITION BY task_id ORDER BY ` + triageRank + ` DESC, seq) AS n
	FROM task_triage
), listed_tasks AS (
	SELECT tasks.*, r.top_rank, r.seq AS top_seq FROM tasks
	LEFT JOIN ranked_reasons r ON r.task_id = tasks.id AND r.n = 1
)
SELECT ` + taskColumns + ` FROM listed_tasks
WHERE (?1 = '' OR EXISTS (
	SELECT 1 FROM task_tags tt JOIN tags ON tags.id = tt.tag_id
	WHERE tt.task_id = listed_tasks.id AND tags.name = ?1 COLLATE NOCASE))
AND (?2 = '' OR project = ?2 OR substr(project, 1, length(?2) + 1) = ?2 || '.')
AND `

// ListTasks lists reconciled task classifications in their cursor order.
// All SQL fragments are constants; filter and cursor values are bound parameters.
func ListTasks(ctx context.Context, q Q, tq TaskQuery) ([]domain.Task, error) {
	var query string
	var keyCount int
	// ?3 marks a cursor, ?4..?6 hold its keys, ?7 its ID, and ?8 the limit.
	switch tq.View {
	case domain.ViewTriage:
		keyCount = 2
		query = taskListSelect + `status = 'open' AND deleted_at IS NULL AND defer_date IS NULL AND top_rank IS NOT NULL
		AND (?3 = 0 OR top_rank < ?4 OR (top_rank = ?4 AND (top_seq, id) > (?5, ?7)))
		ORDER BY top_rank DESC, top_seq, id LIMIT ?8`
	case domain.ViewBacklog:
		keyCount = 1
		query = taskListSelect + `status = 'open' AND deleted_at IS NULL AND defer_date IS NULL AND top_rank IS NULL
		AND (?3 = 0 OR (rank, id) > (?4, ?7)) ORDER BY rank, id LIMIT ?8`
	case domain.ViewUpcoming:
		keyCount = 3
		query = taskListSelect + `status = 'open' AND deleted_at IS NULL AND defer_date IS NOT NULL
		AND (?3 = 0 OR (defer_date, COALESCE(defer_min, 0), rank, id) > (?4, ?5, ?6, ?7))
		ORDER BY defer_date, COALESCE(defer_min, 0), rank, id LIMIT ?8`
	case domain.ViewDone:
		keyCount = 1
		query = taskListSelect + `status = 'done' AND deleted_at IS NULL
		AND (?3 = 0 OR (completed_at, id) < (?4, ?7)) ORDER BY completed_at DESC, id DESC LIMIT ?8`
	case domain.ViewDropped:
		keyCount = 1
		query = taskListSelect + `status = 'dropped' AND deleted_at IS NULL
		AND (?3 = 0 OR (updated_at, id) < (?4, ?7)) ORDER BY updated_at DESC, id DESC LIMIT ?8`
	case domain.ViewDeleted:
		keyCount = 1
		query = taskListSelect + `deleted_at IS NOT NULL
		AND (?3 = 0 OR (deleted_at, id) < (?4, ?7)) ORDER BY deleted_at DESC, id DESC LIMIT ?8`
	case domain.ViewAll:
		query = taskListSelect + `deleted_at IS NULL AND (?3 = 0 OR id > ?7) ORDER BY id LIMIT ?8`
	default:
		return nil, domain.Errorf(domain.CodeValidation, "unknown task view")
	}
	hasCursor := tq.AfterKeys != nil
	if hasCursor && len(tq.AfterKeys) != keyCount {
		return nil, domain.Errorf(domain.CodeValidation, "wrong number of task cursor keys")
	}
	var keys [3]any
	copy(keys[:], tq.AfterKeys)
	rows, err := q.QueryContext(ctx, query, tq.Tag, tq.Project, hasCursor, keys[0], keys[1], keys[2], tq.AfterID, tq.Limit)
	if err != nil {
		return nil, MapErr(err)
	}
	defer rows.Close()
	var tasks []domain.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, MapErr(rows.Err())
}
