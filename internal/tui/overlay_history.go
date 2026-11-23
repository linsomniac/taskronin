package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/client"
	"github.com/linsomniac/taskronin/internal/textsafe"
)

type historyScreen struct {
	state    loadState
	id       int64
	req      int
	page     *api.Page[api.EventView]
	viewport viewport.Model
}

type historyMsg struct {
	req  int
	page *api.Page[api.EventView]
	err  error
}

func (a *App) openHistory(id int64) tea.Cmd {
	return a.openOverlay(&historyScreen{id: id, viewport: viewport.New()})
}

func (s *historyScreen) load(a *App) tea.Cmd {
	req, ctx, c, id := a.nextReq("history"), a.ctx, a.api, s.id
	s.req, s.state = req, loadLoading
	return func() tea.Msg {
		p, err := c.TaskHistory(ctx, id, client.PageQuery{Limit: 100})
		return historyMsg{req: req, page: p, err: err}
	}
}

func (s *historyScreen) update(a *App, msg tea.Msg) tea.Cmd {
	m, ok := msg.(historyMsg)
	if !ok || m.req != s.req || !a.latest("history", m.req) {
		return nil
	}
	if m.err != nil {
		return s.state.finish(a, m.err, s)
	}
	s.state.finish(a, nil)
	s.page = m.page
	return nil
}

func (s *historyScreen) selection() (int64, string, bool) { return s.id, "", true }

func (s *historyScreen) handle(_ *App, k tea.KeyPressMsg) (tea.Cmd, bool) {
	switch listKeys.command(k) {
	case "j":
		s.viewport.ScrollDown(1)
	case "k":
		s.viewport.ScrollUp(1)
	default:
		return nil, false
	}
	return nil, true
}

func (s *historyScreen) view(a *App, w, h int) string {
	if s.page == nil {
		return a.line(s.state.notice("history"), w)
	}
	lines := []string{fmt.Sprintf("History for #%d", s.id), "j/k scroll · esc back"}
	for _, e := range s.page.Items {
		stamp := eventTime(e.Op.At, a.clock.Today)
		lines = append(lines, textsafe.SanitizeLine(fmt.Sprintf("%s %s by %s  %s:%s", stamp, e.Op.Kind, e.Op.Actor, e.Entity, e.Action)))
	}
	if len(s.page.Items) == 0 {
		lines = append(lines, "No history")
	}
	if s.page.NextCursor != nil {
		lines = append(lines, "more… (older events not shown)")
	}
	if notice := s.state.retainedNotice("history", true); notice != "" {
		body := overlayViewport(&s.viewport, lines, w, max(0, h-1))
		return clip(append([]string{a.line(notice, w)}, strings.Split(body, "\n")...), w, h)
	}
	return overlayViewport(&s.viewport, lines, w, h)
}

func eventTime(at api.Time, today civil.Date) string {
	if at.Format("2006-01-02") != today.String() {
		return at.Format("2006-01-02 15:04")
	}
	return at.Format("15:04")
}
