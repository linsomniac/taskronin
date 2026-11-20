package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/client"
	"taskronin/internal/textsafe"
)

type todayRow struct {
	taskID  int64
	title   string
	etag    string
	section string
	block   *api.Block
}

type todayScreen struct {
	rows   []todayRow
	scroll int // Extra lines beyond the last task when reading the nonselectable tail.
	cursor int // -1 or len(rows) leaves actions to the implicit focus/current-block rule.
}

// App owns the aggregate read, shared by Today, headers and implicit log links.
func (*todayScreen) load(_ *App) tea.Cmd { return nil }

func (s *todayScreen) update(a *App, msg tea.Msg) tea.Cmd {
	if _, ok := msg.(todayMsg); !ok || a.today == nil {
		return nil
	}
	var selected todayRow
	inTail := s.cursor == len(s.rows)
	if s.cursor >= 0 && s.cursor < len(s.rows) {
		selected = s.rows[s.cursor]
	}
	s.rows, s.cursor = nil, -1
	for _, b := range a.today.Blocks {
		s.rows = append(s.rows, todayRow{taskID: b.TaskID, title: b.TaskTitle, etag: b.TaskETag, section: "blocks", block: &b})
	}
	for _, t := range a.today.TriageTop[:min(5, len(a.today.TriageTop))] {
		s.rows = append(s.rows, todayRow{taskID: t.ID, title: t.Title, etag: t.ETag, section: "triage"})
	}

	for _, section := range []struct {
		name  string
		tasks []api.Task
	}{{"overdue", a.today.Overdue}, {"due soon", a.today.DueSoon}} {
		for _, t := range section.tasks {
			s.rows = append(s.rows, todayRow{taskID: t.ID, title: t.Title, etag: t.ETag, section: section.name})
		}
	}
	// A task can have several blocks and a triage row. Follow the exact row
	// across a refresh; if it disappears, require a new explicit selection.
	for i, row := range s.rows {
		if row.taskID == selected.taskID && row.section == selected.section && ((row.block == nil && selected.block == nil) ||
			(row.block != nil && selected.block != nil && row.block.ID == selected.block.ID)) {
			s.cursor = i
			break
		}
	}
	if inTail {
		s.cursor = len(s.rows)
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
		if s.cursor < len(s.rows)-1 {
			s.cursor++
			s.scroll = 0
		} else {
			// AIDEV-NOTE: passing the last selectable row clears selection.
			// len(rows) anchors tail scrolling without an actionable hidden row;
			// x/l use focus/implicit linking, and k reselects at the tail boundary.
			s.cursor = len(s.rows)
			s.scroll++
		}
		return nil, true
	case "k":
		if s.scroll > 0 {
			s.scroll--
			if s.scroll == 0 {
				s.cursor = len(s.rows) - 1
			}
		} else if s.cursor >= 0 {
			s.cursor = max(0, s.cursor-1)
		}
		return nil, true
	case "esc":
		s.cursor, s.scroll = -1, 0
		return nil, false // Also clear the app banner.
	case "l":
		return a.openLog(a.clock.Today, a.logLink()), true

	case "x":
		var target api.Task
		switch {
		case s.cursor >= 0 && s.cursor < len(s.rows):
			row := s.rows[s.cursor]
			target = api.Task{ID: row.taskID, Title: row.title, ETag: row.etag}
		case a.today != nil && a.today.Focus != nil:
			target = *a.today.Focus
		default:
			a.setBanner(bannerInfo, "no focus task")
			return nil, true
		}
		return a.mutate("completed", func(ctx context.Context, c API) (*api.OpResponse, error) {
			return c.CompleteTask(ctx, client.Mut{IfMatch: target.ETag}, target.ID, api.CompleteRequest{})
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
			if k.String() == "B" {
				row := s.rows[s.cursor]
				return a.openBlock(api.Task{ID: row.taskID, Title: row.title, ETag: row.etag}, nil).focus(), true
			}
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
	if td == nil {
		return a.line(a.todayLoad.notice("Today"), w)
	}
	var notice []string
	if text := a.todayLoad.retainedNotice("Today", true); text != "" {
		notice = append(notice, a.line(text, w))
	}
	height := max(0, h-len(notice))
	focus := "no focus"
	b := td.CurrentBlock
	if f := td.Focus; f != nil {
		focus = "▶ " + a.id("#", f.ID) + " " + textsafe.SanitizeLine(f.Title)
		if b != nil && b.TaskID == f.ID {
			focus += " (→" + hhmm(int(b.End)) + ")"
		}
	} else if b != nil {
		focus = "▷ " + a.id("#", b.TaskID) + " " + textsafe.SanitizeLine(b.TaskTitle) + " (→" + hhmm(int(b.End)) + ")"
	}
	lines := []string{renderOver(a.styles.Current, textsafe.Truncate(focus, w, "…")), a.styles.Head.Render(a.line("Blocks", w))}
	selectedLine := -1
	anchor := s.cursor
	if anchor == len(s.rows) {
		anchor-- // The last task anchors scrolling but has no selection marker.
	}
	for i, b := range td.Blocks {
		text := a.blockText(b)
		switch {
		case b.Current:
			text += " " + a.styles.OK.Render("◀ now")
		case b.NeedsReblock:
			text += " " + a.styles.Warn.Render("reblock?")
		case b.Status != "planned":
			text += " " + a.blockStatus(b.Status)
		}
		line := a.styledRow(text, w, i == s.cursor)
		if b.Current && i != s.cursor {
			line = renderOver(a.styles.Current, line)
		}
		if i == anchor {
			selectedLine = len(lines)
		}
		lines = append(lines, line)
	}
	lines = append(lines, a.styles.Head.Render(a.line(fmt.Sprintf("Triage %d", td.TriageCount), w)))
	for i, task := range td.TriageTop[:min(5, len(td.TriageTop))] {
		selected := len(td.Blocks)+i == s.cursor
		if len(td.Blocks)+i == anchor {
			selectedLine = len(lines)
		}
		lines = append(lines, a.styledRow(a.todayTaskText(task), w, selected))
	}
	rowIndex := len(td.Blocks) + min(5, len(td.TriageTop))
	for _, section := range []struct {
		title string
		tasks []api.Task
	}{{"Overdue", td.Overdue}, {"Due soon", td.DueSoon}} {
		if len(section.tasks) > 0 {
			lines = append(lines, a.styles.Head.Render(a.line(section.title, w)))
			for _, t := range section.tasks {
				selected := rowIndex == s.cursor
				if rowIndex == anchor {
					selectedLine = len(lines)
				}
				lines = append(lines, a.styledRow(a.todayTaskText(t), w, selected))
				rowIndex++
			}
		}
	}
	lines = append(lines, a.styles.Head.Render(a.line("Log", w)))
	for _, e := range td.Entries {
		lines = append(lines, a.styledRow(a.entryText(e, " "), w, false))
	}
	if len(td.Interruptions) > 0 {
		lines = append(lines, a.styles.Head.Render(a.line("Interruptions", w)))
		titles, blocks := titlesFrom(td), make(map[int64]api.Block)
		for _, b := range td.Blocks {
			blocks[b.ID] = b
		}
		if td.CurrentBlock != nil {
			blocks[td.CurrentBlock.ID] = *td.CurrentBlock
		}
		for _, i := range td.Interruptions {
			lines = append(lines, a.styledRow(interruptionLine(i, titles, blocks, a.styles.ID), w, false))
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
		if b.Kind == "ai_changes" {
			continue // The dedicated AI notice above includes the review shortcut.
		}
		lines = append(lines, a.line(b.Message, w))
	}
	// Keep the selected row visible even when the dashboard exceeds the frame.
	start := 0
	if height > 0 && selectedLine >= height {
		start = selectedLine - height + 1
	}
	// j/k continue beyond task rows into log, interruptions, AI and notices.
	s.scroll = min(s.scroll, max(0, len(lines)-height-start))
	return clip(append(notice, lines[min(len(lines), start+s.scroll):]...), w, h)
}

func (a *App) todayTaskText(t api.Task) string {
	text := a.id("#", t.ID) + " " + textsafe.SanitizeLine(t.Title)
	if len(t.Reasons) > 0 {
		reasons := make([]string, 0, len(t.Reasons))
		for _, r := range t.Reasons {
			reasons = append(reasons, r.Reason)
		}
		text += " " + a.styles.Warn.Render(textsafe.SanitizeLine("{"+strings.Join(reasons, ", ")+"}"))
	}
	return text
}

func hhmm(m int) string { return fmt.Sprintf("%02d:%02d", m/60, m%60) }

func spanText(start civil.Minute, end civil.EndMinute) string {
	return hhmm(int(start)) + "–" + hhmm(int(end))
}

func interruptionLine(i api.Interruption, titles map[int64]string, blocks map[int64]api.Block, idStyle lipgloss.Style) string {
	text := i.At.Format("15:04") + " " + idStyle.Render(fmt.Sprintf("#%d", i.ToTaskID))
	if title := titles[i.ToTaskID]; title != "" {
		text += " '" + textsafe.SanitizeLine(title) + "'"
	}
	if i.FromTaskID != nil {
		text += " interrupted " + idStyle.Render(fmt.Sprintf("#%d", *i.FromTaskID))
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
