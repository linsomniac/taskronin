package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/client"
)

// Like taskPagesAPI, keep cursor fixtures local to the tab's paging tests.
type entryPagesAPI struct {
	*fakeAPI
	pages map[string]api.Page[api.Entry]
}

func (f *entryPagesAPI) ListEntries(_ context.Context, q client.EntryQuery) (*api.Page[api.Entry], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(call{Method: "ListEntries", Q: q}); err != nil {
		return nil, err
	}
	p := f.pages[q.Cursor]
	return &p, nil
}

func logEntry(id int64, title string) api.Entry {
	return api.Entry{ID: id, Title: title, Kind: "work", WorkDate: testClock().Today, ETag: fmt.Sprintf("e-%d", id)}
}

func logApp(t *testing.T) (*App, *entryPagesAPI) {
	t.Helper()
	e := logEntry(9, "Restarted replica")
	e.TaskID, e.Time = ptr(int64(60)), ptr(civil.Minute(620))
	f := &entryPagesAPI{fakeAPI: newFake(), pages: map[string]api.Page[api.Entry]{
		"":   {Items: []api.Entry{e, logEntry(8, "Untimed")}, NextCursor: ptr("c2")},
		"c2": {Items: []api.Entry{logEntry(7, "Older")}},
	}}
	a := newTestApp(t, f.fakeAPI, func(c *Config) { c.API, c.StartTab = f, TabLog })
	return a, f
}

func logQuery(from, to, cursor string) client.EntryQuery {
	return client.EntryQuery{From: ptr(civil.MustDate(from)), To: ptr(civil.MustDate(to)), PageQuery: client.PageQuery{Limit: 100, Cursor: cursor}}
}

func requireLogSelection(t *testing.T, a *App, id int64) {
	t.Helper()
	for _, line := range strings.Split(viewText(a), "\n") {
		if strings.HasPrefix(line, "› ") && strings.Contains(line, fmt.Sprintf("  e%d ", id)) {
			return
		}
	}
	t.Fatalf("entry e%d is not visibly selected:\n%s", id, viewText(a))
}

func TestLogTabEmptyRange(t *testing.T) {
	a := newTestApp(t, newFake(), func(c *Config) { c.StartTab = TabLog })
	eq(t, emptyLogText(a.clock.Today, a.clock.Today), "No entries for Wed 10-28 — l logs work")
	// The displayed range still ends on Wednesday until a new range loads.
	a.clock.Today = a.clock.Today.AddDays(1)
	if out := viewText(a); !strings.Contains(out, "No entries Thu 10-15 – Wed 10-28 — l logs work") {
		t.Fatal(out)
	}
}

func TestLogTabRangeAndPaging(t *testing.T) {
	a, f := logApp(t)
	eq(t, f.count("ListEntries"), 1)
	eq(t, f.mustLast(t, "ListEntries").Q, logQuery("2026-10-15", "2026-10-28", ""))
	if !strings.Contains(viewText(a), "Wed 10-28  e9 10:20 Restarted replica  [#60]") {
		t.Fatal(viewText(a))
	}
	_, next := a.Update(key("j"))
	if next == nil {
		t.Fatal("moving onto the last row did not schedule a page")
	}
	eq(t, f.count("ListEntries"), 1) // Reads run only in Cmds.
	press(t, a, "j", "k", "down")
	eq(t, f.count("ListEntries"), 1) // Only one next-page Cmd in flight.
	drain(t, a, next)
	eq(t, f.count("ListEntries"), 2)
	eq(t, f.mustLast(t, "ListEntries").Q, logQuery("2026-10-15", "2026-10-28", "c2"))
	last := -1
	for _, text := range []string{"e9 10:20", "e8 Untimed", "e7 Older"} {
		i := strings.Index(viewText(a), text)
		if i <= last {
			t.Fatal("entries lost server order", viewText(a))
		}
		last = i
	}
	requireLogSelection(t, a, 8)
	press(t, a, "j", "j", "up")
	requireLogSelection(t, a, 8)
	eq(t, f.count("ListEntries"), 2)
}

func TestLogTabActions(t *testing.T) {
	for _, value := range []string{"42", "#42", "-"} {
		t.Run("relink "+value, func(t *testing.T) {
			a, f := logApp(t)
			press(t, a, "L")
			typeText(t, a, value)
			press(t, a, "enter")
			want := api.Opt[int64]{Set: true, V: 42}
			if value == "-" {
				want = api.Opt[int64]{Set: true, Null: true}
			}
			eq(t, f.mustLast(t, "RelinkEntry"), call{Method: "RelinkEntry", ID: 9, Mut: client.Mut{Key: freshFormKey(t, f.fakeAPI, "RelinkEntry"), IfMatch: "e-9"}, Req: api.RelinkRequest{TaskID: want}})
			eq(t, f.count("ListEntries"), 2)
			eq(t, a.form, nil)
		})
	}
	for _, answer := range []string{"y", "n"} {
		t.Run("delete "+answer, func(t *testing.T) {
			a, f := logApp(t)
			press(t, a, "X")
			if !strings.Contains(viewText(a), "delete e9? [y/N]") {
				t.Fatal("missing confirmation", viewText(a))
			}
			eq(t, f.count("DeleteEntry"), 0)
			press(t, a, answer)
			if answer == "y" {
				eq(t, f.mustLast(t, "DeleteEntry"), call{Method: "DeleteEntry", ID: 9, Mut: client.Mut{IfMatch: "e-9"}})
			} else {
				eq(t, f.count("DeleteEntry"), 0)
			}
		})
	}
	t.Run("editor", func(t *testing.T) {
		a, f := logApp(t)
		var initial []byte
		var readErr error
		runs := 0
		a.cfg.Editor.Exec = func(cmd *exec.Cmd, cb tea.ExecCallback) tea.Cmd {
			initial, readErr = os.ReadFile(cmd.Args[len(cmd.Args)-1])
			return stubExec("Updated replica\n\nnotes", readErr, &runs)(cmd, cb)
		}
		press(t, a, "e")
		eq(t, readErr, nil)
		eq(t, string(initial), "Restarted replica\n\n")
		eq(t, runs, 1)
		eq(t, f.mustLast(t, "UpdateEntry"), call{Method: "UpdateEntry", ID: 9, Mut: client.Mut{IfMatch: "e-9"}, Req: api.UpdateEntryRequest{
			Title: api.Opt[string]{Set: true, V: "Updated replica"}, Notes: api.Opt[string]{Set: true, V: "notes"},
		}})
		eq(t, len(draftFiles(t, a.cfg.Editor.DraftsDir)), 0)
		eq(t, f.count("GetTask"), 0)
	})
}

func TestLogTabInvalidRelink(t *testing.T) {
	for _, value := range []string{"", "#", "0", "#0", "-1", "+42", "##42", "abc", "42 words", "9223372036854775808"} {
		t.Run(value, func(t *testing.T) {
			a, f := logApp(t)
			press(t, a, "L")
			typeText(t, a, value)
			before := len(f.calls)
			press(t, a, "enter")
			eq(t, len(f.calls), before)
			fm, ok := a.form.(*form)
			if !ok || fm.errText == "" {
				t.Fatal("missing form error")
			}
			eq(t, fm.input.Value(), value)
		})
	}
}

func TestLogTabImplicitLink(t *testing.T) {
	for _, want := range []int64{42, 60, 0} {
		t.Run(fmt.Sprint(want), func(t *testing.T) {
			a, f := logApp(t)
			td := api.Today{Clock: f.clock}
			if want == 42 {
				td.Focus = ptr(task(42, "focus"))
			}
			if want != 0 {
				td.CurrentBlock = &api.Block{TaskID: 60}
			}
			f.setToday(td, "link")
			_, _, selected := a.current().selection()
			eq(t, selected, false)
			press(t, a, "l")
			typeText(t, a, "new log")
			press(t, a, "enter")
			eq(t, f.mustLast(t, "AddEntry").Req, api.AddEntryRequest{Title: "new log", TaskID: api.Opt[int64]{Set: true, Null: want == 0, V: want}})
		})
	}
}

func TestLogTabMidnight(t *testing.T) {
	for _, nextPage := range []bool{false, true} {
		t.Run(fmt.Sprint(nextPage), func(t *testing.T) {
			a, f := logApp(t)
			clock := f.clock
			clock.Today = civil.MustDate("2026-10-29")
			clock.Now.Time = clock.Now.Add(24 * time.Hour)
			f.setToday(api.Today{Clock: clock}, "midnight")
			drain(t, a, a.fetchToday(false))
			if nextPage {
				press(t, a, "j") // A cursor from yesterday must become a full reload.
			} else {
				drain(t, a, a.current().load(a))
			}
			eq(t, f.mustLast(t, "ListEntries").Q, logQuery("2026-10-16", "2026-10-29", ""))
		})
	}
}

func TestLogTabClockPollKeepsPages(t *testing.T) { // CF02.
	a, f := logApp(t)
	press(t, a, "j", "j")
	td := *a.today
	td.Clock.Now.Time = td.Clock.Now.Add(5 * time.Second)
	f.setToday(td, "clock-only")
	drain(t, a, func() tea.Msg { return pollMsg{} })
	eq(t, f.count("ListEntries"), 2)
	requireLogSelection(t, a, 7)
	td.TriageCount++
	f.setToday(td, "changed")
	drain(t, a, func() tea.Msg { return pollMsg{} })
	eq(t, f.count("ListEntries"), 3)
	if strings.Contains(viewText(a), "e7 Older") {
		t.Fatal("changed payload did not reload first page")
	}
	requireLogSelection(t, a, 8)
}

func TestLogTabStalePageAfterReload(t *testing.T) {
	for _, reload := range []string{"poll", "op", "midnight"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/error=%v", reload, fail), func(t *testing.T) {
				a, f := logApp(t)
				_, next := a.Update(key("j"))
				if next == nil {
					t.Fatal("missing page request")
				}
				if fail {
					f.errs["ListEntries"] = outcomeUnknown("")
				}
				stale := next() // Hold delivery across a full reload.
				f.pages[""] = api.Page[api.Entry]{Items: []api.Entry{logEntry(11, "Fresh"), logEntry(10, "Fresh last")}, NextCursor: ptr("c3")}
				f.pages["c3"] = api.Page[api.Entry]{Items: []api.Entry{logEntry(6, "Fresh older")}}
				switch reload {
				case "poll":
					f.setToday(api.Today{Clock: f.clock, TriageCount: 1}, "changed")
					drain(t, a, func() tea.Msg { return pollMsg{} })
				case "op":
					press(t, a, "X", "y")
				case "midnight":
					clock := f.clock
					clock.Today = clock.Today.AddDays(1)
					f.setToday(api.Today{Clock: clock}, "next-day")
					drain(t, a, a.fetchToday(false))
					drain(t, a, a.current().load(a))
				}
				before, banner := f.count("ListEntries"), a.banner.text
				press(t, a, "k", "j")
				eq(t, f.count("ListEntries"), before) // Old append still owns the in-flight guard.
				drain(t, a, func() tea.Msg { return stale })
				eq(t, a.banner.text, banner)
				if strings.Contains(viewText(a), "e7 Older") {
					t.Fatal("old page appended to the new list")
				}
				press(t, a, "k", "j")
				eq(t, f.count("ListEntries"), before+1)
				eq(t, f.mustLast(t, "ListEntries").Q.(client.EntryQuery).Cursor, "c3")
			})
		}
	}
}

func TestLogTabReadFailureKeepsRows(t *testing.T) {
	for _, appendPage := range []bool{false, true} {
		for _, tc := range []struct {
			err    error
			banner string
		}{
			{notSent(), "cannot reach the Taskronin server"}, {outcomeUnknown(""), "server did not answer"},
		} {
			t.Run(fmt.Sprintf("append=%v/%s", appendPage, tc.banner), func(t *testing.T) {
				a, f := logApp(t)
				f.errs["ListEntries"] = tc.err
				if appendPage {
					press(t, a, "j")
				} else {
					drain(t, a, a.current().load(a))
				}
				eq(t, f.count("ListEntries"), 2)
				eq(t, f.count("Today"), 1)
				eq(t, a.banner.text, tc.banner)
				eq(t, a.banner.readError, true)
				if !strings.Contains(viewText(a), "Restarted replica") || !strings.Contains(viewText(a), "Untimed") {
					t.Fatal("failed read lost rows")
				}
				if appendPage {
					press(t, a, "k", "j")
					eq(t, f.count("ListEntries"), 3)
					if !strings.Contains(viewText(a), "e7 Older") {
						t.Fatal("explicit navigation could not retry the page")
					}
				}
			})
		}
	}
}

func TestLogTabReloadSelectionAndStaleness(t *testing.T) {
	a, f := logApp(t)
	press(t, a, "j", "j")
	fresh := logEntry(7, "Fresh selected")
	fresh.ETag = "e-7-fresh"
	f.pages[""] = api.Page[api.Entry]{Items: []api.Entry{fresh, logEntry(9, "Moved")}}
	old, latest := a.current().load(a), a.current().load(a)
	if old == nil || latest == nil {
		t.Fatal("missing reload")
	}
	drain(t, a, latest)
	requireLogSelection(t, a, 7)
	f.errs["ListEntries"] = outcomeUnknown("")
	drain(t, a, old)
	eq(t, a.banner.text, "")
	press(t, a, "X", "y")
	eq(t, f.mustLast(t, "DeleteEntry"), call{Method: "DeleteEntry", ID: 7, Mut: client.Mut{IfMatch: "e-7-fresh"}})
	f.pages[""] = api.Page[api.Entry]{Items: []api.Entry{logEntry(9, "Fallback")}}
	drain(t, a, a.current().load(a))
	requireLogSelection(t, a, 9)
	f.pages[""] = api.Page[api.Entry]{Items: []api.Entry{logEntry(10, "New"), logEntry(9, "Fallback")}}
	drain(t, a, a.current().load(a))
	requireLogSelection(t, a, 9)
}

func TestLogTabPendingReloadInvalidatesPageImmediately(t *testing.T) {
	a, f := logApp(t)
	_, page := a.Update(key("j"))
	if page == nil {
		t.Fatal("missing page request")
	}
	reload := a.current().load(a)
	drain(t, a, page) // The new full load has been created but has not executed.
	if strings.Contains(viewText(a), "e7 Older") {
		t.Fatal("page survived a newer reload request")
	}
	before := f.count("ListEntries")
	press(t, a, "k", "j", "j")
	eq(t, f.count("ListEntries"), before)
	f.pages[""] = api.Page[api.Entry]{Items: []api.Entry{logEntry(10, "New first")}, NextCursor: ptr("c3")}
	f.pages["c3"] = api.Page[api.Entry]{Items: []api.Entry{logEntry(6, "New page")}}
	drain(t, a, reload)
	press(t, a, "j") // One-row pages must still allow paging at the boundary.
	eq(t, f.mustLast(t, "ListEntries").Q.(client.EntryQuery).Cursor, "c3")
	if !strings.Contains(viewText(a), "e6 New page") {
		t.Fatal("new page did not append")
	}
}

func TestLogTabLoadCapturesInputsAndCreationOrder(t *testing.T) {
	a, f := logApp(t)
	old, latest := a.current().load(a), a.current().load(a)
	f.pages[""] = api.Page[api.Entry]{Items: []api.Entry{logEntry(11, "Latest")}}
	drain(t, a, latest)
	other := newFake()
	a.api, a.clock.Today = other, civil.MustDate("2026-10-29")
	f.pages[""] = api.Page[api.Entry]{Items: []api.Entry{logEntry(12, "Old response")}}
	drain(t, a, old) // Running later must not make an older command current.
	eq(t, f.mustLast(t, "ListEntries").Q, logQuery("2026-10-15", "2026-10-28", ""))
	eq(t, other.count("ListEntries"), 0)
	requireLogSelection(t, a, 11)
	if strings.Contains(viewText(a), "Old response") {
		t.Fatal("stale reload replaced latest rows")
	}
}

func TestLogTabCapturesActionsAndGuards(t *testing.T) {
	for _, k := range []string{"L", "X", "e"} {
		t.Run(k, func(t *testing.T) {
			a, f := logApp(t)
			runs := 0
			a.cfg.Editor.Exec = stubExec("Edited\n\n", nil, &runs)
			_, cmd := a.Update(key(k))
			if k != "e" {
				drain(t, a, cmd)
			}
			f.pages[""] = api.Page[api.Entry]{Items: []api.Entry{logEntry(10, "Replacement")}}
			drain(t, a, a.current().load(a))
			method := "UpdateEntry"
			switch k {
			case "L":
				typeText(t, a, "42")
				_, cmd = a.Update(key("enter"))
				method = "RelinkEntry"
			case "X":
				_, cmd = a.Update(key("y"))
				method = "DeleteEntry"
			}
			press(t, a, k, "enter")
			eq(t, a.banner.text, "busy…")
			drain(t, a, cmd)
			eq(t, f.count(method), 1)
			eq(t, f.mustLast(t, method).ID, int64(9))
			eq(t, f.mustLast(t, method).Mut.IfMatch, "e-9")
		})
	}
}

func TestLogTabRelinkRetrySnapshot(t *testing.T) {
	a, f := logApp(t)
	press(t, a, "L")
	typeText(t, a, "42")
	f.errs["RelinkEntry"] = &client.OutcomeUnknownError{Key: "log-retry", Err: problem("precondition_failed", 412, "")}
	press(t, a, "enter")
	first := f.mustLast(t, "RelinkEntry")
	fm, ok := a.form.(*form)
	if !ok || fm.retry == nil || fm.input.Value() != "42" {
		t.Fatal("relink lost its retry snapshot or text")
	}
	f.pages[""] = api.Page[api.Entry]{Items: []api.Entry{logEntry(10, "Replacement")}}
	drain(t, a, a.current().load(a))
	press(t, a, "enter")
	first.Mut.Key = "log-retry"
	eq(t, f.mustLast(t, "RelinkEntry"), first)
}

func TestLogTabMutationErrorPolicy(t *testing.T) {
	for _, tc := range []struct {
		err    error
		banner string
		reload bool
	}{
		{problem("precondition_failed", 412, ""), "changed elsewhere — refreshed", true},
		{problem("generation_mismatch", 412, ""), "database was restored — refreshed", true},
		{notSent(), "cannot reach the Taskronin server — nothing was saved", false},
		{&client.OutcomeUnknownError{Key: "delete-key", Err: problem("precondition_failed", 412, "")}, "outcome unknown — refreshed; check before repeating", true},
	} {
		t.Run(tc.banner, func(t *testing.T) {
			a, f := logApp(t)
			f.errs["DeleteEntry"] = tc.err
			press(t, a, "X", "y")
			eq(t, f.count("DeleteEntry"), 1)
			eq(t, a.banner.text, tc.banner)
			eq(t, f.count("ListEntries") == 2, tc.reload)
		})
	}
}

func TestLogTabEmptyAndRendering(t *testing.T) {
	for _, color := range []bool{false, true} {
		a, f := logApp(t)
		a.styles, a.cfg.Color = newStyles(color), color
		e := logEntry(6, "bad\x1b\u009b\u202e"+strings.Repeat("宽", 100))
		e.WorkDate, e.Reopened = e.WorkDate.AddDays(-1), true
		p := f.pages[""]
		p.Items = append(p.Items, e)
		p.NextCursor = nil
		f.pages[""] = p
		drain(t, a, a.current().load(a))
		out := viewText(a)
		if !strings.Contains(out, "Tue 10-27") || !strings.Contains(out, "Wed 10-28") {
			t.Fatal("missing day headers", out)
		}
		for _, size := range []tea.WindowSizeMsg{{Width: 100, Height: 30}, {Width: 20, Height: 5}, {Width: 1, Height: 1}, {}} {
			a.Update(size)
			press(t, a, "j", "j")
			out = viewText(a)
			if size.Width == 20 && !strings.Contains(ansi.Strip(out), "e6 ") {
				t.Fatal("selected row hidden", out)
			}
			if strings.ContainsAny(out, "\u009b\u202e") || (!color && strings.ContainsRune(out, '\x1b')) {
				t.Fatalf("unsafe output %q", out)
			}
			for _, line := range strings.Split(out, "\n") {
				if lipgloss.Width(line) > size.Width {
					t.Fatalf("oversized line %q", line)
				}
			}
			if size.Height > 0 && strings.Count(out, "\n") >= size.Height {
				t.Fatal("too many lines")
			}
		}
		f.pages[""] = api.Page[api.Entry]{}
		drain(t, a, a.current().load(a))
		before := len(f.calls)
		press(t, a, "j", "k", "e", "L", "X")
		eq(t, len(f.calls), before)
		eq(t, a.form, nil)
	}
}

func TestLogNilPageRetainsRows(t *testing.T) {
	a, _ := logApp(t)
	a.Update(entriesMsg{req: a.nextReq("entries")})
	requireLogSelection(t, a, 9)
	if !strings.Contains(viewText(a), "log not loaded") {
		t.Fatal(viewText(a))
	}
}
