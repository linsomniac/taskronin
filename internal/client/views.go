package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/civil"
)

// Health checks the server's unversioned health endpoint.
func (c *Client) Health(ctx context.Context) error {
	_, err := c.do(ctx, call{Kind: callRead, Method: http.MethodGet, Path: "/healthz"}, nil) //nolint:bodyclose // do consumes and closes the response body.
	return err
}

// Clock reads the server clock and instance settings.
func (c *Client) Clock(ctx context.Context) (*api.Clock, error) {
	var out api.Clock
	if _, err := c.do(ctx, call{Kind: callRead, Method: http.MethodGet, Path: "/clock"}, &out); err != nil { //nolint:bodyclose // do consumes and closes the response body.
		return nil, err
	}
	return &out, nil
}

// WhoAmI reads the authenticated actor, scopes and current database generation.
func (c *Client) WhoAmI(ctx context.Context) (*api.WhoAmI, error) {
	var out api.WhoAmI
	if _, err := c.do(ctx, call{Kind: callRead, Method: http.MethodGet, Path: "/whoami"}, &out); err != nil { //nolint:bodyclose // do consumes and closes the response body.
		return nil, err
	}
	return &out, nil
}

// Today reads the dashboard, optionally reusing a cached representation.
func (c *Client) Today(ctx context.Context, ifNoneMatch string) (*api.Today, string, bool, error) {
	return readAggregate[api.Today](ctx, c, "/today", ifNoneMatch)
}

// Status reads the compact status, optionally reusing a cached representation.
func (c *Client) Status(ctx context.Context, ifNoneMatch string) (*api.Status, string, bool, error) {
	return readAggregate[api.Status](ctx, c, "/status", ifNoneMatch)
}

func readAggregate[T any](ctx context.Context, c *Client, path, etag string) (*T, string, bool, error) {
	var out T
	resp, err := c.do(ctx, call{Kind: callRead, Method: http.MethodGet, Path: path, IfNoneMatch: etag}, &out) //nolint:bodyclose // do consumes and closes the response body.
	if err != nil {
		return nil, "", false, err
	}
	etag = resp.Header.Get("ETag")
	if resp.StatusCode == http.StatusNotModified {
		return nil, etag, true, nil
	}
	return &out, etag, false, nil
}

func readView[T any](ctx context.Context, c *Client, path string, query url.Values) (*T, error) {
	var out T
	if _, err := c.do(ctx, call{Kind: callRead, Method: http.MethodGet, Path: path, Query: query}, &out); err != nil { //nolint:bodyclose // do consumes and closes the response body.
		return nil, err
	}
	return &out, nil
}

// Day reads the recap for a civil date.
func (c *Client) Day(ctx context.Context, d civil.Date) (*api.Day, error) {
	return readView[api.Day](ctx, c, "/days/"+d.String(), nil)
}

// Upcoming reads deferred and soon-due tasks.
func (c *Client) Upcoming(ctx context.Context) (*api.Upcoming, error) {
	return readView[api.Upcoming](ctx, c, "/upcoming", nil)
}

// Report reads work over a date range with optional current task sections.
func (c *Client) Report(ctx context.Context, q ReportQuery) (*api.Report, error) {
	values := url.Values{"tag": {q.Tag}, "project": {q.Project}, "todo": {strconv.FormatBool(q.Todo)}, "upcoming": {strconv.FormatBool(q.Upcoming)}}
	if !q.From.IsZero() {
		values.Set("from", q.From.String())
	}
	if !q.To.IsZero() {
		values.Set("to", q.To.String())
	}
	return readView[api.Report](ctx, c, "/report", values)
}

// Search searches tasks or entries with pagination.
func (c *Client) Search(ctx context.Context, q SearchQuery) (*api.Page[api.SearchHit], error) {
	values := pageValues(q.PageQuery)
	values.Set("q", q.Q)
	values.Set("kind", q.Kind)
	return readView[api.Page[api.SearchHit]](ctx, c, "/search", values)
}

// Tags counts tags on open tasks.
func (c *Client) Tags(ctx context.Context) ([]api.TagCount, error) {
	out, err := readView[[]api.TagCount](ctx, c, "/tags", nil)
	if err != nil {
		return nil, err
	}
	return *out, nil
}

// ListTasks reads a task view with optional filters and pagination.
func (c *Client) ListTasks(ctx context.Context, q TaskQuery) (*api.Page[api.Task], error) {
	values := pageValues(q.PageQuery)
	values.Set("view", q.View)
	values.Set("tag", q.Tag)
	values.Set("project", q.Project)
	return readView[api.Page[api.Task]](ctx, c, "/tasks", values)
}

// GetTask reads a task and its associated records.
func (c *Client) GetTask(ctx context.Context, id int64) (*api.TaskDetail, error) {
	return readView[api.TaskDetail](ctx, c, "/tasks/"+strconv.FormatInt(id, 10), nil)
}

// TaskHistory reads a page of a task's audit events.
func (c *Client) TaskHistory(ctx context.Context, id int64, p PageQuery) (*api.Page[api.EventView], error) {
	return readView[api.Page[api.EventView]](ctx, c, "/tasks/"+strconv.FormatInt(id, 10)+"/history", pageValues(p))
}

// ListEntries reads work and completion entries.
func (c *Client) ListEntries(ctx context.Context, q EntryQuery) (*api.Page[api.Entry], error) {
	values := pageValues(q.PageQuery)
	rangeValues(values, q.From, q.To, q.Task)
	if q.Standalone {
		values.Set("task", "none")
	}
	values.Set("include_deleted", strconv.FormatBool(q.IncludeDeleted))
	return readView[api.Page[api.Entry]](ctx, c, "/entries", values)
}

// GetEntry reads an entry with its linked task's title.
func (c *Client) GetEntry(ctx context.Context, id int64) (*api.Entry, error) {
	return readView[api.Entry](ctx, c, "/entries/"+strconv.FormatInt(id, 10), nil)
}

// ListBlocks reads blocks by date or concrete task.
func (c *Client) ListBlocks(ctx context.Context, q BlockQuery) ([]api.Block, error) {
	values := make(url.Values)
	rangeValues(values, q.From, q.To, q.Task)
	out, err := readView[[]api.Block](ctx, c, "/blocks", values)
	if err != nil {
		return nil, err
	}
	return *out, nil
}

// GetBlock reads a block with both entity and task ETags.
func (c *Client) GetBlock(ctx context.Context, id int64) (*api.Block, error) {
	return readView[api.Block](ctx, c, "/blocks/"+strconv.FormatInt(id, 10), nil)
}

// ListOps reads a page of operations matching the actor, op ID prefix and sequence filters.
func (c *Client) ListOps(ctx context.Context, q OpsQuery) (*api.Page[api.OpSummary], error) {
	values := pageValues(q.PageQuery)
	values.Set("actor", q.Actor)
	if q.OpPrefix != "" {
		values.Set("op_prefix", q.OpPrefix)
	}
	values.Set("since_seq", strconv.FormatInt(q.SinceSeq, 10))
	return readView[api.Page[api.OpSummary]](ctx, c, "/ops", values)
}

// GetOp reads an operation and its audit events.
func (c *Client) GetOp(ctx context.Context, opID string) (*api.OpDetail, error) {
	return readView[api.OpDetail](ctx, c, "/ops/"+opID, nil)
}

// AIChanges reads token changes and the owner's review progress.
func (c *Client) AIChanges(ctx context.Context, all bool, p PageQuery) (*api.AIChanges, error) {
	values := pageValues(p)
	values.Set("all", strconv.FormatBool(all))
	return readView[api.AIChanges](ctx, c, "/ai-changes", values)
}

// Settings reads the instance's mutable settings.
func (c *Client) Settings(ctx context.Context) (*api.Settings, error) {
	return readView[api.Settings](ctx, c, "/settings", nil)
}

func pageValues(p PageQuery) url.Values {
	values := make(url.Values)
	if p.Limit != 0 {
		values.Set("limit", strconv.Itoa(p.Limit))
	}
	if p.Cursor != "" {
		values.Set("cursor", p.Cursor)
	}
	return values
}

func rangeValues(values url.Values, from, to *civil.Date, task *int64) {
	if from != nil {
		values.Set("from", from.String())
	}
	if to != nil {
		values.Set("to", to.String())
	}
	if task != nil {
		values.Set("task", strconv.FormatInt(*task, 10))
	}
}
