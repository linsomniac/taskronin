package client

import (
	"context"
	"net/http"
	"strconv"

	"github.com/linsomniac/taskronin/internal/api"
)

// AddEntry logs work against an explicit task or as a standalone entry.
func (c *Client) AddEntry(ctx context.Context, m Mut, r api.AddEntryRequest) (*api.OpResponse, error) {
	return c.mutateOp(ctx, m, http.MethodPost, "/entries", r)
}

// UpdateEntry patches an entry's content, work date and time.
func (c *Client) UpdateEntry(ctx context.Context, m Mut, id int64, r api.UpdateEntryRequest) (*api.OpResponse, error) {
	return c.mutateOp(ctx, m, http.MethodPatch, entryPath(id), r)
}

// RelinkEntry changes or clears an entry's task link.
func (c *Client) RelinkEntry(ctx context.Context, m Mut, id int64, r api.RelinkRequest) (*api.OpResponse, error) {
	return c.mutateOp(ctx, m, http.MethodPost, entryPath(id)+"/relink", r)
}

// DeleteEntry soft-deletes an entry.
func (c *Client) DeleteEntry(ctx context.Context, m Mut, id int64) (*api.OpResponse, error) {
	return c.mutateOp(ctx, m, http.MethodPost, entryPath(id)+"/delete", struct{}{})
}

// UndeleteEntry restores a soft-deleted entry.
func (c *Client) UndeleteEntry(ctx context.Context, m Mut, id int64) (*api.OpResponse, error) {
	return c.mutateOp(ctx, m, http.MethodPost, entryPath(id)+"/undelete", struct{}{})
}

func entryPath(id int64) string { return "/entries/" + strconv.FormatInt(id, 10) }
