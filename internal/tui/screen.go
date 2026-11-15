package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
)

// Tab identifies one of the five main views.
type Tab int

// Main tabs, in display order.
const (
	TabToday Tab = iota
	TabTriage
	TabBacklog
	TabUpcoming
	TabLog
)

// Mode identifies an interactive workflow, or the normal tabbed view.
type Mode int

// Interactive workflows available from the tabbed app or at startup.
const (
	ModeTabs Mode = iota
	ModeTriage
	ModePlan
	ModeRecap
)

var tabNames = [...]string{"Today", "Triage", "Backlog", "Upcoming", "Log"}

// ParseTab accepts the tab names used by the CLI.
func ParseTab(s string) (Tab, error) {
	switch s {
	case "today":
		return TabToday, nil
	case "triage":
		return TabTriage, nil
	case "backlog":
		return TabBacklog, nil
	case "upcoming":
		return TabUpcoming, nil
	case "log":
		return TabLog, nil
	default:
		return TabToday, fmt.Errorf("unknown tab %q (want today, triage, backlog, upcoming, or log)", s)
	}
}

type screen interface {
	load(a *App) tea.Cmd
	handle(a *App, k tea.KeyPressMsg) (tea.Cmd, bool)
	update(a *App, msg tea.Msg) tea.Cmd
	view(a *App, w, h int) string
	selection() (taskID int64, title string, ok bool)
}

type stubScreen struct{ name string }

func (stubScreen) load(_ *App) tea.Cmd                              { return nil }
func (stubScreen) handle(_ *App, _ tea.KeyPressMsg) (tea.Cmd, bool) { return nil, false }
func (stubScreen) update(_ *App, _ tea.Msg) tea.Cmd                 { return nil }
func (s stubScreen) view(a *App, w, _ int) string                   { return a.line(s.name, w) }
func (stubScreen) selection() (int64, string, bool)                 { return 0, "", false }
