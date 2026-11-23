package client

import (
	"context"
	"net/http"

	"github.com/linsomniac/taskronin/internal/api"
)

// SetFocus sets or clears today's focus without an entity precondition.
func (c *Client) SetFocus(ctx context.Context, m Mut, r api.FocusRequest) (*api.OpResponse, error) {
	return c.mutateOp(ctx, m, http.MethodPost, "/focus", r)
}

// Revert reverses an operation by its operation ID.
func (c *Client) Revert(ctx context.Context, m Mut, opID string) (*api.OpResponse, error) {
	return c.mutateOp(ctx, m, http.MethodPost, "/ops/"+opID+"/revert", struct{}{})
}

// Undo reverses the latest undoable operation on the caller's stack.
func (c *Client) Undo(ctx context.Context, m Mut) (*api.OpResponse, error) {
	return c.mutateOp(ctx, m, http.MethodPost, "/undo", struct{}{})
}

// Redo reapplies the latest undone operation on the caller's stack.
func (c *Client) Redo(ctx context.Context, m Mut) (*api.OpResponse, error) {
	return c.mutateOp(ctx, m, http.MethodPost, "/redo", struct{}{})
}

// ReviewAIChanges advances the owner's AI review watermark.
func (c *Client) ReviewAIChanges(ctx context.Context, m Mut, r api.ReviewRequest) (*api.OpResponse, error) {
	return c.mutateOp(ctx, m, http.MethodPost, "/ai-changes/review", r)
}
