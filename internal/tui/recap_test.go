package tui

import (
	"fmt"
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

func recapFixture(date civil.Date) api.Day {
	return api.Day{
		Date: date,
		Blocks: []api.Block{
			{ID: 1, TaskID: 1, TaskTitle: "cluster", TaskETag: "g-1", ETag: "b-1", Date: date, Start: 780, End: 900, Status: "missed", OutcomeSource: ptr("auto"), Slipped: true},
			{ID: 2, TaskID: 2, TaskTitle: "checks", TaskETag: "g-2", ETag: "b-2", Date: date, Start: 540, End: 600, Status: "worked", OutcomeSource: ptr("auto")},
		},
		Entries:  []api.Entry{{ID: 5, TaskID: ptr(int64(2)), TaskTitle: ptr("checks"), Title: "x", WorkDate: date, ETag: "e-5"}},
		Unlinked: []api.Entry{{ID: 6, Title: "Answered Bob's DNS question", WorkDate: date, ETag: "e-6"}},
		Interruptions: []api.Interruption{{
			At:   api.Time{Time: testClock().Now.AddDate(0, 0, date.DaysSince(testClock().Today)).Add(20 * time.Minute)},
			Date: date, FromTaskID: ptr(int64(42)), FromBlockID: ptr(int64(9)), ToTaskID: 60,
		}},
	}
}

func recapApp(t *testing.T, f *fakeAPI, date civil.Date) *App {
	t.Helper()
	return newTestApp(t, f, func(c *Config) { c.StartMode, c.RecapDate = ModeRecap, date })
}

func historicalRecap(t *testing.T) (*App, *fakeAPI, civil.Date) {
	t.Helper()
	f := newFake()
	date := civil.MustDate("2026-10-27")
	f.days[date] = recapFixture(date)
	return recapApp(t, f, date), f, date
}

func TestRecapOutcomeKeys(t *testing.T) { // BLK-44, BLK-45, BLK-49, BLK-50 (TUI side).
	a, f, date := historicalRecap(t)
	for _, tc := range []struct {
		key, outcome string
		id           int64
	}{{"w", "worked", 1}, {"m", "missed", 2}, {"a", "auto", 2}} {
		if tc.key == "m" {
			press(t, a, "j")
		}
		reads, todayReads := f.count("Day"), f.count("Today")
		press(t, a, tc.key)
		eq(t, f.mustLast(t, "SetOutcome"), call{Method: "SetOutcome", ID: tc.id, Mut: client.Mut{IfMatch: fmt.Sprintf("b-%d", tc.id)}, Req: api.OutcomeRequest{Outcome: tc.outcome}})
		eq(t, f.count("Day"), reads+1)
		eq(t, f.count("Today"), todayReads+1)
		eq(t, f.mustLast(t, "Day").Q, date)
		if a.form != nil {
			t.Fatal("outcome key opened a form")
		}
	}
	eq(t, f.count("AddTask"), 0)
}

func TestRecapStartedTodayBlock(t *testing.T) { // BLK-47/R9 (TUI side).
	for _, k := range []string{"w", "m"} {
		t.Run(k, func(t *testing.T) {
			f := newFake()
			date := f.clock.Today
			d := recapFixture(date)
			d.Blocks[0].Status, d.Blocks[0].OutcomeSource = "planned", nil
			f.days[date] = d
			a := recapApp(t, f, date)
			press(t, a, k)
			eq(t, f.count("SetOutcome"), 0)
			eq(t, a.banner.text, "not started — use unblock")
			d.Blocks = slices.Clone(d.Blocks)
			d.Blocks[0].Started = true
			f.days[date] = d
			drain(t, a, a.refresh())
			press(t, a, k)
			eq(t, f.count("SetOutcome"), 1)
			eq(t, f.mustLast(t, "SetOutcome").Mut, client.Mut{IfMatch: "b-1"})
		})
	}
}

func TestRecapDoneTask(t *testing.T) {
	a, f, _ := historicalRecap(t)
	reads := f.count("Day")
	press(t, a, "x")
	eq(t, f.mustLast(t, "CompleteTask"), call{Method: "CompleteTask", ID: 1, Mut: client.Mut{IfMatch: "g-1"}, Req: api.CompleteRequest{}})
	eq(t, f.count("GetTask"), 0)
	eq(t, f.count("Day"), reads+1)
}

func TestRecapLinkUnlinked(t *testing.T) { // RCP-09 (TUI side).
	for _, value := range []string{"1", "#1", "abc", "-"} {
		t.Run(value, func(t *testing.T) {
			a, f, _ := historicalRecap(t)
			press(t, a, "j", "j", "L")
			fm, ok := a.form.(*form)
			if !ok {
				t.Fatal("missing link form")
			}
			for _, want := range []string{"#1 cluster", "#2 checks"} {
				if !strings.Contains(fm.hint, want) {
					t.Fatalf("hint %q lacks %q", fm.hint, want)
				}
			}
			reads := f.count("Day")
			typeText(t, a, value)
			press(t, a, "enter")
			if value == "abc" || value == "-" {
				eq(t, f.count("RelinkEntry"), 0)
				eq(t, f.count("Generation"), 0)
				if fm.errText == "" {
					t.Fatal("missing form error")
				}
				return
			}
			eq(t, f.mustLast(t, "RelinkEntry"), call{Method: "RelinkEntry", ID: 6, Mut: client.Mut{IfMatch: "e-6"}, Req: api.RelinkRequest{TaskID: api.Opt[int64]{Set: true, V: 1}}})
			eq(t, f.count("Day"), reads+1)
		})
	}
}

func TestRecapBackdatedLogNeedsChoice(t *testing.T) { // FOC-07 (TUI side).
	for _, choice := range []string{"-", "2", "#2"} {
		t.Run(choice, func(t *testing.T) {
			a, f, date := historicalRecap(t)
			f.today.Focus = ptr(task(42, "today's focus"))
			press(t, a, "j", "j", "l")
			if !strings.Contains(viewText(a), "task # or - for none") {
				t.Fatalf("missing historical task picker: %s", viewText(a))
			}
			reads := f.count("Day")
			typeText(t, a, choice)
			press(t, a, "enter")
			eq(t, f.count("Generation"), 0)
			eq(t, f.count("AddEntry"), 0)
			eq(t, f.count("Day"), reads)
			if a.inflight {
				t.Fatal("task picker reserved a mutation")
			}
			fm, ok := a.form.(*form)
			if !ok || !strings.HasPrefix(fm.title, "Log → ") {
				t.Fatal("picker did not open log form")
			}
			want := api.Opt[int64]{Set: true, V: 2}
			if choice == "-" {
				want = api.Opt[int64]{Set: true, Null: true}
			}
			typeText(t, a, "x")
			press(t, a, "enter")
			eq(t, f.mustLast(t, "AddEntry").Req, api.AddEntryRequest{TaskID: want, Title: "x", WorkDate: &date})
			eq(t, f.count("Day"), reads+1)
		})
	}
	t.Run("selected block", func(t *testing.T) {
		a, f, date := historicalRecap(t)
		press(t, a, "l")
		if !strings.Contains(viewText(a), "Log → #1 (selected)") {
			t.Fatal(viewText(a))
		}
		typeText(t, a, "x")
		press(t, a, "enter")
		eq(t, f.mustLast(t, "AddEntry").Req, api.AddEntryRequest{TaskID: api.Opt[int64]{Set: true, V: 1}, Title: "x", WorkDate: &date})
	})
}

func TestRecapPickerValidationAndCancel(t *testing.T) {
	a, f, _ := historicalRecap(t)
	press(t, a, "j", "j", "l")
	typeText(t, a, "abc")
	press(t, a, "enter")
	if !strings.Contains(viewText(a), "expected a task ID") {
		t.Fatal(viewText(a))
	}
	eq(t, f.count("AddEntry"), 0)
	eq(t, f.count("Generation"), 0)
	press(t, a, "esc")
	if a.form != nil || a.quitting {
		t.Fatal("escape should close only the picker")
	}
}

func TestRecapTodayLogImplicitLink(t *testing.T) {
	for _, tc := range []struct {
		name  string
		focus *api.Task
		block *api.Block
		want  api.Opt[int64]
	}{
		{"focus", ptr(task(42, "focus")), &api.Block{TaskID: 60}, api.Opt[int64]{Set: true, V: 42}},
		{"current block", nil, &api.Block{TaskID: 60}, api.Opt[int64]{Set: true, V: 60}},
		{"no task", nil, nil, api.Opt[int64]{Set: true, Null: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.days[f.clock.Today] = recapFixture(f.clock.Today)
			f.today.Focus, f.today.CurrentBlock = tc.focus, tc.block
			a := recapApp(t, f, civil.Date{})
			press(t, a, "down", "down", "l")
			if !strings.Contains(viewText(a), "Log → ") || strings.Contains(viewText(a), "task # or - for none") {
				t.Fatal(viewText(a))
			}
			typeText(t, a, "x")
			press(t, a, "enter")
			eq(t, f.mustLast(t, "AddEntry").Req, api.AddEntryRequest{TaskID: tc.want, Title: "x"})
		})
	}
}

func TestRecapGlobals(t *testing.T) {
	a, f, _ := historicalRecap(t)
	press(t, a, "?")
	eq(t, a.help.ShowAll, true)
	press(t, a, "?", "1", "tab")
	eq(t, a.mode, ModeRecap)
	selectedID(t, a, 1)
	press(t, a, "j", "j", "w", "m", "a", "x")
	if a.form != nil {
		t.Fatal("row-inapplicable mode key fell through to add")
	}
	eq(t, f.count("SetOutcome"), 0)
	eq(t, f.count("CompleteTask"), 0)
}

func TestRecapShowsInterruptions(t *testing.T) { // INT-08 (TUI side).
	a, f, date := historicalRecap(t)
	if !strings.Contains(viewText(a), interruptionLine(f.days[date].Interruptions[0], nil, nil)) {
		t.Fatal(viewText(a))
	}
	d := f.days[date]
	d.Blocks = append(slices.Clone(d.Blocks), api.Block{ID: 9, TaskID: 42, TaskTitle: "interrupted", Start: 540, End: 660})
	d.Entries = append(slices.Clone(d.Entries), api.Entry{ID: 7, TaskID: ptr(int64(60)), TaskTitle: ptr("Restart stuck pg replica")})
	f.days[date] = d
	drain(t, a, a.refresh())
	want := "10:20 #60 'Restart stuck pg replica' interrupted #42 (block 09:00–11:00)"
	if !strings.Contains(viewText(a), want) {
		t.Fatalf("missing %q: %s", want, viewText(a))
	}
}

func TestRecapDateHeader(t *testing.T) {
	for _, tc := range []struct {
		date civil.Date
		want string
	}{{civil.Date{}, "Recap Wed 2026-10-28"}, {civil.MustDate("2026-10-27"), "Recap Tue 2026-10-27"}} {
		f := newFake()
		a := recapApp(t, f, tc.date)
		if !strings.Contains(viewText(a), tc.want) {
			t.Fatal(viewText(a))
		}
		date := tc.date
		if date == (civil.Date{}) {
			date = f.clock.Today
		}
		eq(t, f.mustLast(t, "Day").Q, date)
	}
}

func TestRecapNavigationAndRows(t *testing.T) {
	a, _, _ := historicalRecap(t)
	out := viewText(a)
	for _, want := range []string{"› b1 13:00–15:00 #1 cluster  missed (auto) slipped", "b2 09:00–10:00 #2 checks  worked (auto)", "e6 Answered Bob's DNS question"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q: %s", want, out)
		}
	}
	press(t, a, "up", "k")
	selectedID(t, a, 1)
	press(t, a, "down")
	selectedID(t, a, 2)
	press(t, a, "up")
	selectedID(t, a, 1)
	press(t, a, "j", "j", "down")
	if _, _, ok := a.current().selection(); ok {
		t.Fatal("unlinked entry selected a task")
	}
	if !strings.Contains(viewText(a), "› e6 ") {
		t.Fatal("cursor did not clamp on the last row")
	}
	press(t, a, "k")
	selectedID(t, a, 2)
}

func TestRecapLoadsIgnoreStaleReplies(t *testing.T) {
	a, f, date := historicalRecap(t)
	old, latest := a.current().load(a), a.current().load(a)
	if old == nil || latest == nil {
		t.Fatal("missing day load")
	}
	d := recapFixture(date)
	d.Blocks[0].TaskTitle, d.Blocks[0].ETag = "fresh", "b-1-fresh"
	f.days[date] = d
	drain(t, a, latest)
	f.days[date] = recapFixture(date)
	drain(t, a, old) // Execution order must not redefine creation order.
	if !strings.Contains(viewText(a), "fresh") {
		t.Fatal("stale day overwrote current rows")
	}
	press(t, a, "w")
	eq(t, f.mustLast(t, "SetOutcome").Mut.IfMatch, "b-1-fresh")
	old, latest = a.current().load(a), a.current().load(a)
	drain(t, a, latest)
	a.banner = banner{}
	f.errs["Day"] = outcomeUnknown("")
	drain(t, a, old)
	eq(t, a.banner.text, "")

	old = a.current().load(a)
	otherDate := civil.MustDate("2026-10-26")
	other := recapFixture(otherDate)
	other.Blocks[0].TaskTitle = "other date"
	f.days[otherDate] = other
	fresh := NewApp(Config{API: f, Clock: f.clock, StartMode: ModeRecap, RecapDate: otherDate}).modeScreen
	drain(t, a, a.enterMode(ModeRecap, fresh))
	drain(t, a, old)
	if !strings.Contains(viewText(a), "Recap Mon 2026-10-26") || !strings.Contains(viewText(a), "other date") {
		t.Fatal("previous date's reply replaced current day")
	}
}

func TestRecapLoadCapturesClientAndDate(t *testing.T) {
	a, f, date := historicalRecap(t)
	cmd := a.current().load(a)
	other := newFake()
	a.api, a.clock.Today, a.cfg.RecapDate = other, date.AddDays(10), date.AddDays(20)
	drain(t, a, cmd)
	eq(t, f.count("Day"), 2)
	eq(t, f.mustLast(t, "Day").Q, date)
	eq(t, other.count("Day"), 0)
}

func TestRecapRejectsWrongDatePayload(t *testing.T) {
	a, f, date := historicalRecap(t)
	wrong := recapFixture(date.AddDays(-1))
	wrong.Blocks[0].TaskTitle = "wrong day"
	f.days[date] = wrong
	drain(t, a, a.current().load(a))
	if strings.Contains(viewText(a), "wrong day") {
		t.Fatal("accepted a payload for another date")
	}
	selectedID(t, a, 1)
}

func TestRecapLoadingFailureAndEmptyDay(t *testing.T) {
	f := newFake()
	date := civil.MustDate("2026-10-27")
	a := NewApp(Config{API: f, Ctx: t.Context(), Clock: f.clock, StartMode: ModeRecap, RecapDate: date, PollInterval: -1})
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if !strings.Contains(viewText(a), "loading day…") {
		t.Fatal(viewText(a))
	}
	f.errs["Day"] = outcomeUnknown("")
	drain(t, a, a.Init())
	if !strings.Contains(viewText(a), "day not loaded") {
		t.Fatal(viewText(a))
	}
	eq(t, f.count("Day"), 1)
	f.days[date] = api.Day{Date: date}
	drain(t, a, a.refresh())
	if !strings.Contains(viewText(a), "no blocks or unlinked entries") {
		t.Fatal(viewText(a))
	}
	press(t, a, "l")
	if !strings.Contains(viewText(a), "task # or - for none") || !strings.Contains(viewText(a), "no block tasks for this day") {
		t.Fatal(viewText(a))
	}
	press(t, a, "-", "enter", "x", "enter")
	eq(t, f.mustLast(t, "AddEntry").Req, api.AddEntryRequest{TaskID: api.Opt[int64]{Set: true, Null: true}, Title: "x", WorkDate: &date})
}

func TestRecapSelectionFollowsRowIdentity(t *testing.T) {
	a, f, date := historicalRecap(t)
	d := recapFixture(date)
	// Two blocks can share one task, and block/entry IDs can overlap.
	d.Blocks[1].TaskID = 1
	f.days[date] = d
	drain(t, a, a.refresh())
	press(t, a, "j")
	d.Blocks = []api.Block{d.Blocks[1], d.Blocks[0]}
	d.Blocks[0].ETag = "b-2-fresh"
	f.days[date] = d
	drain(t, a, a.refresh())
	press(t, a, "m")
	eq(t, f.mustLast(t, "SetOutcome").ID, int64(2))
	eq(t, f.mustLast(t, "SetOutcome").Mut.IfMatch, "b-2-fresh")
	press(t, a, "j", "j")
	d.Blocks = append(slices.Clone(d.Blocks), api.Block{ID: 6, TaskID: 1})
	d.Unlinked = []api.Entry{{ID: 7, Title: "new entry", ETag: "e-7"}, {ID: 6, Title: "same entry", ETag: "e-6-fresh"}}
	f.days[date] = d
	drain(t, a, a.refresh())
	press(t, a, "L", "1", "enter")
	eq(t, f.mustLast(t, "RelinkEntry").ID, int64(6))
	eq(t, f.mustLast(t, "RelinkEntry").Mut.IfMatch, "e-6-fresh")
	d.Unlinked = d.Unlinked[:1]
	f.days[date] = d
	drain(t, a, a.refresh())
	if !strings.Contains(viewText(a), "› e7 new entry") {
		t.Fatal("vanished row did not clamp")
	}
	f.days[date] = api.Day{Date: date}
	drain(t, a, a.refresh())
	press(t, a, "j", "k", "up", "down", "w", "m", "a", "x", "L")
	eq(t, f.count("SetOutcome"), 1)
	eq(t, f.count("CompleteTask"), 0)
	if a.form != nil {
		t.Fatal("empty recap opened an action form")
	}
}

func TestRecapReadErrorsDoNotRetry(t *testing.T) { // CF04.
	for _, tc := range []struct {
		err  error
		text string
	}{{notSent(), "cannot reach the Taskronin server"}, {outcomeUnknown(""), "server did not answer"}} {
		t.Run(tc.text, func(t *testing.T) {
			a, f, _ := historicalRecap(t)
			before := f.count("Day")
			f.errs["Day"] = tc.err
			drain(t, a, a.current().load(a))
			eq(t, f.count("Day"), before+1)
			eq(t, a.banner.text, tc.text)
			selectedID(t, a, 1)
		})
	}
}

func TestRecapMutationErrorPolicy(t *testing.T) { // CF03: an unknown outcome can wrap a 412.
	for _, tc := range []struct {
		err     error
		text    string
		refresh bool
	}{
		{problem("precondition_failed", 412, ""), "changed elsewhere — refreshed", true},
		{problem("generation_mismatch", 412, ""), "database was restored — refreshed", true},
		{notSent(), "cannot reach the Taskronin server — nothing was saved", false},
		{&client.OutcomeUnknownError{Key: "recap-key", Err: problem("precondition_failed", 412, "")}, "outcome unknown — refreshed; check before repeating", true},
	} {
		t.Run(tc.text, func(t *testing.T) {
			a, f, _ := historicalRecap(t)
			before := f.count("Day")
			f.errs["SetOutcome"] = tc.err
			press(t, a, "w")
			eq(t, f.count("SetOutcome"), 1)
			eq(t, a.banner.text, tc.text)
			if tc.refresh {
				before++
			}
			eq(t, f.count("Day"), before)
		})
	}
}

func TestRecapMutationCapturesAndGuards(t *testing.T) {
	for _, k := range []string{"w", "x"} {
		t.Run(k, func(t *testing.T) {
			a, f, _ := historicalRecap(t)
			_, cmd := a.Update(key(k))
			press(t, a, "j", k, "l", "L")
			eq(t, a.banner.text, "busy…")
			if a.form != nil {
				t.Fatal("opened a form during a pending mutation")
			}
			drain(t, a, cmd)
			method, etag := "SetOutcome", "b-1"
			if k == "x" {
				method, etag = "CompleteTask", "g-1"
			}
			eq(t, f.count(method), 1)
			eq(t, f.mustLast(t, method).ID, int64(1))
			eq(t, f.mustLast(t, method).Mut.IfMatch, etag)
		})
	}
}

func TestRecapFormsKeepRetrySnapshots(t *testing.T) {
	for _, k := range []string{"L", "l"} {
		t.Run(k, func(t *testing.T) {
			a, f, date := historicalRecap(t)
			press(t, a, "j", "j", k)
			method := "RelinkEntry"
			if k == "l" {
				press(t, a, "2", "enter")
				method = "AddEntry"
			}
			typeText(t, a, "1")
			f.errs[method] = &client.OutcomeUnknownError{Key: "recap-retry", Err: problem("precondition_failed", 412, "")}
			press(t, a, "enter")
			first := f.mustLast(t, method)
			fm, ok := a.form.(*form)
			if !ok || fm.retry == nil || fm.input.Value() != "1" {
				t.Fatal("form lost text or retry snapshot")
			}
			d := recapFixture(date)
			d.Unlinked[0].ETag = "e-6-new"
			f.days[date] = d
			f.clock.Today = f.clock.Today.AddDays(1)
			f.clock.Now.Time = f.clock.Now.Add(24 * time.Hour)
			f.today.Clock, f.today.Focus = f.clock, ptr(task(99, "new focus"))
			drain(t, a, a.refresh())
			_, cmd := a.Update(key("enter"))
			press(t, a, "enter")
			eq(t, a.banner.text, "busy…")
			drain(t, a, cmd)
			first.Mut.Key = "recap-retry"
			eq(t, f.mustLast(t, method), first)
			eq(t, f.count(method), 2)
		})
	}
}

func TestRecapExit(t *testing.T) {
	for _, k := range []string{"q", "esc"} {
		a, _, _ := historicalRecap(t)
		press(t, a, k)
		eq(t, a.quitting, true)
		f := newFake()
		f.days[f.clock.Today] = recapFixture(f.clock.Today)
		a = newTestApp(t, f)
		s := NewApp(Config{API: f, Clock: f.clock, StartMode: ModeRecap}).modeScreen
		drain(t, a, a.enterMode(ModeRecap, s))
		press(t, a, k)
		eq(t, a.mode, ModeTabs)
		eq(t, a.quitting, false)
	}
}

func TestRecapRenderSanitizesAndClips(t *testing.T) {
	for _, color := range []bool{false, true} {
		f := newFake()
		date := civil.MustDate("2026-10-27")
		d := recapFixture(date)
		hostile := "bad\x1b\u009b\u202e" + strings.Repeat("宽", 100)
		d.Blocks[0].TaskTitle, d.Unlinked[0].Title = hostile, hostile
		for i := range 30 {
			d.Unlinked = append(d.Unlinked, api.Entry{ID: int64(10 + i), Title: "last entry"})
		}
		f.days[date] = d
		a := newTestApp(t, f, func(c *Config) { c.StartMode, c.RecapDate, c.Color = ModeRecap, date, color })
		for _, size := range []tea.WindowSizeMsg{{Width: 100, Height: 30}, {Width: 20, Height: 5}, {Width: 1, Height: 1}, {}} {
			a.Update(size)
			for _, k := range []string{"", "L", "l"} {
				if k == "L" {
					press(t, a, "up", "up", "down", "down")
				}
				if k != "" {
					press(t, a, k)
				}
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
				if a.form != nil {
					press(t, a, "esc")
				}
			}
		}
		a.Update(tea.WindowSizeMsg{Width: 60, Height: 8})
		for range 40 {
			press(t, a, "j")
		}
		if !strings.Contains(viewText(a), "› e39 last entry") {
			t.Fatal("selected row scrolled out of view")
		}
	}
}
