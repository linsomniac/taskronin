package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/datespec"
	"taskronin/internal/textsafe"
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

// recordList shares identity-preserving selection and scrolling between tasks
// and entries. The owning screen supplies identity when replacing its records.
type recordList[T any] struct {
	items  []T
	id     func(T) int64
	cursor int
	selID  int64
	offset int
}

type taskList struct{ recordList[api.Task] }

func (l *taskList) set(items []api.Task) {
	l.setItems(items, func(t api.Task) int64 { return t.ID })
}

func (l *recordList[T]) setItems(items []T, id func(T) int64) {
	l.items, l.id = items, id
	if len(items) == 0 {
		l.cursor, l.selID, l.offset = 0, 0, 0
		return
	}
	// AIDEV-NOTE: refreshes preserve identity, not position. If that record
	// vanished, adopt the clamped row's ID so the next refresh follows it.
	for i, t := range items {
		if id(t) == l.selID {
			l.cursor = i
			return
		}
	}
	l.cursor = min(max(0, l.cursor), len(items)-1)
	l.selID = id(items[l.cursor])
}

func (l *recordList[T]) move(delta int) {
	if len(l.items) == 0 {
		return
	}
	l.cursor = min(max(0, l.cursor+delta), len(l.items)-1)
	l.selID = l.id(l.items[l.cursor])
}

func (l *recordList[T]) selected() (T, bool) {
	if l.cursor < 0 || l.cursor >= len(l.items) {
		var zero T
		return zero, false
	}
	return l.items[l.cursor], true
}

func (l *taskList) selection() (int64, string, bool) {
	t, ok := l.selected()
	return t.ID, t.Title, ok
}

func (l *recordList[T]) render(a *App, w, h int, row func(T, int) string) string {
	return l.renderHeadings(a, w, h, row, nil)
}

// Headings occupy display lines without becoming selectable rows. offset is a
// display-line offset so scrolling also accounts for Upcoming's two headings.
func (l *recordList[T]) renderHeadings(a *App, w, h int, row func(T, int) string, headings map[int]string) string {
	if w <= 0 || h <= 0 || len(l.items) == 0 {
		return ""
	}
	lines := make([]string, 0, len(l.items)+len(headings))
	selectedLine := 0
	for i, t := range l.items {
		if heading := headings[i]; heading != "" {
			lines = append(lines, a.styles.Head.Render(a.line(heading, w)))
		}
		if i == l.cursor {
			selectedLine = len(lines)
		}
		lines = append(lines, a.styledRow(row(t, max(0, w-2)), w, i == l.cursor))
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
	text := a.id("#", t.ID) + " "
	if len(t.Reasons) > 0 {
		reasons := make([]string, 0, len(t.Reasons))
		for _, r := range t.Reasons {
			reason := textsafe.SanitizeLine(r.Reason)
			if reason == "slipped" {
				reason = a.styles.Bad.Render(fmt.Sprintf("slipped×%d", t.Slips.Lifetime))
			}
			reasons = append(reasons, reason)
		}
		text += renderOver(a.styles.Warn, "["+strings.Join(reasons, ",")+"]") + " "
	}
	text += textsafe.SanitizeLine(t.Title)
	var labels []string
	if t.Project != nil {
		labels = append(labels, *t.Project)
	}
	for _, tag := range t.Tags {
		labels = append(labels, "+"+tag)
	}
	if len(labels) > 0 {
		text += "  " + a.styles.Dim.Render(textsafe.SanitizeLine(strings.Join(labels, " ")))
	}
	if t.Due != nil {
		marker, style := "due", a.styles.Warn
		if t.DuePhase == "overdue" {
			marker, style = "overdue", a.styles.Bad
		}
		text += "  " + style.Render(fmt.Sprintf("%s %.3s %s", marker, t.Due.Date.Weekday(), t.Due.Date.String()[5:]))
	}
	return textsafe.Truncate(text, w, "…")
}

func (a *App) openDefer(t api.Task, allowNone bool, after func(*App, *api.OpResponse) tea.Cmd) *form {
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

func (a *App) openBlock(t api.Task, after func(*App, *api.OpResponse) tea.Cmd) *form {
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

// listTab shares load bookkeeping and navigation for the three task-list tabs.
// Rows survive a failed reload, but are explicitly labelled as previous results.
type listTab struct {
	list  taskList
	state loadState
}

func (s *listTab) accept(a *App, tasks []api.Task, truncated bool, err error) tea.Cmd {
	cmd := s.state.finish(a, err)
	if err == nil {
		s.list.set(tasks)
		if truncated {
			a.setTruncationBanner(&s.state)
		}
	}
	return cmd
}

func (s *listTab) selection() (int64, string, bool) { return s.list.selection() }

func (s *listTab) move(k tea.KeyPressMsg) bool {
	switch listKeys.command(k) {
	case "j":
		s.list.move(1)
	case "k":
		s.list.move(-1)
	default:
		return false
	}
	return true
}

func (s *listTab) view(a *App, name, empty string, w, h int, headings map[int]string) string {
	var lines []string
	if notice := s.state.notice(name); notice != "" {
		if len(s.list.items) > 0 {
			notice += " · previous results"
		}
		lines = append(lines, a.line(notice, w))
	} else if len(s.list.items) == 0 {
		return a.line(empty, w)
	}
	body := s.list.renderHeadings(a, w, max(0, h-len(lines)), func(t api.Task, width int) string { return taskRow(a, t, width) }, headings)
	return clip(append(lines, strings.Split(body, "\n")...), w, h)
}
