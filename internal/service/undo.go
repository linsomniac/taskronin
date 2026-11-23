package service

import (
	"context"

	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/store"
)

type undoRowKey struct {
	Actor string
	Stack store.Stack
	Seq   int64
}

// AIDEV-NOTE: A terminal stack failure commits only this exact row's removal
// and the replayable 409. Ordinary reversal conflicts must never use this type:
// they leave both stacks intact so the caller can resolve the conflict and retry.
type committedError struct {
	Err      *domain.Error
	StackRow undoRowKey
}

// Error returns the terminal stack error's safe message.
func (e *committedError) Error() string { return e.Err.Error() }

// Unwrap exposes the terminal domain error.
func (e *committedError) Unwrap() error { return e.Err }

// Undo reverses the newest operation on the caller's undo stack.
func (s *Service) Undo(ctx context.Context, m Meta) (*OpResult, error) {
	return s.run(ctx, m, opSpec{kind: domain.OpUndo, class: classUndo, idem: true, revertible: true}, func(o *op) error {
		return o.revertTx(*o.target)
	})
}

// Redo reverses the active reversal of the caller's newest redo entry.
func (s *Service) Redo(ctx context.Context, m Meta) (*OpResult, error) {
	return s.run(ctx, m, opSpec{kind: domain.OpRedo, class: classRedo, idem: true, revertible: true}, func(o *op) error {
		return o.revertTx(*o.target)
	})
}

func selectStackOp(ctx context.Context, q store.Q, actor string, class opClass) (*domain.OpRecord, undoRowKey, error) {
	row := undoRowKey{Actor: actor, Stack: store.StackUndo}
	code := domain.CodeNothingToUndo
	if class == classRedo {
		row.Stack, code = store.StackRedo, domain.CodeNothingToRedo
	}
	seq, id, ok, err := store.TopOfStack(ctx, q, actor, row.Stack)
	if err != nil {
		return nil, row, err
	}
	if !ok {
		return nil, row, domain.Errorf(code, "nothing to %s", row.Stack)
	}
	row.Seq = seq
	target, err := store.GetOp(ctx, q, id)
	return &target, row, err
}

// resolveStackTarget runs after authorization of the popped op. Redo reverses
// that op's active reversal but still moves the original stack entry.
func resolveStackTarget(ctx context.Context, q store.Q, target *domain.OpRecord, row undoRowKey) (*domain.OpRecord, error) {
	terminal := func(code domain.Code, detail string) (*domain.OpRecord, error) {
		return nil, &committedError{Err: &domain.Error{Code: code, Detail: detail}, StackRow: row}
	}
	if row.Stack == store.StackUndo && !target.Revertible {
		return terminal(domain.CodeNotRevertible, "operation is not revertible")
	}
	if row.Stack == store.StackUndo && target.State == domain.OpActive {
		return target, nil
	}
	reversal, err := store.ActiveReversal(ctx, q, target.ID)
	if err != nil {
		return nil, err
	}
	if row.Stack == store.StackUndo {
		if !reversal.Valid {
			return terminal(domain.CodeRevertConflict, "operation is already reverted")
		}
		return terminal(domain.CodeRevertConflict, "already reverted by "+reversal.V.Actor)
	}
	if target.State != domain.OpReverted || !reversal.Valid {
		return terminal(domain.CodeRevertConflict, "operation has no active reversal to redo")
	}
	if !target.Revertible || !reversal.V.Revertible {
		return terminal(domain.CodeNotRevertible, "operation is not revertible")
	}
	return &reversal.V, nil
}

func (s *Service) moveStackOp(ctx context.Context, q store.Q, row undoRowKey, id domain.OpID) error {
	destination := store.StackRedo
	if row.Stack == store.StackRedo {
		destination = store.StackUndo
	}
	if err := store.DeleteStackRow(ctx, q, row.Actor, row.Stack, row.Seq); err != nil {
		return err
	}
	if err := store.PushStack(ctx, q, row.Actor, destination, id); err != nil {
		return err
	}
	return store.TrimStack(ctx, q, row.Actor, destination, s.undoDepth)
}
