package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"taskronin/internal/api"
	"taskronin/internal/client"
)

type upcomingMsg struct {
	req int
	up  *api.Upcoming
	err error
}

type upcomingScreen struct {
	listTab
	deferred int
}

func (s *upcomingScreen) load(a *App) tea.Cmd {
	s.state = loadLoading
	req, ctx, c := a.nextReq("upcoming"), a.ctx, a.api
	return func() tea.Msg {
		up, err := c.Upcoming(ctx)
		return upcomingMsg{req: req, up: up, err: err}
	}
}

func (s *upcomingScreen) update(a *App, msg tea.Msg) tea.Cmd {
	m, ok := msg.(upcomingMsg)
	if !ok || !a.latest("upcoming", m.req) {
		return nil
	}
	if m.err != nil {
		return s.state.finish(a, m.err)
	}
	s.deferred = len(m.up.Deferred)
	items := make([]api.Task, 0, s.deferred+len(m.up.DueSoon))
	items = append(items, m.up.Deferred...)
	items = append(items, m.up.DueSoon...)
	return s.accept(a, items, false, nil)
}

func (s *upcomingScreen) handle(a *App, k tea.KeyPressMsg) (tea.Cmd, bool) {
	if s.move(k) {
		return nil, true
	}
	if a.inflight && (k.String() == "d") {
		a.setBanner(bannerInfo, "busy…")
		return nil, true
	}
	t, ok := s.list.selected()
	if !ok {
		return nil, false
	}
	switch upcomingKeys.command(k) {
	case "K": // CF10: lowercase k is navigation in every list.
		return a.mutate("kept in backlog", func(ctx context.Context, c API) (*api.OpResponse, error) {
			return c.PlaceTask(ctx, client.Mut{IfMatch: t.ETag}, t.ID, api.PlaceRequest{How: "keep"})
		}, nil, nil), true
	case "d":
		return a.openDefer(t, true, nil).focus(), true
	case "enter":
		return a.openDetail(t.ID), true
	default:
		return nil, false
	}
}

func (s *upcomingScreen) view(a *App, w, h int) string {
	headings := make(map[int]string, 2)
	if s.deferred > 0 {
		headings[0] = "Deferred"
	}
	if len(s.list.items) > s.deferred {
		headings[s.deferred] = "Due soon"
	}
	return s.listTab.view(a, "upcoming", "nothing upcoming", w, h, headings)
}
