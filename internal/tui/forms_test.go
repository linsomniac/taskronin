package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/client"
	"taskronin/internal/datespec"
)

func TestAddTask(t *testing.T) {
	f := newFake()
	f.resp.Task = ptr(task(42, "Restart stuck pg replica"))
	a := newTestApp(t, f)
	press(t, a, "a")
	typeText(t, a, "Restart stuck pg replica")
	if !strings.Contains(viewText(a), "Restart stuck pg replica") {
		t.Fatal("form not rendered by App.View")
	}
	press(t, a, "enter")
	eq(t, f.mustLast(t, "AddTask").Req, any(api.AddTaskRequest{Title: "Restart stuck pg replica"}))
	eq(t, a.form, nil)
	eq(t, a.banner.text, "#42 added to triage (new)")
}

func TestFormKeepsTextOnFailure(t *testing.T) { // Review Focus 3; CF03 wrapped unknown takes precedence.
	for _, err := range []error{outcomeUnknown("K1"), fmt.Errorf("wrapped: %w", &client.OutcomeUnknownError{
		Key: "K1", Err: problem("validation", 422, "bad"),
	})} {
		f := newFake()
		f.errs["AddTask"] = notSent()
		a := newTestApp(t, f)
		press(t, a, "a")
		typeText(t, a, "Call Bob")
		press(t, a, "enter")
		fm := a.form.(*form)
		eq(t, fm.input.Value(), "Call Bob")
		eq(t, fm.waiting, false)
		if !strings.Contains(viewText(a), "nothing was saved") {
			t.Fatal("banner")
		}
		f.errs["AddTask"] = err
		press(t, a, "enter")
		if !strings.Contains(viewText(a), "enter resends the same request") {
			t.Fatal("banner")
		}
		press(t, a, "enter")
		eq(t, f.mustLast(t, "AddTask").Mut.Key, "K1")
		eq(t, a.form, nil)
	}
}

func openDeferForm(t *testing.T, a *App) *form {
	t.Helper()
	drain(t, a, a.openForm("Defer", "date", "tomorrow", func(a *App, value string) (string, sendFunc, error) {
		m, err := a.parseMoment(value, datespec.Future)
		if err != nil {
			return "", nil, err
		}
		r := api.DeferRequest{Until: api.Opt[api.Moment]{Set: true, V: m}}
		return "deferred", func(ctx context.Context, c API, key string) (*api.OpResponse, error) {
			return c.DeferTask(ctx, client.Mut{IfMatch: "g-1", Key: key}, 1, r)
		}, nil
	}, nil))
	return a.form.(*form)
}

func TestFormRetrySendsSnapshot(t *testing.T) {
	f := newFake()
	f.clock.Now.Time = time.Date(2026, time.October, 28, 23, 59, 0, 0, f.clock.Now.Location())
	f.today.Clock = f.clock
	a := newTestApp(t, f)
	fm := openDeferForm(t, a)
	f.errs["DeferTask"] = outcomeUnknown("K2")
	press(t, a, "enter")
	first := f.mustLast(t, "DeferTask")
	eq(t, first.Req.(api.DeferRequest).Until.V.Date, civil.MustDate("2026-10-29"))
	f.clock.Today = civil.MustDate("2026-10-29")
	f.clock.Now.Time = f.clock.Now.Add(2 * time.Minute)
	f.setToday(api.Today{Clock: f.clock}, "next-day")
	drain(t, a, a.fetchToday(false))
	f.errs["DeferTask"] = outcomeUnknown("different-key-must-not-replace-snapshot")
	snapshot := fm.retry
	press(t, a, "enter")
	eq(t, f.mustLast(t, "DeferTask").Mut, client.Mut{IfMatch: "g-1", Key: "K2"})
	eq(t, f.mustLast(t, "DeferTask").Req, first.Req)
	if fm.retry != snapshot {
		t.Fatal("resend replaced snapshot")
	}
	eq(t, f.count("RefreshGeneration"), 1)
	eq(t, f.count("Generation"), 1)
	// I1 controller ruling: restoring the original spelling resends the snapshot.
	typeText(t, a, " ")
	_, cmd := a.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	drain(t, a, cmd)
	eq(t, fm.input.Value(), "tomorrow")
	press(t, a, "enter")
	eq(t, f.mustLast(t, "DeferTask").Mut.Key, "K2")
	eq(t, f.mustLast(t, "DeferTask").Req, first.Req)
}

func TestFormRetryRefusedWhenStale(t *testing.T) {
	t.Run("restore", func(t *testing.T) {
		f := newFake()
		a := newTestApp(t, f)
		fm := openDeferForm(t, a)
		f.errs["DeferTask"] = outcomeUnknown("K2")
		press(t, a, "enter")
		first := f.mustLast(t, "DeferTask").Req.(api.DeferRequest)
		f.gen = "fedcba9876543210"
		f.clock.Today = civil.MustDate("2026-10-29")
		f.setToday(api.Today{Clock: f.clock}, "restored")
		visible := &recScreen{}
		a.setTabScreen(TabToday, visible)
		reads := f.count("Today")
		press(t, a, "enter")
		eq(t, f.count("DeferTask"), 1)
		eq(t, fm.retry, (*formRetry)(nil))
		eq(t, fm.waiting, false)
		if !strings.Contains(a.banner.text, "may have changed since the first attempt") {
			t.Fatal(a.banner.text)
		}
		eq(t, f.count("Today"), reads+1) // M4: refresh aggregate and visible screen.
		eq(t, visible.loads, 1)
		eq(t, a.todayETag, "restored")
		press(t, a, "enter")
		eq(t, f.mustLast(t, "DeferTask").Mut.Key, "")
		eq(t, first.Until.V.Date, civil.MustDate("2026-10-29"))
		eq(t, f.mustLast(t, "DeferTask").Req.(api.DeferRequest).Until.V.Date, civil.MustDate("2026-10-30")) // M6
	})
	for _, age := range []time.Duration{client.PendingResendWindow, 7*24*time.Hour + time.Second} {
		t.Run(age.String(), func(t *testing.T) {
			f := newFake()
			a := newTestApp(t, f)
			fm := openDeferForm(t, a)
			t0 := a.clock.Now.Time
			f.errs["DeferTask"] = outcomeUnknown("K2")
			_, cmd := a.Update(key("enter"))
			op := cmd()
			f.clock.Now.Time = t0.Add(5 * time.Second)
			f.setToday(api.Today{Clock: f.clock}, "later")
			drain(t, a, a.fetchToday(false))
			_, cmd = a.Update(op)
			drain(t, a, cmd)
			eq(t, fm.retry.at, t0)
			f.clock.Now.Time = t0.Add(6 * 24 * time.Hour)
			f.errs["DeferTask"] = outcomeUnknown("K2")
			press(t, a, "enter")
			eq(t, fm.retry.at, t0)
			eq(t, f.count("DeferTask"), 2)
			f.clock.Now.Time = t0.Add(age) // No todayMsg: must read /clock again.
			press(t, a, "enter")
			eq(t, f.count("Clock"), 2)
			eq(t, f.count("DeferTask"), 2)
			eq(t, fm.retry, (*formRetry)(nil))
			if !strings.Contains(a.banner.text, "may have changed since the first attempt") {
				t.Fatal(a.banner.text)
			}
		})
	}
}

func TestFormRetryGenerationMismatchDropsSnapshot(t *testing.T) { // CF06: restore races with preflight.
	for _, wrapped := range []bool{false, true} {
		f := newFake()
		a := newTestApp(t, f)
		fm := openDeferForm(t, a)
		f.errs["DeferTask"] = outcomeUnknown("K2")
		press(t, a, "enter")
		err := problem("generation_mismatch", 412, "restored")
		if wrapped {
			err = &client.OutcomeUnknownError{Key: "K2", Err: err}
		}
		f.errs["DeferTask"] = err
		press(t, a, "enter")
		eq(t, fm.retry, (*formRetry)(nil))
		eq(t, fm.waiting, false)
		press(t, a, "enter")
		eq(t, f.mustLast(t, "DeferTask").Mut.Key, "")
	}
}

func TestFormRetryPreflightFailureSendsNothing(t *testing.T) {
	for _, method := range []string{"Clock", "RefreshGeneration"} {
		for _, err := range []error{notSent(), outcomeUnknown("")} {
			f := newFake()
			a := newTestApp(t, f)
			fm := openDeferForm(t, a)
			f.errs["DeferTask"] = outcomeUnknown("K2")
			press(t, a, "enter")
			snapshot := fm.retry
			reads := f.count("Today")
			f.errs[method] = err
			press(t, a, "enter")
			eq(t, f.count("DeferTask"), 1)
			eq(t, fm.waiting, false)
			if fm.retry != snapshot {
				t.Fatal("preflight failure lost retry")
			}
			eq(t, f.count("Today"), reads)
			eq(t, a.banner.text, "not sent — outcome of the first attempt still unknown; enter resends the same request")
			eq(t, a.banner.readError, false)
			banner := a.banner.text
			drain(t, a, a.fetchToday(false))
			eq(t, a.banner.text, banner)
			press(t, a, "enter")
			eq(t, f.mustLast(t, "DeferTask").Mut.Key, "K2")
		}
	}
}

func TestFormRetryEditAndCloseBanners(t *testing.T) { // I1
	for _, changed := range []bool{false, true} {
		f := newFake()
		a := newTestApp(t, f)
		fm := openDeferForm(t, a)
		f.errs["DeferTask"] = outcomeUnknown("K2")
		press(t, a, "enter")
		snapshot := fm.retry
		typeText(t, a, " ")
		if fm.retry != snapshot {
			t.Fatal("editing discarded the snapshot before enter")
		}
		eq(t, a.banner.text, "text changed — enter sends a NEW request; the first attempt may have saved")
		if !changed {
			_, cmd := a.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
			drain(t, a, cmd)
			eq(t, a.banner.text, "outcome unknown — enter resends the same request")
		}
		press(t, a, "esc")
		eq(t, a.form, nil)
		eq(t, a.banner.text, "closed — the first attempt may have saved; check before re-adding")
		eq(t, f.count("DeferTask"), 1)
	}
}

func TestFormRetryChangedInputSendsNewRequest(t *testing.T) { // I1: brief's "tomorrow " -> "tomorrow".
	f := newFake()
	a := newTestApp(t, f)
	fm := openDeferForm(t, a)
	typeText(t, a, " ")
	f.errs["DeferTask"] = outcomeUnknown("K2")
	press(t, a, "enter")
	snapshot := fm.retry
	f.clock.Today = civil.MustDate("2026-10-29")
	f.setToday(api.Today{Clock: f.clock}, "next-day")
	drain(t, a, a.fetchToday(false))
	_, cmd := a.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	drain(t, a, cmd)
	if fm.retry != snapshot {
		t.Fatal("snapshot must survive editing until enter")
	}
	eq(t, a.banner.text, "text changed — enter sends a NEW request; the first attempt may have saved")
	press(t, a, "enter")
	eq(t, f.mustLast(t, "DeferTask").Mut.Key, "")
	eq(t, f.mustLast(t, "DeferTask").Req.(api.DeferRequest).Until.V.Date, civil.MustDate("2026-10-30"))
	eq(t, f.count("RefreshGeneration"), 0)
}

func TestFormRetryNotSentKeepsUnknownOutcome(t *testing.T) { // I2
	f := newFake()
	a := newTestApp(t, f)
	fm := openDeferForm(t, a)
	f.errs["DeferTask"] = outcomeUnknown("K2")
	press(t, a, "enter")
	snapshot := fm.retry
	f.errs["DeferTask"] = notSent()
	press(t, a, "enter")
	eq(t, a.banner.text, "not sent — outcome of the first attempt still unknown; enter resends the same request")
	if fm.retry != snapshot {
		t.Fatal("not-sent resend lost snapshot")
	}
	press(t, a, "enter")
	eq(t, f.mustLast(t, "DeferTask").Mut.Key, "K2")
}

func TestFormRetryDefinitiveProblemDropsSnapshot(t *testing.T) { // M1; CF03 still protects wrapped unknowns.
	for _, tc := range []struct {
		code   string
		status int
		banner string
	}{
		{"precondition_failed", 412, "changed elsewhere — refreshed"},
		{"validation", 422, "bad input"},
	} {
		for _, unknown := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/unknown=%v", tc.code, unknown), func(t *testing.T) {
				f := newFake()
				a := newTestApp(t, f)
				fm := openDeferForm(t, a)
				f.errs["DeferTask"] = outcomeUnknown("K2")
				press(t, a, "enter")
				err := problem(tc.code, tc.status, "bad input")
				if unknown {
					err = &client.OutcomeUnknownError{Key: "K2", Err: err}
				}
				f.errs["DeferTask"] = err
				press(t, a, "enter")
				eq(t, fm.retry != nil, unknown)
				wantKey := ""
				if unknown {
					wantKey = "K2"
				} else {
					eq(t, a.banner.text, tc.banner)
				}
				press(t, a, "enter")
				eq(t, f.mustLast(t, "DeferTask").Mut.Key, wantKey)
			})
		}
	}
}

func TestFormUnknownWithoutKeyHasNoRetry(t *testing.T) { // M2
	f := newFake()
	a := newTestApp(t, f)
	fm := openDeferForm(t, a)
	f.errs["DeferTask"] = outcomeUnknown("")
	press(t, a, "enter")
	eq(t, fm.retry, (*formRetry)(nil))
	eq(t, fm.waiting, false)
	eq(t, fm.input.Value(), "tomorrow")
	eq(t, a.banner.text, "outcome unknown — refreshed; check before repeating")
}

func TestFormZeroClockCapturesFirstSendTime(t *testing.T) { // M3
	f := newFake()
	a := newTestApp(t, f)
	a.clock.Now = api.Time{}
	press(t, a, "a")
	typeText(t, a, "text")
	fm := a.form.(*form)
	f.errs["AddTask"] = outcomeUnknown("K3")
	_, cmd := a.Update(key("enter"))
	eq(t, f.count("Clock"), 0) // The fallback read belongs to the command.
	op := cmd()
	t0 := f.clock.Now.Time
	f.clock.Now.Time = t0.Add(5 * time.Second)
	f.setToday(api.Today{Clock: f.clock}, "later")
	drain(t, a, a.fetchToday(false))
	_, cmd = a.Update(op)
	drain(t, a, cmd)
	eq(t, f.count("Clock"), 1)
	eq(t, fm.retry.at, t0)
	press(t, a, "enter")
	eq(t, f.mustLast(t, "AddTask").Mut.Key, "K3")
	eq(t, a.form, nil)
}

func TestFormZeroClockReadFailureSendsNothing(t *testing.T) { // M3
	f := newFake()
	a := newTestApp(t, f)
	a.clock.Now = api.Time{}
	press(t, a, "a")
	typeText(t, a, "text")
	fm := a.form.(*form)
	f.errs["Clock"] = notSent()
	press(t, a, "enter")
	eq(t, f.count("AddTask"), 0)
	eq(t, fm.waiting, false)
	eq(t, fm.retry, (*formRetry)(nil))
	eq(t, a.banner.text, "cannot reach the Taskronin server")
}

func TestFormEarlierSuccessKeepsNewerForm(t *testing.T) { // M5
	f := newFake()
	a := newTestApp(t, f)
	press(t, a, "a")
	typeText(t, a, "first")
	_, cmd := a.Update(key("enter"))
	drain(t, a, a.openLog(a.clock.Today, link{Why: "no task"}))
	next := a.form
	typeText(t, a, "second")
	drain(t, a, cmd)
	if a.form != next {
		t.Fatal("earlier reply closed the newer form")
	}
	eq(t, a.form.(*form).input.Value(), "second")
}

func TestFormVisibleCaret(t *testing.T) { // M7: visible in plain and colour modes, including scrolling.
	for _, color := range []bool{false, true} {
		t.Run(fmt.Sprintf("color=%v", color), func(t *testing.T) {
			a := newTestApp(t, newFake(), func(c *Config) { c.Color = color })
			press(t, a, "a")
			typeText(t, a, "宽bc")
			fm := a.form.(*form)
			fm.input.SetCursor(1)
			if !strings.Contains(viewText(a), "宽▏bc") {
				t.Fatal("missing caret at the editing position")
			}
			fm.input.CursorEnd()
			typeText(t, a, strings.Repeat("宽", 100))
			for _, width := range []int{100, 20, 3, 2, 1} {
				a.Update(tea.WindowSizeMsg{Width: width, Height: 8})
				out := viewText(a)
				if !strings.Contains(out, "▏") {
					t.Fatalf("width %d lost the scrolled caret", width)
				}
				if !color && strings.ContainsRune(out, '\x1b') {
					t.Fatalf("plain form emitted ANSI: %q", out)
				}
				for _, line := range strings.Split(out, "\n") {
					if lipgloss.Width(line) > width {
						t.Fatalf("oversized line %q", line)
					}
				}
			}
		})
	}
}

func TestFormInitialGenerationFailureSendsNothing(t *testing.T) {
	f := newFake()
	a := newTestApp(t, f)
	fm := openDeferForm(t, a)
	f.errs["Generation"] = outcomeUnknown("")
	press(t, a, "enter")
	eq(t, f.count("DeferTask"), 0)
	eq(t, fm.retry, (*formRetry)(nil))
	eq(t, fm.waiting, false)
	eq(t, a.banner.text, "server did not answer")
}

func TestLogImplicitLink(t *testing.T) { // FOC-04, FOC-05, FOC-06 (TUI side)
	cases := []struct {
		focus *api.Task
		cur   *api.Block
		want  api.Opt[int64]
		label string
	}{
		{ptr(task(1, "A")), &api.Block{ID: 9, TaskID: 2}, api.Opt[int64]{Set: true, V: 1}, "→ #1 (focus)"},
		{nil, &api.Block{ID: 9, TaskID: 2}, api.Opt[int64]{Set: true, V: 2}, "→ #2 (current block)"},
		{nil, nil, api.Opt[int64]{Set: true, Null: true}, "→ no task"},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			f := newFake()
			f.setToday(api.Today{Clock: f.clock, Focus: c.focus, CurrentBlock: c.cur}, `"a-1"`)
			a := newTestApp(t, f)
			press(t, a, "l")
			if !strings.Contains(viewText(a), c.label) {
				t.Fatal(c.label)
			}
			typeText(t, a, "text")
			press(t, a, "enter")
			eq(t, f.mustLast(t, "AddEntry").Req, any(api.AddEntryRequest{TaskID: c.want, Title: "text"}))
			wantBanner := "logged (no task)"
			if !c.want.Null {
				wantBanner = fmt.Sprintf("logged → #%d", c.want.V)
			}
			eq(t, a.banner.text, wantBanner)
		})
	}
}

type selectedScreen struct{ stubScreen }

func (selectedScreen) selection() (int64, string, bool) { return 7, "X", true }

func TestLogSelectedTaskWins(t *testing.T) { // FOC-08 (TUI side)
	f := newFake()
	f.today.Focus = ptr(task(1, "A"))
	a := newTestApp(t, f)
	a.setTabScreen(TabToday, selectedScreen{})
	press(t, a, "l")
	if !strings.Contains(viewText(a), "→ #7 (selected)") {
		t.Fatal(viewText(a))
	}
	typeText(t, a, "text")
	press(t, a, "enter")
	eq(t, f.mustLast(t, "AddEntry").Req.(api.AddEntryRequest).TaskID, api.Opt[int64]{Set: true, V: 7})
}

func TestLogRefreshesLinkAndKeepsRetry(t *testing.T) {
	f := newFake()
	a := newTestApp(t, f)
	f.setToday(api.Today{Clock: f.clock, Focus: ptr(task(42, "fresh"))}, "fresh")
	press(t, a, "l")
	eq(t, f.count("Today"), 2)
	eq(t, f.mustLast(t, "Today").INM, "")
	if !strings.Contains(viewText(a), "→ #42 (focus)") {
		t.Fatal(viewText(a))
	}
	typeText(t, a, "text")
	f.errs["AddEntry"] = outcomeUnknown("log-key")
	f.setToday(api.Today{Clock: f.clock, Focus: ptr(task(99, "later"))}, "later")
	press(t, a, "enter")
	first := f.mustLast(t, "AddEntry").Req
	eq(t, first.(api.AddEntryRequest).TaskID.V, int64(42))
	if !strings.Contains(viewText(a), "→ #42 (focus)") {
		t.Fatal("retry must echo its saved link")
	}
	press(t, a, "enter")
	eq(t, f.mustLast(t, "AddEntry").Req, first)
	eq(t, f.mustLast(t, "AddEntry").Mut.Key, "log-key")
	eq(t, a.banner.text, "logged → #42")
}

func TestLogHistoricalDateKeepsExplicitLink(t *testing.T) {
	f := newFake()
	f.today.Focus = ptr(task(42, "today"))
	a := newTestApp(t, f)
	date := civil.MustDate("2026-10-27")
	drain(t, a, a.openLog(date, link{Why: "no task"}))
	typeText(t, a, "yesterday")
	press(t, a, "enter")
	eq(t, f.mustLast(t, "AddEntry").Req, any(api.AddEntryRequest{
		TaskID: api.Opt[int64]{Set: true, Null: true}, Title: "yesterday", WorkDate: &date,
	}))
}

func TestLogStaleRetryRecomputesLink(t *testing.T) {
	f := newFake()
	f.today.Focus = ptr(task(42, "first"))
	a := newTestApp(t, f)
	press(t, a, "l")
	typeText(t, a, "text")
	f.errs["AddEntry"] = outcomeUnknown("log-key")
	f.setToday(api.Today{Clock: f.clock, Focus: ptr(task(99, "later"))}, "later")
	press(t, a, "enter")
	f.gen = "fedcba9876543210"
	press(t, a, "enter")
	if !strings.Contains(viewText(a), "→ #99 (focus)") {
		t.Fatal("dropping the snapshot must refresh the echoed link")
	}
	press(t, a, "enter")
	eq(t, f.mustLast(t, "AddEntry").Mut.Key, "")
	eq(t, f.mustLast(t, "AddEntry").Req.(api.AddEntryRequest).TaskID.V, int64(99))
}

func TestLogIgnoresStaleRefresh(t *testing.T) {
	f := newFake()
	a := newTestApp(t, f)
	_, first := a.Update(key("l"))
	second := a.fetchToday(false)
	f.setToday(api.Today{Clock: f.clock, Focus: ptr(task(42, "latest"))}, "latest")
	drain(t, a, second)
	f.setToday(api.Today{Clock: f.clock, Focus: ptr(task(1, "old"))}, "old")
	drain(t, a, first)
	if !strings.Contains(viewText(a), "→ #42 (focus)") {
		t.Fatal("stale refresh replaced the echoed link")
	}
}

func TestDatespecModes(t *testing.T) { // DAT-02, DAT-04, DAT-09, DAT-12 (TUI side)
	a := newTestApp(t, newFake())
	for _, c := range []struct {
		text string
		mode datespec.Mode
		want api.Moment
	}{
		{"thu", datespec.Future, api.Moment{Date: civil.MustDate("2026-10-29")}},
		{"wed", datespec.Future, api.Moment{Date: civil.MustDate("2026-11-04")}},
		{"mon 09:00", datespec.Future, api.Moment{Date: civil.MustDate("2026-11-02"), Time: ptr(civil.Minute(540))}},
		{"tue", datespec.Past, api.Moment{Date: civil.MustDate("2026-10-27")}},
		{"wed", datespec.Past, api.Moment{Date: civil.MustDate("2026-10-28")}},
	} {
		m, err := a.parseMoment(c.text, c.mode)
		if err != nil {
			t.Fatal(err)
		}
		eq(t, m, c.want)
		if c.want.Time == nil {
			d, err := a.parseDate(c.text, c.mode)
			if err != nil {
				t.Fatal(err)
			}
			eq(t, d, c.want.Date)
		}
	}
	s, err := a.parseSpan("fri 13:00-15:00")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, s, api.BlockRequest{Date: civil.MustDate("2026-10-30"), Start: 780, End: 900})
	s, err = a.parseSpan("23:00-24:00")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, s.End, civil.EndMinute(1440))
	if _, err := a.parseMoment("nextweek", datespec.Future); err == nil {
		t.Fatal("DAT-12")
	}
	if _, err := a.parseDate("tue 10:00", datespec.Past); err == nil {
		t.Fatal("date only")
	}
	for i, day := range []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"} {
		a.clock.WeekStart = day
		eq(t, a.ref(), datespec.Ref{Today: a.clock.Today, WeekStart: time.Weekday(i)})
	}
	eq(t, weekdayOf("unknown"), time.Monday)
}

func TestFormParseTaskRef(t *testing.T) {
	for _, value := range []string{"42", "#42"} {
		id, none, err := parseTaskRef(value)
		eq(t, err, nil)
		eq(t, id, int64(42))
		eq(t, none, false)
	}
	id, none, err := parseTaskRef("-")
	eq(t, err, nil)
	eq(t, id, int64(0))
	eq(t, none, true)
	for _, value := range []string{"", "#", "0", "#0", "-1", "+42", "##42", "x", "9223372036854775808"} {
		if _, _, err := parseTaskRef(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}

func TestFormParseErrorSendsNothing(t *testing.T) {
	f := newFake()
	a := newTestApp(t, f)
	drain(t, a, a.openForm("Block", "span", "25:00-26:00", func(a *App, value string) (string, sendFunc, error) {
		r, err := a.parseSpan(value)
		return "blocked", func(ctx context.Context, c API, key string) (*api.OpResponse, error) {
			return c.BlockTask(ctx, client.Mut{IfMatch: "g-1", Key: key}, 1, r)
		}, err
	}, nil))
	press(t, a, "enter")
	fm := a.form.(*form)
	if fm.errText == "" || !strings.Contains(viewText(a), fm.errText) {
		t.Fatal("missing parse error")
	}
	eq(t, f.count("BlockTask"), 0)
	eq(t, f.count("Generation"), 0)
	eq(t, fm.waiting, false)
}

func TestFormAfterHookRunsOnSuccessOnly(t *testing.T) {
	for _, fail := range []bool{false, true} {
		f := newFake()
		a := newTestApp(t, f)
		calls := 0
		drain(t, a, a.openForm("Add", "", "text", func(_ *App, value string) (string, sendFunc, error) {
			return "added", func(ctx context.Context, c API, key string) (*api.OpResponse, error) {
				return c.AddTask(ctx, client.Mut{Key: key}, api.AddTaskRequest{Title: value})
			}, nil
		}, func(a *App, r *api.OpResponse) tea.Cmd {
			calls++
			eq(t, a.form, nil)
			eq(t, r.OpID, f.resp.OpID)
			return nil
		}))
		if fail {
			f.errs["AddTask"] = notSent()
		}
		press(t, a, "enter")
		want := 1
		if fail {
			want = 0
		}
		eq(t, calls, want)
		eq(t, a.form != nil, fail)
	}
}

func TestFormDoubleEnterAndCancel(t *testing.T) {
	f := newFake()
	a := newTestApp(t, f)
	press(t, a, "a")
	typeText(t, a, "text")
	_, cmd := a.Update(key("enter"))
	press(t, a, "enter", "esc", "x")
	fm := a.form.(*form)
	eq(t, fm.waiting, true)
	eq(t, fm.input.Value(), "text")
	eq(t, f.count("AddTask"), 0)
	if !strings.Contains(a.banner.text, "busy") {
		t.Fatal(a.banner.text)
	}
	drain(t, a, cmd)
	eq(t, f.count("AddTask"), 1)
	press(t, a, "a", "esc")
	eq(t, a.form, nil)
}

func TestFormGlobalsAndConfirm(t *testing.T) {
	for _, mode := range []bool{false, true} {
		a := newTestApp(t, newFake())
		if mode {
			drain(t, a, a.enterMode(ModePlan, &recScreen{}))
		} else {
			drain(t, a, a.openOverlay(&recScreen{}))
		}
		for _, k := range []string{"a", "l"} {
			press(t, a, k)
			if a.form == nil {
				t.Fatalf("global %s did not open form", k)
			}
			press(t, a, "esc")
		}
		for _, k := range []string{"y", "n", "enter", "esc", "Y"} {
			calls := 0
			drain(t, a, a.confirm("Proceed?", func(a *App) tea.Cmd {
				calls++
				eq(t, a.form, nil)
				return nil
			}))
			if !strings.Contains(viewText(a), "Proceed?") {
				t.Fatal("confirmation missing")
			}
			press(t, a, k)
			eq(t, a.form, nil)
			eq(t, calls == 1, k == "y")
		}
	}
}

func TestFormRenderSanitizesAndClips(t *testing.T) {
	for _, color := range []bool{false, true} {
		a := newTestApp(t, newFake(), func(c *Config) { c.Color = color })
		press(t, a, "a")
		fm := a.form.(*form)
		eq(t, fm.input.Styles().Cursor.Blink, false)
		fm.title, fm.hint = "bad\x1b\u202e", "bad\u009b"
		_, cmd := a.Update(tea.PasteMsg{Content: "bad\u202e\u009b" + strings.Repeat("宽", 150)})
		drain(t, a, cmd)
		for _, w := range []int{100, 20, 1, 0} {
			a.Update(tea.WindowSizeMsg{Width: w, Height: 8})
			out := viewText(a)
			if strings.ContainsAny(out, "\u202e\u009b") || (!color && strings.ContainsRune(out, '\x1b')) {
				t.Fatalf("unsafe form: %q", out)
			}
			for _, line := range strings.Split(out, "\n") {
				if lipgloss.Width(line) > w {
					t.Fatalf("oversized form: %q", line)
				}
			}
		}
	}
}
