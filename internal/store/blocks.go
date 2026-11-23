package store

import (
	"context"
	"database/sql"
	"slices"
	"time"

	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/domain"
)

const blockColumns = `id, task_id, date, start_min, end_min, status, outcome_source,
	cancel_reason, slipped, slipped_at, slipped_seq, created_at, resolved_at,
	resolved_seq, deleted_at, version`

const liveBlockSelect = `SELECT ` + blockColumns + ` FROM time_blocks WHERE deleted_at IS NULL AND `
const blockOrder = ` ORDER BY date, start_min, id`

func scanBlock(row rowScanner) (domain.TimeBlock, error) {
	var b domain.TimeBlock
	var source sql.Null[domain.OutcomeSource]
	var cancel sql.Null[domain.CancelReason]
	var slipped, resolved, deleted sql.Null[int64]
	var created int64
	if err := row.Scan(&b.ID, &b.TaskID, &b.Span.Date, &b.Span.Start, &b.Span.End,
		&b.Status, &source, &cancel, &b.Slipped, &slipped, &b.SlippedSeq, &created,
		&resolved, &b.ResolvedSeq, &deleted, &b.Version); err != nil {
		return domain.TimeBlock{}, MapErr(err)
	}
	b.OutcomeSource, b.CancelReason = source.V, cancel.V
	b.CreatedAt = time.UnixMilli(created).UTC()
	b.SlippedAt, b.ResolvedAt, b.DeletedAt = nullTime(slipped), nullTime(resolved), nullTime(deleted)
	return b, nil
}

// InsertBlock inserts b with a generated ID and version 1, assigning both on success.
func InsertBlock(ctx context.Context, q Q, b *domain.TimeBlock) error {
	var id domain.BlockID
	err := q.QueryRowContext(ctx, `INSERT INTO time_blocks (
		task_id, date, start_min, end_min, status, outcome_source, cancel_reason,
		slipped, slipped_at, slipped_seq, created_at, resolved_at, resolved_seq, deleted_at, version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1) RETURNING id`,
		b.TaskID, b.Span.Date, b.Span.Start, b.Span.End, b.Status, nullString(b.OutcomeSource),
		nullString(b.CancelReason), b.Slipped, nullMillis(b.SlippedAt), b.SlippedSeq,
		b.CreatedAt.UnixMilli(), nullMillis(b.ResolvedAt), b.ResolvedSeq, nullMillis(b.DeletedAt)).Scan(&id)
	if err != nil {
		return MapErr(err)
	}
	b.ID, b.Version = id, 1
	return nil
}

// GetBlock returns a block, including tombstoned rows.
func GetBlock(ctx context.Context, q Q, id domain.BlockID) (domain.TimeBlock, error) {
	// AIDEV-NOTE: ID lookups retain tombstones (A§2.8); the service needs
	// DeletedAt to distinguish a deleted target from a missing one.
	return scanBlock(q.QueryRowContext(ctx, `SELECT `+blockColumns+` FROM time_blocks WHERE id = ?`, id))
}

// UpdateBlock replaces mutable fields, preserving task_id, created_at and version.
func UpdateBlock(ctx context.Context, q Q, b domain.TimeBlock) error {
	result, err := q.ExecContext(ctx, `UPDATE time_blocks SET date = ?, start_min = ?, end_min = ?,
		status = ?, outcome_source = ?, cancel_reason = ?, slipped = ?, slipped_at = ?, slipped_seq = ?,
		resolved_at = ?, resolved_seq = ?, deleted_at = ? WHERE id = ?`,
		b.Span.Date, b.Span.Start, b.Span.End, b.Status, nullString(b.OutcomeSource), nullString(b.CancelReason),
		b.Slipped, nullMillis(b.SlippedAt), b.SlippedSeq, nullMillis(b.ResolvedAt), b.ResolvedSeq, nullMillis(b.DeletedAt), b.ID)
	return requireAffected(result, err)
}

// BlocksOfTask returns non-deleted blocks by date, start minute and ID.
func BlocksOfTask(ctx context.Context, q Q, task domain.TaskID) ([]domain.TimeBlock, error) {
	return queryBlocks(ctx, q, liveBlockSelect+`task_id = ?`+blockOrder, task)
}

// BlocksBetween returns non-deleted blocks in an inclusive date range, optionally
// restricted to a task. Zero dates leave that side of the range unbounded.
func BlocksBetween(ctx context.Context, q Q, from, to civil.Date, task sql.Null[domain.TaskID]) ([]domain.TimeBlock, error) {
	return queryBlocks(ctx, q, liveBlockSelect+`
		(?1 IS NULL OR date >= ?1) AND (?2 IS NULL OR date <= ?2)
		AND (?3 IS NULL OR task_id = ?3)`+blockOrder, nullDate(from), nullDate(to), task)
}

// PlannedOverlapping returns live planned blocks intersecting the half-open span,
// omitting any explicitly excluded IDs.
func PlannedOverlapping(ctx context.Context, q Q, span domain.BlockSpan, exclude ...domain.BlockID) ([]domain.TimeBlock, error) {
	blocks, err := queryBlocks(ctx, q, liveBlockSelect+`
		status = 'planned' AND date = ? AND start_min < ? AND end_min > ?`+blockOrder,
		span.Date, span.End, span.Start)
	if err != nil {
		return nil, err
	}
	// Filter the bounded day's candidates without constructing an SQL IN list.
	return slices.DeleteFunc(blocks, func(b domain.TimeBlock) bool { return slices.Contains(exclude, b.ID) }), nil
}

// PlannedOn returns live planned blocks on d, by start minute and ID.
func PlannedOn(ctx context.Context, q Q, d civil.Date) ([]domain.TimeBlock, error) {
	return queryBlocks(ctx, q, liveBlockSelect+`status = 'planned' AND date = ?`+blockOrder, d)
}

// PlannedBefore returns live planned blocks strictly before d, by date, start and ID.
func PlannedBefore(ctx context.Context, q Q, d civil.Date) ([]domain.TimeBlock, error) {
	return queryBlocks(ctx, q, liveBlockSelect+`status = 'planned' AND date < ?`+blockOrder, d)
}

// PlannedOfTask returns a task's live planned blocks by date, start and ID.
func PlannedOfTask(ctx context.Context, q Q, task domain.TaskID) ([]domain.TimeBlock, error) {
	return queryBlocks(ctx, q, liveBlockSelect+`status = 'planned' AND task_id = ?`+blockOrder, task)
}

// AutoResolvedOn returns live automatically resolved blocks of a task on d.
func AutoResolvedOn(ctx context.Context, q Q, task domain.TaskID, d civil.Date) ([]domain.TimeBlock, error) {
	return queryBlocks(ctx, q, liveBlockSelect+`task_id = ? AND date = ? AND outcome_source = 'auto'`+blockOrder, task, d)
}

// HasPlannedAfter reports whether a task has a live planned block strictly after d.
func HasPlannedAfter(ctx context.Context, q Q, task domain.TaskID, d civil.Date) (bool, error) {
	var exists bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM time_blocks
		WHERE deleted_at IS NULL AND task_id = ? AND status = 'planned' AND date > ?)`, task, d).Scan(&exists)
	return exists, MapErr(err)
}

// HasPlannedOn reports whether a task has a live planned block on d.
func HasPlannedOn(ctx context.Context, q Q, task domain.TaskID, d civil.Date) (bool, error) {
	var exists bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM time_blocks
		WHERE deleted_at IS NULL AND task_id = ? AND status = 'planned' AND date = ?)`, task, d).Scan(&exists)
	return exists, MapErr(err)
}

// OtherSlippedSince reports whether another live block is slipped with slipped_seq > seq.
func OtherSlippedSince(ctx context.Context, q Q, task domain.TaskID, exclude domain.BlockID, seq int64) (bool, error) {
	var exists bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM time_blocks
		WHERE deleted_at IS NULL AND task_id = ? AND id <> ? AND slipped = 1 AND slipped_seq > ?)`,
		task, exclude, seq).Scan(&exists)
	return exists, MapErr(err)
}

// OtherWorkedSince reports whether another live block is worked with resolved_seq > seq.
func OtherWorkedSince(ctx context.Context, q Q, task domain.TaskID, exclude domain.BlockID, seq int64) (bool, error) {
	var exists bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM time_blocks
		WHERE deleted_at IS NULL AND task_id = ? AND id <> ? AND status = 'worked' AND resolved_seq > ?)`,
		task, exclude, seq).Scan(&exists)
	return exists, MapErr(err)
}

// SlipCounts counts live slipped blocks in total and with slipped_seq > lastPlacedSeq.
func SlipCounts(ctx context.Context, q Q, task domain.TaskID, lastPlacedSeq int64) (lifetime, sinceTriage int, err error) {
	err = q.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(CASE WHEN slipped_seq > ? THEN 1 END)
		FROM time_blocks WHERE deleted_at IS NULL AND task_id = ? AND slipped = 1`,
		lastPlacedSeq, task).Scan(&lifetime, &sinceTriage)
	return lifetime, sinceTriage, MapErr(err)
}

// queryBlocks accepts only constant SQL from this package; values are bound.
func queryBlocks(ctx context.Context, q Q, query string, args ...any) ([]domain.TimeBlock, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, MapErr(err)
	}
	defer rows.Close()
	var blocks []domain.TimeBlock
	for rows.Next() {
		b, err := scanBlock(rows)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, b)
	}
	return blocks, MapErr(rows.Err())
}
