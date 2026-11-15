package tui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"taskronin/internal/api"
	"taskronin/internal/client"
)

type blocksMsg struct {
	req    int
	blocks []api.Block
	err    error
}

type slot struct{ Start, End int }

// freeSlots returns gaps of at least 15 minutes in today's remaining schedule.
func freeSlots(blocks []api.Block, from int) []slot {
	var planned, free []slot
	for _, b := range blocks {
		if b.Status == "planned" {
			planned = append(planned, slot{int(b.Start), int(b.End)})
		}
	}
	slices.SortFunc(planned, func(a, b slot) int { return cmp.Compare(a.Start, b.Start) })
	from = min(1440, max(0, from))
	for _, b := range planned {
		if b.Start-from >= 15 {
			free = append(free, slot{from, b.Start})
		}
		from = max(from, b.End)
	}
	if 1440-from >= 15 {
		free = append(free, slot{from, 1440})
	}
	return free
}

type planMode struct {
	step                         int
	deck                         *cardDeck
	list                         taskList
	blocks                       []api.Block
	step2Blocked                 int
	backlogLoaded, backlogFailed bool
	blocksLoaded, blocksFailed   bool
}

func newPlanMode() *planMode { return &planMode{step: 1, deck: &cardDeck{}} }

func (s *planMode) load(a *App) tea.Cmd {
	switch s.step {
	case 1:
		return s.deck.load(a, "plan-triage", "triage")
	case 2:
		return tea.Batch(s.loadBacklog(a), s.loadBlocks(a))
	default:
		return s.loadBlocks(a)
	}
}

func (s *planMode) loadBacklog(a *App) tea.Cmd {
	s.backlogFailed = false
	req, ctx, c := a.nextReq("plan-backlog"), a.ctx, a.api
	return func() tea.Msg {
		page, err := c.ListTasks(ctx, client.TaskQuery{View: "backlog", PageQuery: client.PageQuery{Limit: 10}})
		msg := tasksMsg{req: req, target: "plan-backlog", err: err}
		if err == nil {
			msg.tasks = page.Items
		}
		return msg
	}
}

func (s *planMode) loadBlocks(a *App) tea.Cmd {
	s.blocksFailed = false
	req, ctx, c, today := a.nextReq("plan-blocks"), a.ctx, a.api, a.clock.Today
	return func() tea.Msg {
		blocks, err := c.ListBlocks(ctx, client.BlockQuery{From: &today, To: &today})
		return blocksMsg{req: req, blocks: blocks, err: err}
	}
}

func (s *planMode) update(a *App, msg tea.Msg) tea.Cmd {
	if s.step == 1 {
		cmd := s.deck.update(a, msg, "plan-triage")
		m, ok := msg.(tasksMsg)
		// AIDEV-NOTE: exhaustion must be confirmed by the latest successful
		// triage read. An empty queue while loading or after a failed read is
		// not permission to skip triage; the deck owns that bookkeeping.
		if ok && m.target == "plan-triage" && a.latest(m.target, m.req) && m.err == nil && s.deck.loaded {
			if _, current := s.deck.current(); !current {
				s.step = 2
				return tea.Batch(cmd, s.load(a))
			}
		}
		return cmd
	}
	switch m := msg.(type) {
	case tasksMsg:
		if m.target != "plan-backlog" || !a.latest(m.target, m.req) {
			return nil
		}
		if m.err != nil {
			s.backlogFailed = true
			return a.handleReadErr(m.err)
		}
		s.list.set(m.tasks)
		s.backlogLoaded, s.backlogFailed = true, false
	case blocksMsg:
		if !a.latest("plan-blocks", m.req) {
			return nil
		}
		if m.err != nil {
			s.blocksFailed = true
			return a.handleReadErr(m.err)
		}
		s.blocks, s.blocksLoaded, s.blocksFailed = m.blocks, true, false
	}
	return nil
}

func (s *planMode) selection() (int64, string, bool) {
	switch s.step {
	case 1:
		t, ok := s.deck.current()
		return t.ID, t.Title, ok
	case 2:
		return s.list.selection()
	default:
		return 0, "", false
	}
}

func (s *planMode) handle(a *App, k tea.KeyPressMsg) (tea.Cmd, bool) {
	if k.String() == "enter" {
		if a.inflight {
			a.setBanner(bannerInfo, "busy…")
			return nil, true
		}
		if s.step == 3 {
			return a.exitMode(), true
		}
		if s.step == 1 && (!s.deck.loaded || s.deck.loadFailed) {
			return nil, true
		}
		s.step++
		return a.refresh(), true
	}
	if s.step == 1 {
		return cardKeys(a, s.deck, k)
	}
	if s.step == 2 {
		switch k.String() {
		case "j", "down": // App only aliases arrows in tabs and overlays.
			s.list.move(1)
			return nil, true
		case "k", "up":
			s.list.move(-1)
			return nil, true
		case "B":
			if a.inflight {
				a.setBanner(bannerInfo, "busy…")
				return nil, true
			}
			if t, ok := s.list.selected(); ok {
				return s.openBlockToday(a, t), true
			}
			return nil, true
		}
	}
	return nil, false
}

func (s *planMode) openBlockToday(a *App, t api.Task) tea.Cmd {
	cmd := a.openBlock(t, func(_ *App, _ *api.OpResponse) tea.Cmd {
		s.step2Blocked++
		// App's successful-op refresh reloads both blocks and backlog.
		return nil
	})
	f := a.form.(*form)
	submit := f.submit
	f.hint = "today's span, e.g. 13:00-15:00 · enter saves · esc cancels"
	// AIDEV-NOTE: validate only fresh submissions. The shared form still
	// replays an unknown outcome's exact snapshot, even across midnight.
	f.submit = func(a *App, value string) (string, sendFunc, error) {
		span, err := a.parseSpan(value)
		if err != nil {
			return "", nil, err
		}
		if span.Date != a.clock.Today {
			return "", nil, fmt.Errorf("plan blocks must be for today (%s)", a.clock.Today)
		}
		return submit(a, value)
	}
	return cmd
}

func (s *planMode) freeText(a *App) string {
	if !s.blocksLoaded {
		if s.blocksFailed {
			return "blocks not loaded"
		}
		return "loading blocks…"
	}
	from := a.clock.Now.Hour()*60 + a.clock.Now.Minute()
	var spans []string
	for _, slot := range freeSlots(s.blocks, from) {
		spans = append(spans, hhmm(slot.Start)+"–"+hhmm(slot.End))
	}
	if len(spans) == 0 {
		return "no free time left today"
	}
	return "free " + strings.Join(spans, ", ")
}

func (s *planMode) view(a *App, w, h int) string {
	if s.step == 1 {
		return (&cardMode{deck: s.deck}).view(a, w, h)
	}
	if s.step == 2 {
		leftWidth := max(0, w/2)
		rightWidth := max(0, w-leftWidth-3)
		left := []string{a.line("Backlog top 10", leftWidth)}
		switch {
		case !s.backlogLoaded && s.backlogFailed:
			left = append(left, a.line("backlog not loaded", leftWidth))
		case !s.backlogLoaded:
			left = append(left, a.line("loading backlog…", leftWidth))
		case len(s.list.items) == 0:
			left = append(left, a.line("backlog is empty", leftWidth))
		default:
			body := s.list.render(a, leftWidth, max(0, h-2), func(t api.Task, width int) string { return taskRow(a, t, width) })
			left = append(left, strings.Split(body, "\n")...)
		}
		left[0] += strings.Repeat(" ", max(0, leftWidth-lipgloss.Width(left[0]))+3) + a.line(s.freeText(a), rightWidth)
		lines := []string{a.line("Plan 2/3 · j/k/↑/↓ move · B block today · enter summary", w)}
		lines = append(lines, left...)
		return clip(lines, w, h)
	}
	triageLeft := 0
	if a.today != nil {
		triageLeft = a.today.TriageCount
	}
	lines := []string{
		a.line("Plan 3/3 · enter/q ends", w),
		a.line(fmt.Sprintf("placed %d · blocked %d · triage left %d", s.deck.placed, s.deck.blocked+s.step2Blocked, triageLeft), w),
		a.line("Today's blocks · "+a.clock.Today.String(), w),
	}
	if !s.blocksLoaded {
		lines = append(lines, a.line(s.freeText(a), w))
	} else if len(s.blocks) == 0 {
		lines = append(lines, a.line("no blocks today", w))
	}
	for _, b := range s.blocks {
		lines = append(lines, a.line(fmt.Sprintf("b%d %s #%d %s · %s", b.ID, spanText(b.Start, b.End), b.TaskID, b.TaskTitle, b.Status), w))
	}
	return clip(lines, w, h)
}
