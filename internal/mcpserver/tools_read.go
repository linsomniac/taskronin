package mcpserver

import (
	"context"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/datespec"
	"taskronin/internal/domain"
)

const (
	defaultLimit = 50
	maxLimit     = 200
)

// readAnnotations marks a tool read-only and closed-world. An unset
// OpenWorldHint means open-world in the SDK, so it must be set explicitly.
func readAnnotations() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}
}

// limitBounds restricts the "limit" property to 1..200.
func limitBounds(s *jsonschema.Schema) {
	p := s.Properties["limit"]
	p.Minimum, p.Maximum = ptr(1.0), ptr(float64(maxLimit))
}

func enum(prop string, values ...string) func(*jsonschema.Schema) {
	return func(s *jsonschema.Schema) {
		for _, v := range values {
			s.Properties[prop].Enum = append(s.Properties[prop].Enum, v)
		}
	}
}

func nonEmpty(prop string) func(*jsonschema.Schema) {
	return func(s *jsonschema.Schema) { s.Properties[prop].MinLength = ptr(1) }
}

func orDefault(limit int) int {
	if limit == 0 {
		return defaultLimit
	}
	return limit
}

// registerReadTools registers today, list_tasks, get_task, search and work_report.
func registerReadTools(s *mcp.Server, c *client.Client) {
	addTool(s, &mcp.Tool{
		Name:        "today",
		Description: "Today's dashboard: focus, time blocks, triage count, overdue and due-soon tasks, log entries and banners.",
		Annotations: readAnnotations(),
	}, outputSchema[api.Today](), func(ctx context.Context, _ emptyInput) (any, error) {
		v, _, _, err := c.Today(ctx, "")
		return v, err
	})

	addTool(s, &mcp.Tool{
		Name:        "list_tasks",
		Description: "List tasks in a view (triage, backlog, upcoming, done, all; default backlog), optionally filtered by tag or project.",
		Annotations: readAnnotations(),
	}, outputSchema[api.Page[api.Task]](), func(ctx context.Context, in listTasksInput) (any, error) {
		view := in.View
		if view == "" {
			view = "backlog"
		}
		return c.ListTasks(ctx, client.TaskQuery{
			View: view, Tag: in.Tag, Project: in.Project,
			PageQuery: client.PageQuery{Limit: orDefault(in.Limit), Cursor: in.Cursor},
		})
	}, enum("view", "triage", "backlog", "upcoming", "done", "all"), limitBounds)

	addTool(s, &mcp.Tool{
		Name:        "get_task",
		Description: "One task with its log entries, time blocks and interruptions. The etag identifies the version.",
		Annotations: readAnnotations(),
	}, outputSchema[api.TaskDetail](), func(ctx context.Context, in getTaskInput) (any, error) {
		return c.GetTask(ctx, in.ID)
	})

	addTool(s, &mcp.Tool{
		Name:        "search",
		Description: "Full-text search over task titles and notes and over log entries.",
		Annotations: readAnnotations(),
	}, outputSchema[api.Page[api.SearchHit]](), func(ctx context.Context, in searchInput) (any, error) {
		kind := in.Kind
		if kind == "" {
			kind = "all"
		}
		return c.Search(ctx, client.SearchQuery{Q: in.Query, Kind: kind, PageQuery: client.PageQuery{Limit: orDefault(in.Limit)}})
	}, enum("kind", "task", "entry", "all"), nonEmpty("query"), limitBounds)

	addTool(s, &mcp.Tool{
		Name:        "work_report",
		Description: "Work logged and tasks completed between two days (default: this week so far), grouped by day.",
		Annotations: readAnnotations(),
	}, outputSchema[api.Report](), func(ctx context.Context, in workReportInput) (any, error) {
		ref, err := clockRef(ctx, c)
		if err != nil {
			return nil, err
		}
		from, _ := datespec.Week(ref)
		to := ref.Today
		// AIDEV-NOTE: work_report takes whole days; a time of day in from/to is a
		// validation error (as in `tkr report`), never silently dropped.
		if in.From != "" {
			m, err := parseDate(in.From, datespec.Past, ref)
			if err != nil {
				return nil, err
			}
			if m.Min.Valid {
				return nil, domain.Errorf(domain.CodeValidation, "from takes a date without a time (YYYY-MM-DD, today, a weekday, -2d), got %q", in.From)
			}
			from = m.Date
		}
		if in.To != "" {
			m, err := parseDate(in.To, datespec.Past, ref)
			if err != nil {
				return nil, err
			}
			if m.Min.Valid {
				return nil, domain.Errorf(domain.CodeValidation, "to takes a date without a time (YYYY-MM-DD, today, a weekday, -2d), got %q", in.To)
			}
			to = m.Date
		}
		return c.Report(ctx, client.ReportQuery{From: from, To: to, Tag: in.Tag, Project: in.Project, Todo: in.IncludeTodo})
	})
}
