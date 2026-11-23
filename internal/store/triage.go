package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/linsomniac/taskronin/internal/domain"
)

const triageRank = `CASE reason WHEN 'slipped' THEN 8 WHEN 'overdue' THEN 7
	WHEN 'due' THEN 6 WHEN 'continue' THEN 5 WHEN 'surfaced' THEN 4
	WHEN 'reopened' THEN 3 WHEN 'manual' THEN 2 WHEN 'new' THEN 1 ELSE 0 END`

func scanReason(row rowScanner) (domain.TriageRow, error) {
	var r domain.TriageRow
	var since int64
	err := row.Scan(&r.TaskID, &r.Reason, &since, &r.Seq)
	if err != nil {
		return domain.TriageRow{}, MapErr(err)
	}
	r.Since = time.UnixMilli(since).UTC()
	return r, nil
}

// AddReason adds a reason, preserving an existing row's since and sequence.
// It reports whether a new row was inserted.
func AddReason(ctx context.Context, q Q, row domain.TriageRow) (bool, error) {
	result, err := q.ExecContext(ctx, `INSERT INTO task_triage (task_id, reason, since, seq)
		VALUES (?, ?, ?, ?) ON CONFLICT (task_id, reason) DO NOTHING`, row.TaskID, row.Reason, row.Since.UnixMilli(), row.Seq)
	if err != nil {
		return false, MapErr(err)
	}
	n, err := result.RowsAffected()
	return n > 0, MapErr(err)
}

// RemoveReason deletes and returns the reason, or an invalid Null if absent.
func RemoveReason(ctx context.Context, q Q, task domain.TaskID, r domain.TriageReason) (sql.Null[domain.TriageRow], error) {
	row, err := scanReason(q.QueryRowContext(ctx, `DELETE FROM task_triage WHERE task_id = ? AND reason = ?
		RETURNING task_id, reason, since, seq`, task, r))
	if errors.Is(err, ErrNotFound) {
		return sql.Null[domain.TriageRow]{}, nil
	}
	if err != nil {
		return sql.Null[domain.TriageRow]{}, err
	}
	return sql.Null[domain.TriageRow]{V: row, Valid: true}, nil
}

// Reasons returns a task's reasons in TriageOrder, then sequence order.
func Reasons(ctx context.Context, q Q, task domain.TaskID) ([]domain.TriageRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT task_id, reason, since, seq FROM task_triage
		WHERE task_id = ? ORDER BY `+triageRank+` DESC, seq`, task)
	if err != nil {
		return nil, MapErr(err)
	}
	defer rows.Close()
	var reasons []domain.TriageRow
	for rows.Next() {
		row, err := scanReason(rows)
		if err != nil {
			return nil, err
		}
		reasons = append(reasons, row)
	}
	return reasons, MapErr(rows.Err())
}
