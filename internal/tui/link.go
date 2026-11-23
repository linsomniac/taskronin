package tui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/client"
)

type link struct {
	TaskID int64
	Has    bool
	Why    string
}

func (a *App) logLink() link {
	if id, _, ok := a.current().selection(); ok {
		return link{TaskID: id, Has: true, Why: "selected"}
	}
	if a.today != nil {
		if a.today.Focus != nil {
			return link{TaskID: a.today.Focus.ID, Has: true, Why: "focus"}
		}
		if a.today.CurrentBlock != nil {
			return link{TaskID: a.today.CurrentBlock.TaskID, Has: true, Why: "current block"}
		}
	}
	return link{Why: "no task"}
}

func (l link) label() string {
	if !l.Has {
		return "→ no task"
	}
	return fmt.Sprintf("→ #%d (%s)", l.TaskID, l.Why)
}

func (a *App) openLog(date civil.Date, l link) tea.Cmd {
	f := a.openForm("Log "+l.label(), "enter saves · esc cancels", "", func(a *App, value string) (string, sendFunc, error) {
		r := api.AddEntryRequest{Title: value, TaskID: api.Opt[int64]{Set: true, Null: !l.Has, V: l.TaskID}}
		if date != a.clock.Today {
			r.WorkDate = &date
		}
		label := "logged (no task)"
		if l.Has {
			label = fmt.Sprintf("logged → #%d", l.TaskID)
		}
		return label, func(ctx context.Context, c API, key string) (*api.OpResponse, error) {
			return c.AddEntry(ctx, client.Mut{Key: key}, r)
		}, nil
	}, nil)
	// Historical dates and explicit selections retain their supplied link.
	// A retry keeps both the echoed link and resolved request until Enter
	// discards the snapshot for changed text or a definitive/stale reply.
	if date == a.clock.Today && l.Why != "selected" {
		f.onToday = func(a *App) {
			if date == a.clock.Today {
				l = a.logLink()
				f.title = "Log " + l.label()
			}
		}
	}
	return tea.Batch(f.focus(), a.fetchToday(false))
}
