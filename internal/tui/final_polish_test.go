package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"taskronin/internal/api"
	"taskronin/internal/client"
)

func TestFinalQuitAndEscapeGuards(t *testing.T) {
	for _, mode := range []Mode{ModeTabs, ModeTriage, ModePlan, ModeRecap} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			f := newFake()
			f.days[f.clock.Today] = api.Day{Date: f.clock.Today}
			a := newTestApp(t, f, func(c *Config) { c.StartMode = mode })
			a.inflight = true
			press(t, a, "q")
			if a.quitting {
				t.Fatal("q quit while busy")
			}
			eq(t, a.banner.text, "busy… (ctrl+c forces)")
			a.inflight = false
			if mode != ModeTabs {
				press(t, a, "esc")
				eq(t, a.quitting, false)
				eq(t, a.banner.text, "")
				press(t, a, "esc")
				eq(t, a.quitting, true)
			}
			a.quitting = false
			drain(t, a, a.openAdd())
			a.inflight = true
			press(t, a, "q")
			eq(t, a.quitting, false)
			eq(t, a.banner.text, "busy… (ctrl+c forces)")
			press(t, a, "ctrl+c")
			eq(t, a.quitting, true)
		})
	}
}

func TestFinalAIOverlayDoesNotStack(t *testing.T) {
	for _, overlay := range []string{"detail", "history", "search"} {
		t.Run(overlay, func(t *testing.T) {
			a := newTestApp(t, newFake())
			switch overlay {
			case "detail":
				drain(t, a, a.openDetail(42))
			case "history":
				drain(t, a, a.openHistory(42))
			case "search":
				drain(t, a, a.openOverlay(newSearchOverlay("q")))
			}
			before := a.current()
			press(t, a, "A")
			eq(t, len(a.overlays), 1)
			if a.current() != before {
				t.Fatal("A replaced overlay")
			}
		})
	}
}

func TestFinalTodayScrollAndDueActions(t *testing.T) {
	td := todayFixture()
	td.Overdue = []api.Task{task(81, "overdue")}
	td.DueSoon = []api.Task{task(82, "due soon")}
	for i := range 45 {
		td.Entries = append(td.Entries, api.Entry{ID: int64(100 + i), Title: fmt.Sprintf("tail entry %d", i)})
	}
	f := fakeWithToday(td)
	a := newTestApp(t, f)
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	_, _, selected := a.current().selection()
	eq(t, selected, false)
	seen := map[string]bool{}
	for range 100 {
		out := viewText(a)
		for _, needle := range []string{"tail entry 44", "Interruptions", "AI changes: 2", "disk full"} {
			seen[needle] = seen[needle] || strings.Contains(out, needle)
		}
		press(t, a, "j")
	}
	for needle, ok := range seen {
		if !ok {
			t.Errorf("cannot scroll to %s", needle)
		}
	}
	for range 100 {
		press(t, a, "k")
	}
	if !strings.Contains(viewText(a), "▶ #42") {
		t.Fatal("cannot scroll back to top", viewText(a))
	}
	for _, tc := range []struct {
		id    int64
		steps int
	}{{81, 5}, {82, 6}} {
		for _, action := range []string{"f", "x", "B"} {
			t.Run(fmt.Sprintf("%d/%s", tc.id, action), func(t *testing.T) {
				f := fakeWithToday(td)
				a := newTestApp(t, f)
				for range tc.steps {
					press(t, a, "j")
				}
				selectedID(t, a, tc.id)
				press(t, a, action)
				switch action {
				case "f":
					eq(t, f.mustLast(t, "SetFocus").Req, any(api.FocusRequest{TaskID: api.Opt[int64]{Set: true, V: tc.id}}))
				case "x":
					eq(t, f.mustLast(t, "CompleteTask").Mut, client.Mut{IfMatch: fmt.Sprintf("g-%d", tc.id)})
					eq(t, f.mustLast(t, "CompleteTask").ID, tc.id)
				case "B":
					typeText(t, a, "13:00-14:00")
					press(t, a, "enter")
					eq(t, f.mustLast(t, "BlockTask").ID, tc.id)
					eq(t, f.mustLast(t, "BlockTask").Mut.IfMatch, fmt.Sprintf("g-%d", tc.id))
				}
			})
		}
	}
}

func TestFinalStaleEditorReadReleasesGuard(t *testing.T) {
	a := newTestApp(t, newFake())
	a.inflight = true
	req := a.nextReq("editor")
	a.nextReq("editor")
	_, cmd := a.Update(editorTaskMsg{req: req, id: 42})
	eq(t, cmd == nil, true)
	eq(t, a.inflight, false)
}

func TestFinalConfirmSuffixAndPlanHeader(t *testing.T) {
	a, _ := logApp(t)
	press(t, a, "X")
	out := viewText(a)
	if strings.Count(out, "[y/N]") != 1 || strings.Contains(out, "(y/n)") {
		t.Fatal(out)
	}
	f := newFake()
	f.tasks["triage"] = []api.Task{task(42, "new", "new")}
	a = newTestApp(t, f, func(c *Config) { c.StartMode = ModePlan })
	if !strings.Contains(viewText(a), "Plan 1/3 · enter skips to backlog") {
		t.Fatal(viewText(a))
	}
	press(t, a, "A")
	out = viewText(a)
	if strings.Count(out, "[y/N]") != 1 || strings.Contains(out, "(y/n)") {
		t.Fatal(out)
	}
}

func TestFinalAITimestampsAndHelpBanner(t *testing.T) {
	op := aiOp(12)
	op.At.Time = op.At.Add(-24 * time.Hour)
	a, _ := aiApp(t, api.AIChanges{Ops: []api.OpSummary{op}})
	if !strings.Contains(viewText(a), "12 "+op.At.Format("2006-01-02 15:04")) {
		t.Fatal(viewText(a))
	}
	a.setBanner(bannerError, "changed elsewhere — refreshed")
	press(t, a, "?")
	if !strings.Contains(viewText(a), "changed elsewhere — refreshed") {
		t.Fatal(viewText(a))
	}
}

func TestFinalLaunchEscapeClearsBanner(t *testing.T) {
	for _, mode := range []Mode{ModeTriage, ModePlan, ModeRecap} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			f := newFake()
			f.days[f.clock.Today] = api.Day{Date: f.clock.Today}
			a := newTestApp(t, f, func(c *Config) { c.StartMode = mode })
			a.setBanner(bannerError, "cannot reach server")
			press(t, a, "esc")
			eq(t, a.quitting, false)
			eq(t, a.banner.text, "")
			press(t, a, "esc")
			eq(t, a.quitting, true)
		})
	}
}

func TestFinalPlanStepOneHeader(t *testing.T) {
	f := newFake()
	f.tasks["triage"] = []api.Task{task(42, "new", "new")}
	a := newTestApp(t, f, func(c *Config) { c.StartMode = ModePlan })
	if !strings.Contains(viewText(a), "Plan 1/3 · enter skips to backlog") {
		t.Fatal(viewText(a))
	}
}

func TestFinalHelpKeepsErrorBanner(t *testing.T) {
	a := newTestApp(t, newFake())
	a.setBanner(bannerError, "changed elsewhere — refreshed")
	press(t, a, "?")
	if !strings.Contains(viewText(a), "changed elsewhere — refreshed") {
		t.Fatal(viewText(a))
	}
}
