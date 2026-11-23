package tui

import (
	"fmt"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/linsomniac/taskronin/internal/api"
)

func TestPollNotModifiedKeepsState(t *testing.T) {
	f := fakeWithToday(todayFixture())
	a := newTestApp(t, f)
	press(t, a, "j")
	before, td := viewText(a), a.today
	drain(t, a, func() tea.Msg { return pollMsg{} })
	eq(t, viewText(a), before)
	eq(t, a.today == td, true)
	eq(t, f.mustLast(t, "Today").INM, `"a-1"`)
}

func TestPollChangeRefetchesVisibleTab(t *testing.T) {
	f := fakeWithToday(todayFixture())
	a := newTestApp(t, f)
	s := &recScreen{}
	a.setTabScreen(TabBacklog, s)
	press(t, a, "3")
	eq(t, s.loads, 1)
	td := withFocusTitle(todayFixture(), "Renamed elsewhere")
	f.setToday(td, `"a-2"`)
	drain(t, a, func() tea.Msg { return pollMsg{} })
	eq(t, s.loads, 2)
	eq(t, a.today.Focus.Title, "Renamed elsewhere")
	drain(t, a, func() tea.Msg { return pollMsg{} }) // 304.
	eq(t, s.loads, 2)
	td.Clock.Now.Time = td.Clock.Now.Add(5 * time.Second)
	f.setToday(td, `"a-3"`)
	drain(t, a, func() tea.Msg { return pollMsg{} }) // CF02: only Clock.Now changed.
	eq(t, s.loads, 2)
	eq(t, a.clock, td.Clock)
	eq(t, a.todayETag, `"a-3"`)
	td.Clock.Generation = "fedcba9876543210"
	f.setToday(td, `"a-4"`)
	drain(t, a, func() tea.Msg { return pollMsg{} }) // Clock settings alone do not reload the visible screen.
	eq(t, s.loads, 2)
	eq(t, a.clock, td.Clock)
	td.TriageCount++
	f.setToday(td, `"a-5"`)
	press(t, a, "l") // An unconditional Today read can discover the change first.
	eq(t, s.loads, 2)
	drain(t, a, a.poll()) // The new ETag now yields a 304.
	eq(t, s.loads, 3)
}

func TestPollNeverReloadsOverlays(t *testing.T) {
	f := fakeWithToday(todayFixture())
	a := newTestApp(t, f)
	base, lower, upper := &recScreen{}, &recScreen{}, &recScreen{}
	a.setTabScreen(TabBacklog, base)
	press(t, a, "3")
	drain(t, a, a.openOverlay(lower))
	drain(t, a, a.openOverlay(upper))
	f.setToday(withFocusTitle(todayFixture(), "Changed below overlay"), `"a-2"`)
	drain(t, a, func() tea.Msg { return pollMsg{} })
	eq(t, base.loads, 1)
	eq(t, lower.loads, 1)
	eq(t, upper.loads, 1)
	eq(t, a.current() == upper, true)
	eq(t, a.today.Focus.Title, "Changed below overlay")
}

func TestPollBehindOverlayReloadsRevealedScreen(t *testing.T) {
	for _, mode := range []bool{false, true} {
		for _, change := range []string{"payload", "clock", "304"} {
			t.Run(fmt.Sprintf("mode=%v/%s", mode, change), func(t *testing.T) {
				f := fakeWithToday(todayFixture())
				a := newTestApp(t, f)
				base, lower, upper := &recScreen{}, &recScreen{}, &recScreen{}
				if mode {
					drain(t, a, a.enterMode(ModePlan, base))
				} else {
					a.setTabScreen(TabBacklog, base)
					press(t, a, "3")
				}
				drain(t, a, a.openOverlay(lower))
				drain(t, a, a.openOverlay(upper))
				switch change {
				case "payload":
					f.setToday(withFocusTitle(todayFixture(), "Changed below overlay"), "changed")
				case "clock":
					td := todayFixture()
					td.Clock.Now.Time = td.Clock.Now.Add(5 * time.Second)
					f.setToday(td, "clock-tick")
				}
				drain(t, a, func() tea.Msg { return pollMsg{} })
				drain(t, a, func() tea.Msg { return pollMsg{} }) // A subsequent 304 must preserve a pending reload.
				eq(t, base.loads, 1)
				eq(t, lower.loads, 1)
				eq(t, upper.loads, 1)
				press(t, a, "esc")
				eq(t, base.loads, 1)
				press(t, a, "esc")
				want := 1
				if change == "payload" {
					want++
				}
				eq(t, base.loads, want)
				drain(t, a, a.openOverlay(lower))
				press(t, a, "esc")
				eq(t, base.loads, want)
			})
		}
	}
}

func TestPollChangeReloadsVisibleMode(t *testing.T) {
	f := fakeWithToday(todayFixture())
	a := newTestApp(t, f)
	s := &recScreen{}
	drain(t, a, a.enterMode(ModePlan, s))
	f.setToday(withFocusTitle(todayFixture(), "Changed in mode"), `"a-2"`)
	drain(t, a, func() tea.Msg { return pollMsg{} })
	eq(t, s.loads, 2)
}

func TestPollDoesNotReloadToday(t *testing.T) {
	f := fakeWithToday(todayFixture())
	a := newTestApp(t, f)
	s := &recScreen{}
	a.setTabScreen(TabToday, s)
	f.setToday(withFocusTitle(todayFixture(), "Renamed"), `"a-2"`)
	drain(t, a, func() tea.Msg { return pollMsg{} })
	eq(t, s.loads, 0)
	eq(t, a.today.Focus.Title, "Renamed")
}

func TestPollInflightAndStaleReplies(t *testing.T) {
	for _, reply := range []string{"200", "304", "error"} {
		t.Run(reply, func(t *testing.T) {
			f := fakeWithToday(todayFixture())
			a := newTestApp(t, f)
			_, first := a.Update(pollMsg{})
			presses := f.count("Today")
			_, second := a.Update(pollMsg{})
			drain(t, a, second)
			eq(t, f.count("Today"), presses) // No overlapping poll.
			switch reply {
			case "200":
				f.setToday(withFocusTitle(todayFixture(), "Stale"), `"old"`)
			case "error":
				f.errs["Today"] = outcomeUnknown("")
			}
			stale := first()
			f.setToday(withFocusTitle(todayFixture(), "Fresh"), `"fresh"`)
			drain(t, a, a.fetchToday(false))
			_, cmd := a.Update(stale)
			drain(t, a, cmd)
			eq(t, a.todayETag, `"fresh"`)
			eq(t, a.banner.text, "")
			before := f.count("Today")
			drain(t, a, func() tea.Msg { return pollMsg{} })
			eq(t, f.count("Today"), before+1) // A stale reply also releases the poll guard.
		})
	}
}

func TestPollReadErrorsWaitForNextTick(t *testing.T) { // CF04.
	for _, tc := range []struct {
		err  error
		want string
	}{{notSent(), "cannot reach the Taskronin server"}, {outcomeUnknown(""), "server did not answer"}} {
		t.Run(tc.want, func(t *testing.T) {
			f := fakeWithToday(todayFixture())
			a := newTestApp(t, f)
			s := &recScreen{}
			a.setTabScreen(TabBacklog, s)
			press(t, a, "3")
			f.errs["Today"] = tc.err
			n := f.count("Today")
			drain(t, a, func() tea.Msg { return pollMsg{} })
			eq(t, f.count("Today"), n+1)
			eq(t, a.banner.text, tc.want)
			eq(t, s.loads, 1)
			drain(t, a, func() tea.Msg { return pollMsg{} })
			eq(t, f.count("Today"), n+2)
			eq(t, a.banner.text, "")
		})
	}
}

func TestPollSuccessRestoresFormRetryBanner(t *testing.T) {
	for _, changed := range []bool{false, true} {
		for _, notModified := range []bool{false, true} {
			t.Run(fmt.Sprintf("edited=%v/304=%v", changed, notModified), func(t *testing.T) {
				f := fakeWithToday(todayFixture())
				a := newTestApp(t, f)
				press(t, a, "a")
				typeText(t, a, "Keep this snapshot")
				f.errs["AddTask"] = outcomeUnknown("retry-key")
				press(t, a, "enter")
				fm := a.form.(*form)
				snapshot := fm.retry
				if snapshot == nil {
					t.Fatal("missing retry snapshot")
				}
				if changed {
					typeText(t, a, " edited")
				}
				want := a.banner.text
				f.errs["Today"] = notSent()
				drain(t, a, func() tea.Msg { return pollMsg{} })
				eq(t, a.banner.text, "cannot reach the Taskronin server")
				if !notModified {
					f.setToday(withFocusTitle(todayFixture(), "Updated"), `"a-2"`)
				}
				drain(t, a, func() tea.Msg { return pollMsg{} })
				eq(t, a.banner.text, want)
				eq(t, fm.retry == snapshot, true)
				eq(t, f.count("AddTask"), 1)
			})
		}
	}
}

func TestPollTick(t *testing.T) {
	a := NewApp(Config{API: newFake(), PollInterval: -1})
	eq(t, a.pollTick() == nil, true)
	a.cfg.PollInterval = time.Millisecond
	cmd := a.pollTick()
	if cmd == nil {
		t.Fatal("enabled polling has no timer")
	}
	if _, ok := cmd().(pollMsg); !ok {
		t.Fatal("timer did not return pollMsg")
	}
	// A tick during an in-flight read still re-arms the timer.
	_ = a.fetchToday(true)
	_, cmd = a.Update(pollMsg{})
	if cmd == nil {
		t.Fatal("in-flight poll lost its timer")
	}
	if _, ok := cmd().(pollMsg); !ok {
		t.Fatal("in-flight poll did not re-arm")
	}
}

func TestPollingRunsUnderTeatest(t *testing.T) {
	f := fakeWithToday(todayFixture())
	tm := teatest.NewTestModel(t, NewApp(Config{API: f, Ctx: t.Context(), Clock: f.clock, PollInterval: 20 * time.Millisecond}), teatest.WithInitialTermSize(100, 30))
	t.Cleanup(func() {
		if err := tm.Quit(); err != nil {
			t.Error(err)
		}
		tm.WaitFinished(t, teatest.WithFinalTimeout(15*time.Second))
	})
	teatest.WaitFor(t, tm.Output(), contains("Set up keycloak"), teatest.WithDuration(15*time.Second))
	n := f.count("Today")
	f.setToday(withFocusTitle(todayFixture(), "Renamed elsewhere"), `"a-2"`)
	teatest.WaitFor(t, tm.Output(), contains("Renamed elsewhere"), teatest.WithDuration(15*time.Second))
	if f.count("Today") <= n {
		t.Fatal("no poll after the change")
	}
	f.mu.Lock()
	conditional, reads := false, 0
	for _, call := range f.calls {
		if call.Method == "Today" {
			reads++
			if reads > n && call.INM == `"a-1"` {
				conditional = true
			}
		}
	}
	f.mu.Unlock()
	if !conditional {
		t.Fatal("change was not fetched by a conditional poll")
	}
	tm.Send(key("q"))
	tm.WaitFinished(t, teatest.WithFinalTimeout(15*time.Second))
}

func TestPollMidnightReloadsVisibleTab(t *testing.T) {
	a, f := logApp(t)
	td := *a.today
	td.Clock.Today = td.Clock.Today.AddDays(1)
	f.setToday(td, "midnight")
	drain(t, a, a.poll())
	eq(t, f.count("ListEntries"), 2)
	eq(t, f.mustLast(t, "ListEntries").Q, logQuery("2026-10-16", "2026-10-29", ""))
}

func TestPollReplyAfterTabSwitch(t *testing.T) {
	f := fakeWithToday(todayFixture())
	a := newTestApp(t, f)
	f.setToday(withFocusTitle(todayFixture(), "stale"), "old")
	reply := a.poll()()
	f.tasks["backlog"] = []api.Task{task(7, "current backlog")}
	press(t, a, "3")
	f.tasks["backlog"] = []api.Task{task(8, "unwanted reload")}
	_, cmd := a.Update(reply)
	drain(t, a, cmd)
	eq(t, f.count("ListTasks"), 1)
	selectedID(t, a, 7)
	eq(t, a.todayETag, `"a-1"`)
	eq(t, a.pollReq, 0)
	f.setToday(withFocusTitle(todayFixture(), "fresh"), "fresh")
	drain(t, a, a.poll())
	eq(t, a.today.Focus.Title, "fresh")
	eq(t, f.count("ListTasks"), 2)
}
