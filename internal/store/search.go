package store

import (
	"context"
	"database/sql"

	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/domain"
)

// FTSHit is a live task or entry matching a full-text query.
type FTSHit struct {
	Kind     string // "task" | "entry"
	ID       int64
	TaskID   sql.Null[domain.TaskID]
	Title    string
	Snippet  string               // Plain user text; rendering is responsible for sanitization.
	Status   domain.TaskStatus    // Tasks only.
	WorkDate sql.Null[civil.Date] // Entries only.
	Score    float64              // bm25; lower is better.
}

// Search returns hits ordered by bm25, kind and ID. match is an FTS5 expression
// prepared by the caller; kind is task, entry or all. Titles have twice the
// weight of notes, and snippets use ten tokens with no highlighting markers.
func Search(ctx context.Context, q Q, match, kind string, limit, offset int) ([]FTSHit, error) {
	if kind != "task" && kind != "entry" && kind != "all" {
		return nil, domain.Errorf(domain.CodeValidation, "unknown search kind")
	}
	rows, err := q.QueryContext(ctx, `
		SELECT 'task' AS kind, tasks.id, NULL AS task_id, tasks.title,
			snippet(tasks_fts, -1, '', '', '…', 10) AS snippet,
			tasks.status, NULL AS work_date, bm25(tasks_fts, 2.0, 1.0) AS score
		FROM tasks_fts JOIN tasks ON tasks.id = tasks_fts.rowid
		WHERE tasks_fts MATCH ?1 AND ?2 IN ('task', 'all') AND tasks.deleted_at IS NULL
		UNION ALL
		SELECT 'entry', log_entries.id, log_entries.task_id, log_entries.title,
			snippet(log_entries_fts, -1, '', '', '…', 10),
			'', log_entries.work_date, bm25(log_entries_fts, 2.0, 1.0)
		FROM log_entries_fts JOIN log_entries ON log_entries.id = log_entries_fts.rowid
		WHERE log_entries_fts MATCH ?1 AND ?2 IN ('entry', 'all') AND log_entries.deleted_at IS NULL
		ORDER BY score, kind, id LIMIT ?3 OFFSET ?4`, match, kind, limit, offset)
	if err != nil {
		return nil, MapErr(err)
	}
	defer rows.Close()
	var hits []FTSHit
	for rows.Next() {
		var hit FTSHit
		if err := rows.Scan(&hit.Kind, &hit.ID, &hit.TaskID, &hit.Title, &hit.Snippet,
			&hit.Status, &hit.WorkDate, &hit.Score); err != nil {
			return nil, MapErr(err)
		}
		hits = append(hits, hit)
	}
	return hits, MapErr(rows.Err())
}
