package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"taskronin/internal/api"
	"taskronin/internal/client"
)

func detailFixture() *fakeAPI {
	f := editFixture()
	d := f.details[42]
	d.Reasons = []api.Reason{{Reason: "manual", Since: f.clock.Now}}
	d.Slips = api.Slips{Lifetime: 3, SinceTriage: 2}
	d.Notes = "ok\x1b]52;c;evil\x07"
	d.Tags = []string{"old"}
	d.Blocks = []api.Block{{ID: 7, Date: f.clock.Today, Start: 780, End: 900, Status: "missed"}}
	d.Entries = []api.Entry{{ID: 9, WorkDate: f.clock.Today, Title: "Work", Kind: "work"}}
	f.details[42] = d
	f.history[42] = []api.EventView{{Op: api.OpSummary{At: f.clock.Now, Kind: "task.update", Actor: "owner"}, Entity: "task", Action: "update"}}
	return f
}

func TestDetailOverlay(t *testing.T) {
	f := detailFixture()
	a := newTestApp(t, f)
	drain(t, a, a.openDetail(42))
	detail := a.current()
	v := viewText(a)
	for _, want := range []string{"#42 Old", "manual", "3 lifetime", "2 since triage", "b7", "13:00–15:00", "e9", "Work", "ok"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q in\n%s", want, v)
		}
	}
	if strings.ContainsRune(v, '\x1b') {
		t.Fatal(v)
	}
	id, title, selected := detail.selection()
	eq(t, id, int64(42))
	eq(t, title, "Old")
	eq(t, selected, true)
	press(t, a, "t")
	typeText(t, a, "+db -old x")
	press(t, a, "enter")
	eq(t, f.mustLast(t, "UpdateTask"), call{Method: "UpdateTask", ID: 42, Mut: client.Mut{IfMatch: "g-42"}, Req: api.UpdateTaskRequest{TagsAdd: []string{"db", "x"}, TagsRemove: []string{"old"}}})
	eq(t, a.form, nil)
	press(t, a, "h")
	eq(t, f.mustLast(t, "TaskHistory"), call{Method: "TaskHistory", ID: 42, Q: client.PageQuery{Limit: 100}})
	if !strings.Contains(viewText(a), "10:00 task.update by owner  task:update") {
		t.Fatal(viewText(a))
	}
	press(t, a, "esc")
	eq(t, a.current() == detail, true)
	press(t, a, "esc")
	eq(t, a.current() == a.tabs[TabToday], true)
}

func TestDetailEditUsesFreshETag(t *testing.T) {
	f, runs := detailFixture(), 0
	a := newTestApp(t, f, func(c *Config) { c.Editor.Exec = stubExec("New\n\nn1", nil, &runs) })
	base := &recScreen{}
	a.setTabScreen(TabBacklog, base)
	press(t, a, "3")
	drain(t, a, a.openDetail(42))
	d := f.details[42]
	d.ETag = "fresh-42"
	d.Notes = "n1"
	f.details[42] = d
	press(t, a, "e")
	eq(t, runs, 1)
	eq(t, f.mustLast(t, "UpdateTask").Mut.IfMatch, "fresh-42")
	eq(t, f.count("GetTask"), 3) // Open, edit preflight, own-op refresh.
	eq(t, base.loads, 1)
	press(t, a, "esc")
	eq(t, base.loads, 2)
	drain(t, a, a.openDetail(42))
	press(t, a, "esc")
	eq(t, base.loads, 2)
}

func TestDetailOwnOpReloadsRevealedScreenOnce(t *testing.T) {
	for _, mode := range []bool{false, true} {
		for _, success := range []bool{false, true} {
			t.Run(fmt.Sprintf("mode=%v/success=%v", mode, success), func(t *testing.T) {
				f := detailFixture()
				a := newTestApp(t, f)
				base := &recScreen{}
				if mode {
					drain(t, a, a.enterMode(ModePlan, base))
				} else {
					a.setTabScreen(TabBacklog, base)
					press(t, a, "3")
				}
				drain(t, a, a.openDetail(42))
				press(t, a, "h")
				for range 2 {
					if !success {
						f.errs["Undo"] = problem("validation", 422, "refused")
					}
					drain(t, a, a.mutate("undo", func(ctx context.Context, c API) (*api.OpResponse, error) { return c.Undo(ctx, client.Mut{}) }, nil, nil))
				}
				eq(t, base.loads, 1)
				press(t, a, "esc")
				eq(t, base.loads, 1)
				press(t, a, "esc")
				want := 1
				if success {
					want++
				}
				eq(t, base.loads, want)
				// Reopening and closing without another op must not refetch again.
				drain(t, a, a.openDetail(42))
				press(t, a, "esc")
				eq(t, base.loads, want)
			})
		}
	}
}

func TestDetailLoadsIgnoreStaleReplies(t *testing.T) {
	for _, history := range []bool{false, true} {
		f := detailFixture()
		a := newTestApp(t, f)
		var first tea.Cmd
		method := "GetTask"
		if history {
			first = a.openHistory(42)
			method = "TaskHistory"
		} else {
			first = a.openDetail(42)
		}
		second := a.current().load(a)
		drain(t, a, second)
		before := viewText(a)
		f.errs[method] = outcomeUnknown("")
		drain(t, a, first)
		eq(t, viewText(a), before)
		eq(t, a.banner.text, "")
		f.errs[method] = notSent()
		drain(t, a, a.current().load(a))
		eq(t, a.banner.text, "cannot reach the Taskronin server")
		eq(t, f.count(method), 3)
		eq(t, f.count("Today"), 1)
	}
}

func TestDetailAndHistoryScrollSanitizeAndClip(t *testing.T) {
	for _, history := range []bool{false, true} {
		f := detailFixture()
		d := f.details[42]
		d.Title = "unsafe\x1b\u009b\u202e" + strings.Repeat("宽", 100)
		d.Notes = strings.Repeat("note\n", 50) + "last note"
		f.details[42] = d
		for i := range 50 {
			f.history[42] = append(f.history[42], api.EventView{Op: api.OpSummary{At: f.clock.Now, Kind: "update", Actor: d.Title}, Entity: "task", Action: fmt.Sprintf("change-%d", i)})
		}
		a := newTestApp(t, f)
		if history {
			drain(t, a, a.openHistory(42))
		} else {
			drain(t, a, a.openDetail(42))
		}
		a.Update(tea.WindowSizeMsg{Width: 40, Height: 8})
		before := viewText(a)
		press(t, a, "j", "j", "j")
		if viewText(a) == before {
			t.Fatal("did not scroll down")
		}
		press(t, a, "k", "k", "k")
		eq(t, viewText(a), before)
		for _, size := range []tea.WindowSizeMsg{{Width: 20, Height: 5}, {Width: 1, Height: 1}, {}} {
			a.Update(size)
			v := viewText(a)
			if strings.ContainsAny(v, "\x1b\u009b\u202e") {
				t.Fatalf("unsafe %q", v)
			}
			for _, line := range strings.Split(v, "\n") {
				if lipgloss.Width(line) > size.Width {
					t.Fatal("oversized line", line)
				}
			}
			if size.Height > 0 && len(strings.Split(v, "\n")) > size.Height {
				t.Fatal("too many lines")
			}
		}
	}
}
