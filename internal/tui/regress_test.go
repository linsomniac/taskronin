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

func regressionScreen(t *testing.T, name string) (*App, *fakeAPI, string, string, string) {
	t.Helper()
	f := fakeWithToday(todayFixture())
	f.tasks["triage"] = []api.Task{task(41, "first card", "new"), task(43, "next card", "new")}
	f.tasks["backlog"] = []api.Task{task(41, "backlog task")}
	f.details[41] = api.TaskDetail{Task: task(41, "detail task")}
	f.history[41] = []api.EventView{{Op: api.OpSummary{At: f.clock.Now, Kind: "task.add", Actor: "owner"}}}
	f.days[f.clock.Today] = recapFixture(f.clock.Today)
	a := newTestApp(t, f)
	switch name {
	case "card":
		drain(t, a, a.enterMode(ModeTriage, newCardMode(0)))
		return a, f, "ListTasks", "triage", "#41 first card"
	case "plan1":
		drain(t, a, a.enterMode(ModePlan, newPlanMode()))
		return a, f, "ListTasks", "triage", "#41 first card"
	case "plan2":
		s := newPlanMode()
		s.step = 2
		drain(t, a, a.enterMode(ModePlan, s))
		return a, f, "ListTasks", "backlog", "› #41 backlog task"
	case "detail":
		drain(t, a, a.openDetail(41))
		return a, f, "GetTask", "detail", "#41 detail task"
	case "history":
		drain(t, a, a.openHistory(41))
		return a, f, "TaskHistory", "history", "task.add by owner"
	case "recap":
		drain(t, a, a.enterMode(ModeRecap, newRecapMode(f.clock.Today)))
		return a, f, "Day", "day", "› b1 13:00–15:00 #1 cluster"
	default:
		return a, f, "Today", "Today", "▶ #42 Set up keycloak"
	}
}

func TestRegressFailedReloadKeepsContent(t *testing.T) {
	for _, name := range []string{"card", "plan1", "plan2", "detail", "history", "recap", "today"} {
		t.Run(name, func(t *testing.T) {
			a, f, method, label, content := regressionScreen(t, name)
			f.errs[method] = notSent()
			press(t, a, "r")
			out := viewText(a)
			notice := label + " not loaded · r retries"
			if !strings.Contains(out, notice) || !strings.Contains(out, content) || strings.Index(out, notice) > strings.Index(out, content) {
				t.Fatalf("reload must show %q above retained %q:\n%s", notice, content, out)
			}
			switch name {
			case "detail":
				press(t, a, "t")
				typeText(t, a, "+visible")
				press(t, a, "enter")
				eq(t, f.mustLast(t, "UpdateTask").ID, int64(41))
			case "recap":
				press(t, a, "x")
				eq(t, f.mustLast(t, "CompleteTask").ID, int64(1))
			case "today":
				press(t, a, "x")
				eq(t, f.mustLast(t, "CompleteTask").ID, int64(42))
			}
		})
	}
}

func TestRegressCardAdvanceThenFailedReload(t *testing.T) {
	for _, name := range []string{"card", "plan1"} {
		t.Run(name, func(t *testing.T) {
			a, f, _, _, _ := regressionScreen(t, name)
			f.errs["ListTasks"] = notSent()
			press(t, a, "k")
			eq(t, f.mustLast(t, "PlaceTask").ID, int64(41))
			if out := viewText(a); !strings.Contains(out, "#43 next card") || strings.Contains(out, "#41 first card") {
				t.Fatalf("advanced card hidden after reload failure:\n%s", out)
			}
			press(t, a, "x")
			eq(t, f.mustLast(t, "CompleteTask").ID, int64(43))
			eq(t, f.mustLast(t, "CompleteTask").Mut.IfMatch, "g-43")
		})
	}
}

func TestRegressActionsBeforeFirstLoadSendNothing(t *testing.T) {
	for _, name := range []string{"card", "plan1", "plan2", "detail", "recap", "today"} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			a := NewApp(Config{API: f, Ctx: t.Context(), Clock: f.clock, PollInterval: -1})
			a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			action, method := "x", "CompleteTask"
			var load tea.Cmd
			switch name {
			case "card":
				load = a.enterMode(ModeTriage, newCardMode(0))
				f.errs["ListTasks"] = notSent()
			case "plan1", "plan2":
				s := newPlanMode()
				if name == "plan2" {
					s.step, action, method = 2, "B", "BlockTask"
				}
				load = a.enterMode(ModePlan, s)
				f.errs["ListTasks"] = notSent()
			case "detail":
				load = a.openDetail(41)
				f.errs["GetTask"] = notSent()
				action, method = "e", "GetTask"
			case "recap":
				load = a.enterMode(ModeRecap, newRecapMode(f.clock.Today))
				f.errs["Day"] = notSent()
			default:
				load = a.fetchToday(false)
				f.errs["Today"] = notSent()
			}
			// Check the command before execution: an editor read is itself unwanted.
			_, cmd := a.Update(key(action))
			if cmd != nil || a.form != nil {
				t.Fatal("action started before the first load")
			}
			drain(t, a, load)
			before := f.count(method)
			_, cmd = a.Update(key(action))
			if cmd != nil || a.form != nil {
				t.Fatal("action started after the first load failed")
			}
			eq(t, f.count(method), before)
		})
	}
}

func TestRegressOwnOpKeepsContentWhileRefreshing(t *testing.T) {
	for _, name := range []string{"today", "detail", "history", "recap", "plan2"} {
		t.Run(name, func(t *testing.T) {
			a, _, _, _, content := regressionScreen(t, name)
			_, refresh := a.Update(opMsg{label: "saved"})
			out := viewText(a)
			if !strings.Contains(out, content) || !strings.Contains(out, "refreshing…") {
				t.Fatalf("refresh must keep %q with refreshing marker:\n%s", content, out)
			}
			if name == "plan2" && !strings.Contains(out, "free 10:00–24:00") {
				t.Fatal("refresh hid free slots", out)
			}
			drain(t, a, refresh)
			if strings.Contains(viewText(a), "refreshing…") {
				t.Fatal("refresh marker survived success")
			}
		})
	}
}

func TestRegressTodayScrollNeverActsOnHiddenSelection(t *testing.T) {
	for _, action := range []string{"x", "f", "B", "U", "l"} {
		t.Run(action, func(t *testing.T) {
			td := todayFixture()
			for i := range 30 {
				td.DueSoon = append(td.DueSoon, task(int64(100+i), fmt.Sprintf("due task %d", i)))
				td.Entries = append(td.Entries, api.Entry{ID: int64(100 + i), Title: fmt.Sprintf("tail entry %d", i)})
			}
			f := fakeWithToday(td)
			a := newTestApp(t, f)
			a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			for range 100 {
				press(t, a, "j")
				out := viewText(a)
				if id, title, ok := a.current().selection(); ok && (!strings.Contains(out, "› #"+fmt.Sprint(id)+" "+title) && !strings.Contains(out, "› b")) {
					t.Fatalf("selected #%d is off screen:\n%s", id, out)
				}
			}
			out := viewText(a)
			if !strings.Contains(out, "tail entry 29") || !strings.Contains(out, "AI changes: 2") {
				t.Fatal("tail is unreachable", out)
			}
			_, _, selected := a.current().selection()
			eq(t, selected, false)
			// A poll must not restore the hidden selection while reading the tail.
			drain(t, a, a.fetchToday(false))
			_, _, selected = a.current().selection()
			eq(t, selected, false)
			press(t, a, action)
			switch action {
			case "x":
				eq(t, f.mustLast(t, "CompleteTask").ID, int64(42))
			case "l":
				eq(t, a.form.(*form).title, "Log → #42 (focus)")
			default:
				eq(t, f.count("SetFocus"), 0)
				eq(t, f.count("Unblock"), 0)
				eq(t, a.form, nil)
			}
		})
	}
}

func TestRegressBusyFormsAcceptQ(t *testing.T) {
	for _, open := range []string{"a", "l", "/"} {
		t.Run(open, func(t *testing.T) {
			a := newTestApp(t, newFake())
			a.inflight = true
			press(t, a, open)
			typeText(t, a, "quiz")
			var f *form
			if search, ok := a.form.(*searchForm); ok {
				f = search.form
			} else {
				f = a.form.(*form)
			}
			eq(t, f.input.Value(), "quiz")
			f.waiting = true
			press(t, a, "q")
			eq(t, f.input.Value(), "quiz")
			eq(t, a.banner.text, "busy… (ctrl+c forces)")
		})
	}
}

func TestRegressBusySnapshotKeepsP4Guards(t *testing.T) {
	for _, stale := range []string{"generation", "window", "seven days"} {
		t.Run(stale, func(t *testing.T) {
			f := newFake()
			a := newTestApp(t, f)
			fm := openDeferForm(t, a)
			f.errs["DeferTask"] = problem("busy", 503, "busy")
			press(t, a, "enter")
			if fm.retry == nil {
				t.Fatal("busy discarded the retry snapshot")
			}
			first := f.mustLast(t, "DeferTask")
			if first.Mut.Key == "" {
				t.Fatal("first send must capture its key")
			}
			eq(t, fm.retry.key, first.Mut.Key)
			eq(t, a.banner.text, "server busy — enter resends the same request")
			// The same payload stays resolved across midnight and repeated busy.
			f.clock.Now.Time = f.clock.Now.Add(24 * time.Hour)
			f.clock.Today = f.clock.Today.AddDays(1)
			f.setToday(api.Today{Clock: f.clock}, "tomorrow")
			drain(t, a, a.fetchToday(false))
			eq(t, a.banner.text, "server busy — enter resends the same request")
			f.errs["DeferTask"] = problem("busy", 503, "busy")
			snapshot := fm.retry
			press(t, a, "enter")
			eq(t, f.mustLast(t, "DeferTask").Mut, first.Mut)
			eq(t, f.mustLast(t, "DeferTask").Req, first.Req)
			if fm.retry != snapshot {
				t.Fatal("busy resend replaced original snapshot")
			}
			switch stale {
			case "generation":
				f.gen = "fedcba9876543210"
			case "window":
				f.clock.Now.Time = snapshot.at.Add(client.PendingResendWindow)
			default:
				f.clock.Now.Time = snapshot.at.Add(7*24*time.Hour + time.Second)
			}
			press(t, a, "enter")
			eq(t, f.count("DeferTask"), 2)
			eq(t, fm.retry, (*formRetry)(nil))
			eq(t, a.banner.text, retryStaleBanner)
		})
	}
}
