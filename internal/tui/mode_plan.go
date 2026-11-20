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
	step          int
	deck          *cardDeck
	list          taskList
	blocks        []api.Block
	step2Blocked  int
	backlogState  loadState
	blocksState   loadState
	backlogLoaded bool
	blocksLoaded  bool
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
	s.backlogState = loadLoading
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
	s.blocksState = loadLoading
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
		if ok && m.target == "plan-triage" && a.latest(m.target, m.req) && m.err == nil && s.deck.state == loadLoaded {
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
			return s.backlogState.finish(a, m.err, s)
		}
		s.list.set(m.tasks)
		s.backlogLoaded = true
		s.backlogState.finish(a, nil)
	case blocksMsg:
		if !a.latest("plan-blocks", m.req) {
			return nil
		}
		if m.err != nil {
			return s.blocksState.finish(a, m.err, s)
		}
		s.blocks = m.blocks
		s.blocksLoaded = true
		s.blocksState.finish(a, nil)
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
		if s.step == 1 && s.deck.state != loadLoaded {
			return nil, true
		}
		s.step++
		return a.refresh(), true
	}
	if s.step == 1 {
		return cardKeys(a, s.deck, k)
	}
	if s.step == 2 {
		switch planBacklogKeys.command(k) {
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
	f := a.openBlock(t, func(_ *App, _ *api.OpResponse) tea.Cmd {
		s.step2Blocked++
		// App's successful-op refresh reloads both blocks and backlog.
		return nil
	})
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
	return f.focus()
}

func (s *planMode) freeText(a *App) string {
	if !s.blocksLoaded {
		return s.blocksState.notice("blocks")
	}
	// AIDEV-NOTE: clock.Now advances on Today fetches (normally the 5 s poll).
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
		header := a.line("Plan 1/3 · enter skips to backlog", w)
		body := s.deck.screenView(a, w, max(0, h-1))
		return clip(append([]string{header}, strings.Split(body, "\n")...), w, h)
	}
	if s.step == 2 {
		leftWidth := max(0, w/2)
		rightWidth := max(0, w-leftWidth-3)
		left := []string{a.line("Backlog top 10", leftWidth)}
		if notice := s.backlogState.retainedNotice("backlog", s.backlogLoaded); notice != "" {
			left = append(left, a.line(notice, leftWidth))
		}
		if s.backlogState == loadLoaded && len(s.list.items) == 0 {
			left = append(left, a.line("Backlog empty — nothing to block", leftWidth))
		} else {
			body := s.list.render(a, leftWidth, max(0, h-1-len(left)), func(t api.Task, width int) string { return taskRow(a, t, width) })
			left = append(left, strings.Split(body, "\n")...)
		}
		right := []string{}
		if notice := s.blocksState.retainedNotice("blocks", s.blocksLoaded); notice != "" {
			right = append(right, notice)
		}
		if s.blocksLoaded {
			right = append(right, strings.Split(lipgloss.Wrap(s.freeText(a), rightWidth, ""), "\n")...)
		}
		for i, text := range right {
			if i == len(left) {
				left = append(left, "")
			}
			left[i] += strings.Repeat(" ", max(0, leftWidth-lipgloss.Width(left[i]))+3) + a.line(text, rightWidth)
		}
		lines := []string{a.line("Plan 2/3 · j/k/↑/↓ move · B block today · enter summary", w)}
		lines = append(lines, left...)
		return clip(lines, w, h)
	}
	summary := a.todayLoad.notice("Today")
	if a.todayLoad == loadLoaded && a.today != nil {
		triage := "triage clear"
		if a.today.TriageCount > 0 {
			triage = fmt.Sprintf("triage left %d", a.today.TriageCount)
		}
		summary = fmt.Sprintf("Plan done · placed %d · blocked %d · %s", s.deck.placed, s.deck.blocked+s.step2Blocked, triage)
	}
	lines := []string{
		a.line("Plan 3/3 · enter/q ends", w),
		a.line(summary, w),
		a.line("Today's blocks · "+a.clock.Today.String(), w),
	}
	if s.blocksState != loadLoaded {
		lines = append(lines, a.line(s.blocksState.notice("blocks"), w))
	} else if len(s.blocks) == 0 {
		lines = append(lines, a.line("no blocks today", w))
	}
	for _, b := range s.blocks {
		if s.blocksState != loadLoaded {
			break
		}
		lines = append(lines, a.blockText(b)+" · "+a.blockStatus(b.Status))
	}
	return clip(lines, w, h)
}
