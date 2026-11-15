package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"taskronin/internal/api"
	"taskronin/internal/client"
)

type backlogScreen struct{ list taskList }

func (*backlogScreen) load(a *App) tea.Cmd { return a.loadTasks("backlog", "backlog") }

func (s *backlogScreen) update(a *App, msg tea.Msg) tea.Cmd {
	m, ok := msg.(tasksMsg)
	if !ok || m.target != "backlog" || !a.latest(m.target, m.req) {
		return nil
	}
	if m.err != nil {
		return a.handleReadErr(m.err)
	}
	s.list.set(m.tasks)
	if m.truncated {
		a.setBanner(bannerInfo, "showing the first 2500")
	}
	return nil
}

func (s *backlogScreen) selection() (int64, string, bool) { return s.list.selection() }

func (s *backlogScreen) handle(a *App, k tea.KeyPressMsg) (tea.Cmd, bool) {
	switch k.String() {
	case "j":
		s.list.move(1)
		return nil, true
	case "k":
		s.list.move(-1)
		return nil, true
	}
	t, ok := s.list.selected()
	if !ok {
		return nil, false
	}
	switch k.String() {
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
		return a.openBlock(t, nil), true
	case "d":
		return a.openDefer(t, false, nil), true
	case "e":
		return a.editTask(t.ID), true
	case "enter":
		return a.openDetail(t.ID), true
	default:
		return nil, false
	}
}

func (s *backlogScreen) view(a *App, w, h int) string {
	if len(s.list.items) == 0 {
		return a.line("backlog is empty", w)
	}
	return s.list.render(a, w, h, func(t api.Task, width int) string { return taskRow(a, t, width) })
}
