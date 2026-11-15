package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/client"
)

type dayMsg struct {
	req int
	day *api.Day
	err error
}

type recapRow struct {
	block *api.Block
	entry *api.Entry
}

type recapMode struct {
	date   civil.Date
	req    int
	day    *api.Day
	rows   []recapRow
	cursor int
	state  loadState
}

func newRecapMode(date civil.Date) *recapMode { return &recapMode{date: date} }

func (s *recapMode) load(a *App) tea.Cmd {
	req, ctx, c, date := a.nextReq("recap-day"), a.ctx, a.api, s.date
	s.req, s.state = req, loadLoading
	return func() tea.Msg {
		day, err := c.Day(ctx, date)
		return dayMsg{req: req, day: day, err: err}
	}
}

func (s *recapMode) update(a *App, msg tea.Msg) tea.Cmd {
	m, ok := msg.(dayMsg)
	// AIDEV-NOTE: both the mode instance and the app's creation-order sequence
	// must match. A previous date's response (including an error) cannot replace
	// a newly opened recap, and older same-date reads cannot undo a refresh.
	if !ok || m.req != s.req || !a.latest("recap-day", m.req) {
		return nil
	}
	if m.err != nil {
		return s.state.finish(a, m.err)
	}
	if m.day == nil || m.day.Date != s.date {
		return nil
	}
	selected := s.selected()
	s.state.finish(a, nil)
	s.day, s.rows = m.day, nil
	for _, b := range m.day.Blocks {
		s.rows = append(s.rows, recapRow{block: &b})
	}
	for _, e := range m.day.Unlinked {
		s.rows = append(s.rows, recapRow{entry: &e})
	}
	// A task can own several blocks, and block/entry IDs occupy separate
	// namespaces. Follow the row's own identity, then clamp if it disappeared.
	for i, row := range s.rows {
		if (row.block != nil && selected.block != nil && row.block.ID == selected.block.ID) ||
			(row.entry != nil && selected.entry != nil && row.entry.ID == selected.entry.ID) {
			s.cursor = i
			return nil
		}
	}
	s.cursor = max(0, min(s.cursor, len(s.rows)-1))
	return nil
}

func (s *recapMode) selected() recapRow {
	if s.cursor < 0 || s.cursor >= len(s.rows) {
		return recapRow{}
	}
	return s.rows[s.cursor]
}

func (s *recapMode) selection() (int64, string, bool) {
	if b := s.selected().block; b != nil {
		return b.TaskID, b.TaskTitle, true
	}
	return 0, "", false
}

func (s *recapMode) handle(a *App, k tea.KeyPressMsg) (tea.Cmd, bool) {
	switch recapKeys.command(k) {
	case "j", "down": // App aliases arrows only in tabs and overlays.
		s.cursor = max(0, min(s.cursor+1, len(s.rows)-1))
		return nil, true
	case "k", "up":
		s.cursor = max(0, s.cursor-1)
		return nil, true
	case "w", "m", "a", "x", "l", "L":
		// Consume mode actions even on an inapplicable row; in particular,
		// auto must never fall through to the global add-task binding.
	default:
		return nil, false
	}
	if a.inflight {
		a.setBanner(bannerInfo, "busy…")
		return nil, true
	}
	row := s.selected()
	if k.String() == "l" {
		if row.block != nil {
			return a.openLog(s.date, link{TaskID: row.block.TaskID, Has: true, Why: "selected"}), true
		}
		if s.date == a.clock.Today {
			return a.openLog(s.date, a.logLink()), true
		}
		return s.openLogPicker(a), true
	}
	if k.String() == "L" {
		if row.entry != nil {
			return s.openLink(a, *row.entry), true
		}
		return nil, true
	}
	if row.block == nil {
		return nil, true
	}
	b := *row.block
	if k.String() == "x" {
		return a.mutate("completed", func(ctx context.Context, c API) (*api.OpResponse, error) {
			return c.CompleteTask(ctx, client.Mut{IfMatch: b.TaskETag}, b.TaskID, api.CompleteRequest{})
		}, nil, nil), true
	}
	outcome := map[string]string{"w": "worked", "m": "missed", "a": "auto"}[k.String()]
	if outcome != "auto" && b.Date == a.clock.Today && b.Status == "planned" && !b.Started {
		a.setBanner(bannerInfo, "not started — use unblock")
		return nil, true
	}
	return a.mutate("outcome updated", func(ctx context.Context, c API) (*api.OpResponse, error) {
		return c.SetOutcome(ctx, client.Mut{IfMatch: b.ETag}, b.ID, api.OutcomeRequest{Outcome: outcome})
	}, nil, nil), true
}

func (s *recapMode) taskHint() string {
	var tasks []string
	seen := make(map[int64]bool)
	for _, row := range s.rows {
		if b := row.block; b != nil && !seen[b.TaskID] {
			tasks = append(tasks, fmt.Sprintf("#%d %s", b.TaskID, b.TaskTitle))
			seen[b.TaskID] = true
		}
	}
	if len(tasks) == 0 {
		return "no block tasks for this day"
	}
	return "day's tasks: " + strings.Join(tasks, " · ")
}

func (s *recapMode) openLink(a *App, e api.Entry) tea.Cmd {
	return a.openForm(fmt.Sprintf("Link e%d · task ID", e.ID), s.taskHint(), "",
		func(_ *App, value string) (string, sendFunc, error) {
			id, none, err := parseTaskRef(value)
			if err != nil {
				return "", nil, err
			}
			if none {
				return "", nil, fmt.Errorf("expected a task ID or #ID")
			}
			r := api.RelinkRequest{TaskID: api.Opt[int64]{Set: true, V: id}}
			return fmt.Sprintf("linked e%d → #%d", e.ID, id), func(ctx context.Context, c API, key string) (*api.OpResponse, error) {
				return c.RelinkEntry(ctx, client.Mut{IfMatch: e.ETag, Key: key}, e.ID, r)
			}, nil
		}, nil).focus()
}

// recapLogPicker reuses form editing/rendering, but Enter only chooses the next
// form. No mutation or retry snapshot exists until the log text is submitted.
type recapLogPicker struct {
	*form
	date civil.Date
}

func (s *recapMode) openLogPicker(a *App) tea.Cmd {
	f := a.openForm("Log · task # or - for none", s.taskHint(), "", nil, nil)
	a.form = &recapLogPicker{form: f, date: s.date}
	return f.focus()
}

func (p *recapLogPicker) handle(a *App, k tea.KeyPressMsg) (tea.Cmd, bool) {
	if k.String() != "enter" {
		return p.form.handle(a, k)
	}
	if a.inflight {
		a.setBanner(bannerInfo, "busy…")
		return nil, true
	}
	id, none, err := parseTaskRef(p.input.Value())
	if err != nil {
		p.errText = err.Error()
		return nil, true
	}
	return a.openLog(p.date, link{TaskID: id, Has: !none, Why: "selected"}), true
}

func (s *recapMode) view(a *App, w, h int) string {
	lines := []string{a.styles.Header.Render(a.line(fmt.Sprintf("Recap %.3s %s", s.date.Weekday(), s.date), w))}
	if s.state != loadLoaded {
		return clip(append(lines, a.line(s.state.notice("day"), w)), w, h)
	}
	lines = append(lines, a.line("j/k/↑/↓ move · w worked · m missed · a auto · x done · l log · L link", w))
	selectedLine := -1
	for i, row := range s.rows {
		var text string
		if b := row.block; b != nil {
			if i == 0 {
				lines = append(lines, a.line("Blocks", w))
			}
			text = fmt.Sprintf("b%d %s #%d %s  %s", b.ID, spanText(b.Start, b.End), b.TaskID, b.TaskTitle, b.Status)
			if b.OutcomeSource != nil {
				text += " (" + *b.OutcomeSource + ")"
			}
			if b.Slipped {
				text += " slipped"
			}
		} else if e := row.entry; e != nil {
			if i == len(s.day.Blocks) {
				lines = append(lines, a.line("Unlinked entries", w))
			}
			text = fmt.Sprintf("e%d %s", e.ID, e.Title)
		}
		if i == s.cursor {
			selectedLine = len(lines)
		}
		lines = append(lines, a.row(text, w, i == s.cursor))
	}
	if len(s.rows) == 0 {
		lines = append(lines, a.line("no blocks or unlinked entries", w))
	}
	if len(s.day.Interruptions) > 0 {
		lines = append(lines, a.line("Interruptions", w))
		titles := titlesFrom(&api.Today{Blocks: s.day.Blocks, Entries: s.day.Entries})
		blocks := make(map[int64]api.Block)
		for _, b := range s.day.Blocks {
			blocks[b.ID] = b
		}
		for _, i := range s.day.Interruptions {
			lines = append(lines, a.row(interruptionLine(i, titles, blocks), w, false))
		}
	}
	start := 0
	if h > 0 && selectedLine >= h {
		start = selectedLine - h + 1
	}
	return clip(lines[start:], w, h)
}
