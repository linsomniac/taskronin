package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/client"
)

func TestFinalStaleFormsCloseAndReopenFresh(t *testing.T) {
	for _, name := range []string{"backlog defer", "backlog block", "upcoming defer", "today reblock", "log relink", "log delete", "recap link", "detail tags", "plan block"} {
		t.Run(name, func(t *testing.T) {
			f := fakeWithToday(todayFixture())
			f.tasks["backlog"] = []api.Task{task(2, "task")}
			f.upcoming.Deferred = []api.Task{task(2, "task")}
			f.entries = []api.Entry{logEntry(9, "entry")}
			f.days[f.clock.Today] = recapFixture(f.clock.Today)
			f.details[2] = api.TaskDetail{Task: task(2, "task")}
			a := newTestApp(t, f)
			k, value, method, old := "d", "tomorrow", "DeferTask", "g-2"
			switch name {
			case "backlog defer", "backlog block":
				press(t, a, "3")
				if name == "backlog block" {
					k, value, method = "B", "13:00-15:00", "BlockTask"
				}
			case "upcoming defer":
				press(t, a, "4")
			case "today reblock":
				press(t, a, "j")
				k, value, method, old = "B", "15:00-16:00", "Reblock", "b-8"
			case "log relink", "log delete":
				press(t, a, "5")
				k, value, method, old = "L", "2", "RelinkEntry", "e-9"
				if name == "log delete" {
					k, value, method = "X", "", "DeleteEntry"
				}
			case "recap link":
				drain(t, a, a.enterMode(ModeRecap, newRecapMode(f.clock.Today)))
				press(t, a, "j", "j")
				k, value, method, old = "L", "2", "RelinkEntry", "e-6"
			case "detail tags":
				drain(t, a, a.openDetail(2))
				k, value, method = "t", "+work", "UpdateTask"
			case "plan block":
				drain(t, a, a.enterMode(ModePlan, newPlanMode()))
				k, value, method = "B", "13:00-15:00", "BlockTask"
			}
			press(t, a, k)
			typeText(t, a, value)
			f.tasks["backlog"][0].ETag = "fresh"
			f.upcoming.Deferred[0].ETag = "fresh"
			f.today.Blocks[0].ETag = "fresh"
			f.entries[0].ETag = "fresh"
			day := f.days[f.clock.Today]
			day.Unlinked[0].ETag = "fresh"
			f.days[f.clock.Today] = day
			detail := f.details[2]
			detail.ETag = "fresh"
			f.details[2] = detail
			f.errs[method] = problem("precondition_failed", 412, "stale")
			submit := "enter"
			if k == "X" {
				submit = "y"
			}
			press(t, a, submit)
			eq(t, f.count(method), 1)
			eq(t, f.mustLast(t, method).Mut.IfMatch, old)
			if a.form != nil {
				t.Fatal("definitive 412 left stale form open")
			}
			eq(t, a.banner.text, "changed elsewhere — refreshed")
			press(t, a, k)
			typeText(t, a, value)
			press(t, a, submit)
			eq(t, f.count(method), 2)
			eq(t, f.mustLast(t, method).Mut.IfMatch, "fresh")
		})
	}
}

func TestFinalLoadsRetryAndKeepScopedErrors(t *testing.T) {
	for _, name := range []string{"today", "triage", "backlog", "upcoming", "log", "detail", "history", "search", "AI changes", "card", "plan backlog", "plan blocks", "recap"} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			f.todayETag = "v1"
			f.days[f.clock.Today] = api.Day{Date: f.clock.Today}
			a := newTestApp(t, f)
			method, label := "ListTasks", name
			var load tea.Cmd
			switch name {
			case "today":
				method = "Today"
				load = a.fetchToday(false)
			case "triage":
				a.tab = TabTriage
				load = a.current().load(a)
			case "backlog":
				a.tab = TabBacklog
				load = a.current().load(a)
			case "upcoming":
				a.tab = TabUpcoming
				method = "Upcoming"
				load = a.current().load(a)
			case "log":
				a.tab = TabLog
				method = "ListEntries"
				load = a.current().load(a)
			case "detail":
				method = "GetTask"
				load = a.openDetail(2)
			case "history":
				method = "TaskHistory"
				load = a.openHistory(2)
			case "search":
				method = "Search"
				load = a.openOverlay(newSearchOverlay("query"))
			case "AI changes":
				method = "AIChanges"
				load = a.openOverlay(newAIOverlay())
			case "card":
				label = "triage"
				load = a.enterMode(ModeTriage, newCardMode(0))
			case "plan backlog", "plan blocks":
				drain(t, a, a.enterMode(ModePlan, newPlanMode()))
				label = strings.TrimPrefix(name, "plan ")
				if name == "plan blocks" {
					method = "ListBlocks"
				}
				load = a.current().load(a)
			case "recap":
				method = "Day"
				label = "day"
				load = a.enterMode(ModeRecap, newRecapMode(f.clock.Today))
			}
			f.errs[method] = notSent()
			drain(t, a, load)
			want := label + " not loaded · r retries"
			if !strings.Contains(strings.ToLower(viewText(a)), strings.ToLower(want)) {
				t.Fatalf("missing %q:\n%s", want, viewText(a))
			}
			if name != "today" {
				before := f.count(method)
				drain(t, a, a.fetchToday(true)) // A successful unrelated 304 must not erase this failure.
				eq(t, a.banner.text, "cannot reach the Taskronin server")
				eq(t, f.count(method), before)
			}
			before := f.count(method)
			press(t, a, "r")
			eq(t, f.count(method), before+1)
			eq(t, a.banner.text, "")
			if strings.Contains(viewText(a), "not loaded") {
				t.Fatal(viewText(a))
			}
		})
	}
}

func TestFinalMutatingFormsRespectInflight(t *testing.T) {
	for _, name := range []string{"backlog d", "backlog B", "upcoming d", "today B", "detail t"} {
		t.Run(name, func(t *testing.T) {
			f := fakeWithToday(todayFixture())
			f.tasks["backlog"] = []api.Task{task(2, "task")}
			f.upcoming.Deferred = f.tasks["backlog"]
			f.details[2] = api.TaskDetail{Task: task(2, "task")}
			a := newTestApp(t, f)
			switch strings.Fields(name)[0] {
			case "backlog":
				press(t, a, "3")
			case "upcoming":
				press(t, a, "4")
			case "today":
				press(t, a, "j")
			case "detail":
				drain(t, a, a.openDetail(2))
			}
			a.inflight = true
			press(t, a, strings.Fields(name)[1])
			if a.form != nil {
				t.Fatal("opened mutating form while busy")
			}
			eq(t, a.banner.text, "busy…")
		})
	}
}

func TestFinalLogFailedReloadRetriesPagingAndMidnight(t *testing.T) {
	for _, midnight := range []bool{false, true} {
		t.Run(fmt.Sprint(midnight), func(t *testing.T) {
			a, f := logApp(t)
			if midnight {
				a.clock.Today = a.clock.Today.AddDays(1)
			}
			f.errs["ListEntries"] = notSent()
			drain(t, a, a.current().load(a))
			press(t, a, "j")
			eq(t, f.count("ListEntries"), 3)
			q := f.mustLast(t, "ListEntries").Q.(client.EntryQuery)
			if midnight {
				eq(t, q.Cursor, "")
				eq(t, *q.To, a.clock.Today)
			} else {
				eq(t, q.Cursor, "c2")
			}
		})
	}
}
