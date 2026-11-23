package tui

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/linsomniac/taskronin/internal/api"
)

func TestFinalHeaderWarningsAt80(t *testing.T) {
	f := newFake()
	f.today.AIChanges = 3
	f.today.Banners = []api.Banner{{Kind: "backup_failed", Message: "disk full\x1b\u202e"}, {Kind: "backup_stale", Message: "old backup"}, {Kind: "purge_incomplete", Message: "scrub needed"}, {Kind: "purge_ledger_damaged", Message: "ledger damaged"}, {Kind: "tz_changed", Message: "timezone changed"}}
	f.today.Backup = api.BackupHealth{Healthy: false, LastError: "disk full\u009b"}
	a := newTestApp(t, f)
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	header := clip(strings.Split(a.header(80), "\n"), 80, 4)
	for _, want := range []string{"⚠ backup", "⚠ purge", "ai 3", "⚠ timezone"} {
		if !strings.Contains(header, want) {
			t.Errorf("missing %q: %s", want, header)
		}
	}
	body := a.current().view(a, 80, 40)
	for _, want := range []string{"disk full", "old backup", "scrub needed", "ledger damaged", "timezone changed"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q: %s", want, body)
		}
	}
	if strings.ContainsAny(body, "\x1b\u009b\u202e") {
		t.Fatalf("unsafe body: %q", body)
	}
	f.today.Banners = nil
	f.today.Backup = api.BackupHealth{Healthy: true, LastSuccessAt: &f.clock.Now}
	drain(t, a, a.fetchToday(false))
	if body = a.current().view(a, 80, 40); strings.Contains(body, "Backup:") {
		t.Fatal(body)
	}
}

func TestFinalDraftNoticeWrapsAndSurvivesBanners(t *testing.T) {
	for _, fail := range []string{"editor", "title", "server"} {
		t.Run(fail, func(t *testing.T) {
			f, runs := editFixture(), 0
			text := "Changed\n\nnotes"
			var exitErr error
			switch fail {
			case "editor":
				exitErr = errors.New("editor failed")
			case "title":
				text = "\n\nnotes"
			case "server":
				f.errs["UpdateTask"] = notSent()
			}
			a := newTestApp(t, f, func(c *Config) {
				c.Editor.DraftsDir = filepath.Join(t.TempDir(), strings.Repeat("long", 16))
				c.Editor.Exec = stubExec(text, exitErr, &runs)
			})
			a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			drain(t, a, a.editTask(42))
			files := draftFiles(t, a.cfg.Editor.DraftsDir)
			eq(t, len(files), 1)
			assertPath := func() {
				t.Helper()
				out := viewText(a)
				if !strings.Contains(strings.ReplaceAll(out, "\n", ""), files[0]) {
					t.Fatalf("draft path lost: %s", out)
				}
				for _, line := range strings.Split(out, "\n") {
					if lipgloss.Width(line) > 80 {
						t.Fatalf("oversized line: %q", line)
					}
				}
			}
			assertPath()
			a.setBanner(bannerInfo, "busy…")
			assertPath()
			drain(t, a, a.handleReadErr(notSent()))
			assertPath()
			drain(t, a, a.fetchToday(false))
			assertPath()
			press(t, a, "?")
			assertPath()
			press(t, a, "?")
			press(t, a, "esc")
			if strings.Contains(strings.ReplaceAll(viewText(a), "\n", ""), files[0]) {
				t.Fatal("esc did not dismiss draft notice")
			}
		})
	}
}

func TestFinalHelpListsVisibleActionsAt80(t *testing.T) {
	for _, tc := range []struct {
		name string
		tab  Tab
		mode Mode
		want []string
	}{
		{"today", TabToday, ModeTabs, []string{"f focus", "B block/reblock", "U unblock", "x done", "j/↓ down"}},
		{"triage", TabTriage, ModeTabs, []string{"enter triage", "j/↓ down"}},
		{"backlog", TabBacklog, ModeTabs, []string{"J move down", "K move up", "x done", "B block", "d defer", "f focus", "e edit", "D drop", "enter detail"}},
		{"upcoming", TabUpcoming, ModeTabs, []string{"K keep", "d defer", "enter detail"}},
		{"log", TabLog, ModeTabs, []string{"L relink", "X delete", "e edit"}},
		{"card", TabToday, ModeTriage, []string{"k keep", "t top", "b bottom", "p place", "s skip", "A accept new"}},
		{"plan", TabToday, ModePlan, []string{"B block today", "enter summary"}},
		{"recap", TabToday, ModeRecap, []string{"w worked", "m missed", "a auto", "L link"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.days[f.clock.Today] = api.Day{Date: f.clock.Today}
			a := newTestApp(t, f, func(c *Config) { c.StartTab, c.StartMode = tc.tab, tc.mode })
			a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			press(t, a, "?")
			out := viewText(a)
			for _, want := range append(tc.want, "r refresh", "ctrl+r redo") {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q:\n%s", want, out)
				}
			}
			for _, line := range strings.Split(out, "\n") {
				if lipgloss.Width(line) > 80 {
					t.Fatalf("oversized: %q", line)
				}
			}
		})
	}
	for _, overlay := range []string{"detail", "history", "search", "AI"} {
		t.Run(fmt.Sprintf("overlay/%s", overlay), func(t *testing.T) {
			a := newTestApp(t, newFake())
			want := "j/↓ down"
			switch overlay {
			case "detail":
				drain(t, a, a.openDetail(42))
				want = "t tags"
			case "history":
				drain(t, a, a.openHistory(42))
			case "search":
				drain(t, a, a.openOverlay(newSearchOverlay("q")))
				want = "enter detail"
			case "AI":
				drain(t, a, a.openOverlay(newAIOverlay()))
				want = "R revert"
			}
			a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			press(t, a, "?")
			if !strings.Contains(viewText(a), want) {
				t.Fatal(viewText(a))
			}
		})
	}
}

func TestHeaderFitsTabStrip(t *testing.T) {
	for _, color := range []bool{false, true} {
		a := newTestApp(t, newFake(), func(c *Config) { c.Color = color })
		for _, tc := range []struct {
			width  int
			prefix string
		}{{80, "Taskronin · Wed 2026-10-28 10:00 "}, {70, "Wed 2026-10-28 10:00 "}, {60, "Wed 10:00 "}} {
			a.Update(tea.WindowSizeMsg{Width: tc.width, Height: 24})
			for i := range tabNames {
				a.tab = Tab(i)
				line, _, _ := strings.Cut(viewText(a), "\n")
				line = ansi.Strip(line)
				if !strings.HasPrefix(line, tc.prefix) {
					t.Errorf("color %t width %d tab %d: %q", color, tc.width, i+1, line)
				}
				for j, name := range tabNames {
					if !strings.Contains(line, fmt.Sprintf("%d %s", j+1, name)) {
						t.Errorf("missing tab %d in %q", j+1, line)
					}
				}
			}
		}
	}
}

func TestHeaderPrefixBoundaries(t *testing.T) {
	a := newTestApp(t, newFake(), func(c *Config) { c.Color, c.StartTab = false, TabToday })
	for _, tc := range []struct {
		width  int
		prefix string
	}{
		{78, "Taskronin · Wed 2026-10-28 10:00 "},
		{77, "Wed 2026-10-28 10:00 "},
		{66, "Wed 2026-10-28 10:00 "},
		{65, "Wed 10:00 "},
		{55, "Wed 10:00 "},
		{54, "[1 Today]"},
	} {
		a.Update(tea.WindowSizeMsg{Width: tc.width, Height: 24})
		line, _, _ := strings.Cut(viewText(a), "\n")
		if !strings.HasPrefix(line, tc.prefix) {
			t.Errorf("width %d: header %q, want prefix %q", tc.width, line, tc.prefix)
		}
	}
}

func TestHeaderNeverOverflows(t *testing.T) {
	for _, color := range []bool{false, true} {
		a := newTestApp(t, newFake(), func(c *Config) { c.Color = color })
		for width := 20; width <= 120; width++ {
			a.Update(tea.WindowSizeMsg{Width: width, Height: 24})
			for i := range tabNames {
				a.tab = Tab(i)
				line, _, _ := strings.Cut(viewText(a), "\n")
				if got := lipgloss.Width(line); got > width {
					t.Fatalf("color %t width %d tab %d: header uses %d cells", color, width, i+1, got)
				}
				plain := ansi.Strip(line)
				fields := strings.Fields(strings.ReplaceAll(plain, "[", ""))
				for _, number := range []string{"1", "2", "3", "4", "5"} {
					if !slices.Contains(fields, number) {
						t.Fatalf("color %t width %d tab %d: missing tab %s in %q", color, width, i+1, number, plain)
					}
				}
			}
		}
	}
}

func TestHeaderNarrowForms(t *testing.T) {
	for _, tc := range []struct {
		width int
		tab   Tab
		color bool
		want  string
	}{
		{54, TabToday, false, "[1 Today] 2 Triage 3 Backlog 4 Upcoming 5 Log"},
		{45, TabToday, false, "[1 Today] 2 Triage 3 Backlog 4 Upcoming 5 Log"},
		{44, TabToday, false, "[1 Today] 2 3 4 5"},
		{30, TabUpcoming, false, "1 2 3 [4 Upcoming] 5"},
		{44, TabTriage, true, "1  2 Triage  3 4 5"},
	} {
		a := newTestApp(t, newFake(), func(c *Config) { c.Color, c.StartTab = tc.color, tc.tab })
		a.Update(tea.WindowSizeMsg{Width: tc.width, Height: 24})
		line, _, _ := strings.Cut(viewText(a), "\n")
		if got := ansi.Strip(line); got != tc.want {
			t.Errorf("color %t width %d tab %d: got %q, want %q", tc.color, tc.width, tc.tab+1, got, tc.want)
		}
	}
}

func TestModeShortHelp(t *testing.T) {
	f := newFake()
	f.tasks["triage"] = []api.Task{task(1, "triage")}
	f.days[f.clock.Today] = recapFixture(f.clock.Today)
	a := newTestApp(t, f)
	check := func(width int, want string) {
		t.Helper()
		a.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		lines := strings.Split(viewText(a), "\n")
		if got := lines[len(lines)-1]; got != want {
			t.Errorf("footer = %q, want %q", got, want)
		}
	}
	check(80, "1–5/tab tabs • a add • l log • ? help • q quit")
	drain(t, a, a.enterMode(ModeRecap, newRecapMode(f.clock.Today)))
	check(80, "w worked · m missed · a auto · x done · l log · L link · ? help · q back")
	drain(t, a, a.enterMode(ModeTriage, newCardMode(0)))
	check(80, "? help · q back · esc back")
	press(t, a, "?")
	if !strings.Contains(a.helpView(80), "q back") {
		t.Error(a.helpView(80))
	}
	press(t, a, "?")
	drain(t, a, a.enterMode(ModePlan, newPlanMode()))
	check(80, "? help · q back · esc back")
	press(t, a, "enter")
	check(80, "j/↓ down · k/↑ up · B block today · enter summary · ? help · q back")
	press(t, a, "enter")
	check(80, "enter finish · ? help · q back")
}

func TestCardLegendAndExpandedHelp(t *testing.T) {
	for _, mode := range []Mode{ModeTriage, ModePlan} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			f := newFake()
			f.tasks["triage"] = []api.Task{task(1, "triage")}
			a := newTestApp(t, f, func(c *Config) { c.StartMode = mode })
			a.Update(tea.WindowSizeMsg{Width: 160, Height: 24})
			frame := viewText(a)
			eq(t, strings.Count(frame, cardLegend), 1)
			if !strings.HasSuffix(frame, "\n? help · q quit · esc quit") {
				t.Error(frame)
			}
			press(t, a, "?")
			help := a.helpView(160)
			for _, hint := range strings.Split(cardLegend, " · ") {
				if !strings.Contains(help, hint) {
					t.Errorf("expanded help missing %q", hint)
				}
			}
			if mode == ModePlan && !strings.Contains(help, "enter skip to backlog") {
				t.Error(help)
			}
			a.setBanner(bannerInfo, "saved")
			if !strings.Contains(a.helpView(160), "esc clear") {
				t.Error(a.helpView(160))
			}
		})
	}
}

func TestOverlayHelpShowsBackKey(t *testing.T) {
	for _, mode := range []Mode{ModeTabs, ModeRecap} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			f := detailFixture()
			f.days[f.clock.Today] = recapFixture(f.clock.Today)
			a := newTestApp(t, f, func(c *Config) { c.StartMode = mode })
			drain(t, a, a.openDetail(42))
			press(t, a, "h")
			for _, expanded := range []bool{false, true} {
				a.help.ShowAll = expanded
				help := a.helpView(100)
				if !strings.Contains(help, "esc back") || strings.Contains(help, "q ") {
					t.Errorf("expanded=%v: %q", expanded, help)
				}
			}
			press(t, a, "q")
			eq(t, len(a.overlays), 2)
			press(t, a, "esc", "esc")
			eq(t, len(a.overlays), 0)
			eq(t, a.quitting, false)
		})
	}
}
