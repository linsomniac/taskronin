package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/linsomniac/taskronin/internal/domain"
)

const opColumns = `ops.seq, ops.op_id, ops.kind, ops.actor, ops.channel, ops.at,
	ops.scopes, ops.revertible, ops.state, ops.reverts_op_id, ops.via`

const aiOpFilter = `(ops.actor LIKE 'token:%' OR ops.kind = 'settings.retention_lowered')`

// opScan shares the operation decoder with joined event queries.
type opScan struct {
	record domain.OpRecord
	at     int64
	scopes string
	via    sql.Null[string]
}

func (s *opScan) fields() []any {
	r := &s.record
	return []any{&r.Seq, &r.ID, &r.Kind, &r.Actor, &r.Channel, &s.at,
		&s.scopes, &r.Revertible, &r.State, &r.RevertsOpID, &s.via}
}

func (s *opScan) value() (domain.OpRecord, error) {
	scopes, err := domain.ParseScopes(s.scopes)
	if err != nil {
		return domain.OpRecord{}, MapErr(err)
	}
	s.record.At, s.record.Scopes, s.record.Via = time.UnixMilli(s.at).UTC(), scopes, s.via.V
	return s.record, nil
}

func scanOp(row rowScanner) (domain.OpRecord, error) {
	var s opScan
	if err := row.Scan(s.fields()...); err != nil {
		return domain.OpRecord{}, MapErr(err)
	}
	return s.value()
}

// InsertOp inserts r, assigning its generated sequence on success.
func InsertOp(ctx context.Context, q Q, r *domain.OpRecord) error {
	var seq domain.OpSeq
	// AIDEV-NOTE: via and reverts_op_id must be inserted together to satisfy
	// the reversal-pair CHECK, including eventless undo/redo operations.
	err := q.QueryRowContext(ctx, `INSERT INTO ops
		(op_id, kind, actor, channel, at, scopes, revertible, state, reverts_op_id, via)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING seq`,
		r.ID, r.Kind, r.Actor, r.Channel, r.At.UnixMilli(), r.Scopes.String(),
		r.Revertible, r.State, r.RevertsOpID, nullString(r.Via)).Scan(&seq)
	if err != nil {
		return MapErr(err)
	}
	r.Seq = seq
	return nil
}

// GetOp returns an operation, or ErrNotFound if its ID is absent.
func GetOp(ctx context.Context, q Q, id domain.OpID) (domain.OpRecord, error) {
	return scanOp(q.QueryRowContext(ctx, `SELECT `+opColumns+` FROM ops WHERE op_id = ?`, id))
}

// SetOpState changes an operation's reversal state.
func SetOpState(ctx context.Context, q Q, id domain.OpID, s domain.OpState) error {
	result, err := q.ExecContext(ctx, `UPDATE ops SET state = ? WHERE op_id = ?`, s, id)
	return requireAffected(result, err)
}

// SetOpKind sets the kind of an operation whose classification depends on transactional state.
func SetOpKind(ctx context.Context, q Q, id domain.OpID, kind domain.OpKind) error {
	result, err := q.ExecContext(ctx, `UPDATE ops SET kind = ? WHERE op_id = ?`, kind, id)
	return requireAffected(result, err)
}

// ActiveReversal returns the active operation reverting target, if one exists.
func ActiveReversal(ctx context.Context, q Q, target domain.OpID) (sql.Null[domain.OpRecord], error) {
	r, err := scanOp(q.QueryRowContext(ctx, `SELECT `+opColumns+` FROM ops
		WHERE reverts_op_id = ? AND state = 'active' ORDER BY seq DESC LIMIT 1`, target))
	if errors.Is(err, ErrNotFound) {
		return sql.Null[domain.OpRecord]{}, nil
	}
	if err != nil {
		return sql.Null[domain.OpRecord]{}, err
	}
	return sql.Null[domain.OpRecord]{V: r, Valid: true}, nil
}

// LatestSeq returns the greatest operation sequence, or zero for an empty store.
func LatestSeq(ctx context.Context, q Q) (domain.OpSeq, error) {
	var seq domain.OpSeq
	err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM ops`).Scan(&seq)
	return seq, MapErr(err)
}

// CountOpsSince counts actor's operations strictly after since and returns their oldest time.
func CountOpsSince(ctx context.Context, q Q, actor string, since time.Time) (n int, oldest sql.Null[time.Time], err error) {
	var at sql.Null[int64]
	err = q.QueryRowContext(ctx, `SELECT COUNT(*), MIN(at) FROM ops WHERE actor = ? AND at > ?`, actor, since.UnixMilli()).Scan(&n, &at)
	return n, nullTime(at), MapErr(err)
}

// OpQuery selects operations with optional actor, AI, op ID prefix and
// exclusive sequence filters.
type OpQuery struct {
	Actor               string
	AIOnly              bool
	IDPrefix            string       // Lowercase hex, validated by the caller; empty means any.
	SinceSeq, BeforeSeq domain.OpSeq // Zero means unbounded.
	Limit               int
}

// ListOps returns matching operations in descending sequence order.
func ListOps(ctx context.Context, q Q, oq OpQuery) ([]domain.OpRecord, error) {
	// AIDEV-NOTE: the prefix match is a range: op IDs are lowercase hex, and 'g'
	// sorts after every hex digit, so [p, p||'g') holds exactly the IDs starting with p.
	rows, err := q.QueryContext(ctx, `SELECT `+opColumns+` FROM ops
		WHERE (?1 = '' OR actor = ?1) AND (?2 = 0 OR `+aiOpFilter+`)
		AND (?3 = 0 OR seq > ?3) AND (?4 = 0 OR seq < ?4)
		AND (?6 = '' OR (op_id >= ?6 AND op_id < ?6 || 'g')) ORDER BY seq DESC LIMIT ?5`,
		oq.Actor, oq.AIOnly, oq.SinceSeq, oq.BeforeSeq, oq.Limit, oq.IDPrefix)
	if err != nil {
		return nil, MapErr(err)
	}
	defer rows.Close()
	var ops []domain.OpRecord
	for rows.Next() {
		r, err := scanOp(rows)
		if err != nil {
			return nil, err
		}
		ops = append(ops, r)
	}
	return ops, MapErr(rows.Err())
}

// CountAIOps counts token and retention-lowering operations strictly after afterSeq.
func CountAIOps(ctx context.Context, q Q, afterSeq domain.OpSeq) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM ops WHERE seq > ? AND `+aiOpFilter, afterSeq).Scan(&n)
	return n, MapErr(err)
}
