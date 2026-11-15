package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/datespec"
)

type tasksMsg struct {
	req       int
	target    string
	tasks     []api.Task
	truncated bool
	err       error
}

func (a *App) loadTasks(target, view string) tea.Cmd {
	req, ctx, c := a.nextReq(target), a.ctx, a.api
	return func() tea.Msg {
		msg := tasksMsg{req: req, target: target}
		q := client.TaskQuery{View: view, PageQuery: client.PageQuery{Limit: 500}}
		for range 5 {
			page, err := c.ListTasks(ctx, q)
			if err != nil {
				msg.err = err
				return msg
			}
			msg.tasks = append(msg.tasks, page.Items...)
			if page.NextCursor == nil {
				return msg
			}
			q.Cursor = *page.NextCursor
		}
		msg.truncated = true
		return msg
	}
}

type taskList struct {
	items  []api.Task
	cursor int
	selID  int64
	offset int
}

func (l *taskList) set(items []api.Task) {
	l.items = items
	if len(items) == 0 {
		l.cursor, l.selID, l.offset = 0, 0, 0
		return
	}
	// AIDEV-NOTE: refreshes preserve identity, not position. If that task
	// vanished, adopt the clamped row's ID so the next refresh follows it.
	for i, t := range items {
		if t.ID == l.selID {
			l.cursor = i
			return
		}
	}
	l.cursor = min(max(0, l.cursor), len(items)-1)
	l.selID = items[l.cursor].ID
}

func (l *taskList) move(delta int) {
	if len(l.items) == 0 {
		return
	}
	l.cursor = min(max(0, l.cursor+delta), len(l.items)-1)
	l.selID = l.items[l.cursor].ID
}

func (l *taskList) selected() (api.Task, bool) {
	if l.cursor < 0 || l.cursor >= len(l.items) {
		return api.Task{}, false
	}
	return l.items[l.cursor], true
}

func (l *taskList) selection() (int64, string, bool) {
	t, ok := l.selected()
	return t.ID, t.Title, ok
}

func (l *taskList) render(a *App, w, h int, row func(api.Task, int) string) string {
	return l.renderHeadings(a, w, h, row, nil)
}

// Headings occupy display lines without becoming selectable rows. offset is a
// display-line offset so scrolling also accounts for Upcoming's two headings.
func (l *taskList) renderHeadings(a *App, w, h int, row func(api.Task, int) string, headings map[int]string) string {
	if w <= 0 || h <= 0 || len(l.items) == 0 {
		return ""
	}
	lines := make([]string, 0, len(l.items)+len(headings))
	selectedLine := 0
	for i, t := range l.items {
		if heading := headings[i]; heading != "" {
			lines = append(lines, a.styles.Header.Render(a.line(heading, w)))
		}
		if i == l.cursor {
			selectedLine = len(lines)
		}
		lines = append(lines, a.row(row(t, max(0, w-2)), w, i == l.cursor))
	}
	l.offset = min(max(0, l.offset), max(0, len(lines)-h))
	if selectedLine < l.offset {
		l.offset = selectedLine
	} else if selectedLine >= l.offset+h {
		l.offset = selectedLine - h + 1
	}
	return clip(lines[l.offset:], w, h)
}

func taskRow(a *App, t api.Task, w int) string {
	text := fmt.Sprintf("#%d ", t.ID)
	if len(t.Reasons) > 0 {
		reasons := make([]string, 0, len(t.Reasons))
		for _, r := range t.Reasons {
			reason := r.Reason
			if reason == "slipped" {
				reason = fmt.Sprintf("slipped×%d", t.Slips.Lifetime)
			}
			reasons = append(reasons, reason)
		}
		text += "[" + strings.Join(reasons, ",") + "] "
	}
	text += t.Title
	var labels []string
	if t.Project != nil {
		labels = append(labels, *t.Project)
	}
	for _, tag := range t.Tags {
		labels = append(labels, "+"+tag)
	}
	if len(labels) > 0 {
		text += "  " + strings.Join(labels, " ")
	}
	if t.Due != nil {
		marker := "due"
		if t.DuePhase == "overdue" {
			marker = "overdue"
		}
		text += fmt.Sprintf("  %s %.3s %s", marker, t.Due.Date.Weekday(), t.Due.Date.String()[5:])
	}
	return a.line(text, w)
}

func (a *App) openDefer(t api.Task, allowNone bool, after func(*App, *api.OpResponse) tea.Cmd) tea.Cmd {
	hint := "date, e.g. thu · enter saves · esc cancels"
	if allowNone {
		hint = "date or none to clear · enter saves · esc cancels"
	}
	return a.openForm(fmt.Sprintf("Defer #%d", t.ID), hint, "", func(a *App, value string) (string, sendFunc, error) {
		r := api.DeferRequest{Until: api.Opt[api.Moment]{Set: true}}
		if allowNone && strings.TrimSpace(value) == "none" {
			r.Until.Null = true
		} else {
			moment, err := a.parseMoment(value, datespec.Future)
			if err != nil {
				return "", nil, err
			}
			r.Until.V = moment
		}
		return "defer updated", func(ctx context.Context, c API, key string) (*api.OpResponse, error) {
			return c.DeferTask(ctx, client.Mut{IfMatch: t.ETag, Key: key}, t.ID, r)
		}, nil
	}, after)
}

func (a *App) openBlock(t api.Task, after func(*App, *api.OpResponse) tea.Cmd) tea.Cmd {
	return a.openForm(fmt.Sprintf("Block #%d", t.ID), "span, e.g. 13:00-15:00 · enter saves · esc cancels", "",
		func(a *App, value string) (string, sendFunc, error) {
			r, err := a.parseSpan(value)
			if err != nil {
				return "", nil, err
			}
			return "blocked", func(ctx context.Context, c API, key string) (*api.OpResponse, error) {
				return c.BlockTask(ctx, client.Mut{IfMatch: t.ETag, Key: key}, t.ID, r)
			}, nil
		}, after)
}
