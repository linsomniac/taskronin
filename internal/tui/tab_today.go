package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/client"
	"taskronin/internal/textsafe"
)

type todayRow struct {
	taskID int64
	title  string
	block  *api.Block
}

type todayScreen struct {
	rows   []todayRow
	cursor int // -1 leaves logging to the implicit focus/current-block rule.
}

// App owns the aggregate read, shared by Today, headers and implicit log links.
func (*todayScreen) load(_ *App) tea.Cmd { return nil }

func (s *todayScreen) update(a *App, msg tea.Msg) tea.Cmd {
	if _, ok := msg.(todayMsg); !ok || a.today == nil {
		return nil
	}
	var selected todayRow
	if s.cursor >= 0 && s.cursor < len(s.rows) {
		selected = s.rows[s.cursor]
	}
	s.rows, s.cursor = nil, -1
	for _, b := range a.today.Blocks {
		s.rows = append(s.rows, todayRow{taskID: b.TaskID, title: b.TaskTitle, block: &b})
	}
	for _, t := range a.today.TriageTop[:min(5, len(a.today.TriageTop))] {
		s.rows = append(s.rows, todayRow{taskID: t.ID, title: t.Title})
	}
	// A task can have several blocks and a triage row. Follow the exact row
	// across a refresh; if it disappears, require a new explicit selection.
	for i, row := range s.rows {
		if row.taskID == selected.taskID && ((row.block == nil && selected.block == nil) ||
			(row.block != nil && selected.block != nil && row.block.ID == selected.block.ID)) {
			s.cursor = i
			break
		}
	}
	return nil
}

func (s *todayScreen) selection() (int64, string, bool) {
	if s.cursor < 0 || s.cursor >= len(s.rows) {
		return 0, "", false
	}
	r := s.rows[s.cursor]
	return r.taskID, r.title, true
}

func (s *todayScreen) handle(a *App, k tea.KeyPressMsg) (tea.Cmd, bool) {
	if a.inflight && (k.String() == "B") {
		a.setBanner(bannerInfo, "busy…")
		return nil, true
	}
	switch todayKeys.command(k) {
	case "j":
		s.cursor = min(s.cursor+1, len(s.rows)-1)
		return nil, true
	case "k":
		if s.cursor >= 0 {
			s.cursor = max(0, s.cursor-1)
		}
		return nil, true
	case "esc":
		s.cursor = -1
		return nil, false // Also clear the app banner.
	case "l":
		return a.openLog(a.clock.Today, a.logLink()), true
	case "x":
		if a.today == nil || a.today.Focus == nil {
			a.setBanner(bannerInfo, "no focus task")
			return nil, true
		}
		focus := *a.today.Focus
		return a.mutate("completed", func(ctx context.Context, c API) (*api.OpResponse, error) {
			return c.CompleteTask(ctx, client.Mut{IfMatch: focus.ETag}, focus.ID, api.CompleteRequest{})
		}, nil, nil), true
	case "f", "B", "U":
		id, _, ok := s.selection()
		if !ok {
			a.setBanner(bannerInfo, "select a row first (j)")
			return nil, true
		}
		if k.String() == "f" {
			r := api.FocusRequest{TaskID: api.Opt[int64]{Set: true, V: id}}
			if a.today != nil && a.today.Focus != nil && a.today.Focus.ID == id {
				r.TaskID = api.Opt[int64]{Set: true, Null: true}
			}
			return a.mutate("focus updated", func(ctx context.Context, c API) (*api.OpResponse, error) {
				return c.SetFocus(ctx, client.Mut{}, r)
			}, nil, nil), true
		}
		if s.rows[s.cursor].block == nil {
			a.setBanner(bannerInfo, "select a block row")
			return nil, true
		}
		b := *s.rows[s.cursor].block
		if k.String() == "U" {
			return a.mutate("unblocked", func(ctx context.Context, c API) (*api.OpResponse, error) {
				return c.Unblock(ctx, client.Mut{IfMatch: b.ETag}, b.ID)
			}, nil, nil), true
		}
		return a.openForm(fmt.Sprintf("Reblock b%d", b.ID), "span, e.g. 15:00-16:00 · enter saves · esc cancels", "",
			func(a *App, value string) (string, sendFunc, error) {
				span, err := a.parseSpan(value)
				if err != nil {
					return "", nil, err
				}
				r := api.ReblockRequest{BlockRequest: span, TaskETag: b.TaskETag}
				return "reblocked", func(ctx context.Context, c API, key string) (*api.OpResponse, error) {
					return c.Reblock(ctx, client.Mut{IfMatch: b.ETag, Key: key}, b.ID, r)
				}, nil
			}, nil).focus(), true
	default:
		return nil, false
	}
}

func (s *todayScreen) view(a *App, w, h int) string {
	td := a.today
	if a.todayLoad != loadLoaded {
		return a.line(a.todayLoad.notice("Today"), w)
	}
	focus := "no focus"
	b := td.CurrentBlock
	if f := td.Focus; f != nil {
		focus = fmt.Sprintf("▶ #%d %s", f.ID, f.Title)
		if b != nil && b.TaskID == f.ID {
			focus += " (→" + hhmm(int(b.End)) + ")"
		}
	} else if b != nil {
		focus = fmt.Sprintf("▷ #%d %s (→%s)", b.TaskID, b.TaskTitle, hhmm(int(b.End)))
	}
	lines := []string{a.styles.Current.Render(a.line(focus, w)), a.line("Blocks", w)}
	selectedLine := -1
	for i, b := range td.Blocks {
		text := fmt.Sprintf("b%d %s #%d %s", b.ID, spanText(b.Start, b.End), b.TaskID, b.TaskTitle)
		switch {
		case b.Current:
			text += " ◀ now"
		case b.NeedsReblock:
			text += " reblock?"
		case b.Status != "planned":
			text += " " + b.Status
		}
		line := a.row(text, w, i == s.cursor)
		if b.Current && i != s.cursor {
			line = a.styles.Current.Render(line)
		}
		if i == s.cursor {
			selectedLine = len(lines)
		}
		lines = append(lines, line)
	}
	lines = append(lines, a.line(fmt.Sprintf("Triage %d", td.TriageCount), w))
	for i, task := range td.TriageTop[:min(5, len(td.TriageTop))] {
		selected := len(td.Blocks)+i == s.cursor
		if selected {
			selectedLine = len(lines)
		}
		lines = append(lines, a.row(todayTaskText(task), w, selected))
	}
	for _, section := range []struct {
		title string
		tasks []api.Task
	}{{"Overdue", td.Overdue}, {"Due soon", td.DueSoon}} {
		if len(section.tasks) > 0 {
			lines = append(lines, a.line(section.title, w))
			for _, t := range section.tasks {
				lines = append(lines, a.row(todayTaskText(t), w, false))
			}
		}
	}
	lines = append(lines, a.line("Log", w))
	for _, e := range td.Entries {
		text := fmt.Sprintf("e%d", e.ID)
		if e.Time != nil {
			text += " " + hhmm(int(*e.Time))
		}
		text += " " + e.Title
		if e.TaskID != nil {
			text += fmt.Sprintf(" [#%d]", *e.TaskID)
		}
		lines = append(lines, a.row(text, w, false))
	}
	if len(td.Interruptions) > 0 {
		lines = append(lines, a.line("Interruptions", w))
		titles, blocks := titlesFrom(td), make(map[int64]api.Block)
		for _, b := range td.Blocks {
			blocks[b.ID] = b
		}
		if td.CurrentBlock != nil {
			blocks[td.CurrentBlock.ID] = *td.CurrentBlock
		}
		for _, i := range td.Interruptions {
			lines = append(lines, a.row(interruptionLine(i, titles, blocks), w, false))
		}
	}
	if td.AIChanges > 0 {
		lines = append(lines, a.styles.Info.Render(a.line(fmt.Sprintf("AI changes: %d unreviewed — A to review", td.AIChanges), w)))
	}

	health := "Backup: healthy"
	if !td.Backup.Healthy {
		health = "Backup: needs attention"
	}
	if td.Backup.LastSuccessAt != nil {
		health += " · last success " + td.Backup.LastSuccessAt.Format("2006-01-02 15:04")
	}
	if td.Backup.LastError != "" {
		health += " · " + td.Backup.LastError
	}
	lines = append(lines, a.line(health, w))
	for _, b := range td.Banners {
		lines = append(lines, a.line(b.Message, w))
	}
	// Keep the selected row visible even when the dashboard exceeds the frame.
	start := 0
	if h > 0 && selectedLine >= h {
		start = selectedLine - h + 1
	}
	return clip(lines[start:], w, h)
}

func todayTaskText(t api.Task) string {
	text := fmt.Sprintf("#%d %s", t.ID, t.Title)
	if len(t.Reasons) > 0 {
		reasons := make([]string, 0, len(t.Reasons))
		for _, r := range t.Reasons {
			reasons = append(reasons, r.Reason)
		}
		text += " {" + strings.Join(reasons, ", ") + "}"
	}
	return text
}

func hhmm(m int) string { return fmt.Sprintf("%02d:%02d", m/60, m%60) }

func spanText(start civil.Minute, end civil.EndMinute) string {
	return hhmm(int(start)) + "–" + hhmm(int(end))
}

func interruptionLine(i api.Interruption, titles map[int64]string, blocks map[int64]api.Block) string {
	text := fmt.Sprintf("%s #%d", i.At.Format("15:04"), i.ToTaskID)
	if title := titles[i.ToTaskID]; title != "" {
		text += " '" + textsafe.SanitizeLine(title) + "'"
	}
	if i.FromTaskID != nil {
		text += fmt.Sprintf(" interrupted #%d", *i.FromTaskID)
	}
	if i.FromBlockID != nil {
		if b, ok := blocks[*i.FromBlockID]; ok {
			text += " (block " + spanText(b.Start, b.End) + ")"
		}
	}
	return text
}

func titlesFrom(td *api.Today) map[int64]string {
	titles := make(map[int64]string)
	if td == nil {
		return titles
	}
	for _, tasks := range [][]api.Task{td.TriageTop, td.Overdue, td.DueSoon} {
		for _, t := range tasks {
			titles[t.ID] = t.Title
		}
	}
	for _, b := range td.Blocks {
		titles[b.TaskID] = b.TaskTitle
	}
	for _, e := range td.Entries {
		if e.TaskID != nil && e.TaskTitle != nil {
			titles[*e.TaskID] = *e.TaskTitle
		}
	}
	if b := td.CurrentBlock; b != nil {
		titles[b.TaskID] = b.TaskTitle
	}
	if f := td.Focus; f != nil {
		titles[f.ID] = f.Title
	}
	return titles
}
