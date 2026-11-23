package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/domain"
)

const interruptionColumns = `id, at, date, from_task_id, from_block_id, to_task_id, op_id, deleted_at`

func scanInterruption(row rowScanner) (domain.Interruption, error) {
	var in domain.Interruption
	var at int64
	var deleted sql.Null[int64]
	if err := row.Scan(&in.ID, &at, &in.Date, &in.FromTaskID, &in.FromBlockID, &in.ToTaskID, &in.OpID, &deleted); err != nil {
		return domain.Interruption{}, MapErr(err)
	}
	in.At, in.DeletedAt = time.UnixMilli(at).UTC(), nullTime(deleted)
	return in, nil
}

// InsertInterruption inserts in, assigning its generated ID on success.
func InsertInterruption(ctx context.Context, q Q, in *domain.Interruption) error {
	var id domain.InterruptionID
	err := q.QueryRowContext(ctx, `INSERT INTO interruptions (
		at, date, from_task_id, from_block_id, to_task_id, op_id, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`, in.At.UnixMilli(), in.Date,
		in.FromTaskID, in.FromBlockID, in.ToTaskID, in.OpID, nullMillis(in.DeletedAt)).Scan(&id)
	if err != nil {
		return MapErr(err)
	}
	in.ID = id
	return nil
}

// GetInterruption returns an interruption, including tombstoned rows.
func GetInterruption(ctx context.Context, q Q, id domain.InterruptionID) (domain.Interruption, error) {
	return scanInterruption(q.QueryRowContext(ctx, `SELECT `+interruptionColumns+` FROM interruptions WHERE id = ?`, id))
}

// InterruptionsOn returns non-deleted interruptions on d, ordered by time then ID.
func InterruptionsOn(ctx context.Context, q Q, d civil.Date) ([]domain.Interruption, error) {
	return queryInterruptions(ctx, q, `SELECT `+interruptionColumns+` FROM interruptions
		WHERE deleted_at IS NULL AND date = ? ORDER BY at, id`, d)
}

// InterruptionsOfTask returns non-deleted interruptions from or to a task,
// ordered by time then ID.
func InterruptionsOfTask(ctx context.Context, q Q, task domain.TaskID) ([]domain.Interruption, error) {
	return queryInterruptions(ctx, q, `SELECT `+interruptionColumns+` FROM interruptions
		WHERE deleted_at IS NULL AND (from_task_id = ? OR to_task_id = ?) ORDER BY at, id`, task, task)
}

// queryInterruptions accepts only constant SQL from this package; values are bound.
func queryInterruptions(ctx context.Context, q Q, query string, args ...any) ([]domain.Interruption, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, MapErr(err)
	}
	defer rows.Close()
	var interruptions []domain.Interruption
	for rows.Next() {
		in, err := scanInterruption(rows)
		if err != nil {
			return nil, err
		}
		interruptions = append(interruptions, in)
	}
	return interruptions, MapErr(rows.Err())
}
