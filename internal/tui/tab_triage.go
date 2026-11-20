package tui

import (
	tea "charm.land/bubbletea/v2"
)

type triageScreen struct{ listTab }

func (s *triageScreen) load(a *App) tea.Cmd {
	s.state = loadLoading
	return a.loadTasks("triage", "triage")
}

func (s *triageScreen) update(a *App, msg tea.Msg) tea.Cmd {
	m, ok := msg.(tasksMsg)
	if !ok || m.target != "triage" || !a.latest(m.target, m.req) {
		return nil
	}
	return s.accept(a, m.tasks, m.truncated, m.err)
}

func (s *triageScreen) handle(a *App, k tea.KeyPressMsg) (tea.Cmd, bool) {
	if s.move(k) {
		return nil, true
	}
	switch triageKeys.command(k) {
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
	return s.listTab.view(a, "triage", "Triage clear ✓ — nothing needs a decision", w, h, nil)
}
