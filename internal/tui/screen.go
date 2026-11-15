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

// AIDEV-NOTE: screen methods use App synchronously on the model goroutine.
// Returned tea.Cmd closures run off the Update goroutine: capture their inputs
// (client, context, request values/IDs) and never read or mutate *App from them.
// Loads allocate nextReq when constructing the command and handle errors through
// handleReadErr in update; mutations go through mutate/handleErr.
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

// loadState distinguishes an unanswered read from a successful empty result.
// Its address scopes read-error recovery to the owning screen (and Plan pane).
type loadState uint8

const (
	loadLoading loadState = iota
	loadLoaded
	loadFailed
)

func (s *loadState) finish(a *App, err error) tea.Cmd {
	if err != nil {
		*s = loadFailed
		return a.handleReadErr(err, s)
	}
	*s = loadLoaded
	if a.banner.readError && a.banner.readTarget == s {
		a.banner = banner{}
	}
	return nil
}

func (s loadState) notice(name string) string {
	switch s {
	case loadLoading:
		return "loading " + name + "…"
	case loadFailed:
		return name + " not loaded · r retries"
	default:
		return ""
	}
}
