package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"taskronin/internal/api"
	"taskronin/internal/client"
)

func TestTabKeys(t *testing.T) {
	a := newTestApp(t, newFake())
	for _, c := range []struct {
		k    string
		want Tab
	}{{"2", TabTriage}, {"3", TabBacklog}, {"4", TabUpcoming}, {"5", TabLog}, {"1", TabToday}, {"tab", TabTriage}, {"shift+tab", TabToday}, {"shift+tab", TabLog}, {"tab", TabToday}} {
		press(t, a, c.k)
		eq(t, a.tab, c.want)
	}
}

func TestTabKeypadKeys(t *testing.T) {
	for _, c := range []struct {
		name string
		key  tea.KeyPressMsg
		want Tab
	}{
		{"kitty", tea.KeyPressMsg{Code: tea.KeyKp2, Text: "2"}, TabTriage},
		{"legacy", tea.KeyPressMsg{Code: tea.KeyKp3}, TabBacklog},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := newTestApp(t, newFake())
			_, cmd := a.Update(c.key)
			drain(t, a, cmd)
			eq(t, a.tab, c.want)
		})
	}
}

func TestQuitKeys(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c"} {
		a := newTestApp(t, newFake())
		press(t, a, k)
		eq(t, a.quitting, true)
	}
}

func TestHelpToggle(t *testing.T) {
	a := newTestApp(t, newFake())
	a.setBanner(bannerError, "server busy — try again")
	press(t, a, "?")
	if !contains("ctrl+r redo")([]byte(viewText(a))) {
		t.Fatal(viewText(a))
	}
	press(t, a, "?")
	if strings.Contains(viewText(a), "ctrl+r redo") {
		t.Fatal(viewText(a))
	}
	if !strings.Contains(viewText(a), "server busy — try again") {
		t.Fatal("closing help lost the banner")
	}
}

func TestGlobalsInModes(t *testing.T) {
	a := newTestApp(t, newFake())
	m := &recScreen{}
	drain(t, a, a.enterMode(ModeTriage, m))
	press(t, a, "?")
	if !strings.Contains(viewText(a), "ctrl+r redo") {
		t.Fatal("help")
	}
	press(t, a, "2")
	eq(t, a.mode, ModeTriage)
	eq(t, a.tab, TabToday)
	press(t, a, "q")
	eq(t, a.mode, ModeTabs)
	eq(t, a.quitting, false)
}

func TestOverlayStack(t *testing.T) {
	a := newTestApp(t, newFake())
	s1, s2 := &recScreen{}, &recScreen{}
	drain(t, a, a.openOverlay(s1))
	drain(t, a, a.openOverlay(s2))
	if a.current() != screen(s2) {
		t.Fatal("top")
	}
	press(t, a, "2", "tab", "q", "?")
	eq(t, a.tab, TabToday)
	eq(t, a.quitting, false)
	eq(t, a.help.ShowAll, true)
	eq(t, s1.keys, 0)
	press(t, a, "esc")
	if a.current() != screen(s1) {
		t.Fatal("pop")
	}
	press(t, a, "esc")
	if a.current() != a.tabs[TabToday] {
		t.Fatal("tab")
	}
	eq(t, s1.loads, 1)
	eq(t, s2.loads, 1)
}

func TestMutateIgnoresWhileInflight(t *testing.T) {
	f := newFake()
	a := newTestApp(t, f)
	undo := func(ctx context.Context, c API) (*api.OpResponse, error) { return c.Undo(ctx, client.Mut{}) }
	cmd := a.mutate("undo", undo, nil, nil)
	if a.mutate("undo", undo, nil, nil) != nil {
		t.Fatal("second mutate must return nil")
	}
	if !strings.Contains(viewText(a), "busy") {
		t.Fatal("no busy banner")
	}
	eq(t, f.count("Undo"), 0)
	drain(t, a, cmd)
	eq(t, f.count("Undo"), 1)
	eq(t, a.inflight, false)
}

func TestHandleErrPolicy(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		banner    string
		refreshes bool
	}{
		{"precondition", problem("precondition_failed", 412, "x"), "changed elsewhere — refreshed", true},
		{"other 412", problem("other", 412, "x"), "changed elsewhere — refreshed", true},
		{"generation", problem("generation_mismatch", 412, "x"), "database was restored — refreshed", true},
		{"undo", problem("nothing_to_undo", 409, "x"), "nothing to undo", false},
		{"redo", problem("nothing_to_redo", 409, "x"), "nothing to redo", false},
		{"rate", problem("rate_limited", 429, "x"), "server busy — try again", false},
		{"busy", problem("busy", 503, "x"), "server busy — try again", false},
		{"not sent", notSent(), "cannot reach the Taskronin server — nothing was saved", false},
		{"unknown", outcomeUnknown("K1"), "outcome unknown — refreshed; check before repeating", true},
		{"unknown wrapping problem", &client.OutcomeUnknownError{Key: "K1", Err: errors.Join(notSent(), problem("precondition_failed", 412, "x"))}, "outcome unknown — refreshed; check before repeating", true},
		{"unknown wrapping validation", fmt.Errorf("wrapped: %w", &client.OutcomeUnknownError{Key: "K1", Err: problem("validation", 422, "bad")}), "outcome unknown — refreshed; check before repeating", true},
		{"validation", &client.ProblemError{Status: 422, Problem: api.Problem{Code: "validation", Detail: "bad", Errors: []api.FieldError{{Field: "span", Message: "past"}}}}, "bad (span: past)", false},
		{"unsafe", errors.New("bad\x1b\u202e\ntext"), "bad���text", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake()
			a := newTestApp(t, f)
			n := f.count("Today")
			drain(t, a, a.handleErr(c.err))
			eq(t, a.banner.text, c.banner)
			eq(t, f.count("Today") > n, c.refreshes)
		})
	}
	for _, c := range []struct {
		err  error
		want string
	}{
		{notSent(), "cannot reach the Taskronin server"},
		{outcomeUnknown(""), "server did not answer"},
		{&client.OutcomeUnknownError{Err: problem("precondition_failed", 412, "x")}, "server did not answer"},
	} {
		t.Run("read "+c.want, func(t *testing.T) {
			f := newFake()
			a := newTestApp(t, f)
			n := f.count("Today")
			_, cmd := a.Update(todayMsg{req: a.nextReq("today"), err: c.err})
			drain(t, a, cmd)
			eq(t, a.banner.text, c.want)
			eq(t, f.count("Today"), n)
		})
	}
}

func TestHandleErrFallback(t *testing.T) {
	for _, c := range []struct {
		name    string
		problem api.Problem
		want    string
	}{
		{"detail", api.Problem{Detail: "detail", Title: "title", Code: "conflict"}, "detail"},
		{"title", api.Problem{Title: "title\u009b", Code: "conflict"}, "title�"},
		{"code", api.Problem{Code: "conflict"}, "conflict"},
		{"status", api.Problem{}, "HTTP 500"},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := newTestApp(t, newFake())
			drain(t, a, a.handleErr(&client.ProblemError{Status: 500, Problem: c.problem}))
			eq(t, a.banner.text, c.want)
		})
	}
}

func TestTodayReadRecovery(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
	}{
		{"not sent", notSent()},
		{"unanswered", outcomeUnknown("")},
		{"problem", problem("validation", 422, "bad read")},
	} {
		for _, notModified := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/304=%v", c.name, notModified), func(t *testing.T) {
				f := newFake()
				f.todayETag = "v1"
				a := newTestApp(t, f)
				staleReq := a.reqSeq["today"]
				f.errs["Today"] = c.err
				drain(t, a, a.fetchToday(true))
				failedBanner := a.banner.text
				if failedBanner == "" {
					t.Fatal("read failure has no banner")
				}
				a.Update(todayMsg{req: staleReq, notModified: true})
				eq(t, a.banner.text, failedBanner)
				if !notModified {
					f.setToday(api.Today{Clock: f.clock}, "v2")
				}
				drain(t, a, a.fetchToday(true))
				eq(t, a.banner.text, "")
			})
		}
	}
}

func TestTodayReadKeepsMutationBanner(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("failure=%v", fail), func(t *testing.T) {
			f := newFake()
			a := newTestApp(t, f)
			drain(t, a, a.handleReadErr(notSent()))
			want := "undo"
			if fail {
				f.errs["Undo"] = notSent()
				want = "cannot reach the Taskronin server — nothing was saved"
			}
			drain(t, a, a.mutate("undo", func(ctx context.Context, c API) (*api.OpResponse, error) {
				return c.Undo(ctx, client.Mut{})
			}, nil, nil))
			drain(t, a, a.fetchToday(false))
			eq(t, a.banner.text, want)
		})
	}
}

func TestBannerPrefixesWithoutColor(t *testing.T) {
	f := newFake()
	a := newTestApp(t, f)
	f.errs["Undo"] = problem("nothing_to_undo", 409, "")
	for _, tc := range []struct{ key, want string }{
		{"ctrl+r", "✓ redone"},
		{"u", "⚠ nothing to undo"},
	} {
		press(t, a, tc.key)
		frame := viewText(a)
		if !strings.HasSuffix(frame, tc.want) || strings.ContainsRune(frame, '\x1b') {
			t.Errorf("after %s, want plain footer %q, got %q", tc.key, tc.want, frame)
		}
	}
}

func TestRevertBannerKeepsConsequencesAndWarnings(t *testing.T) {
	a := newTestApp(t, newFake())
	a.Update(opMsg{label: "reverted", resp: &api.OpResponse{
		Effects:  []api.Effect{{Kind: "updated"}, {Kind: "reverted", Via: "redo", To: "task.complete"}, {Kind: "completed", TaskID: 42}},
		Warnings: []string{"check the task"},
	}})
	const want = "redid complete · #42 completed · ⚠ check the task"
	eq(t, a.banner.text, want)
	frame := viewText(a)
	eq(t, strings.Count(frame, "✓"), 1)
	if !strings.HasSuffix(frame, "✓ "+want) {
		t.Fatal(frame)
	}
}

func TestEffectsLine(t *testing.T) {
	eq(t, effectsLine([]api.Effect{{Kind: "block_cancelled", BlockID: 7}, {Kind: "slipped", BlockID: 7},
		{Kind: "reason_added", TaskID: 42, Reason: "slipped"}, {Kind: "updated", TaskID: 42}}), "b7 cancelled · b7 slipped · #42 needs triage (slipped)")
	eq(t, effectsLine([]api.Effect{{Kind: "interrupted", TaskID: 42, From: "17"}, {Kind: "unslipped", BlockID: 7},
		{Kind: "focus_set", TaskID: 42}, {Kind: "focus_cleared"}, {Kind: "block_resolved", BlockID: 7},
		{Kind: "block_resolved", BlockID: 8, To: "worked"}, {Kind: "completed", TaskID: 42},
		{Kind: "entry_logged", EntryID: 9}, {Kind: "reverted"}}),
		"interrupted #17 · b7 unslipped · focus #42 · focus cleared · b7 resolved · b8 worked · #42 completed · e9 logged · reverted")
}

func TestEffectsLineReverted(t *testing.T) {
	for _, tc := range []struct{ via, to, want string }{
		{"undo", "task.complete", "undid complete"},
		{"redo", "entry.add", "redid log"},
		{"revert", "task.update", "reverted edit"},
		{"", "future.kind", "reverted future.kind"},
		{"undo", "", "undone"},
		{"redo", "", "redone"},
		{"revert", "", "reverted"},
	} {
		eq(t, effectsLine([]api.Effect{{Kind: "reverted", Via: tc.via, From: "abcdef12", To: tc.to}}), tc.want)
	}
}

func TestRenderSanitizesAndClips(t *testing.T) {
	a := newTestApp(t, newFake())
	out := a.line("evil\x1b[31mred\u009b31m\u202e"+strings.Repeat("宽", 200), 40)
	if strings.ContainsAny(out, "\x1b\u009b\u202e") || lipgloss.Width(out) > 40 {
		t.Fatalf("%q", out)
	}
	for _, size := range []tea.WindowSizeMsg{{Width: 20, Height: 5}, {Width: 1, Height: 1}, {Width: 0, Height: 0}} {
		a.Update(size)
		out := viewText(a)
		for _, l := range strings.Split(out, "\n") {
			if lipgloss.Width(l) > size.Width {
				t.Fatal(l)
			}
		}
		if size.Height > 0 && strings.Count(out, "\n") >= size.Height {
			t.Fatal(out)
		}
		if strings.ContainsRune(out, '\x1b') {
			t.Fatal("plain output has escapes")
		}
	}
}

func TestHeaderTabMarker(t *testing.T) {
	plain := newTestApp(t, newFake())
	if header := plain.header(120); !strings.Contains(header, "[1 Today] 2 Triage") {
		t.Fatalf("plain header: %q", header)
	}
	a := newTestApp(t, newFake(), func(c *Config) { c.Color = true })
	header := a.header(120)
	for _, want := range []string{"\x1b[7m" + a.styles.Head.Render(" 1 Today "), a.styles.TabOff.Render("2 Triage") + " " + a.styles.TabOff.Render("3 Backlog")} {
		if !strings.Contains(header, want) {
			t.Errorf("missing %q in %q", want, header)
		}
	}
}

func TestHelpFooterColors(t *testing.T) {
	a := newTestApp(t, newFake(), func(c *Config) { c.Color = true })
	if !strings.Contains(a.helpView(80), a.styles.Dim.Render("?")) {
		t.Fatal("short help key lacks dim style")
	}
	plain := newTestApp(t, newFake())
	plain.help.ShowAll, a.help.ShowAll = true, true
	lines := strings.Split(plain.helpView(40), "\n")
	for i, line := range lines {
		lines[i] = a.styles.Dim.Render(line)
	}
	eq(t, a.helpView(40), strings.Join(lines, "\n"))
}

func TestRowPaddingFitsWidth(t *testing.T) {
	a := newTestApp(t, newFake(), func(c *Config) { c.Color = true })
	for _, w := range []int{1, 2, 5, 20, 100} {
		for _, title := range []string{"x", strings.Repeat("宽", 300)} {
			row := a.row(title, w, true)
			if !strings.HasPrefix(row, "\x1b[7m") || lipgloss.Width(row) != w {
				t.Errorf("width %d selected row %q", w, row)
			}
			if row := a.row(title, w, false); lipgloss.Width(row) > w {
				t.Errorf("width %d unselected row %q", w, row)
			}
		}
	}
	plain := newTestApp(t, newFake())
	eq(t, plain.row("x", 20, true), "› x")
}

func TestRenderColorFrameClips(t *testing.T) {
	check := func(a *App) {
		t.Helper()
		out := viewText(a)
		if strings.ContainsRune(out, '\x1b') != a.cfg.Color {
			t.Fatalf("color=%v, frame=%q", a.cfg.Color, out)
		}
		lines := strings.Split(out, "\n")
		if len(lines) > 8 {
			t.Fatalf("frame has %d lines", len(lines))
		}
		for _, line := range lines {
			if lipgloss.Width(line) > 20 {
				t.Fatalf("oversized color line: %q", line)
			}
		}
	}
	a := newTestApp(t, newFake(), func(c *Config) { c.Color = true })
	a.Update(tea.WindowSizeMsg{Width: 20, Height: 8})
	a.setTabScreen(TabToday, &shellScreen{body: a.row(strings.Repeat("宽", 40), 80, true)})
	a.form = &shellScreen{body: a.styles.Info.Render("prompt\ninput\nhint")}
	a.setBanner(bannerError, strings.Repeat("宽", 40))
	for _, fullHelp := range []bool{false, true} {
		a.help.ShowAll = fullHelp
		check(a)
	}
	for _, color := range []bool{false, true} {
		f := newFake()
		f.days[f.clock.Today] = api.Day{Date: f.clock.Today}
		a := newTestApp(t, f, func(c *Config) { c.Color = color })
		a.Update(tea.WindowSizeMsg{Width: 20, Height: 8})
		for _, tab := range []string{"1", "2", "3", "4", "5"} {
			press(t, a, tab)
			check(a)
		}
		drain(t, a, a.enterMode(ModePlan, newPlanMode()))
		check(a)
		press(t, a, "enter")
		check(a)
		drain(t, a, a.enterMode(ModeRecap, newRecapMode(f.clock.Today)))
		check(a)
	}
}

func TestTodayMsgStoresAggregate(t *testing.T) {
	f := newFake()
	f.today = api.Today{Clock: f.clock, AIChanges: 2, Banners: []api.Banner{{Kind: "backup_failed"}}}
	f.todayETag = `"a-1"`
	a := newTestApp(t, f)
	eq(t, a.todayETag, `"a-1"`)
	if !strings.Contains(viewText(a), "⚠ backup") || !strings.Contains(viewText(a), "ai 2") {
		t.Fatal(viewText(a))
	}
	r1, r2 := a.nextReq("today"), a.nextReq("today")
	newClock := f.clock
	newClock.Now.Time = newClock.Now.Add(time.Hour)
	a.Update(todayMsg{req: r2, today: &api.Today{Clock: newClock}, etag: `"a-3"`})
	a.Update(todayMsg{req: r1, today: &api.Today{Clock: f.clock}, etag: `"a-2"`})
	eq(t, a.todayETag, `"a-3"`)
	eq(t, a.clock, newClock)
	_, cmd := a.Update(todayMsg{req: r1, err: notSent()})
	eq(t, cmd == nil, true)
	eq(t, a.banner.text, "")
}

func TestAppDefaultsAndStartModes(t *testing.T) {
	for _, mode := range []Mode{ModeTabs, ModeTriage, ModePlan, ModeRecap} {
		f := newFake()
		a := NewApp(Config{API: f, Ctx: t.Context(), Clock: f.clock, StartMode: mode, StartTab: TabBacklog})
		// Exercise defaults without draining the deliberately endless poll loop.
		drain(t, a, a.refresh())
		eq(t, a.cfg.PollInterval, DefaultPollInterval)
		eq(t, DefaultPollInterval, 5*time.Second)
		eq(t, a.cfg.Editor.Getenv("EDITOR"), "")
		eq(t, a.cfg.RecapDate, a.clock.Today)
		eq(t, a.tab, TabBacklog)
		eq(t, a.mode, mode)
		if a.current() == nil {
			t.Fatal("missing startup screen")
		}
		eq(t, a.View().AltScreen, true)
		if mode != ModeTabs {
			press(t, a, "esc")
			eq(t, a.quitting, true)
		}
	}
}

func TestParseTab(t *testing.T) {
	for i, name := range []string{"today", "triage", "backlog", "upcoming", "log"} {
		got, err := ParseTab(name)
		if err != nil {
			t.Fatal(err)
		}
		eq(t, got, Tab(i))
	}
	for _, name := range []string{"", "Today", "other"} {
		if _, err := ParseTab(name); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
}

type shellScreen struct {
	recScreen
	consume bool
	seen    []string
	msgs    []tea.Msg
	body    string
}

func (s *shellScreen) handle(_ *App, k tea.KeyPressMsg) (tea.Cmd, bool) {
	s.seen = append(s.seen, k.String())
	return nil, s.consume
}

func (s *shellScreen) update(_ *App, msg tea.Msg) tea.Cmd {
	s.msgs = append(s.msgs, msg)
	return nil
}

func (s *shellScreen) view(_ *App, _, _ int) string { return s.body }

func TestFormAndScreenKeyRouting(t *testing.T) {
	a := newTestApp(t, newFake())
	tab, overlay, form := &shellScreen{}, &shellScreen{}, &shellScreen{}
	a.setTabScreen(TabToday, tab)
	drain(t, a, a.openOverlay(overlay))
	a.form = form
	typeText(t, a, "?2q")
	press(t, a, "esc", "up")
	eq(t, form.seen, []string{"?", "2", "q", "esc", "up"})
	eq(t, len(tab.seen), 0)
	eq(t, len(overlay.seen), 0)
	eq(t, a.help.ShowAll, false)
	if a.current() != screen(overlay) {
		t.Fatal("form became current screen")
	}
	press(t, a, "ctrl+c")
	eq(t, a.quitting, true)
	eq(t, len(form.seen), 5)

	a = newTestApp(t, newFake())
	tab = &shellScreen{consume: true}
	a.setTabScreen(TabToday, tab)
	press(t, a, "?", "up", "down", "J", "K")
	eq(t, tab.seen, []string{"?", "k", "j", "J", "K"})
	eq(t, a.help.ShowAll, false)
	mode := &shellScreen{consume: true}
	drain(t, a, a.enterMode(ModeTriage, mode))
	press(t, a, "?", "up", "down", "k", "j", "q")
	eq(t, mode.seen, []string{"?", "up", "down", "k", "j"})
	eq(t, a.mode, ModeTabs)
}

func TestScreenMessagesAndRefresh(t *testing.T) {
	f := newFake()
	a := newTestApp(t, f)
	tab, mode, overlay, form := &shellScreen{}, &shellScreen{}, &shellScreen{}, &shellScreen{}
	a.setTabScreen(TabToday, tab)
	drain(t, a, a.enterMode(ModePlan, mode))
	drain(t, a, a.openOverlay(overlay))
	a.form = form
	type dataMsg struct{}
	_, cmd := a.Update(dataMsg{})
	drain(t, a, cmd)
	for _, s := range []*shellScreen{tab, mode, overlay, form} {
		eq(t, s.msgs, []tea.Msg{dataMsg{}})
	}
	drain(t, a, a.refresh())
	eq(t, tab.loads, 0)
	eq(t, mode.loads, 1)
	eq(t, overlay.loads, 2)
	eq(t, form.loads, 0)
	eq(t, f.mustLast(t, "Today").INM, "")
}

func TestFetchTodayCapturesRequest(t *testing.T) {
	f := newFake()
	f.todayETag = "v1"
	a := newTestApp(t, f)
	cmd := a.fetchToday(true)
	a.todayETag = "v2"
	drain(t, a, cmd)
	eq(t, f.mustLast(t, "Today").INM, "v1")
	eq(t, a.todayETag, "v2") // 304 leaves all stored state alone.
	drain(t, a, a.fetchToday(false))
	eq(t, f.mustLast(t, "Today").INM, "")
	eq(t, a.todayETag, "v1")
	r := a.nextReq("tasks")
	a.nextReq("today")
	eq(t, a.latest("tasks", r), true)
}

func TestFetchTodaySequenceCapturedBeforeExecution(t *testing.T) {
	f := newFake()
	a := newTestApp(t, f)
	first, second := a.fetchToday(false), a.fetchToday(false)
	eq(t, f.count("Today"), 1)
	f.setToday(api.Today{Clock: f.clock, AIChanges: 2}, "newer")
	drain(t, a, second)
	f.setToday(api.Today{Clock: f.clock, AIChanges: 1}, "older")
	drain(t, a, first)
	eq(t, a.todayETag, "newer")
	eq(t, a.today.AIChanges, 2)
}

func TestMutateRefreshesAfterNavigation(t *testing.T) {
	for _, navigation := range []string{"close overlay", "change mode"} {
		t.Run(navigation, func(t *testing.T) {
			f := newFake()
			a := newTestApp(t, f)
			old, next := &recScreen{}, &recScreen{}
			wantLoads := 1
			if navigation == "close overlay" {
				a.setTabScreen(TabToday, next)
				drain(t, a, a.openOverlay(old))
			} else {
				drain(t, a, a.enterMode(ModeTriage, old))
				wantLoads = 2 // Entering the new mode loads it; the own-op refresh loads it again.
			}
			drain(t, a, a.mutate("undo", func(ctx context.Context, c API) (*api.OpResponse, error) {
				return c.Undo(ctx, client.Mut{})
			}, func(a *App, _ *api.OpResponse) tea.Cmd {
				if navigation == "close overlay" {
					a.closeOverlay()
					return nil
				}
				return a.enterMode(ModePlan, next)
			}, nil))
			if a.current() != screen(next) {
				t.Fatal("after hook did not change the visible screen")
			}
			eq(t, old.loads, 1)
			eq(t, next.loads, wantLoads)
			eq(t, f.count("Today"), 2)
		})
	}
}

func TestMutateCallbacksAndBanners(t *testing.T) {
	f := newFake()
	f.resp.OpID = ptr("test-op")
	f.resp.Effects = []api.Effect{{Kind: "completed", TaskID: 42}}
	f.resp.Warnings = []string{"bad\x1b\n"}
	a := newTestApp(t, f)
	call := func(ctx context.Context, c API) (*api.OpResponse, error) { return c.Undo(ctx, client.Mut{}) }
	after, onErr, afterCmd := 0, 0, 0
	success := func(a *App, r *api.OpResponse) tea.Cmd {
		after++
		eq(t, a.inflight, false)
		eq(t, r.OpID, f.resp.OpID)
		return func() tea.Msg { afterCmd++; return nil }
	}
	failure := func(a *App, err error) {
		onErr++
		eq(t, a.inflight, false)
		if !errors.Is(err, client.ErrNotSent) {
			t.Fatal(err)
		}
		a.appendBanner("kept\ntext")
	}
	drain(t, a, a.mutate("done", call, success, failure))
	eq(t, after, 1)
	eq(t, afterCmd, 1)
	eq(t, onErr, 0)
	eq(t, a.banner.text, "done · #42 completed · ⚠ bad��")
	eq(t, f.count("Today"), 2)
	f.errs["Undo"] = notSent()
	drain(t, a, a.mutate("done", call, success, failure))
	eq(t, after, 1)
	eq(t, onErr, 1)
	eq(t, a.banner.text, "cannot reach the Taskronin server — nothing was saved · kept�text")
	eq(t, f.count("Today"), 2)
	press(t, a, "esc")
	eq(t, a.banner.text, "")
}

func TestRenderFormAndHeader(t *testing.T) {
	f := newFake()
	f.today.Banners = []api.Banner{{Kind: "backup_stale"}, {Kind: "purge_incomplete"}, {Kind: "purge_ledger_damaged"}}
	a := newTestApp(t, f)
	a.Update(tea.WindowSizeMsg{Width: 150, Height: 10})
	a.form = &shellScreen{body: "prompt\ninput\nhint"}
	a.setTabScreen(TabToday, &shellScreen{body: strings.Repeat("body\n", 20)})
	a.setBanner(bannerError, "message")
	lines := strings.Split(viewText(a), "\n")
	eq(t, len(lines), 10)
	eq(t, lines[6:], []string{"prompt", "input", "hint", "⚠ message"})
	for _, part := range []string{"Taskronin · Wed 2026-10-28 10:00", "[1 Today] 2 Triage 3 Backlog 4 Upcoming 5 Log", "⚠ backup", "⚠ purge"} {
		if !strings.Contains(strings.Join(lines[:2], "\n"), part) {
			t.Fatal(lines[:2])
		}
	}
	if strings.ContainsRune(viewText(a), '\x1b') {
		t.Fatal("plain output has escapes")
	}
}

func TestRenderSelectedRow(t *testing.T) {
	for _, color := range []bool{false, true} {
		a := newTestApp(t, newFake(), func(c *Config) { c.Color = color })
		for _, selected := range []bool{false, true} {
			out := a.row("safe\u202e"+strings.Repeat("宽", 20), 20, selected)
			prefix := "  "
			if selected {
				prefix = "› "
			}
			if !strings.Contains(out, prefix+"safe�") || strings.ContainsRune(out, '\u202e') || lipgloss.Width(out) > 20 {
				t.Fatalf("color=%v selected=%v: %q", color, selected, out)
			}
			if !color && strings.ContainsRune(out, '\x1b') {
				t.Fatal("plain row has escapes")
			}
		}
	}
}

func TestEditorReadBannerClearsOnSuccessfulRetry(t *testing.T) {
	f := newFake()
	a := newTestApp(t, f)
	f.errs["GetTask"] = notSent()
	drain(t, a, a.editTask(7))
	eq(t, a.banner.text, "cannot reach the Taskronin server")
	reply := a.editTask(7)()
	_, cmd := a.Update(reply)
	eq(t, a.banner.text, "")
	drain(t, a, cmd)
}

func TestReadBannerClearsWhenOwnerCloses(t *testing.T) {
	for _, name := range []string{"overlay", "mode", "form"} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			a := newTestApp(t, f)
			exit := "esc"
			switch name {
			case "overlay":
				f.errs["GetTask"] = notSent()
				drain(t, a, a.openDetail(7))
			case "mode":
				f.errs["Day"] = notSent()
				drain(t, a, a.enterMode(ModeRecap, newRecapMode(f.clock.Today)))
				exit = "q"
			case "form":
				press(t, a, "a")
				typeText(t, a, "read before send")
				f.errs["Generation"] = notSent()
				press(t, a, "enter")
			}
			eq(t, a.banner.text, "cannot reach the Taskronin server")
			press(t, a, exit)
			eq(t, a.banner.text, "")
		})
	}
}

func TestCompletionAndDraftDiscardBanners(t *testing.T) {
	a := newTestApp(t, newFake())
	a.Update(opMsg{label: "completed", resp: &api.OpResponse{Effects: []api.Effect{{Kind: "completed", TaskID: 3}}}})
	if !strings.HasSuffix(viewText(a), "\n✓ #3 completed") {
		t.Error(viewText(a))
	}
	a.Update(opMsg{label: "saved"})
	a.Update(draftDiscardedMsg{path: "kept-draft", err: errors.New("permission denied")})
	if !strings.HasSuffix(viewText(a), "\n⚠ saved · cannot discard draft: permission denied") {
		t.Error(viewText(a))
	}
}
