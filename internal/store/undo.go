package store

import (
	"context"
	"errors"

	"github.com/linsomniac/taskronin/internal/domain"
)

// Stack identifies an actor's undo or redo stack.
type Stack string

const (
	// StackUndo contains operations available to undo.
	StackUndo Stack = "undo"
	// StackRedo contains operations available to redo.
	StackRedo Stack = "redo"
)

// PushStack appends op with one greater than the current maximum actor/stack sequence.
func PushStack(ctx context.Context, q Q, actor string, s Stack, op domain.OpID) error {
	_, err := q.ExecContext(ctx, `INSERT INTO undo_stack (actor, stack, seq, op_id)
		SELECT ?1, ?2, COALESCE(MAX(seq), 0) + 1, ?3 FROM undo_stack WHERE actor = ?1 AND stack = ?2`, actor, s, op)
	return MapErr(err)
}

// TopOfStack returns the newest row, with ok false for an empty stack.
func TopOfStack(ctx context.Context, q Q, actor string, s Stack) (seq int64, op domain.OpID, ok bool, err error) {
	err = MapErr(q.QueryRowContext(ctx, `SELECT seq, op_id FROM undo_stack
		WHERE actor = ? AND stack = ? ORDER BY seq DESC LIMIT 1`, actor, s).Scan(&seq, &op))
	if errors.Is(err, ErrNotFound) {
		return 0, domain.OpID{}, false, nil
	}
	return seq, op, err == nil, err
}

// DeleteStackRow removes the specified actor/stack sequence, if present.
func DeleteStackRow(ctx context.Context, q Q, actor string, s Stack, seq int64) error {
	_, err := q.ExecContext(ctx, `DELETE FROM undo_stack WHERE actor = ? AND stack = ? AND seq = ?`, actor, s, seq)
	return MapErr(err)
}

// ClearStack removes every row from the actor's selected stack.
func ClearStack(ctx context.Context, q Q, actor string, s Stack) error {
	_, err := q.ExecContext(ctx, `DELETE FROM undo_stack WHERE actor = ? AND stack = ?`, actor, s)
	return MapErr(err)
}

// TrimStack keeps only the newest depth rows in the actor's selected stack.
func TrimStack(ctx context.Context, q Q, actor string, s Stack, depth int) error {
	_, err := q.ExecContext(ctx, `DELETE FROM undo_stack WHERE actor = ?1 AND stack = ?2
		AND seq IN (SELECT seq FROM undo_stack WHERE actor = ?1 AND stack = ?2
		ORDER BY seq DESC LIMIT -1 OFFSET ?3)`, actor, s, depth)
	return MapErr(err)
}

// StackOps returns the actor's selected stack from bottom to top.
func StackOps(ctx context.Context, q Q, actor string, s Stack) ([]domain.OpID, error) {
	rows, err := q.QueryContext(ctx, `SELECT op_id FROM undo_stack WHERE actor = ? AND stack = ? ORDER BY seq`, actor, s)
	if err != nil {
		return nil, MapErr(err)
	}
	defer rows.Close()
	var ops []domain.OpID
	for rows.Next() {
		var op domain.OpID
		if err := rows.Scan(&op); err != nil {
			return nil, MapErr(err)
		}
		ops = append(ops, op)
	}
	return ops, MapErr(rows.Err())
}
