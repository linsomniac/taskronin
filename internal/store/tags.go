package store

import (
	"context"
	"time"

	"github.com/linsomniac/taskronin/internal/domain"
)

// TaskTags returns stored tag names in case-insensitive name order.
func TaskTags(ctx context.Context, q Q, task domain.TaskID) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT tags.name FROM tags
		JOIN task_tags tt ON tt.tag_id = tags.id WHERE tt.task_id = ? ORDER BY tags.name COLLATE NOCASE`, task)
	if err != nil {
		return nil, MapErr(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, MapErr(err)
		}
		names = append(names, name)
	}
	return names, MapErr(rows.Err())
}

// SetTaskTags replaces a task's tag set, reusing existing names and casing.
// The caller supplies a transaction when the replacement must be atomic.
func SetTaskTags(ctx context.Context, q Q, task domain.TaskID, names []string, now time.Time) error {
	var id domain.TaskID
	if err := q.QueryRowContext(ctx, `SELECT id FROM tasks WHERE id = ?`, task).Scan(&id); err != nil {
		return MapErr(err)
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM task_tags WHERE task_id = ?`, task); err != nil {
		return MapErr(err)
	}
	for _, name := range names {
		// AIDEV-NOTE: The NOCASE unique constraint decides identity. DO NOTHING
		// preserves the first stored casing and created_at across every task.
		if _, err := q.ExecContext(ctx, `INSERT INTO tags (name, created_at) VALUES (?, ?)
			ON CONFLICT (name) DO NOTHING`, name, now.UnixMilli()); err != nil {
			return MapErr(err)
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO task_tags (task_id, tag_id)
			SELECT ?, id FROM tags WHERE name = ? COLLATE NOCASE
			ON CONFLICT (task_id, tag_id) DO NOTHING`, task, name); err != nil {
			return MapErr(err)
		}
	}
	return nil
}

// TagCount counts open, non-deleted tasks with the named tag.
type TagCount struct {
	Name  string
	Count int
}

// TagCounts returns positive tag counts in case-insensitive name order.
func TagCounts(ctx context.Context, q Q) ([]TagCount, error) {
	rows, err := q.QueryContext(ctx, `SELECT tags.name, COUNT(*) FROM tags
		JOIN task_tags tt ON tt.tag_id = tags.id JOIN tasks ON tasks.id = tt.task_id
		WHERE tasks.status = 'open' AND tasks.deleted_at IS NULL
		GROUP BY tags.id, tags.name ORDER BY tags.name COLLATE NOCASE`)
	if err != nil {
		return nil, MapErr(err)
	}
	defer rows.Close()
	var counts []TagCount
	for rows.Next() {
		var count TagCount
		if err := rows.Scan(&count.Name, &count.Count); err != nil {
			return nil, MapErr(err)
		}
		counts = append(counts, count)
	}
	return counts, MapErr(rows.Err())
}
