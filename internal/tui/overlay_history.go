package tui

import (
	"fmt"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/textsafe"
)

type historyScreen struct {
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
	s.req = req
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
		return a.handleReadErr(m.err)
	}
	s.page = m.page
	return nil
}

func (s *historyScreen) selection() (int64, string, bool) { return s.id, "", true }

func (s *historyScreen) handle(_ *App, k tea.KeyPressMsg) (tea.Cmd, bool) {
	switch k.String() {
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
		return a.line(fmt.Sprintf("Loading history for #%d…", s.id), w)
	}
	lines := []string{fmt.Sprintf("History for #%d", s.id), "j/k scroll · esc back"}
	for _, e := range s.page.Items {
		stamp := e.Op.At.Format("15:04")
		if e.Op.At.Format("2006-01-02") != a.clock.Today.String() {
			stamp = e.Op.At.Format("2006-01-02 15:04")
		}
		lines = append(lines, textsafe.SanitizeLine(fmt.Sprintf("%s %s by %s  %s:%s", stamp, e.Op.Kind, e.Op.Actor, e.Entity, e.Action)))
	}
	if len(s.page.Items) == 0 {
		lines = append(lines, "No history")
	}
	if s.page.NextCursor != nil {
		lines = append(lines, "more… (older events not shown)")
	}
	return overlayViewport(&s.viewport, lines, w, h)
}
