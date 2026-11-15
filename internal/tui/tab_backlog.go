package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"taskronin/internal/api"
	"taskronin/internal/client"
)

type backlogScreen struct{ listTab }

func (s *backlogScreen) load(a *App) tea.Cmd {
	s.state = loadLoading
	return a.loadTasks("backlog", "backlog")
}

func (s *backlogScreen) update(a *App, msg tea.Msg) tea.Cmd {
	m, ok := msg.(tasksMsg)
	if !ok || m.target != "backlog" || !a.latest(m.target, m.req) {
		return nil
	}
	return s.accept(a, m.tasks, m.truncated, m.err)
}

func (s *backlogScreen) handle(a *App, k tea.KeyPressMsg) (tea.Cmd, bool) {
	if s.move(k) {
		return nil, true
	}
	if a.inflight && (k.String() == "B" || k.String() == "d") {
		a.setBanner(bannerInfo, "busy…")
		return nil, true
	}
	t, ok := s.list.selected()
	if !ok {
		return nil, false
	}
	switch backlogKeys.command(k) {
	case "J", "K":
		delta, how := 1, "after"
		if k.String() == "K" {
			delta, how = -1, "before"
		}
		neighbor := s.list.cursor + delta
		if neighbor < 0 || neighbor >= len(s.list.items) {
			return nil, true
		}
		r := api.PlaceRequest{How: how, Ref: s.list.items[neighbor].ID}
		return a.mutate("reordered", func(ctx context.Context, c API) (*api.OpResponse, error) {
			return c.PlaceTask(ctx, client.Mut{IfMatch: t.ETag}, t.ID, r)
		}, nil, nil), true
	case "x":
		return a.mutate("completed", func(ctx context.Context, c API) (*api.OpResponse, error) {
			return c.CompleteTask(ctx, client.Mut{IfMatch: t.ETag}, t.ID, api.CompleteRequest{})
		}, nil, nil), true
	case "D":
		return a.mutate("dropped", func(ctx context.Context, c API) (*api.OpResponse, error) {
			return c.DropTask(ctx, client.Mut{IfMatch: t.ETag}, t.ID)
		}, nil, nil), true
	case "f":
		r := api.FocusRequest{TaskID: api.Opt[int64]{Set: true, V: t.ID}}
		return a.mutate("focus updated", func(ctx context.Context, c API) (*api.OpResponse, error) {
			return c.SetFocus(ctx, client.Mut{}, r)
		}, nil, nil), true
	case "B":
		return a.openBlock(t, nil).focus(), true
	case "d":
		return a.openDefer(t, false, nil).focus(), true
	case "e":
		return a.editTask(t.ID), true
	case "enter":
		return a.openDetail(t.ID), true
	default:
		return nil, false
	}
}

func (s *backlogScreen) view(a *App, w, h int) string {
	return s.listTab.view(a, "backlog", "backlog is empty", w, h, nil)
}
