package mcpserver

import (
	"context"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/client"
	"github.com/linsomniac/taskronin/internal/datespec"
	"github.com/linsomniac/taskronin/internal/domain"
)

// writeAnnotations marks a tool non-destructive and closed-world.
func writeAnnotations(idempotent bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{DestructiveHint: ptr(false), IdempotentHint: idempotent, OpenWorldHint: ptr(false)}
}

// orNull sets the enum of a nullable property (project/priority) to values plus null.
func orNull(prop string, enumValues ...any) func(*jsonschema.Schema) {
	return func(s *jsonschema.Schema) {
		p := s.Properties[prop]
		if len(enumValues) > 0 {
			p.Enum = append(append([]any{}, enumValues...), nil)
		}
	}
}

// plainString drops the "null" alternative jsonschema.For adds to *string:
// title and notes may be absent but never null.
func plainString(props ...string) func(*jsonschema.Schema) {
	return func(s *jsonschema.Schema) {
		for _, name := range props {
			p := s.Properties[name]
			p.Types, p.Type = nil, "string"
		}
	}
}

func pattern(prop, re string) func(*jsonschema.Schema) {
	return func(s *jsonschema.Schema) { s.Properties[prop].Pattern = re }
}

// precondition returns the If-Match for a mutation of task id: the caller's
// expected_etag, else the etag of a fresh GetTask (fetch-then-act, A§10).
//
// AIDEV-NOTE: This is the only place MCP write tools read before writing. The
// fetch-then-act window is deliberate: it makes the tool's change apply to
// the version the model last saw only when it passes expected_etag; without
// it the tool acts on whatever is current. A 412 surfaces as a toolError with
// the server's `current` representation.
func precondition(ctx context.Context, c *client.Client, id int64, expected string) (client.Mut, error) {
	if expected != "" {
		return client.Mut{IfMatch: expected}, nil
	}
	d, err := c.GetTask(ctx, id)
	if err != nil {
		return client.Mut{}, err
	}
	return client.Mut{IfMatch: d.ETag}, nil
}

// momentOf converts a resolved datespec moment to its wire form.
func momentOf(m domain.Moment) api.Moment {
	out := api.Moment{Date: m.Date}
	if m.Min.Valid {
		out.Time = &m.Min.V
	}
	return out
}

// optMoment resolves s in mode m, or returns an explicit null for "none".
func optMoment(ctx context.Context, c *client.Client, s string, m datespec.Mode) (api.Opt[api.Moment], error) {
	if s == "none" {
		return api.Opt[api.Moment]{Set: true, Null: true}, nil
	}
	mo, err := resolveDate(ctx, c, s, m)
	if err != nil {
		return api.Opt[api.Moment]{}, err
	}
	return api.Opt[api.Moment]{Set: true, V: momentOf(mo)}, nil
}

// registerWriteTools registers the mutating tools.
//
// AIDEV-NOTE: There is deliberately no delete, drop,
// purge, or remove_entry tool: the owner decided MCP never deletes.
func registerWriteTools(s *mcp.Server, c *client.Client) {
	opOut := outputSchema[api.OpResponse]()

	addTool(s, &mcp.Tool{
		Name:        "add_task",
		Description: "Add a task. It always lands in triage for the user to place.",
		Annotations: writeAnnotations(false),
	}, opOut, func(ctx context.Context, in addTaskInput) (any, error) {
		req := api.AddTaskRequest{Title: in.Title, Notes: in.Notes, Tags: in.Tags, Project: in.Project, Priority: in.Priority}
		if in.Due != "" {
			m, err := resolveDate(ctx, c, in.Due, datespec.Future)
			if err != nil {
				return nil, err
			}
			mo := momentOf(m)
			req.Due = &mo
		}
		return c.AddTask(ctx, client.Mut{}, req)
	}, enum("priority", "H", "M", "L"), nonEmpty("title"))

	addTool(s, &mcp.Tool{
		Name:        "log_work",
		Description: "Log work done, against a task or (task_id null) standalone.",
		Annotations: writeAnnotations(false),
	}, opOut, func(ctx context.Context, in logWorkInput) (any, error) {
		req := api.AddEntryRequest{TaskID: in.TaskID, Title: in.Title, Notes: in.Notes}
		if in.Date != "" {
			m, err := resolveDate(ctx, c, in.Date, datespec.Past)
			if err != nil {
				return nil, err
			}
			req.WorkDate = &m.Date
		}
		if in.Time != "" {
			minute, err := civil.ParseHHMM(in.Time, false)
			if err != nil {
				return nil, domain.Errorf(domain.CodeValidation, "invalid time %q: use HH:MM", in.Time)
			}
			req.Time = &minute
		}
		return c.AddEntry(ctx, client.Mut{}, req)
	}, nonEmpty("title"), pattern("time", minutePattern))

	addTool(s, &mcp.Tool{
		Name:        "complete_task",
		Description: "Mark a task done, optionally logging a completion entry.",
		Annotations: writeAnnotations(false),
	}, opOut, func(ctx context.Context, in completeTaskInput) (any, error) {
		req := api.CompleteRequest{Log: in.LogTitle, Notes: in.Notes}
		if in.AsTitle != "" {
			req.As = &in.AsTitle
		}
		if in.Date != "" {
			m, err := resolveDate(ctx, c, in.Date, datespec.Past)
			if err != nil {
				return nil, err
			}
			req.Date = &m.Date
		}
		mut, err := precondition(ctx, c, in.ID, in.ExpectedETag)
		if err != nil {
			return nil, err
		}
		return c.CompleteTask(ctx, mut, in.ID, req)
	})

	addTool(s, &mcp.Tool{
		Name:        "reopen_task",
		Description: "Reopen a completed task. The completion entry is kept.",
		Annotations: writeAnnotations(false),
	}, opOut, func(ctx context.Context, in reopenTaskInput) (any, error) {
		mut, err := precondition(ctx, c, in.ID, in.ExpectedETag)
		if err != nil {
			return nil, err
		}
		return c.ReopenTask(ctx, mut, in.ID, api.ReopenRequest{})
	})

	addTool(s, &mcp.Tool{
		Name:        "defer_task",
		Description: "Hide a task until a date, or clear its deferral with until \"none\".",
		Annotations: writeAnnotations(false),
	}, opOut, func(ctx context.Context, in deferTaskInput) (any, error) {
		until, err := optMoment(ctx, c, in.Until, datespec.Future)
		if err != nil {
			return nil, err
		}
		mut, err := precondition(ctx, c, in.ID, in.ExpectedETag)
		if err != nil {
			return nil, err
		}
		return c.DeferTask(ctx, mut, in.ID, api.DeferRequest{Until: until})
	})

	addTool(s, &mcp.Tool{
		Name:        "set_due",
		Description: "Set a task's due date, or clear it with due \"none\".",
		Annotations: writeAnnotations(true),
	}, opOut, func(ctx context.Context, in setDueInput) (any, error) {
		due, err := optMoment(ctx, c, in.Due, datespec.Future)
		if err != nil {
			return nil, err
		}
		mut, err := precondition(ctx, c, in.ID, in.ExpectedETag)
		if err != nil {
			return nil, err
		}
		return c.SetDue(ctx, mut, in.ID, api.DueRequest{Due: due})
	})

	addTool(s, &mcp.Tool{
		Name:        "block_time",
		Description: "Schedule a time block for a task on a day (default today). end may be 24:00.",
		Annotations: writeAnnotations(false),
	}, opOut, func(ctx context.Context, in blockTimeInput) (any, error) {
		date := "today"
		if in.Date != "" {
			date = in.Date
		}
		m, err := resolveDate(ctx, c, date, datespec.Future)
		if err != nil {
			return nil, err
		}
		mut, err := precondition(ctx, c, in.TaskID, in.ExpectedETag)
		if err != nil {
			return nil, err
		}
		return c.BlockTask(ctx, mut, in.TaskID, api.BlockRequest{Date: m.Date, Start: in.Start, End: in.End})
	})

	addTool(s, &mcp.Tool{
		Name:        "update_task",
		Description: "Change a task's title, notes, tags, project or priority. project and priority accept null to clear.",
		Annotations: writeAnnotations(false),
	}, opOut, func(ctx context.Context, in updateTaskInput) (any, error) {
		req := api.UpdateTaskRequest{Project: in.Project, Priority: in.Priority, TagsAdd: in.TagsAdd, TagsRemove: in.TagsRemove}
		if in.Title != nil {
			req.Title = api.Opt[string]{Set: true, V: *in.Title}
		}
		if in.Notes != nil {
			req.Notes = api.Opt[string]{Set: true, V: *in.Notes}
		}
		mut, err := precondition(ctx, c, in.ID, in.ExpectedETag)
		if err != nil {
			return nil, err
		}
		return c.UpdateTask(ctx, mut, in.ID, req)
	}, plainString("title", "notes"), orNull("priority", "H", "M", "L"))

	addTool(s, &mcp.Tool{
		Name:        "place_task",
		Description: "Move a task in the backlog order: keep, top, bottom, or before/after the task ref_id.",
		Annotations: writeAnnotations(false),
	}, opOut, func(ctx context.Context, in placeTaskInput) (any, error) {
		mut, err := precondition(ctx, c, in.ID, in.ExpectedETag)
		if err != nil {
			return nil, err
		}
		return c.PlaceTask(ctx, mut, in.ID, api.PlaceRequest{How: in.How, Ref: in.RefID})
	}, enum("how", "keep", "top", "bottom", "before", "after"))
}
