package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/client"
)

func cardApp(t *testing.T, f *fakeAPI) *App {
	t.Helper()
	return newTestApp(t, f, func(c *Config) { c.StartMode = ModeTriage })
}

func TestCardPlacementKeys(t *testing.T) { // TRI-10, TRI-11, TRI-12 (TUI side).
	for _, tc := range []struct {
		key, value string
		want       api.PlaceRequest
	}{
		{"k", "", api.PlaceRequest{How: "keep"}},
		{"t", "", api.PlaceRequest{How: "top"}},
		{"b", "", api.PlaceRequest{How: "bottom"}},
		{"p", "before 3", api.PlaceRequest{How: "before", Ref: 3}},
		{"p", "a #3", api.PlaceRequest{How: "after", Ref: 3}},
		{"p", "B #3", api.PlaceRequest{How: "before", Ref: 3}},
		{"p", "AFTER 3", api.PlaceRequest{How: "after", Ref: 3}},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			f := newFake()
			f.tasks["triage"] = []api.Task{task(1, "A", "new"), task(2, "B", "new")}
			a := cardApp(t, f)
			press(t, a, tc.key)
			if tc.value != "" {
				typeText(t, a, tc.value)
				press(t, a, "enter")
			}
			wantMut := client.Mut{IfMatch: "g-1"}
			if tc.value != "" {
				wantMut.Key = freshFormKey(t, f, "PlaceTask")
			}
			eq(t, f.mustLast(t, "PlaceTask"), call{Method: "PlaceTask", ID: 1, Mut: wantMut, Req: tc.want})
			selectedID(t, a, 2)
			eq(t, a.modeScreen.(*cardMode).deck.placed, 1)
		})
	}
	for _, value := range []string{"x", "before 0", "after 03", "before -3", "before #", "before 9223372036854775808", "before 3 extra"} {
		t.Run(value, func(t *testing.T) {
			f := newFake()
			f.tasks["triage"] = []api.Task{task(1, "A", "new")}
			a := cardApp(t, f)
			press(t, a, "p")
			typeText(t, a, value)
			press(t, a, "enter")
			eq(t, f.count("PlaceTask"), 0)
			fm, ok := a.form.(*form)
			if !ok || fm.errText == "" {
				t.Fatal("missing placement form error")
			}
			selectedID(t, a, 1)
		})
	}
}

func TestCardOtherKeys(t *testing.T) { // TRI-13, TRI-14, TRI-15, TRI-16 (TUI side).
	for _, tc := range []struct {
		key, value, method string
		req                any
		blocked            int
	}{
		{"d", "+2d", "DeferTask", api.DeferRequest{Until: api.Opt[api.Moment]{Set: true, V: api.Moment{Date: civil.MustDate("2026-10-30")}}}, 0},
		{"B", "13:00-15:00", "BlockTask", api.BlockRequest{Date: civil.MustDate("2026-10-28"), Start: 780, End: 900}, 1},
		{"x", "", "CompleteTask", api.CompleteRequest{}, 0},
		{"D", "", "DropTask", nil, 0},
	} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fail=%v", tc.key, fail), func(t *testing.T) {
				f := newFake()
				f.tasks["triage"] = []api.Task{task(1, "A", "new"), task(2, "B", "new")}
				a := cardApp(t, f)
				if fail {
					f.errs[tc.method] = problem("conflict", 409, "x")
				}
				_, cmd := a.Update(key(tc.key))
				if tc.value != "" {
					drain(t, a, cmd)
					typeText(t, a, tc.value)
					_, cmd = a.Update(key("enter"))
				}
				selectedID(t, a, 1)
				eq(t, f.count(tc.method), 0)
				drain(t, a, cmd)
				wantMut := client.Mut{IfMatch: "g-1"}
				if tc.value != "" {
					wantMut.Key = freshFormKey(t, f, tc.method)
				}
				eq(t, f.mustLast(t, tc.method), call{Method: tc.method, ID: 1, Mut: wantMut, Req: tc.req})
				deck := a.modeScreen.(*cardMode).deck
				if fail {
					selectedID(t, a, 1)
					eq(t, deck.placed, 0)
					eq(t, deck.blocked, 0)
				} else {
					selectedID(t, a, 2)
					eq(t, deck.placed, 1)
					eq(t, deck.blocked, tc.blocked)
				}
			})
		}
	}
}

func TestTRI17_CardSkipSendsNothing(t *testing.T) {
	f := newFake()
	f.tasks["triage"] = []api.Task{task(1, "A", "new"), task(2, "B", "new")}
	a := cardApp(t, f)
	before := len(f.calls)
	press(t, a, "s")
	eq(t, len(f.calls), before)
	selectedID(t, a, 2)
	press(t, a, "k")
	eq(t, f.count("PlaceTask"), 1)
	eq(t, f.mustLast(t, "PlaceTask").ID, int64(2))
	if !strings.Contains(viewText(a), "Triage clear (1 skipped)") {
		t.Fatal(viewText(a))
	}
	// The fake deliberately retains both tasks, including skipped #1.
	drain(t, a, a.current().load(a))
	_, _, ok := a.current().selection()
	eq(t, ok, false)
	eq(t, f.tasks["triage"][0], task(1, "A", "new"))
}

func TestCardAcceptNew(t *testing.T) { // TRI-18 (TUI side).
	for _, answer := range []string{"y", "n"} {
		t.Run(answer, func(t *testing.T) {
			f := newFake()
			f.tasks["triage"] = []api.Task{task(1, "A", "new"), task(2, "B", "new"), task(3, "C", "new", "slipped"), task(4, "D", "surfaced")}
			a := cardApp(t, f)
			press(t, a, "A")
			if !strings.Contains(viewText(a), "accept 2 new → bottom? [y/N]") {
				t.Fatal(viewText(a))
			}
			eq(t, f.count("AcceptNew"), 0)
			press(t, a, answer)
			if answer == "n" {
				eq(t, f.count("AcceptNew"), 0)
				selectedID(t, a, 1)
				return
			}
			got := f.mustLast(t, "AcceptNew")
			eq(t, got.Mut, client.Mut{})
			r := got.Req.(api.AcceptNewRequest)
			eq(t, len(r.Items), 2)
			for i, item := range r.Items {
				eq(t, item.ID, int64(i+1))
				eq(t, item.ETag, fmt.Sprintf("g-%d", i+1))
			}
			eq(t, a.modeScreen.(*cardMode).deck.placed, 2)
			selectedID(t, a, 3)
		})
	}
	f := newFake()
	f.tasks["triage"] = []api.Task{task(3, "C", "new", "slipped"), task(4, "D", "surfaced")}
	a := cardApp(t, f)
	press(t, a, "A")
	eq(t, a.banner.text, "no new-only tasks")
	eq(t, a.form, nil)
	eq(t, f.count("AcceptNew"), 0)
}

func TestCardAcceptNewIncludesUnplacedCardsInServerOrder(t *testing.T) {
	f := newFake()
	f.tasks["triage"] = []api.Task{task(9, "first", "new"), task(2, "second", "new"), task(7, "third", "new")}
	a := newTestApp(t, f, func(c *Config) { c.StartTab = TabTriage })
	press(t, a, "j", "enter", "k") // Start on #2 and place it; #9 has not been shown.
	press(t, a, "s")               // Skip #7; the bulk action still accepts all new-only tasks.
	press(t, a, "A", "y")
	r := f.mustLast(t, "AcceptNew").Req.(api.AcceptNewRequest)
	eq(t, len(r.Items), 2)
	eq(t, r.Items[0].ID, int64(9))
	eq(t, r.Items[1].ID, int64(7))
	eq(t, a.modeScreen.(*cardMode).deck.placed, 3)
}

func TestCardAcceptNewFailureAndSnapshot(t *testing.T) {
	f := newFake()
	f.tasks["triage"] = []api.Task{task(1, "A", "new"), task(2, "B", "new")}
	a := cardApp(t, f)
	press(t, a, "A")
	f.tasks["triage"] = []api.Task{task(3, "arrived later", "new")}
	drain(t, a, a.current().load(a))
	f.errs["AcceptNew"] = problem("conflict", 409, "changed")
	press(t, a, "y")
	r := f.mustLast(t, "AcceptNew").Req.(api.AcceptNewRequest)
	eq(t, len(r.Items), 2)
	eq(t, r.Items[0].ID, int64(1))
	eq(t, r.Items[1].ID, int64(2))
	selectedID(t, a, 1)
	eq(t, a.modeScreen.(*cardMode).deck.placed, 0)
	eq(t, f.count("AcceptNew"), 1)
}

func TestCardAcceptNewRefreshesSkippedStaleItem(t *testing.T) {
	f := newFake()
	f.tasks["triage"] = []api.Task{task(1, "A", "new"), task(2, "B", "new")}
	a := cardApp(t, f)
	press(t, a, "s", "A")
	fresh := task(1, "A", "new", "overdue")
	fresh.ETag = "g-1b"
	b, err := json.Marshal(fresh)
	if err != nil {
		t.Fatal(err)
	}
	f.errs["AcceptNew"] = &client.ProblemError{Status: 412, Problem: api.Problem{Code: "precondition_failed", Current: b}}
	press(t, a, "y")
	eq(t, f.count("AcceptNew"), 1)
	eq(t, a.modeScreen.(*cardMode).deck.placed, 0)
	press(t, a, "A", "y")
	r := f.mustLast(t, "AcceptNew").Req.(api.AcceptNewRequest)
	eq(t, len(r.Items), 1)
	eq(t, r.Items[0].ID, int64(2))
}

func TestCardFormsKeepRetrySnapshot(t *testing.T) {
	for _, tc := range []struct{ key, value, method string }{{"p", "before 3", "PlaceTask"}, {"d", "+2d", "DeferTask"}, {"B", "13:00-15:00", "BlockTask"}} {
		t.Run(tc.key, func(t *testing.T) {
			f := newFake()
			f.tasks["triage"] = []api.Task{task(1, "A", "new"), task(2, "B", "new")}
			a := cardApp(t, f)
			press(t, a, tc.key)
			typeText(t, a, tc.value)
			f.errs[tc.method] = outcomeUnknown("card-retry")
			press(t, a, "enter")
			first := f.mustLast(t, tc.method)
			selectedID(t, a, 1)
			eq(t, a.modeScreen.(*cardMode).deck.placed, 0)
			eq(t, a.form.(*form).input.Value(), tc.value)
			f.clock.Today = f.clock.Today.AddDays(1)
			f.today.Clock = f.clock
			drain(t, a, a.refresh())
			press(t, a, "enter")
			first.Mut.Key = "card-retry"
			eq(t, f.mustLast(t, tc.method), first)
			selectedID(t, a, 2)
			eq(t, a.modeScreen.(*cardMode).deck.placed, 1)
		})
	}
}

func TestCardLoadsIgnoreStaleReplies(t *testing.T) {
	f := newFake()
	f.tasks["triage"] = []api.Task{task(1, "A", "new")}
	a := cardApp(t, f)
	old, latest := a.current().load(a), a.current().load(a)
	fresh := task(1, "fresh", "new", "overdue")
	fresh.ETag = "g-1b"
	f.tasks["triage"] = []api.Task{fresh}
	drain(t, a, latest)
	f.tasks["triage"] = []api.Task{task(1, "old", "new")}
	f.errs["ListTasks"] = notSent()
	drain(t, a, old)
	eq(t, a.banner.text, "")
	press(t, a, "k")
	eq(t, f.mustLast(t, "PlaceTask").Mut.IfMatch, "g-1b")
}

func TestCardStale412(t *testing.T) { // TRI-19 (TUI side).
	f := newFake()
	f.tasks["triage"] = []api.Task{task(1, "A", "new")}
	a := cardApp(t, f)
	fresh := task(1, "A", "new", "overdue")
	fresh.ETag = "g-1b"
	b, err := json.Marshal(fresh)
	if err != nil {
		t.Fatal(err)
	}
	f.errs["PlaceTask"] = &client.ProblemError{Status: 412, Problem: api.Problem{Code: "precondition_failed", Current: b}}
	press(t, a, "k")
	eq(t, a.banner.text, "changed elsewhere — refreshed")
	if !strings.Contains(viewText(a), "overdue") {
		t.Fatal(viewText(a))
	}
	eq(t, f.count("PlaceTask"), 1)
	eq(t, a.modeScreen.(*cardMode).deck.placed, 0)
	press(t, a, "k")
	eq(t, f.mustLast(t, "PlaceTask").Mut.IfMatch, "g-1b")
}

func TestCardDoubleKeySendsOnce(t *testing.T) { // Review Focus 2.
	f := newFake()
	f.tasks["triage"] = []api.Task{task(1, "A", "new"), task(2, "B", "new")}
	a := cardApp(t, f)
	_, c1 := a.Update(key("k"))
	_, c2 := a.Update(key("k"))
	eq(t, a.banner.text, "busy…")
	press(t, a, "s", "A", "p", "d", "B")
	selectedID(t, a, 1)
	eq(t, a.form, nil)
	drain(t, a, c1)
	drain(t, a, c2)
	eq(t, f.count("PlaceTask"), 1)
	eq(t, f.mustLast(t, "PlaceTask").ID, int64(1))
	selectedID(t, a, 2)
}

func TestCardShowsSlips(t *testing.T) { // SLP-12 (TUI side).
	f := newFake()
	v := task(1, "A", "slipped", "new")
	v.Slips = api.Slips{Lifetime: 2, SinceTriage: 1}
	f.tasks["triage"] = []api.Task{v}
	a := cardApp(t, f)
	if !strings.Contains(viewText(a), "slipped ×2 lifetime, ×1 since last triage") {
		t.Fatal(viewText(a))
	}
	eq(t, slipText(api.Slips{}), "")
}

func TestCardExit(t *testing.T) {
	f := newFake()
	f.tasks["triage"] = []api.Task{task(1, "A", "new"), task(2, "B", "new"), task(3, "C", "new")}
	a := cardApp(t, f)
	press(t, a, "q")
	eq(t, a.quitting, true)
	a = newTestApp(t, f, func(c *Config) { c.StartTab = TabTriage })
	press(t, a, "j", "enter")
	eq(t, a.mode, ModeTriage)
	selectedID(t, a, 2)
	press(t, a, "up", "down", "j")
	eq(t, f.count("PlaceTask"), 0)
	selectedID(t, a, 2)
	press(t, a, "esc")
	eq(t, a.mode, ModeTabs)
	eq(t, a.tab, TabTriage)
	eq(t, a.quitting, false)
}

func TestCardRefillAndReadErrors(t *testing.T) {
	f := newFake()
	f.tasks["triage"] = []api.Task{task(1, "A", "new")}
	a := cardApp(t, f)
	f.tasks["triage"] = append(f.tasks["triage"], task(3, "C", "slipped"), task(2, "B", "new"))
	press(t, a, "s")
	selectedID(t, a, 3)
	press(t, a, "s")
	selectedID(t, a, 2)
	for _, tc := range []struct {
		err  error
		want string
	}{{notSent(), "cannot reach the Taskronin server"}, {outcomeUnknown(""), "server did not answer"}} {
		f.errs["ListTasks"] = tc.err
		before := f.count("ListTasks")
		drain(t, a, a.current().load(a))
		eq(t, f.count("ListTasks"), before+1)
		eq(t, a.banner.text, tc.want)
		selectedID(t, a, 2)
	}
}

func TestCardStaleFallbackAndErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name    string
		current json.RawMessage
		wrap    func(error) error
		banner  string
	}{
		{"missing", nil, nil, "changed elsewhere — refreshed"},
		{"malformed", json.RawMessage(`{"id":`), nil, "changed elsewhere — refreshed"},
		{"null", json.RawMessage(`null`), nil, "changed elsewhere — refreshed"},
		{"unknown", json.RawMessage(`{"id":1,"etag":"wrong","title":"wrong"}`), func(e error) error { return &client.OutcomeUnknownError{Key: "K1", Err: e} }, "outcome unknown — refreshed; check before repeating"},
		{"not sent", json.RawMessage(`{"id":1,"etag":"wrong","title":"wrong"}`), func(e error) error { return errors.Join(client.ErrNotSent, e) }, "cannot reach the Taskronin server — nothing was saved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.tasks["triage"] = []api.Task{task(1, "A", "new")}
			a := cardApp(t, f)
			fresh := task(1, "fresh", "new", "overdue")
			fresh.ETag = "g-1b"
			f.tasks["triage"] = []api.Task{fresh}
			var err error = &client.ProblemError{Status: 412, Problem: api.Problem{Code: "precondition_failed", Current: tc.current}}
			if tc.wrap != nil {
				err = tc.wrap(err)
			}
			f.errs["PlaceTask"] = err
			press(t, a, "k")
			eq(t, a.banner.text, tc.banner)
			eq(t, f.count("PlaceTask"), 1)
			current, _ := a.modeScreen.(*cardMode).deck.current()
			want := fresh
			if tc.name == "not sent" {
				want = task(1, "A", "new")
			}
			eq(t, current, want)
		})
	}
}

func TestCardRender(t *testing.T) {
	f := newFake()
	v := task(1, "Title", "slipped", "new")
	v.Due = &api.Moment{Date: civil.MustDate("2026-10-29"), Time: ptr(civil.Minute(780))}
	v.Defer = &api.Moment{Date: civil.MustDate("2026-10-30")}
	v.Project, v.Priority, v.Tags = ptr("infra"), ptr("high"), []string{"db", "ops"}
	v.Notes = "line1\nline2\nline3\nline4\nline5\nHIDDEN"
	f.tasks["triage"] = []api.Task{v}
	a := cardApp(t, f)
	a.Update(tea.WindowSizeMsg{Width: 160, Height: 30})
	out := viewText(a)
	for _, want := range []string{"Card 1/1", "#1 Title", "slipped since 2026-10-28", "new since 2026-10-28", "Due: 2026-10-29 13:00", "Defer: 2026-10-30", "infra", "high", "db", "ops", "line5", "k keep · t top · b bottom · p place · d defer · B block · x done · D drop · s skip · A accept new"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "HIDDEN") || strings.Index(out, "slipped since") > strings.Index(out, "new since") {
		t.Fatal(out)
	}
	for _, color := range []bool{false, true} {
		v.Title = "bad\x1b\u009b\u202e" + strings.Repeat("宽", 100)
		v.Notes, v.Tags, v.Project, v.Priority = strings.Repeat("\t"+v.Title+"\n", 6), []string{v.Title}, &v.Title, &v.Title
		v.Reasons[0].Reason = v.Title
		f.tasks["triage"] = []api.Task{v}
		a = newTestApp(t, f, func(c *Config) { c.StartMode, c.Color = ModeTriage, color })
		for _, size := range []tea.WindowSizeMsg{{Width: 100, Height: 30}, {Width: 20, Height: 5}, {Width: 1, Height: 1}, {}} {
			a.Update(size)
			out = viewText(a)
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
		}
	}
}

func TestCardEmptyStateWaitsForSuccessfulLoad(t *testing.T) { // M1.
	t.Run("initial pending", func(t *testing.T) {
		f := newFake()
		a := NewApp(Config{API: f, Ctx: t.Context(), Clock: f.clock, StartMode: ModeTriage, PollInterval: -1})
		a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		cmd := a.Init()
		if out := viewText(a); !strings.Contains(out, "loading…") || strings.Contains(out, "Triage clear") {
			t.Fatalf("pending initial load: %s", out)
		}
		drain(t, a, cmd)
		if !strings.Contains(viewText(a), "Triage clear (0 skipped)") {
			t.Fatal(viewText(a))
		}
	})
	t.Run("end reload pending", func(t *testing.T) {
		f := newFake()
		f.tasks["triage"] = []api.Task{task(1, "A", "new")}
		a := cardApp(t, f)
		_, cmd := a.Update(key("s"))
		if out := viewText(a); !strings.Contains(out, "loading…") || strings.Contains(out, "Triage clear") {
			t.Fatalf("pending end reload: %s", out)
		}
		drain(t, a, cmd)
		if !strings.Contains(viewText(a), "Triage clear (1 skipped)") {
			t.Fatal(viewText(a))
		}
	})
	t.Run("initial failure then recovery", func(t *testing.T) {
		f := newFake()
		f.errs["ListTasks"] = notSent()
		a := cardApp(t, f)
		eq(t, a.banner.text, "cannot reach the Taskronin server")
		if out := viewText(a); !strings.Contains(out, "triage not loaded") || strings.Contains(out, "Triage clear") {
			t.Fatalf("failed initial load: %s", out)
		}
		cmd := a.current().load(a)
		if !strings.Contains(viewText(a), "loading…") {
			t.Fatal(viewText(a))
		}
		drain(t, a, cmd)
		if !strings.Contains(viewText(a), "Triage clear (0 skipped)") {
			t.Fatal(viewText(a))
		}
	})
}

func TestCardCurrentAndRenderingArePure(t *testing.T) { // M2.
	a := newTestApp(t, newFake())
	d := &cardDeck{queue: []api.Task{task(1, "A", "new")}}
	s := &cardMode{deck: d}
	for _, action := range []struct {
		name string
		run  func()
	}{
		{"current", func() { d.current() }},
		{"selection", func() { s.selection() }},
		{"view", func() { s.view(a, 100, 30) }},
	} {
		t.Run(action.name, func(t *testing.T) {
			d.seen = nil
			action.run()
			eq(t, d.seen, map[int64]bool(nil))
		})
	}
}

func TestCardTransitionsMarkOnlyPresentedTasks(t *testing.T) { // M2.
	d := &cardDeck{}
	d.refill([]api.Task{task(1, "A", "new"), task(2, "B", "new")})
	eq(t, d.seen, map[int64]bool{1: true})
	d.advance()
	eq(t, d.seen, map[int64]bool{1: true, 2: true})
	d.advance()
	d.refill([]api.Task{task(1, "A", "new"), task(2, "B", "new"), task(3, "C", "new")})
	eq(t, d.seen, map[int64]bool{1: true, 2: true, 3: true})
	fresh := task(3, "fresh", "new", "overdue")
	d.replace(fresh)
	seen := maps.Clone(d.seen)
	eq(t, seen, map[int64]bool{1: true, 2: true, 3: true})
	got, ok := d.current()
	eq(t, ok, true)
	eq(t, got, fresh)
	eq(t, d.seen, seen)
}

func TestCardRefillNeverDuplicatesQueuedIDs(t *testing.T) { // M3.
	d := &cardDeck{queue: []api.Task{task(1, "A", "new"), task(2, "B", "new"), task(3, "C", "new")}, idx: 1}
	d.refill([]api.Task{task(1, "A", "new"), task(2, "B", "new"), task(3, "C", "new"), task(4, "D", "new")})
	var ids []int64
	for _, v := range d.queue {
		ids = append(ids, v.ID)
	}
	eq(t, ids, []int64{1, 2, 3, 4})
}

func TestCardSelectedStartWrapsOnceWithDistinctTotals(t *testing.T) { // M2/M3.
	f := newFake()
	f.tasks["triage"] = []api.Task{task(1, "A", "new"), task(2, "B", "new"), task(3, "C", "new")}
	a := newTestApp(t, f, func(c *Config) { c.StartTab = TabTriage })
	press(t, a, "j", "enter")
	d := a.modeScreen.(*cardMode).deck
	if !d.seen[2] || d.seen[1] || d.seen[3] {
		t.Errorf("start should present only #2: seen=%v", d.seen)
	}
	for i, id := range []int64{2, 3, 1} {
		selectedID(t, a, id)
		want := fmt.Sprintf("Card %d/3", i+1)
		if out := viewText(a); !strings.Contains(out, want) {
			t.Errorf("missing %q: %s", want, out)
		}
		press(t, a, "s")
	}
	eq(t, len(d.queue), 3)
	eq(t, d.skipped, 3)
	drain(t, a, a.current().load(a))
	_, _, ok := a.current().selection()
	eq(t, ok, false)
	if !strings.Contains(viewText(a), "Triage clear (3 skipped)") {
		t.Fatal(viewText(a))
	}
}

func TestCardSmallHeightKeepsLegend(t *testing.T) { // M4.
	f := newFake()
	v := task(1, "A", "new", "overdue")
	v.Notes = strings.Repeat("note\n", 10)
	f.tasks["triage"] = []api.Task{v}
	a := cardApp(t, f)
	for _, height := range []int{1, 3, 5} {
		out := a.current().view(a, 160, height)
		if !strings.Contains(out, cardLegend) {
			t.Errorf("height %d lost legend: %s", height, out)
		}
		if strings.Count(out, "\n") >= height {
			t.Errorf("height %d overflow: %s", height, out)
		}
	}
}

func TestCardOmitsEmptyTags(t *testing.T) { // M4.
	f := newFake()
	f.tasks["triage"] = []api.Task{task(1, "A", "new")}
	a := cardApp(t, f)
	if strings.Contains(viewText(a), "Tags:") {
		t.Fatal("empty tags should not occupy a card line")
	}
}

func TestCardFormStale412UsesFreshETagOnNextAttempt(t *testing.T) { // M6.
	for _, tc := range []struct{ key, text, method string }{{"p", "before 3", "PlaceTask"}, {"d", "+2d", "DeferTask"}, {"B", "13:00-15:00", "BlockTask"}} {
		t.Run(tc.key, func(t *testing.T) {
			f := newFake()
			f.tasks["triage"] = []api.Task{task(1, "A", "new"), task(2, "B", "new")}
			a := cardApp(t, f)
			fresh := task(1, "fresh", "new", "overdue")
			fresh.ETag = "g-1b"
			b, err := json.Marshal(fresh)
			if err != nil {
				t.Fatal(err)
			}
			press(t, a, tc.key)
			typeText(t, a, tc.text)
			f.errs[tc.method] = &client.ProblemError{Status: 412, Problem: api.Problem{Code: "precondition_failed", Current: b}}
			press(t, a, "enter")
			eq(t, f.count(tc.method), 1)
			eq(t, a.banner.text, "changed elsewhere — refreshed")
			selectedID(t, a, 1)
			d := a.modeScreen.(*cardMode).deck
			eq(t, d.placed, 0)
			current, _ := d.current()
			eq(t, current.ID, fresh.ID)
			eq(t, current.Title, fresh.Title)
			eq(t, current.ETag, fresh.ETag)
			eq(t, current.Reasons[1].Reason, "overdue")
			if a.form == nil { // A stale form may close to require explicit review.
				press(t, a, tc.key)
				typeText(t, a, tc.text)
			}
			press(t, a, "enter")
			eq(t, f.count(tc.method), 2)
			eq(t, f.mustLast(t, tc.method).Mut.IfMatch, "g-1b")
			selectedID(t, a, 2)
			eq(t, d.placed, 1)
		})
	}
}

func TestCardBulkAcceptanceDoesNotMarkUnpresentedTasksSeen(t *testing.T) { // M2.
	f := newFake()
	f.tasks["triage"] = []api.Task{task(1, "A", "new"), task(2, "B", "new"), task(3, "C", "overdue")}
	a := cardApp(t, f)
	press(t, a, "A", "y")
	d := a.modeScreen.(*cardMode).deck
	selectedID(t, a, 3)
	eq(t, d.seen, map[int64]bool{1: true, 3: true})
	eq(t, len(d.queue), 3)
	press(t, a, "s")
	if !strings.Contains(viewText(a), "Triage clear (1 skipped)") {
		t.Fatal(viewText(a))
	}
}

func TestCardEmptyDeckStaleBulkReplyCompletesReload(t *testing.T) { // M1/M6.
	f := newFake()
	f.tasks["triage"] = []api.Task{task(1, "A", "new")}
	a := cardApp(t, f)
	press(t, a, "s", "A")
	fresh := task(1, "A", "new", "overdue")
	fresh.ETag = "g-1b"
	b, err := json.Marshal(fresh)
	if err != nil {
		t.Fatal(err)
	}
	f.tasks["triage"] = []api.Task{fresh}
	f.errs["AcceptNew"] = &client.ProblemError{Status: 412, Problem: api.Problem{Code: "precondition_failed", Current: b}}
	press(t, a, "y")
	eq(t, f.count("AcceptNew"), 1)
	eq(t, a.banner.text, "changed elsewhere — refreshed")
	if !strings.Contains(viewText(a), "Triage clear (1 skipped)") {
		t.Fatal("the successful end-of-deck reload must finish: " + viewText(a))
	}
}
