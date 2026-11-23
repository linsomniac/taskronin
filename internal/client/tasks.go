package client

import (
	"context"
	"net/http"
	"strconv"

	"github.com/linsomniac/taskronin/internal/api"
)

// AddTask captures a task with optional owner placement.
func (c *Client) AddTask(ctx context.Context, m Mut, r api.AddTaskRequest) (*api.OpResponse, error) {
	return c.mutateOp(ctx, m, http.MethodPost, "/tasks", r)
}

// UpdateTask patches task content and metadata.
func (c *Client) UpdateTask(ctx context.Context, m Mut, id int64, r api.UpdateTaskRequest) (*api.OpResponse, error) {
	return c.mutateOp(ctx, m, http.MethodPatch, taskPath(id), r)
}

// SetDue sets or clears the task's due moment.
func (c *Client) SetDue(ctx context.Context, m Mut, id int64, r api.DueRequest) (*api.OpResponse, error) {
	return c.mutateOp(ctx, m, http.MethodPost, taskPath(id)+"/due", r)
}

// PlaceTask places a task and acknowledges its triage reasons.
func (c *Client) PlaceTask(ctx context.Context, m Mut, id int64, r api.PlaceRequest) (*api.OpResponse, error) {
	return c.mutateOp(ctx, m, http.MethodPost, taskPath(id)+"/place", r)
}

// DeferTask sets or clears the task's deferral.
func (c *Client) DeferTask(ctx context.Context, m Mut, id int64, r api.DeferRequest) (*api.OpResponse, error) {
	return c.mutateOp(ctx, m, http.MethodPost, taskPath(id)+"/defer", r)
}

// BlockTask schedules a block for a task.
func (c *Client) BlockTask(ctx context.Context, m Mut, id int64, r api.BlockRequest) (*api.OpResponse, error) {
	return c.mutateOp(ctx, m, http.MethodPost, taskPath(id)+"/block", r)
}

// MarkManual adds a manual triage reason to a task.
func (c *Client) MarkManual(ctx context.Context, m Mut, id int64) (*api.OpResponse, error) {
	return c.mutateOp(ctx, m, http.MethodPost, taskPath(id)+"/triage", struct{}{})
}

// CompleteTask completes a task and records its completion entry.
func (c *Client) CompleteTask(ctx context.Context, m Mut, id int64, r api.CompleteRequest) (*api.OpResponse, error) {
	return c.mutateOp(ctx, m, http.MethodPost, taskPath(id)+"/complete", r)
}

// ReopenTask reopens a closed task, optionally removing its completion entry.
func (c *Client) ReopenTask(ctx context.Context, m Mut, id int64, r api.ReopenRequest) (*api.OpResponse, error) {
	return c.mutateOp(ctx, m, http.MethodPost, taskPath(id)+"/reopen", r)
}

// DropTask closes a task without a completion entry.
func (c *Client) DropTask(ctx context.Context, m Mut, id int64) (*api.OpResponse, error) {
	return c.mutateOp(ctx, m, http.MethodPost, taskPath(id)+"/drop", struct{}{})
}

// DeleteTask soft-deletes a task.
func (c *Client) DeleteTask(ctx context.Context, m Mut, id int64) (*api.OpResponse, error) {
	return c.mutateOp(ctx, m, http.MethodPost, taskPath(id)+"/delete", struct{}{})
}

// UndeleteTask restores a soft-deleted task.
func (c *Client) UndeleteTask(ctx context.Context, m Mut, id int64) (*api.OpResponse, error) {
	return c.mutateOp(ctx, m, http.MethodPost, taskPath(id)+"/undelete", struct{}{})
}

// AcceptNew accepts new tasks atomically using the per-item ETags; m.IfMatch is unused.
func (c *Client) AcceptNew(ctx context.Context, m Mut, r api.AcceptNewRequest) (*api.OpResponse, error) {
	m.IfMatch = ""
	return c.mutateOp(ctx, m, http.MethodPost, "/triage/accept-new", r)
}

func taskPath(id int64) string { return "/tasks/" + strconv.FormatInt(id, 10) }

func (c *Client) mutateOp(ctx context.Context, m Mut, method, path string, body any) (*api.OpResponse, error) {
	var out api.OpResponse
	if _, err := c.do(ctx, call{Kind: callKeyed, Method: method, Path: path, Body: body, Mut: m}, &out); err != nil { //nolint:bodyclose // do consumes and closes the response body.
		return nil, err
	}
	return &out, nil
}
