package tui

import (
	"reflect"
	"time"

	tea "charm.land/bubbletea/v2"

	"taskronin/internal/api"
)

type pollMsg struct{}

func (a *App) pollTick() tea.Cmd {
	if a.cfg.PollInterval < 0 {
		return nil
	}
	return tea.Tick(a.cfg.PollInterval, func(time.Time) tea.Msg { return pollMsg{} })
}

func (a *App) poll() tea.Cmd {
	// AIDEV-NOTE: ETag poll loop. Always re-arm, but allow only one poll in
	// flight. Its reply releases pollReq even if a newer own-op read made it
	// stale. All request/guard state is captured on the model goroutine.
	if a.pollReq != 0 {
		return a.pollTick()
	}
	return tea.Batch(a.fetchToday(true), a.pollTick())
}

func todayPayloadChanged(before, after *api.Today) bool {
	if before == nil || after == nil {
		return before != after
	}
	// CF02: the server ETag includes clock.now, so real polls normally return
	// 200. Compare copies without Clock to avoid resetting other screens for
	// clock ticks. The civil date still matters for date-scoped screen loads.
	if before.Clock.Today != after.Clock.Today {
		return true
	}
	b, a := *before, *after
	b.Clock, a.Clock = api.Clock{}, api.Clock{}
	return !reflect.DeepEqual(b, a)
}
