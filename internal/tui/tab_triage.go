package tui

import (
	tea "charm.land/bubbletea/v2"

	"taskronin/internal/api"
)

type triageScreen struct{ list taskList }

func (*triageScreen) load(a *App) tea.Cmd { return a.loadTasks("triage", "triage") }

func (s *triageScreen) update(a *App, msg tea.Msg) tea.Cmd {
	m, ok := msg.(tasksMsg)
	if !ok || m.target != "triage" || !a.latest(m.target, m.req) {
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

func (s *triageScreen) selection() (int64, string, bool) { return s.list.selection() }

func (s *triageScreen) handle(a *App, k tea.KeyPressMsg) (tea.Cmd, bool) {
	switch k.String() {
	case "j":
		s.list.move(1)
	case "k":
		s.list.move(-1)
	case "enter":
		if t, ok := s.list.selected(); ok {
			return a.enterMode(ModeTriage, newCardMode(t.ID)), true
		}
	default:
		return nil, false
	}
	return nil, true
}

func (s *triageScreen) view(a *App, w, h int) string {
	if len(s.list.items) == 0 {
		return a.line("nothing in triage", w)
	}
	return s.list.render(a, w, h, func(t api.Task, width int) string { return taskRow(a, t, width) })
}
