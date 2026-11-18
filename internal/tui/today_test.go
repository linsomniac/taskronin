package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/client"
)

func todayFixture() api.Today {
	c := testClock()
	blocks := []api.Block{
		{ID: 8, TaskID: 57, TaskTitle: "New task", TaskETag: "g-57", ETag: "b-8", Date: c.Today, Start: 480, End: 540, Status: "planned", Ended: true, NeedsReblock: true},
		{ID: 9, TaskID: 42, TaskTitle: "Set up keycloak postgres cluster", TaskETag: "g-42", ETag: "b-9", Date: c.Today, Start: 540, End: 660, Status: "planned", Current: true},
		{ID: 7, TaskID: 42, TaskTitle: "Set up keycloak postgres cluster", TaskETag: "g-42", ETag: "b-7", Date: c.Today, Start: 780, End: 900, Status: "planned"},
	}
	return api.Today{
		Clock: c, Focus: ptr(task(42, "Set up keycloak postgres cluster")), CurrentBlock: ptr(blocks[1]), Blocks: blocks,
		TriageCount: 3, TriageTop: []api.Task{task(57, "New task", "new")},
		Entries:       []api.Entry{{ID: 9, TaskID: ptr(int64(60)), TaskTitle: ptr("Restart stuck pg replica"), Time: ptr(civil.Minute(620)), Title: "Restarted replica"}},
		Interruptions: []api.Interruption{{At: api.Time{Time: c.Now.Add(20 * time.Minute)}, Date: c.Today, FromTaskID: ptr(int64(42)), FromBlockID: ptr(int64(9)), ToTaskID: 60}},
		AIChanges:     2, Banners: []api.Banner{{Kind: "backup_failed", Message: "disk full"}},
	}
}

func fakeWithToday(td api.Today) *fakeAPI {
	f := newFake()
	f.setToday(td, `"a-1"`)
	return f
}

func withFocusTitle(td api.Today, title string) api.Today {
	focus := *td.Focus
	focus.Title = title
	td.Focus = &focus
	return td
}

func TestTodayRender(t *testing.T) { // BLK-48, BLK-17/RCP-06, INT-08 (TUI side).
	v := viewText(newTestApp(t, fakeWithToday(todayFixture())))
	for _, s := range []string{
		"▶ #42 Set up keycloak postgres cluster (→11:00)", "b8 08:00–09:00 #57", "reblock?",
		"b9 09:00–11:00 #42", "◀ now", "b7 13:00–15:00 #42", "Triage 3", "#57 New task {new}",
		"e9 10:20 Restarted replica [#60]", "10:20 #60 'Restart stuck pg replica' interrupted #42 (block 09:00–11:00)",
		"AI changes: 2 unreviewed — A to review", "⚠ backup",
	} {
		if !strings.Contains(v, s) {
			t.Fatalf("missing %q in\n%s", s, v)
		}
	}
	if strings.Contains(v, "Overdue") || strings.Contains(v, "Due soon") {
		t.Fatal("empty due sections rendered")
	}
	if strings.Index(v, "b8 ") > strings.Index(v, "b9 ") || strings.Index(v, "b9 ") > strings.Index(v, "b7 ") {
		t.Fatal("blocks lost their served order")
	}
}

func TestTodayAIChangesNoticeOnce(t *testing.T) {
	td := todayFixture()
	td.Banners = []api.Banner{
		{Kind: "ai_changes", Message: "2 unreviewed AI changes."},
		{Kind: "backup_failed", Message: "disk full"},
		{Kind: "tz_changed", Message: "timezone changed"},
	}
	a := newTestApp(t, fakeWithToday(td))
	body := a.current().view(a, 120, 40)
	want := "AI changes: 2 unreviewed — A to review"
	if strings.Count(body, "unreviewed") != 1 || !strings.Contains(body, want) {
		t.Fatalf("want exactly one AI notice %q: %s", want, body)
	}
	for _, text := range []string{"disk full", "timezone changed"} {
		if !strings.Contains(body, text) {
			t.Errorf("lost other banner %q: %s", text, body)
		}
	}
	if !strings.Contains(a.header(120), "ai 2") {
		t.Fatal("header lost AI count")
	}
}

func TestCompletionEntryMarkers(t *testing.T) {
	for _, tab := range []Tab{TabToday, TabLog} {
		t.Run(fmt.Sprint(tab), func(t *testing.T) {
			f := newFake()
			entries := []api.Entry{
				{ID: 7, Kind: "complete", Title: "Finished task", WorkDate: f.clock.Today, Time: ptr(civil.Minute(620)), TaskID: ptr(int64(42))},
				{ID: 8, Kind: "work", Title: "Progress", WorkDate: f.clock.Today},
			}
			f.today.Entries = entries
			f.entries = entries
			a := newTestApp(t, f, func(c *Config) { c.StartTab = tab })
			body := a.current().view(a, 120, 40)
			for _, want := range []string{"e7 10:20 ✓ Finished task", "[#42]", "e8 Progress"} {
				if !strings.Contains(body, want) {
					t.Errorf("missing %q: %s", want, body)
				}
			}
			if strings.Count(body, "✓") != 1 {
				t.Fatalf("only completion entries should be marked: %s", body)
			}
		})
	}
}

func TestTodayNoFocusCurrentBlock(t *testing.T) {
	td := todayFixture()
	td.Focus = nil
	v := viewText(newTestApp(t, fakeWithToday(td)))
	if !strings.Contains(v, "▷ #42 Set up keycloak postgres cluster (→11:00)") {
		t.Fatal(v)
	}
	td.CurrentBlock = nil
	v = viewText(newTestApp(t, fakeWithToday(td)))
	if !strings.Contains(v, "no focus") {
		t.Fatal(v)
	}
	td.Focus = ptr(task(57, "Different focus"))
	td.CurrentBlock = ptr(td.Blocks[1])
	v = viewText(newTestApp(t, fakeWithToday(td)))
	if !strings.Contains(v, "▶ #57 Different focus\n") {
		t.Fatal(v)
	}
}

func TestTodayActions(t *testing.T) {
	f := fakeWithToday(todayFixture())
	a := newTestApp(t, f)
	for _, k := range []string{"U", "B", "f"} {
		press(t, a, k)
		eq(t, a.banner.text, "select a row first (j)")
		eq(t, a.form, nil)
	}
	eq(t, f.count("Unblock")+f.count("Reblock")+f.count("SetFocus"), 0)
	press(t, a, "j", "B")
	if a.form == nil {
		t.Fatal("B did not open reblock form")
	}
	typeText(t, a, "15:00-16:00")
	press(t, a, "enter")
	eq(t, f.mustLast(t, "Reblock"), call{Method: "Reblock", ID: 8, Mut: client.Mut{Key: freshFormKey(t, f, "Reblock"), IfMatch: "b-8"}, Req: api.ReblockRequest{
		BlockRequest: api.BlockRequest{Date: testClock().Today, Start: 900, End: 960}, TaskETag: "g-57",
	}})
	eq(t, f.count("GetTask"), 0)
	press(t, a, "f")
	eq(t, f.mustLast(t, "SetFocus"), call{Method: "SetFocus", Req: api.FocusRequest{TaskID: api.Opt[int64]{Set: true, V: 57}}})
	press(t, a, "j", "j", "U")
	eq(t, f.mustLast(t, "Unblock"), call{Method: "Unblock", ID: 7, Mut: client.Mut{IfMatch: "b-7"}})
	press(t, a, "f")
	eq(t, f.mustLast(t, "SetFocus").Req, any(api.FocusRequest{TaskID: api.Opt[int64]{Set: true, Null: true}}))
	press(t, a, "x")
	eq(t, f.mustLast(t, "CompleteTask"), call{Method: "CompleteTask", ID: 42, Mut: client.Mut{IfMatch: "g-42"}, Req: api.CompleteRequest{}})
	td := todayFixture()
	td.Focus = nil
	f.setToday(td, `"a-2"`)
	drain(t, a, a.fetchToday(false))
	press(t, a, "esc", "x")
	eq(t, a.banner.text, "no focus task")
	eq(t, f.count("CompleteTask"), 1)
}

func TestTodaySelectionAndLog(t *testing.T) {
	f := fakeWithToday(todayFixture())
	a := newTestApp(t, f)
	_, _, selected := a.current().selection()
	eq(t, selected, false)
	press(t, a, "l")
	eq(t, a.form.(*form).title, "Log → #42 (focus)")
	press(t, a, "esc", "j", "l")
	eq(t, a.form.(*form).title, "Log → #57 (selected)")
	press(t, a, "esc", "j", "j", "j")
	id, _, selected := a.current().selection()
	eq(t, id, int64(57)) // Last selectable row is triage in this fixture.
	eq(t, selected, true)
	press(t, a, "B")
	eq(t, a.form.(*form).title, "Block #57")
	press(t, a, "esc", "U")
	eq(t, a.form, nil)
	eq(t, f.count("Reblock")+f.count("Unblock"), 0)
	press(t, a, "f")
	eq(t, f.mustLast(t, "SetFocus").Req.(api.FocusRequest).TaskID.V, int64(57))
	press(t, a, "k")
	id, _, _ = a.current().selection()
	eq(t, id, int64(42))
	press(t, a, "esc")
	_, _, selected = a.current().selection()
	eq(t, selected, false)
	press(t, a, "l")
	eq(t, a.form.(*form).title, "Log → #42 (focus)")
}

func TestTodayRefreshAfterOwnOp(t *testing.T) { // REC-05 (TUI side).
	f := fakeWithToday(todayFixture())
	a := newTestApp(t, f)
	_, cmd := a.Update(key("x"))
	td := todayFixture()
	td.Focus, td.CurrentBlock = nil, nil
	f.setToday(td, `"a-2"`)
	drain(t, a, cmd)
	if !strings.Contains(viewText(a), "no focus") {
		t.Fatal(viewText(a))
	}
	eq(t, f.mustLast(t, "Today").INM, "")
}

func TestTodayActionsCaptureAndGuard(t *testing.T) {
	for _, k := range []string{"f", "U", "x"} {
		t.Run(k, func(t *testing.T) {
			f := fakeWithToday(todayFixture())
			a := newTestApp(t, f)
			press(t, a, "j")
			_, cmd := a.Update(key(k))
			press(t, a, k)
			eq(t, a.banner.text, "busy…")
			press(t, a, "j")
			f.setToday(api.Today{Clock: testClock()}, `"a-2"`)
			drain(t, a, a.fetchToday(false))
			drain(t, a, cmd)
			eq(t, f.count("SetFocus")+f.count("Unblock")+f.count("CompleteTask"), 1)
			switch k {
			case "f":
				eq(t, f.mustLast(t, "SetFocus").Req.(api.FocusRequest).TaskID.V, int64(57))
			case "U":
				eq(t, f.mustLast(t, "Unblock").ID, int64(8))
			case "x":
				eq(t, f.mustLast(t, "CompleteTask").ID, int64(57))
			}
		})
	}
}

func TestTodaySelectionSurvivesRefresh(t *testing.T) {
	f := fakeWithToday(todayFixture())
	a := newTestApp(t, f)
	press(t, a, "j", "j", "j") // b7, not the other block for #42.
	td := todayFixture()
	td.Blocks[0], td.Blocks[2] = td.Blocks[2], td.Blocks[0]
	td.Blocks[0].ETag = "fresh-b7"
	f.setToday(td, `"a-2"`)
	drain(t, a, a.fetchToday(false))
	press(t, a, "U")
	eq(t, f.mustLast(t, "Unblock"), call{Method: "Unblock", ID: 7, Mut: client.Mut{IfMatch: "fresh-b7"}})
	td.Blocks = td.Blocks[1:]
	f.setToday(td, `"a-3"`)
	drain(t, a, a.fetchToday(false))
	press(t, a, "U")
	eq(t, f.count("Unblock"), 1)
	eq(t, a.banner.text, "select a row first (j)")
}

func TestTodayTinyTerminal(t *testing.T) { // Review Focus 4.
	td := todayFixture()
	td.Overdue = []api.Task{task(70, "Overdue item")}
	td.DueSoon = []api.Task{task(71, "Due item")}
	td.Blocks[2].Status = "worked"
	for i := range 6 {
		td.TriageTop = append(td.TriageTop, task(int64(100+i), fmt.Sprintf("Triage item %d", i)))
	}
	a := newTestApp(t, fakeWithToday(td))
	v := viewText(a)
	for _, part := range []string{"Overdue", "#70 Overdue item", "Due soon", "#71 Due item", "worked", "Triage item 3"} {
		if !strings.Contains(v, part) {
			t.Fatal(part, v)
		}
	}
	if strings.Contains(v, "Triage item 4") {
		t.Fatal("more than five triage rows rendered")
	}
	for _, color := range []bool{false, true} {
		td = withFocusTitle(td, "evil\x1b\u009b\u202e"+strings.Repeat("宽", 100))
		td.Blocks[0].TaskTitle, td.Entries[0].Title = td.Focus.Title, td.Focus.Title
		a = newTestApp(t, fakeWithToday(td), func(c *Config) { c.Color = color })
		press(t, a, "j")
		for _, size := range []tea.WindowSizeMsg{{Width: 100, Height: 30}, {Width: 20, Height: 5}, {Width: 1, Height: 1}, {}} {
			a.Update(size)
			v = viewText(a)
			if strings.ContainsAny(v, "\u009b\u202e") || (!color && strings.ContainsRune(v, '\x1b')) {
				t.Fatalf("unsafe text: %q", v)
			}
			if size.Height > 0 && len(strings.Split(v, "\n")) > size.Height {
				t.Fatal("too many lines")
			}
			for _, line := range strings.Split(v, "\n") {
				if lipgloss.Width(line) > size.Width {
					t.Fatal("oversized line", line)
				}
			}
		}
	}
}

func TestTodayFormattingHelpers(t *testing.T) {
	eq(t, hhmm(0), "00:00")
	eq(t, hhmm(780), "13:00")
	eq(t, hhmm(1440), "24:00")
	eq(t, spanText(1380, 1440), "23:00–24:00")
	td := todayFixture()
	td.Overdue = []api.Task{task(70, "Overdue")}
	td.DueSoon = []api.Task{task(71, "Due soon")}
	td.Blocks = append(td.Blocks, api.Block{ID: 20, TaskID: 72, TaskTitle: "Block title"})
	td.CurrentBlock = &api.Block{TaskID: 73, TaskTitle: "Current title"}
	titles := titlesFrom(&td)
	eq(t, titles, map[int64]string{42: "Set up keycloak postgres cluster", 57: "New task", 60: "Restart stuck pg replica",
		70: "Overdue", 71: "Due soon", 72: "Block title", 73: "Current title"})
	eq(t, len(titlesFrom(nil)), 0)
	i := td.Interruptions[0]
	eq(t, interruptionLine(i, nil, nil), "10:20 #60 interrupted #42")
	i.FromTaskID, i.FromBlockID = nil, nil
	eq(t, interruptionLine(i, nil, nil), "10:20 #60")
	titles[60] = "unsafe\x1b\u009b\u202e"
	eq(t, interruptionLine(i, titles, nil), "10:20 #60 'unsafe���'")
}

func TestTodayReblockFormKeepsCapturedBlock(t *testing.T) {
	f := fakeWithToday(todayFixture())
	a := newTestApp(t, f)
	press(t, a, "j", "B")
	typeText(t, a, "15:00-16:00")
	td := todayFixture()
	td.Blocks = td.Blocks[1:]
	f.setToday(td, `"a-2"`)
	drain(t, a, func() tea.Msg { return pollMsg{} })
	press(t, a, "enter")
	eq(t, f.mustLast(t, "Reblock"), call{Method: "Reblock", ID: 8, Mut: client.Mut{Key: freshFormKey(t, f, "Reblock"), IfMatch: "b-8"}, Req: api.ReblockRequest{
		BlockRequest: api.BlockRequest{Date: testClock().Today, Start: 900, End: 960}, TaskETag: "g-57",
	}})
}

func TestTodaySelectedRowStaysVisible(t *testing.T) {
	a := newTestApp(t, fakeWithToday(todayFixture()))
	a.Update(tea.WindowSizeMsg{Width: 40, Height: 5})
	press(t, a, "j", "j", "j")
	if !strings.Contains(viewText(a), "› b7 13:00–15:00 #42") {
		t.Fatal(viewText(a))
	}
}

func TestTodayMovesBackFromEndWhenContentFits(t *testing.T) {
	f := newFake()
	f.today.DueSoon = []api.Task{task(1, "first"), task(2, "last")}
	a := newTestApp(t, f)
	press(t, a, "j", "j", "j")
	_ = viewText(a) // Rendering clamps the tail scroll to zero when everything fits.
	_, _, ok := a.current().selection()
	eq(t, ok, false)
	press(t, a, "k")
	selectedID(t, a, 2)
	// A clock tick between keystrokes must preserve the past-end position too.
	press(t, a, "j")
	_ = viewText(a)
	td := *a.today
	td.Clock.Now.Time = td.Clock.Now.Add(5 * time.Second)
	f.setToday(td, "tick")
	drain(t, a, a.poll())
	press(t, a, "k")
	selectedID(t, a, 2)
}
