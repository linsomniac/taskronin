package tui

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/client"
)

func planApp(t *testing.T, f *fakeAPI) *App {
	t.Helper()
	return newTestApp(t, f, func(c *Config) { c.StartMode = ModePlan })
}

func TestFreeSlots(t *testing.T) {
	bs := []api.Block{{Start: 780, End: 900, Status: "planned"}, {Start: 600, End: 660, Status: "planned"}, {Start: 700, End: 760, Status: "cancelled"}}
	eq(t, freeSlots(bs, 605), []slot{{660, 780}, {900, 1440}})
	eq(t, freeSlots(bs, 1430), []slot(nil))
	eq(t, freeSlots(nil, 0), []slot{{0, 1440}})
	before := slices.Clone(bs)
	eq(t, freeSlots(bs, 660), []slot{{660, 780}, {900, 1440}})
	eq(t, bs, before)
	eq(t, freeSlots(nil, 1425), []slot{{1425, 1440}})
	eq(t, freeSlots(nil, 1440), []slot(nil))
	bs = []api.Block{
		{Start: 630, End: 720, Status: "planned"},
		{Start: 600, End: 660, Status: "planned"},
		{Start: 720, End: 780, Status: "planned"},
		{Start: 800, End: 900, Status: "done"},
		{Start: 900, End: 1000, Status: "worked"},
		{Start: 1000, End: 1100, Status: "missed"},
		{Start: 790, End: 1440, Status: "planned"},
	}
	eq(t, freeSlots(bs, 590), []slot(nil)) // Both 10-minute gaps are too short.
	eq(t, freeSlots(bs, 585), []slot{{585, 600}})
}

func TestPlanFlow(t *testing.T) { // D§4.1 walkthrough.
	f := newFake()
	f.tasks["triage"] = []api.Task{task(42, "cluster", "new")}
	f.tasks["backlog"] = []api.Task{task(7, "first"), task(8, "second")}
	f.blocks = []api.Block{
		{ID: 1, TaskID: 7, TaskTitle: "first", Date: f.clock.Today, Start: 540, End: 600, Status: "planned"},
		{ID: 2, TaskID: 42, TaskTitle: "cluster", Date: f.clock.Today, Start: 780, End: 900, Status: "planned"},
	}
	a := planApp(t, f)
	selectedID(t, a, 42)
	press(t, a, "B")
	typeText(t, a, "13:00-15:00")
	f.tasks["triage"] = nil
	press(t, a, "enter")
	eq(t, f.mustLast(t, "BlockTask"), call{Method: "BlockTask", ID: 42, Mut: client.Mut{IfMatch: "g-42"}, Req: api.BlockRequest{Date: civil.MustDate("2026-10-28"), Start: 780, End: 900}})
	if !strings.Contains(viewText(a), "free 10:00–13:00, 15:00–24:00") {
		t.Fatal(viewText(a))
	}
	eq(t, f.mustLast(t, "ListTasks").Q, client.TaskQuery{View: "backlog", PageQuery: client.PageQuery{Limit: 10}})
	eq(t, f.mustLast(t, "ListBlocks").Q, client.BlockQuery{From: &f.clock.Today, To: &f.clock.Today})
	selectedID(t, a, 7)
	press(t, a, "j", "B")
	typeText(t, a, "16:00-17:00")
	f.blocks = append(f.blocks, api.Block{ID: 3, TaskID: 8, TaskTitle: "second", Date: f.clock.Today, Start: 960, End: 1020, Status: "planned"})
	reads := f.count("ListBlocks")
	press(t, a, "enter")
	eq(t, f.mustLast(t, "BlockTask"), call{Method: "BlockTask", ID: 8, Mut: client.Mut{IfMatch: "g-8"}, Req: api.BlockRequest{Date: f.clock.Today, Start: 960, End: 1020}})
	if f.count("ListBlocks") <= reads {
		t.Fatal("successful block did not reload today's blocks")
	}
	eq(t, a.modeScreen.(*planMode).step2Blocked, 1)
	press(t, a, "enter")
	for _, want := range []string{"placed 1 · blocked 2 · triage left 0", "09:00–10:00", "13:00–15:00", "16:00–17:00", "#8 second"} {
		if !strings.Contains(viewText(a), want) {
			t.Fatalf("missing %q: %s", want, viewText(a))
		}
	}
	press(t, a, "enter")
	eq(t, a.quitting, true)
}

func TestPlanEmptyTriageStartsAtStep2(t *testing.T) {
	f := newFake()
	f.tasks["backlog"] = []api.Task{task(7, "backlog task")}
	a := planApp(t, f)
	selectedID(t, a, 7)
	for _, want := range []string{"Backlog top 10", "#7 backlog task", "free 10:00–24:00"} {
		if !strings.Contains(viewText(a), want) {
			t.Fatalf("missing %q: %s", want, viewText(a))
		}
	}
}

func TestPlanCardKeys(t *testing.T) {
	for _, tc := range []struct{ key, value, method string }{
		{"k", "", "PlaceTask"}, {"t", "", "PlaceTask"}, {"b", "", "PlaceTask"},
		{"p", "before 9", "PlaceTask"}, {"d", "+2d", "DeferTask"},
		{"B", "13:00-15:00", "BlockTask"}, {"x", "", "CompleteTask"}, {"D", "", "DropTask"},
		{"s", "", ""}, {"A", "", "AcceptNew"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			f := newFake()
			f.tasks["triage"] = []api.Task{task(42, "triage", "new"), task(43, "next", "slipped")}
			a := planApp(t, f)
			press(t, a, "up", "down", "j")
			selectedID(t, a, 42)
			eq(t, f.count("PlaceTask"), 0)
			press(t, a, tc.key)
			if tc.value != "" {
				typeText(t, a, tc.value)
				press(t, a, "enter")
			}
			if tc.key == "A" {
				press(t, a, "y")
			}
			d := a.modeScreen.(*planMode).deck
			selectedID(t, a, 43)
			if tc.key == "s" {
				eq(t, d.skipped, 1)
				eq(t, d.placed, 0)
				return
			}
			eq(t, f.count(tc.method), 1)
			eq(t, d.placed, 1)
			if tc.key != "A" {
				eq(t, f.mustLast(t, tc.method).ID, int64(42))
				eq(t, f.mustLast(t, tc.method).Mut.IfMatch, "g-42")
			}
		})
	}
}

func TestPlanStep2NavigationAndTodayOnly(t *testing.T) {
	f := newFake()
	f.tasks["backlog"] = []api.Task{task(7, "first"), task(8, "second")}
	a := planApp(t, f)
	press(t, a, "down")
	selectedID(t, a, 8)
	press(t, a, "up")
	selectedID(t, a, 7)
	press(t, a, "j", "k", "j", "B")
	typeText(t, a, "tomorrow 16:00-17:00")
	press(t, a, "enter")
	eq(t, f.count("BlockTask"), 0)
	fm := a.form.(*form)
	if !strings.Contains(fm.errText, "today") {
		t.Fatalf("missing today's date restriction: %q", fm.errText)
	}
	eq(t, fm.input.Value(), "tomorrow 16:00-17:00")
	press(t, a, "esc", "B")
	typeText(t, a, "2026-10-28 16:00-17:00")
	press(t, a, "enter")
	eq(t, f.mustLast(t, "BlockTask").ID, int64(8))
	eq(t, f.mustLast(t, "BlockTask").Mut.IfMatch, "g-8")
	eq(t, f.count("PlaceTask"), 0)
}

func TestPlanWaitsForTriageRead(t *testing.T) {
	f := newFake()
	a := NewApp(Config{API: f, Ctx: t.Context(), Clock: f.clock, StartMode: ModePlan, PollInterval: -1})
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	cmd := a.Init()
	if !strings.Contains(viewText(a), "loading…") {
		t.Fatal(viewText(a))
	}
	f.errs["ListTasks"] = outcomeUnknown("")
	drain(t, a, cmd)
	eq(t, f.count("ListTasks"), 1)
	eq(t, f.count("ListBlocks"), 0)
	eq(t, a.banner.text, "server did not answer")
	if !strings.Contains(viewText(a), "triage not loaded") {
		t.Fatal(viewText(a))
	}
	drain(t, a, a.current().load(a))
	eq(t, f.count("ListBlocks"), 1)
}

func TestPlanTriageIgnoresStaleEmptyReply(t *testing.T) {
	f := newFake()
	f.tasks["triage"] = []api.Task{task(42, "triage", "new")}
	a := planApp(t, f)
	old, latest := a.current().load(a), a.current().load(a)
	drain(t, a, latest)
	f.tasks["triage"] = nil
	drain(t, a, old)
	selectedID(t, a, 42)
	eq(t, f.count("ListBlocks"), 0)
	press(t, a, "enter") // A§11.1 also permits explicitly moving to the next step.
	eq(t, f.count("ListBlocks"), 1)
	eq(t, f.count("PlaceTask"), 0)
}

func TestPlanInitialStep2FailureDoesNotClaimFreeTime(t *testing.T) {
	f := newFake()
	a := planApp(t, f)
	// Enter a fresh mode and stop before its step-2 reads return.
	s := newPlanMode()
	a.modeScreen = s
	triage := s.load(a)
	_, step2 := a.Update(triage())
	if !strings.Contains(viewText(a), "loading blocks…") {
		t.Fatal(viewText(a))
	}
	f.errs["ListTasks"], f.errs["ListBlocks"] = notSent(), notSent()
	drain(t, a, step2)
	for _, want := range []string{"backlog not loaded", "blocks not loaded"} {
		if !strings.Contains(viewText(a), want) {
			t.Fatalf("missing %q: %s", want, viewText(a))
		}
	}
	if strings.Contains(viewText(a), "free 10:00") {
		t.Fatal("failed read presented the entire day as free")
	}
	drain(t, a, s.load(a))
	if !strings.Contains(viewText(a), "free 10:00–24:00") {
		t.Fatal(viewText(a))
	}
}

func TestPlanLoadsCaptureClientAndDate(t *testing.T) {
	f := newFake()
	a := planApp(t, f)
	cmd := a.current().load(a)
	other := newFake()
	a.api = other
	a.clock.Today = a.clock.Today.AddDays(1)
	drain(t, a, cmd)
	eq(t, other.count("ListTasks"), 0)
	eq(t, other.count("ListBlocks"), 0)
	eq(t, f.mustLast(t, "ListBlocks").Q, client.BlockQuery{From: &f.clock.Today, To: &f.clock.Today})
	eq(t, f.mustLast(t, "ListTasks").Q, client.TaskQuery{View: "backlog", PageQuery: client.PageQuery{Limit: 10}})
}

func TestPlanFreeTimeUsesServerMinute(t *testing.T) {
	f := newFake()
	f.clock.Now.Time = f.clock.Now.Add(37 * time.Minute)
	f.today.Clock = f.clock
	a := planApp(t, f)
	if !strings.Contains(viewText(a), "free 10:37–24:00") {
		t.Fatal(viewText(a))
	}
}

func TestPlanLoadsIgnoreStaleReplies(t *testing.T) {
	f := newFake()
	f.tasks["backlog"] = []api.Task{task(7, "first"), task(8, "second")}
	a := planApp(t, f)
	press(t, a, "j")
	old, latest := a.current().load(a), a.current().load(a)
	fresh := task(8, "fresh")
	fresh.ETag = "g-8b"
	f.tasks["backlog"] = []api.Task{fresh, task(7, "first")}
	f.blocks = []api.Block{{Start: 600, End: 720, Status: "planned"}}
	drain(t, a, latest)
	f.tasks["backlog"] = []api.Task{task(9, "stale")}
	f.blocks = nil
	drain(t, a, old)
	selectedID(t, a, 8)
	if !strings.Contains(viewText(a), "free 12:00–24:00") {
		t.Fatal(viewText(a))
	}
	old, latest = a.current().load(a), a.current().load(a)
	f.tasks["backlog"] = []api.Task{fresh}
	drain(t, a, latest)
	f.errs["ListTasks"], f.errs["ListBlocks"] = notSent(), outcomeUnknown("")
	drain(t, a, old)
	eq(t, a.banner.text, "")
	press(t, a, "B")
	typeText(t, a, "16:00-17:00")
	press(t, a, "enter")
	eq(t, f.mustLast(t, "BlockTask").Mut.IfMatch, "g-8b")
}

func TestPlanReadErrorsDoNotRetry(t *testing.T) {
	for _, method := range []string{"ListTasks", "ListBlocks"} {
		for _, tc := range []struct {
			err  error
			text string
		}{{notSent(), "cannot reach the Taskronin server"}, {outcomeUnknown(""), "server did not answer"}} {
			t.Run(method+tc.text, func(t *testing.T) {
				f := newFake()
				f.tasks["backlog"] = []api.Task{task(7, "first")}
				a := planApp(t, f)
				before := f.count(method)
				f.errs[method] = tc.err
				drain(t, a, a.current().load(a))
				eq(t, f.count(method), before+1)
				eq(t, a.banner.text, tc.text)
				selectedID(t, a, 7)
			})
		}
	}
}

func TestPlanCard412AndGuard(t *testing.T) {
	f := newFake()
	f.tasks["triage"] = []api.Task{task(42, "old", "new")}
	a := planApp(t, f)
	fresh := task(42, "fresh", "new", "overdue")
	fresh.ETag = "g-42b"
	b, err := json.Marshal(fresh)
	if err != nil {
		t.Fatal(err)
	}
	press(t, a, "B")
	typeText(t, a, "13:00-15:00")
	f.errs["BlockTask"] = &client.ProblemError{Status: 412, Problem: api.Problem{Code: "precondition_failed", Current: b}}
	press(t, a, "enter")
	eq(t, f.count("BlockTask"), 1)
	eq(t, a.modeScreen.(*planMode).deck.placed, 0)
	eq(t, a.banner.text, "changed elsewhere — refreshed")
	if a.form != nil {
		t.Fatal("stale card form remains open")
	}
	_, cmd := a.Update(key("k"))
	press(t, a, "k", "enter")
	eq(t, a.banner.text, "busy…")
	selectedID(t, a, 42)
	f.tasks["triage"] = nil
	drain(t, a, cmd)
	eq(t, f.count("PlaceTask"), 1)
	eq(t, f.mustLast(t, "PlaceTask").Mut.IfMatch, "g-42b")
}

func TestPlanStep2FailureRetryAndGuard(t *testing.T) {
	f := newFake()
	f.tasks["backlog"] = []api.Task{task(7, "first")}
	a := planApp(t, f)
	press(t, a, "B")
	typeText(t, a, "16:00-17:00")
	f.errs["BlockTask"] = outcomeUnknown("plan-retry")
	press(t, a, "enter")
	first := f.mustLast(t, "BlockTask")
	eq(t, a.modeScreen.(*planMode).step2Blocked, 0)
	eq(t, a.form.(*form).input.Value(), "16:00-17:00")
	f.clock.Today = f.clock.Today.AddDays(1)
	f.clock.Now.Time = f.clock.Now.Add(24 * time.Hour)
	f.setToday(api.Today{Clock: f.clock}, "next-day")
	drain(t, a, a.fetchToday(false))
	_, cmd := a.Update(key("enter"))
	press(t, a, "enter")
	eq(t, a.banner.text, "busy…")
	drain(t, a, cmd)
	first.Mut.Key = "plan-retry"
	eq(t, f.mustLast(t, "BlockTask"), first)
	eq(t, f.count("BlockTask"), 2)
	eq(t, a.modeScreen.(*planMode).step2Blocked, 1)
}

func TestPlanSummaryAndExit(t *testing.T) {
	for _, exitKey := range []string{"enter", "q"} {
		for _, standalone := range []bool{true, false} {
			t.Run(exitKey+"/"+map[bool]string{true: "standalone", false: "embedded"}[standalone], func(t *testing.T) {
				f := newFake()
				f.tasks["triage"] = []api.Task{task(42, "skip me", "new")}
				f.today.TriageCount = 1
				var a *App
				if standalone {
					a = planApp(t, f)
				} else {
					a = newTestApp(t, f)
					drain(t, a, a.enterMode(ModePlan, newPlanMode()))
				}
				press(t, a, "s", "enter")
				if !strings.Contains(viewText(a), "placed 0 · blocked 0 · triage left 1") {
					t.Fatal(viewText(a))
				}
				press(t, a, exitKey)
				eq(t, a.quitting, standalone)
				if !standalone {
					eq(t, a.mode, ModeTabs)
				}
			})
		}
	}
}

func TestPlanRender(t *testing.T) {
	for _, color := range []bool{false, true} {
		f := newFake()
		hostile := "bad\x1b\u009b\u202e" + strings.Repeat("宽", 100)
		f.tasks["backlog"] = []api.Task{task(7, hostile)}
		f.blocks = []api.Block{{TaskID: 7, TaskTitle: hostile, Start: 600, End: 1440, Status: "planned"}}
		a := newTestApp(t, f, func(c *Config) { c.StartMode, c.Color = ModePlan, color })
		if !strings.Contains(viewText(a), "no free time left today") {
			t.Fatal(viewText(a))
		}
		for range 2 {
			for _, size := range []tea.WindowSizeMsg{{Width: 100, Height: 30}, {Width: 20, Height: 5}, {Width: 1, Height: 1}, {}} {
				a.Update(size)
				out := viewText(a)
				if strings.ContainsAny(out, "\u009b\u202e") || (!color && strings.ContainsRune(out, '\x1b')) {
					t.Fatalf("unsafe %q", out)
				}
				for _, line := range strings.Split(out, "\n") {
					if lipgloss.Width(line) > size.Width {
						t.Fatalf("oversized %q", line)
					}
				}
				if size.Height > 0 && strings.Count(out, "\n") >= size.Height {
					t.Fatal("too many lines")
				}
			}
			press(t, a, "enter")
		}
	}
}
