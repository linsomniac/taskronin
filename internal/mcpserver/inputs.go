package mcpserver

import (
	"taskronin/internal/api"
	"taskronin/internal/civil"
)

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

// Write-tool inputs (A§10). Tools that mutate an existing task carry an
// optional ExpectedETag; add_task and log_work create records and have none.

// addTaskInput is the input of add_task. There is deliberately no placement
// field: AI-created tasks always land in triage (SCP-03).
type addTaskInput struct {
	Title    string   `json:"title" jsonschema:"task title"`
	Notes    string   `json:"notes,omitempty" jsonschema:"longer description"`
	Due      string   `json:"due,omitempty" jsonschema:"due date: today, tomorrow, a weekday (mon...sun), +3d, +2w or YYYY-MM-DD, optionally followed by HH:MM"`
	Tags     []string `json:"tags,omitempty" jsonschema:"tags to attach"`
	Project  string   `json:"project,omitempty" jsonschema:"project name"`
	Priority string   `json:"priority,omitempty" jsonschema:"priority H, M or L"`
}

// logWorkInput is the input of log_work. TaskID is required but may be null
// (a standalone entry); its schema is overridden to integer-or-null.
type logWorkInput struct {
	TaskID api.Opt[int64] `json:"task_id" jsonschema:"task to log work against, or null for a standalone entry"`
	Title  string         `json:"title" jsonschema:"what was done"`
	Notes  string         `json:"notes,omitempty" jsonschema:"details"`
	Date   string         `json:"date,omitempty" jsonschema:"day the work happened: today, yesterday, a weekday (mon...sun), -2d or YYYY-MM-DD; default today"`
	Time   string         `json:"time,omitempty" jsonschema:"time of day as HH:MM"`
}

// completeTaskInput is the input of complete_task.
type completeTaskInput struct {
	ID           int64  `json:"id" jsonschema:"task id"`
	AsTitle      string `json:"as_title,omitempty" jsonschema:"title for the completion entry, if different from the task title"`
	Date         string `json:"date,omitempty" jsonschema:"day it was completed: today, yesterday, a weekday (mon...sun), -2d or YYYY-MM-DD; default today"`
	LogTitle     string `json:"log_title,omitempty" jsonschema:"title of an extra work entry to log with the completion"`
	Notes        string `json:"notes,omitempty" jsonschema:"notes for the completion entry"`
	ExpectedETag string `json:"expected_etag,omitempty" jsonschema:"etag from an earlier read of the task; if omitted the current version is used"`
}

// reopenTaskInput is the input of reopen_task.
type reopenTaskInput struct {
	ID           int64  `json:"id" jsonschema:"task id"`
	ExpectedETag string `json:"expected_etag,omitempty" jsonschema:"etag from an earlier read of the task; if omitted the current version is used"`
}

// deferTaskInput is the input of defer_task.
type deferTaskInput struct {
	ID           int64  `json:"id" jsonschema:"task id"`
	Until        string `json:"until" jsonschema:"hide the task until this date (today, tomorrow, a weekday, +3d, +2w or YYYY-MM-DD, optionally with HH:MM), or none to clear the deferral"`
	ExpectedETag string `json:"expected_etag,omitempty" jsonschema:"etag from an earlier read of the task; if omitted the current version is used"`
}

// setDueInput is the input of set_due.
type setDueInput struct {
	ID           int64  `json:"id" jsonschema:"task id"`
	Due          string `json:"due" jsonschema:"due date (today, tomorrow, a weekday, +3d, +2w or YYYY-MM-DD, optionally with HH:MM), or none to clear it"`
	ExpectedETag string `json:"expected_etag,omitempty" jsonschema:"etag from an earlier read of the task; if omitted the current version is used"`
}

// blockTimeInput is the input of block_time.
type blockTimeInput struct {
	TaskID       int64           `json:"task_id" jsonschema:"task to schedule time for"`
	Date         string          `json:"date,omitempty" jsonschema:"day of the block: today, tomorrow, a weekday (mon...sun), +3d or YYYY-MM-DD; default today"`
	Start        civil.Minute    `json:"start" jsonschema:"start time as HH:MM"`
	End          civil.EndMinute `json:"end" jsonschema:"end time as HH:MM; 24:00 means midnight"`
	ExpectedETag string          `json:"expected_etag,omitempty" jsonschema:"etag from an earlier read of the task; if omitted the current version is used"`
}

// updateTaskInput is the input of update_task. Project and Priority are
// absent (unchanged), null (cleared) or a value.
type updateTaskInput struct {
	ID           int64           `json:"id" jsonschema:"task id"`
	Title        *string         `json:"title,omitempty" jsonschema:"new title"`
	Notes        *string         `json:"notes,omitempty" jsonschema:"new notes; empty string clears them"`
	TagsAdd      []string        `json:"tags_add,omitempty" jsonschema:"tags to add"`
	TagsRemove   []string        `json:"tags_remove,omitempty" jsonschema:"tags to remove"`
	Project      api.Opt[string] `json:"project,omitzero" jsonschema:"project name, or null to clear"`
	Priority     api.Opt[string] `json:"priority,omitzero" jsonschema:"priority H, M or L, or null to clear"`
	ExpectedETag string          `json:"expected_etag,omitempty" jsonschema:"etag from an earlier read of the task; if omitted the current version is used"`
}

// placeTaskInput is the input of place_task.
type placeTaskInput struct {
	ID           int64  `json:"id" jsonschema:"task id"`
	How          string `json:"how" jsonschema:"keep, top, bottom, before or after"`
	RefID        int64  `json:"ref_id,omitempty" jsonschema:"reference task id, required for before and after"`
	ExpectedETag string `json:"expected_etag,omitempty" jsonschema:"etag from an earlier read of the task; if omitted the current version is used"`
}
