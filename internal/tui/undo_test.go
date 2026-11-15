package tui

import (
	"context"
	"errors"
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"

	"taskronin/internal/api"
	"taskronin/internal/client"
)

func TestUndoRedo(t *testing.T) { // UND-18 (TUI side).
	for _, mode := range []Mode{ModeTabs, ModeTriage, ModePlan, ModeRecap} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			f := newFake()
			f.resp.Effects = []api.Effect{{Kind: "reverted"}}
			a := newTestApp(t, f)
			s := &recScreen{}
			if mode == ModeTabs {
				a.setTabScreen(TabToday, s)
			} else {
				drain(t, a, a.enterMode(mode, s))
			}
			for _, tc := range []struct{ key, method, label, code, empty string }{
				{"u", "Undo", "undone", "nothing_to_undo", "nothing to undo"},
				{"ctrl+r", "Redo", "redone", "nothing_to_redo", "nothing to redo"},
			} {
				loads, today := s.loads, f.count("Today")
				press(t, a, tc.key)
				eq(t, f.mustLast(t, tc.method), call{Method: tc.method, Mut: client.Mut{}})
				eq(t, a.banner.text, tc.label+" · reverted")
				eq(t, f.count("Today"), today+1)
				eq(t, s.loads, loads+1)
				f.errs[tc.method] = problem(tc.code, 409, "")
				press(t, a, tc.key)
				eq(t, a.banner.text, tc.empty)
				eq(t, f.count("Today"), today+1)
			}
		})
	}
}

func TestUndoRedoBusy(t *testing.T) {
	for _, first := range []string{"u", "ctrl+r", "other mutation"} {
		t.Run(first, func(t *testing.T) {
			f := newFake()
			a := newTestApp(t, f)
			var cmd tea.Cmd
			if first == "other mutation" {
				cmd = a.mutate("completed", func(ctx context.Context, c API) (*api.OpResponse, error) {
					return c.CompleteTask(ctx, client.Mut{IfMatch: "g-42"}, 42, api.CompleteRequest{})
				}, nil, nil)
			} else {
				_, cmd = a.Update(key(first))
			}
			if cmd == nil || !a.inflight {
				t.Fatal("mutation did not reserve the guard")
			}
			press(t, a, "u", "ctrl+r", "u", "ctrl+r")
			eq(t, f.count("Undo")+f.count("Redo")+f.count("CompleteTask"), 0)
			eq(t, a.banner.text, "busy…")
			drain(t, a, cmd)
			eq(t, f.count("Undo")+f.count("Redo")+f.count("CompleteTask"), 1)
			eq(t, a.inflight, false)
		})
	}
}

func TestUndoRedoOverlayRefresh(t *testing.T) {
	f := newFake()
	a := newTestApp(t, f)
	base, overlay := &recScreen{}, &recScreen{}
	a.setTabScreen(TabToday, base)
	drain(t, a, a.openOverlay(overlay))
	press(t, a, "u", "ctrl+r")
	eq(t, overlay.loads, 3)
	eq(t, f.count("Today"), 3)
	eq(t, base.loads, 0)
	eq(t, a.overlayDirty, true)
	press(t, a, "esc")
	eq(t, base.loads, 1)
	eq(t, a.overlayDirty, false)
}

func TestUndoRedoInRecap(t *testing.T) {
	f := newFake()
	f.days[f.clock.Today] = api.Day{Date: f.clock.Today}
	a := newTestApp(t, f, func(c *Config) { c.StartMode = ModeRecap })
	press(t, a, "u", "ctrl+r")
	eq(t, f.count("Undo"), 1)
	eq(t, f.count("Redo"), 1)
	eq(t, f.count("Day"), 3)
	eq(t, f.count("Today"), 3)
}

func TestUndoRedoGlobalsRespectForms(t *testing.T) {
	for _, k := range []string{"u", "ctrl+r", "A", "/"} {
		t.Run(k, func(t *testing.T) {
			f := newFake()
			a := newTestApp(t, f)
			press(t, a, "a")
			fm := a.form.(*form)
			press(t, a, k)
			eq(t, a.form == fm, true)
			if k != "ctrl+r" {
				eq(t, fm.input.Value(), k)
			}
			press(t, a, "esc")
			confirmed := false
			drain(t, a, a.confirm("confirm", func(*App) tea.Cmd { confirmed = true; return nil }))
			press(t, a, k)
			eq(t, confirmed, false)
			eq(t, f.count("Undo")+f.count("Redo")+f.count("Search")+f.count("AIChanges"), 0)
			eq(t, len(a.overlays), 0)
		})
	}
}

func TestUndoRedoErrorPolicy(t *testing.T) {
	for _, k := range []string{"u", "ctrl+r"} {
		for _, tc := range []struct {
			err     error
			banner  string
			refresh bool
		}{
			{notSent(), "cannot reach the Taskronin server — nothing was saved", false},
			{problem("precondition_failed", 412, ""), "changed elsewhere — refreshed", true},
			{problem("generation_mismatch", 412, ""), "database was restored — refreshed", true},
			{&client.OutcomeUnknownError{Err: errors.Join(notSent(), problem("precondition_failed", 412, ""))}, "outcome unknown — refreshed; check before repeating", true},
		} {
			t.Run(k+"/"+tc.banner, func(t *testing.T) {
				f := newFake()
				a := newTestApp(t, f)
				method := "Undo"
				if k == "ctrl+r" {
					method = "Redo"
				}
				f.errs[method] = tc.err
				press(t, a, k)
				eq(t, f.count(method), 1)
				eq(t, a.banner.text, tc.banner)
				eq(t, f.count("Today") == 2, tc.refresh)
			})
		}
	}
}
