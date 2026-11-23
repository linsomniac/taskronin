package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/linsomniac/taskronin/internal/domain"
)

// RankPos identifies a position in the global (rank, id) task order.
type RankPos struct {
	ID   domain.TaskID
	Rank int64
}

// RankBounds returns the minimum and maximum rank over all tasks, or NULLs when empty.
func RankBounds(ctx context.Context, q Q) (lo, hi sql.Null[int64], err error) {
	err = q.QueryRowContext(ctx, `SELECT MIN(rank), MAX(rank) FROM tasks`).Scan(&lo, &hi)
	return lo, hi, MapErr(err)
}

func scanRank(row rowScanner) (sql.Null[RankPos], error) {
	var p RankPos
	err := row.Scan(&p.ID, &p.Rank)
	if errors.Is(err, sql.ErrNoRows) {
		return sql.Null[RankPos]{}, nil
	}
	if err != nil {
		return sql.Null[RankPos]{}, MapErr(err)
	}
	return sql.Null[RankPos]{V: p, Valid: true}, nil
}

// RankPrev returns the predecessor of p, including closed and tombstoned tasks.
func RankPrev(ctx context.Context, q Q, p RankPos) (sql.Null[RankPos], error) {
	return scanRank(q.QueryRowContext(ctx, `SELECT id, rank FROM tasks
		WHERE (rank, id) < (?, ?) ORDER BY rank DESC, id DESC LIMIT 1`, p.Rank, p.ID))
}

// RankNext returns the successor of p, including closed and tombstoned tasks.
func RankNext(ctx context.Context, q Q, p RankPos) (sql.Null[RankPos], error) {
	return scanRank(q.QueryRowContext(ctx, `SELECT id, rank FROM tasks
		WHERE (rank, id) > (?, ?) ORDER BY rank, id LIMIT 1`, p.Rank, p.ID))
}

// SetRank changes only the task's rank.
func SetRank(ctx context.Context, q Q, id domain.TaskID, rank int64) error {
	result, err := q.ExecContext(ctx, `UPDATE tasks SET rank = ? WHERE id = ?`, rank, id)
	return requireAffected(result, err)
}

// RenormalizeRanks assigns successive multiples of 65536 in global order.
// Versions, timestamps and events are untouched.
func RenormalizeRanks(ctx context.Context, q Q) error {
	// AIDEV-NOTE: A§4.8 requires all tasks, including closed and tombstoned rows.
	_, err := q.ExecContext(ctx, `WITH r AS (SELECT id, ROW_NUMBER() OVER (ORDER BY rank, id) n FROM tasks) UPDATE tasks SET rank = (SELECT n FROM r WHERE r.id = tasks.id) * 65536`)
	return MapErr(err)
}

// RankTaken reports whether a task other than id holds rank.
func RankTaken(ctx context.Context, q Q, id domain.TaskID, rank int64) (bool, error) {
	var taken bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM tasks WHERE rank = ? AND id <> ?)`, rank, id).Scan(&taken)
	return taken, MapErr(err)
}
