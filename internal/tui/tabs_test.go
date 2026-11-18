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

func backlogApp(t *testing.T) (*App, *fakeAPI) {
	t.Helper()
	f := newFake()
	f.tasks["backlog"] = []api.Task{task(1, "a"), task(2, "b"), task(3, "c")}
	f.details[2] = api.TaskDetail{Task: f.tasks["backlog"][1]}
	return newTestApp(t, f, func(c *Config) { c.StartTab = TabBacklog }), f
}

func selectedID(t *testing.T, a *App, want int64) {
	t.Helper()
	id, _, ok := a.current().selection()
	eq(t, ok, true)
	eq(t, id, want)
}

func TestBacklogActionAfterReorderTargetsSelected(t *testing.T) { // Review Focus 1.
	a, f := backlogApp(t)
	press(t, a, "j")
	fresh := task(2, "b")
	fresh.ETag = "g-2b"
	f.tasks["backlog"] = []api.Task{task(3, "c"), fresh, task(1, "a")}
	// CF02: a real payload change is required; changing only the ETag does not reload.
	f.setToday(api.Today{Clock: f.clock, TriageCount: 1}, `"a-2"`)
	drain(t, a, func() tea.Msg { return pollMsg{} })
	press(t, a, "x")
	eq(t, f.mustLast(t, "CompleteTask"), call{Method: "CompleteTask", ID: 2, Mut: client.Mut{IfMatch: "g-2b"}, Req: api.CompleteRequest{}})
}

func TestTriageListServerOrder(t *testing.T) { // TRI-20 (TUI side).
	f := newFake()
	for _, id := range []int64{9, 8, 7, 10, 6, 5, 4, 3, 2, 1} {
		f.tasks["triage"] = append(f.tasks["triage"], task(id, "row", "new"))
	}
	a := newTestApp(t, f, func(c *Config) { c.StartTab = TabTriage })
	out, last := viewText(a), -1
	for _, v := range f.tasks["triage"] {
		index := strings.Index(out, fmt.Sprintf("#%d ", v.ID))
		if index <= last {
			t.Fatalf("server order lost: %s", out)
		}
		last = index
	}
	selectedID(t, a, 9)
	press(t, a, "j")
	selectedID(t, a, 8)
	press(t, a, "up")
	selectedID(t, a, 9)
	eq(t, f.mustLast(t, "ListTasks").Q, any(client.TaskQuery{View: "triage", PageQuery: client.PageQuery{Limit: 500}}))
}

func TestBacklogReorder(t *testing.T) {
	for _, tc := range []struct {
		key, how string
		ref      int64
	}{{"J", "after", 3}, {"K", "before", 1}} {
		t.Run(tc.key, func(t *testing.T) {
			a, f := backlogApp(t)
			press(t, a, "j")
			_, cmd := a.Update(key(tc.key))
			eq(t, f.count("PlaceTask"), 0)
			f.tasks["backlog"] = []api.Task{task(2, "b"), task(1, "a"), task(3, "c")}
			drain(t, a, cmd)
			eq(t, f.mustLast(t, "PlaceTask"), call{Method: "PlaceTask", ID: 2, Mut: client.Mut{IfMatch: "g-2"}, Req: api.PlaceRequest{How: tc.how, Ref: tc.ref}})
			selectedID(t, a, 2)
		})
	}
	a, f := backlogApp(t)
	press(t, a, "K", "j", "j", "J")
	eq(t, f.count("PlaceTask"), 0)
}

func TestBacklogKeys(t *testing.T) {
	for _, k := range []string{"D", "f", "d", "B", "e", "enter"} {
		t.Run(k, func(t *testing.T) {
			a, f := backlogApp(t)
			press(t, a, "j", k)
			switch k {
			case "D":
				eq(t, f.mustLast(t, "DropTask"), call{Method: "DropTask", ID: 2, Mut: client.Mut{IfMatch: "g-2"}})
			case "f":
				eq(t, f.mustLast(t, "SetFocus"), call{Method: "SetFocus", Req: api.FocusRequest{TaskID: api.Opt[int64]{Set: true, V: 2}}})
			case "d":
				typeText(t, a, "thu")
				press(t, a, "enter")
				eq(t, f.mustLast(t, "DeferTask"), call{Method: "DeferTask", ID: 2, Mut: client.Mut{Key: freshFormKey(t, f, "DeferTask"), IfMatch: "g-2"}, Req: api.DeferRequest{Until: api.Opt[api.Moment]{Set: true, V: api.Moment{Date: civil.MustDate("2026-10-29")}}}})
			case "B":
				typeText(t, a, "13:00-15:00")
				press(t, a, "enter")
				eq(t, f.mustLast(t, "BlockTask"), call{Method: "BlockTask", ID: 2, Mut: client.Mut{Key: freshFormKey(t, f, "BlockTask"), IfMatch: "g-2"}, Req: api.BlockRequest{Date: civil.MustDate("2026-10-28"), Start: 780, End: 900}})
			case "e":
				eq(t, f.mustLast(t, "GetTask").ID, int64(2))
				eq(t, a.inflight, false)
				eq(t, a.banner.text, "no changes") // CF07 editor stub actually ran.
			case "enter":
				eq(t, len(a.overlays), 1)
				eq(t, a.current().(*detailScreen).id, int64(2))
			}
		})
	}
}

func TestActionFormAfterHook(t *testing.T) {
	for _, method := range []string{"DeferTask", "BlockTask"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fail=%v", method, fail), func(t *testing.T) {
				a, f := backlogApp(t)
				count, afterCmd := 0, 0
				after := func(a *App, r *api.OpResponse) tea.Cmd {
					count++
					eq(t, a.form, nil)
					eq(t, r.OpID, f.resp.OpID)
					return func() tea.Msg { afterCmd++; return nil }
				}
				var cmd tea.Cmd
				value := "thu"
				if method == "DeferTask" {
					cmd = a.openDefer(task(2, "b"), false, after).focus()
				} else {
					cmd = a.openBlock(task(2, "b"), after).focus()
					value = "13:00-15:00"
				}
				drain(t, a, cmd)
				typeText(t, a, value)
				if fail {
					f.errs[method] = problem("validation", 422, "x")
				}
				_, cmd = a.Update(key("enter"))
				eq(t, count, 0) // The hook waits for the server response.
				drain(t, a, cmd)
				want := 1
				if fail {
					want = 0
				}
				eq(t, count, want)
				eq(t, afterCmd, want)
				eq(t, a.form != nil, fail)
				if fail {
					eq(t, a.form.(*form).input.Value(), value)
				}
			})
		}
	}
}

func upcomingFixture() api.Upcoming {
	three, two, one := task(3, "third"), task(2, "second"), task(1, "first")
	three.Defer = &api.Moment{Date: civil.MustDate("2026-10-29")}
	two.Defer = &api.Moment{Date: civil.MustDate("2026-10-29"), Time: ptr(civil.Minute(540))}
	one.Defer = &api.Moment{Date: civil.MustDate("2026-10-30")}
	return api.Upcoming{Deferred: []api.Task{three, two, one}}
}

func TestUpcoming(t *testing.T) { // TRI-26, DUE-18, DUE-19 (TUI side); CF10.
	f := newFake()
	f.upcoming = upcomingFixture()
	a := newTestApp(t, f, func(c *Config) { c.StartTab = TabUpcoming })
	out := viewText(a)
	if !strings.Contains(out, "Deferred") || strings.Contains(out, "Due soon") ||
		strings.Index(out, "#3 ") > strings.Index(out, "#2 ") || strings.Index(out, "#2 ") > strings.Index(out, "#1 ") {
		t.Fatal(out)
	}
	press(t, a, "j", "k")
	selectedID(t, a, 3)
	eq(t, f.count("PlaceTask"), 0) // Lowercase k only navigates.
	press(t, a, "K")
	eq(t, f.mustLast(t, "PlaceTask"), call{Method: "PlaceTask", ID: 3, Mut: client.Mut{IfMatch: "g-3"}, Req: api.PlaceRequest{How: "keep"}})
	press(t, a, "d")
	typeText(t, a, "none")
	press(t, a, "enter")
	eq(t, f.mustLast(t, "DeferTask"), call{Method: "DeferTask", ID: 3, Mut: client.Mut{Key: freshFormKey(t, f, "DeferTask"), IfMatch: "g-3"}, Req: api.DeferRequest{Until: api.Opt[api.Moment]{Set: true, Null: true}}})
	five := task(5, "due task")
	five.Due = &api.Moment{Date: civil.MustDate("2026-10-31")}
	f.upcoming.DueSoon = []api.Task{five}
	drain(t, a, a.current().load(a))
	if !strings.Contains(viewText(a), "Due soon") {
		t.Fatal(viewText(a))
	}
	press(t, a, "j", "j", "j")
	selectedID(t, a, 5)
	press(t, a, "enter")
	eq(t, a.current().(*detailScreen).id, int64(5))
	press(t, a, "esc")
	f.upcoming.Deferred = nil
	drain(t, a, a.current().load(a))
	selectedID(t, a, 5)
	if strings.Contains(viewText(a), "Deferred") {
		t.Fatal(viewText(a))
	}
}

func TestListTabsEmpty(t *testing.T) {
	for _, tc := range []struct {
		tab  Tab
		want string
	}{
		{TabTriage, "nothing in triage"}, {TabBacklog, "backlog is empty"}, {TabUpcoming, "nothing upcoming"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			f := newFake()
			a := newTestApp(t, f, func(c *Config) { c.StartTab = tc.tab })
			if !strings.Contains(viewText(a), tc.want) {
				t.Fatal(viewText(a))
			}
			press(t, a, "j", "k", "J", "K", "x", "B", "d", "f", "D", "e", "enter")
			eq(t, a.form, nil)
			eq(t, len(a.overlays), 0)
			eq(t, f.count("PlaceTask")+f.count("CompleteTask")+f.count("DropTask")+f.count("GetTask")+f.count("SetFocus"), 0)
		})
	}
}

func TestListTabsStaleLoadsAndReadErrors(t *testing.T) {
	for _, tab := range []Tab{TabTriage, TabBacklog, TabUpcoming} {
		for _, staleError := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/error=%v", tab, staleError), func(t *testing.T) {
				f := newFake()
				a := newTestApp(t, f, func(c *Config) { c.StartTab = tab })
				first, second := a.current().load(a), a.current().load(a)
				f.tasks["triage"], f.tasks["backlog"] = []api.Task{task(2, "fresh")}, []api.Task{task(2, "fresh")}
				f.upcoming.Deferred = []api.Task{task(2, "fresh")}
				drain(t, a, second)
				f.tasks["triage"], f.tasks["backlog"] = []api.Task{task(1, "old")}, []api.Task{task(1, "old")}
				f.upcoming.Deferred = []api.Task{task(1, "old")}
				method := "ListTasks"
				if tab == TabUpcoming {
					method = "Upcoming"
				}
				if staleError {
					f.errs[method] = outcomeUnknown("")
				}
				drain(t, a, first)
				selectedID(t, a, 2)
				eq(t, a.banner.text, "")
				for _, tc := range []struct {
					err  error
					want string
				}{{notSent(), "cannot reach the Taskronin server"}, {outcomeUnknown(""), "server did not answer"}} {
					before := f.count(method)
					f.errs[method] = tc.err
					drain(t, a, a.current().load(a))
					eq(t, a.banner.text, tc.want)
					eq(t, f.count(method), before+1)
					selectedID(t, a, 2)
				}
			})
		}
	}
}

func TestBacklogCapturedActionsAndGuard(t *testing.T) {
	for _, tc := range []struct{ key, method string }{{"x", "CompleteTask"}, {"D", "DropTask"}, {"f", "SetFocus"}, {"J", "PlaceTask"}} {
		t.Run(tc.key, func(t *testing.T) {
			a, f := backlogApp(t)
			press(t, a, "j")
			_, cmd := a.Update(key(tc.key))
			press(t, a, tc.key)
			eq(t, a.banner.text, "busy…")
			f.tasks["backlog"] = []api.Task{task(4, "replacement")}
			drain(t, a, a.current().load(a))
			drain(t, a, cmd)
			eq(t, f.count(tc.method), 1)
			got := f.mustLast(t, tc.method)
			if tc.key == "f" {
				eq(t, got.Req.(api.FocusRequest).TaskID.V, int64(2))
			} else {
				eq(t, got.ID, int64(2))
				eq(t, got.Mut.IfMatch, "g-2")
			}
		})
	}
}

func TestActionFormCapturesAndRetries(t *testing.T) {
	for _, tc := range []struct{ key, value, method string }{{"d", "thu", "DeferTask"}, {"B", "13:00-15:00", "BlockTask"}} {
		t.Run(tc.key, func(t *testing.T) {
			a, f := backlogApp(t)
			press(t, a, "j", tc.key)
			typeText(t, a, tc.value)
			f.tasks["backlog"] = []api.Task{task(4, "replacement")}
			drain(t, a, a.current().load(a))
			f.errs[tc.method] = outcomeUnknown("retry-key")
			press(t, a, "enter")
			first := f.mustLast(t, tc.method)
			eq(t, first.ID, int64(2))
			eq(t, first.Mut.IfMatch, "g-2")
			f.clock.Today = f.clock.Today.AddDays(1)
			f.clock.Now.Time = f.clock.Now.Add(24 * time.Hour)
			f.setToday(api.Today{Clock: f.clock}, "next-day")
			drain(t, a, a.fetchToday(false))
			press(t, a, "enter")
			first.Mut.Key = "retry-key"
			eq(t, f.mustLast(t, tc.method), first)
		})
	}
}

func TestActionFormInvalidInput(t *testing.T) {
	for _, tc := range []struct{ key, value, method string }{{"d", "none", "DeferTask"}, {"d", "nextweek", "DeferTask"}, {"B", "25:00-26:00", "BlockTask"}} {
		t.Run(tc.value, func(t *testing.T) {
			a, f := backlogApp(t)
			press(t, a, tc.key)
			typeText(t, a, tc.value)
			press(t, a, "enter")
			eq(t, f.count(tc.method), 0)
			if a.form.(*form).errText == "" {
				t.Fatal("missing parse error")
			}
		})
	}
}

func TestListTabsTinyTerminal(t *testing.T) {
	for _, tab := range []Tab{TabTriage, TabBacklog, TabUpcoming} {
		for _, color := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/color=%v", tab, color), func(t *testing.T) {
				f := newFake()
				var rows []api.Task
				for i := range 10 {
					rows = append(rows, task(int64(i+1), "bad\x1b\u009b\u202e"+strings.Repeat("宽", 100)))
				}
				f.tasks["triage"], f.tasks["backlog"] = rows, rows
				f.upcoming = api.Upcoming{Deferred: rows[:5], DueSoon: rows[5:]}
				a := newTestApp(t, f, func(c *Config) { c.StartTab, c.Color = tab, color })
				for _, size := range []tea.WindowSizeMsg{{Width: 20, Height: 5}, {Width: 1, Height: 1}, {}} {
					a.Update(size)
					for range 9 {
						press(t, a, "j")
					}
					out := viewText(a)
					if size.Width == 20 && !strings.Contains(out, "› #10 ") {
						t.Fatal("selection hidden", out)
					}
					if strings.ContainsAny(out, "\u009b\u202e") || (!color && strings.ContainsRune(out, '\x1b')) {
						t.Fatalf("unsafe text: %q", out)
					}
					if size.Height > 0 && strings.Count(out, "\n") >= size.Height {
						t.Fatal("too many lines")
					}
					for _, line := range strings.Split(out, "\n") {
						if lipgloss.Width(line) > size.Width {
							t.Fatalf("oversized line: %q", line)
						}
					}
				}
			})
		}
	}
}

func TestBacklogConflictRefreshesWithoutRetry(t *testing.T) {
	for _, tc := range []struct{ code, want string }{
		{"precondition_failed", "changed elsewhere — refreshed"},
		{"generation_mismatch", "database was restored — refreshed"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			a, f := backlogApp(t)
			press(t, a, "j")
			f.errs["CompleteTask"] = problem(tc.code, 412, "changed")
			f.tasks["backlog"] = []api.Task{task(4, "replacement")}
			before := f.count("ListTasks")
			press(t, a, "x")
			eq(t, f.count("CompleteTask"), 1)
			eq(t, f.mustLast(t, "CompleteTask").ID, int64(2))
			eq(t, f.count("ListTasks"), before+1)
			eq(t, a.banner.text, tc.want)
			selectedID(t, a, 4)
		})
	}
}

func TestUpcomingKeepCapturesFreshSelectionAndGuards(t *testing.T) {
	f := newFake()
	f.upcoming = upcomingFixture()
	a := newTestApp(t, f, func(c *Config) { c.StartTab = TabUpcoming })
	press(t, a, "j")
	fresh := task(2, "fresh")
	fresh.ETag = "g-2b"
	f.upcoming = api.Upcoming{Deferred: []api.Task{fresh, task(3, "third")}}
	drain(t, a, a.current().load(a))
	selectedID(t, a, 2)
	_, cmd := a.Update(key("K"))
	press(t, a, "K")
	eq(t, a.banner.text, "busy…")
	f.upcoming = api.Upcoming{DueSoon: []api.Task{task(5, "due")}}
	drain(t, a, a.current().load(a))
	drain(t, a, cmd)
	eq(t, f.count("PlaceTask"), 1)
	eq(t, f.mustLast(t, "PlaceTask"), call{Method: "PlaceTask", ID: 2, Mut: client.Mut{IfMatch: "g-2b"}, Req: api.PlaceRequest{How: "keep"}})
	selectedID(t, a, 5)
}

func TestUpcomingNilPayloadRetainsRows(t *testing.T) {
	f := newFake()
	f.upcoming.Deferred = []api.Task{task(7, "retained")}
	a := newTestApp(t, f)
	press(t, a, "4")
	a.Update(upcomingMsg{req: a.nextReq("upcoming")})
	selectedID(t, a, 7)
	if !strings.Contains(viewText(a), "upcoming not loaded") {
		t.Fatal(viewText(a))
	}
}
