package mcpserver

// Input structs for the MCP tools (A§10). The json tags are the wire names;
// the jsonschema tags are the descriptions shown to the model. Date-valued
// fields are datespec strings, resolved against GET /clock by resolveDate.

// emptyInput is the input of tools that take no arguments.
type emptyInput struct{}

// listTasksInput is the input of list_tasks.
type listTasksInput struct {
	View    string `json:"view,omitempty" jsonschema:"which tasks to list; default backlog"`
	Tag     string `json:"tag,omitempty" jsonschema:"only tasks carrying this tag"`
	Project string `json:"project,omitempty" jsonschema:"only tasks in this project"`
	Limit   int    `json:"limit,omitempty" jsonschema:"page size, 1 to 200; default 50"`
	Cursor  string `json:"cursor,omitempty" jsonschema:"next_cursor from an earlier page"`
}

// getTaskInput is the input of get_task.
type getTaskInput struct {
	ID int64 `json:"id" jsonschema:"task id"`
}

// searchInput is the input of search.
type searchInput struct {
	Query string `json:"query" jsonschema:"text to search for"`
	Kind  string `json:"kind,omitempty" jsonschema:"what to search; default all"`
	Limit int    `json:"limit,omitempty" jsonschema:"page size, 1 to 200; default 50"`
}

// workReportInput is the input of work_report.
type workReportInput struct {
	From        string `json:"from,omitempty" jsonschema:"first day: today, yesterday, a weekday (mon...sun), -2d, or YYYY-MM-DD; default the start of this week"`
	To          string `json:"to,omitempty" jsonschema:"last day, same forms as from; default today"`
	Tag         string `json:"tag,omitempty" jsonschema:"only work on tasks carrying this tag"`
	Project     string `json:"project,omitempty" jsonschema:"only work on tasks in this project"`
	IncludeTodo bool   `json:"include_todo,omitempty" jsonschema:"also list the current triage and backlog"`
}
