package client

import (
	"context"
	"net/http"
	"strconv"

	"github.com/linsomniac/taskronin/internal/api"
)

// Reblock reschedules a block using both block and task preconditions.
func (c *Client) Reblock(ctx context.Context, m Mut, blockID int64, r api.ReblockRequest) (*api.OpResponse, error) {
	return c.mutateOp(ctx, m, http.MethodPost, blockPath(blockID)+"/reblock", r)
}

// Unblock cancels an unstarted block or resolves one that has started.
func (c *Client) Unblock(ctx context.Context, m Mut, blockID int64) (*api.OpResponse, error) {
	return c.mutateOp(ctx, m, http.MethodPost, blockPath(blockID)+"/unblock", struct{}{})
}

// SetOutcome sets a block's outcome or restores automatic resolution.
func (c *Client) SetOutcome(ctx context.Context, m Mut, blockID int64, r api.OutcomeRequest) (*api.OpResponse, error) {
	return c.mutateOp(ctx, m, http.MethodPost, blockPath(blockID)+"/outcome", r)
}

func blockPath(id int64) string { return "/blocks/" + strconv.FormatInt(id, 10) }
