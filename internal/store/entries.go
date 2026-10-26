package store

import (
	"context"
	"database/sql"
	"time"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
)

const entryColumns = `id, task_id, kind, title, notes, work_date, occurred_min,
	reopened, created_at, updated_at, deleted_at, purged_at, version`

const entryNewestOrder = ` ORDER BY work_date DESC, occurred_min IS NULL,
	occurred_min DESC, created_at DESC, id DESC`

func scanEntry(row rowScanner) (domain.LogEntry, error) {
	var e domain.LogEntry
	var created, updated int64
	var deleted, purged sql.Null[int64]
	if err := row.Scan(&e.ID, &e.TaskID, &e.Kind, &e.Title, &e.Notes, &e.WorkDate,
		&e.OccurredMin, &e.Reopened, &created, &updated, &deleted, &purged, &e.Version); err != nil {
		return domain.LogEntry{}, MapErr(err)
	}
	e.CreatedAt, e.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
	e.DeletedAt, e.PurgedAt = nullTime(deleted), nullTime(purged)
	return e, nil
}

// InsertEntry inserts e with a generated ID and version 1, assigning both on success.
func InsertEntry(ctx context.Context, q Q, e *domain.LogEntry) error {
	var id domain.EntryID
	err := q.QueryRowContext(ctx, `INSERT INTO log_entries (
		task_id, kind, title, notes, work_date, occurred_min, reopened,
		created_at, updated_at, deleted_at, purged_at, version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1) RETURNING id`,
		e.TaskID, e.Kind, e.Title, e.Notes, e.WorkDate, e.OccurredMin, e.Reopened,
		e.CreatedAt.UnixMilli(), e.UpdatedAt.UnixMilli(), nullMillis(e.DeletedAt), nullMillis(e.PurgedAt)).Scan(&id)
	if err != nil {
		return MapErr(err)
	}
	e.ID, e.Version = id, 1
	return nil
}

// GetEntry returns an entry, including tombstoned or purged rows.
func GetEntry(ctx context.Context, q Q, id domain.EntryID) (domain.LogEntry, error) {
	return scanEntry(q.QueryRowContext(ctx, `SELECT `+entryColumns+` FROM log_entries WHERE id = ?`, id))
}

// UpdateEntry replaces mutable fields, preserving kind, creation/update times,
// version and purged_at for their dedicated writers.
func UpdateEntry(ctx context.Context, q Q, e domain.LogEntry) error {
	result, err := q.ExecContext(ctx, `UPDATE log_entries SET task_id = ?, title = ?, notes = ?,
		work_date = ?, occurred_min = ?, reopened = ?, deleted_at = ? WHERE id = ?`,
		e.TaskID, e.Title, e.Notes, e.WorkDate, e.OccurredMin, e.Reopened, nullMillis(e.DeletedAt), e.ID)
	return requireAffected(result, err)
}

// EntryQuery selects entries with optional date, task, tag/project and cursor filters.
type EntryQuery struct {
	From, To       civil.Date               // Zero means unbounded.
	Task           *sql.Null[domain.TaskID] // Nil means any; invalid means standalone only.
	IncludeDeleted bool
	Tag, Project   string // Either filter excludes standalone entries.
	AfterKeys      []any  // [work_date, untimed 0|1, occurred_min_or_0, created_at_ms]
	AfterID        int64
	Limit          int
}

const entryFilter = ` FROM log_entries
	WHERE (?1 IS NULL OR work_date >= ?1) AND (?2 IS NULL OR work_date <= ?2)
	AND (?3 = '' OR EXISTS (
		SELECT 1 FROM task_tags tt JOIN tags ON tags.id = tt.tag_id
		WHERE tt.task_id = log_entries.task_id AND tags.name = ?3 COLLATE NOCASE))
	AND (?4 = '' OR EXISTS (
		SELECT 1 FROM tasks WHERE tasks.id = log_entries.task_id
		AND (project = ?4 OR substr(project, 1, length(?4) + 1) = ?4 || '.')))`

// ListEntries returns entries newest first, placing timed entries before untimed
// entries on each day. A cursor resumes strictly after its complete sort tuple.
func ListEntries(ctx context.Context, q Q, eq EntryQuery) ([]domain.LogEntry, error) {
	hasCursor := eq.AfterKeys != nil
	if hasCursor && len(eq.AfterKeys) != 4 {
		return nil, domain.Errorf(domain.CodeValidation, "wrong number of entry cursor keys")
	}
	var keys [4]any
	copy(keys[:], eq.AfterKeys)
	var task sql.Null[domain.TaskID]
	if eq.Task != nil {
		task = *eq.Task
	}
	// AIDEV-NOTE: Untimed sorts ASC between descending keys. Compare it
	// separately so midnight (timed minute 0) never merges with an untimed row.
	return queryEntries(ctx, q, `SELECT `+entryColumns+entryFilter+`
		AND (?5 OR deleted_at IS NULL)
		AND (?6 OR task_id IS ?7)
		AND (?8 = 0 OR work_date < ?9 OR (work_date = ?9 AND (
			(occurred_min IS NULL) > ?10 OR ((occurred_min IS NULL) = ?10
			AND (COALESCE(occurred_min, 0), created_at, id) < (?11, ?12, ?13)))))`+
		entryNewestOrder+` LIMIT ?14`,
		nullDate(eq.From), nullDate(eq.To), eq.Tag, eq.Project, eq.IncludeDeleted,
		eq.Task == nil, task, hasCursor, keys[0], keys[1], keys[2], keys[3], eq.AfterID, eq.Limit)
}

func nullDate(d civil.Date) sql.Null[civil.Date] {
	return sql.Null[civil.Date]{V: d, Valid: !d.IsZero()}
}

// ReportEntries returns live entries by ascending day, timed minutes first,
// then creation time and ID, with optional tag/project filters.
func ReportEntries(ctx context.Context, q Q, from, to civil.Date, tag, project string) ([]domain.LogEntry, error) {
	return queryEntries(ctx, q, `SELECT `+entryColumns+entryFilter+`
		AND deleted_at IS NULL ORDER BY work_date, occurred_min IS NULL, occurred_min, created_at, id`,
		nullDate(from), nullDate(to), tag, project)
}

// CountEntriesOn counts non-deleted work and completion entries for a task/day.
func CountEntriesOn(ctx context.Context, q Q, task domain.TaskID, d civil.Date) (work, complete int, err error) {
	err = q.QueryRowContext(ctx, `SELECT COUNT(CASE WHEN kind = 'work' THEN 1 END),
		COUNT(CASE WHEN kind = 'complete' THEN 1 END) FROM log_entries
		WHERE task_id = ? AND work_date = ? AND deleted_at IS NULL`, task, d).Scan(&work, &complete)
	return work, complete, MapErr(err)
}

// EntriesOfTask returns a task's non-deleted entries in the ListEntries order.
func EntriesOfTask(ctx context.Context, q Q, task domain.TaskID) ([]domain.LogEntry, error) {
	return queryEntries(ctx, q, `SELECT `+entryColumns+` FROM log_entries
		WHERE task_id = ? AND deleted_at IS NULL`+entryNewestOrder, task)
}

// CompleteEntries returns every completion entry, including deleted and purged
// rows, in ascending ID order.
func CompleteEntries(ctx context.Context, q Q, task domain.TaskID) ([]domain.LogEntry, error) {
	return queryEntries(ctx, q, `SELECT `+entryColumns+` FROM log_entries
		WHERE task_id = ? AND kind = 'complete' ORDER BY id`, task)
}

// queryEntries accepts only constant SQL from this package; values are bound.
func queryEntries(ctx context.Context, q Q, query string, args ...any) ([]domain.LogEntry, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, MapErr(err)
	}
	defer rows.Close()
	var entries []domain.LogEntry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, MapErr(rows.Err())
}
