package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/client"
)

func searchFor(t *testing.T, a *App, q string) {
	t.Helper()
	press(t, a, "/")
	if a.form == nil {
		t.Fatal("search did not open a query form")
	}
	typeText(t, a, q)
	press(t, a, "enter")
}

func TestSearchToDetail(t *testing.T) {
	f := newFake()
	f.hits = []api.SearchHit{
		{Kind: "task", ID: 42, Title: "keycloak\x1b", Snippet: "setup\x1b\u009b\u202e"},
		{Kind: "entry", ID: 9, Title: "linked work", WorkDate: &f.clock.Today, TaskID: ptr(int64(60))},
		{Kind: "entry", ID: 10, Title: "standalone", WorkDate: &f.clock.Today},
	}
	f.details[42] = api.TaskDetail{Task: task(42, "keycloak detail")}
	a := newTestApp(t, f)
	searchFor(t, a, "keycloak")
	eq(t, f.mustLast(t, "Search").Q, client.SearchQuery{Q: "keycloak", Kind: "all", PageQuery: client.PageQuery{Limit: 50}})
	eq(t, a.form, nil)
	eq(t, f.count("Today"), 1)
	eq(t, f.count("Generation"), 0) // Search is a read, not a form mutation.
	v := viewText(a)
	for _, text := range []string{"task #42 keycloak", " — setup", "entry e9 Wed 10-28 linked work"} {
		if !strings.Contains(v, text) {
			t.Fatalf("missing %q in %s", text, v)
		}
	}
	if strings.ContainsAny(v, "\x1b\u009b\u202e") {
		t.Fatalf("unsafe search text %q", v)
	}
	results := a.current()
	press(t, a, "enter")
	eq(t, f.mustLast(t, "GetTask").ID, int64(42))
	press(t, a, "esc")
	eq(t, a.current() == results, true)
	eq(t, f.count("Search"), 1)
	press(t, a, "down", "enter")
	eq(t, f.mustLast(t, "GetTask").ID, int64(60))
	press(t, a, "esc", "j", "enter")
	eq(t, f.count("GetTask"), 2)
	eq(t, a.banner.text, "standalone entry e10")
	eq(t, a.current() == results, true)
}

func TestSearchRejectsBlankQuery(t *testing.T) {
	for _, q := range []string{"", "   ", "\u2003"} {
		t.Run(q, func(t *testing.T) {
			f := newFake()
			a := newTestApp(t, f)
			searchFor(t, a, q)
			eq(t, f.count("Search"), 0)
			eq(t, a.inflight, false)
			if a.form == nil || !strings.Contains(viewText(a), "query required") {
				t.Fatal("missing query form error", viewText(a))
			}
			press(t, a, "esc")
			eq(t, len(a.overlays), 0)
		})
	}
}

func TestSearchLoadsAndPolls(t *testing.T) {
	f := newFake()
	f.hits = []api.SearchHit{{Kind: "task", ID: 42, Title: "original"}}
	a := newTestApp(t, f)
	searchFor(t, a, "first")
	s := a.current()
	old, latest := s.load(a), s.load(a)
	f.hits = []api.SearchHit{{Kind: "task", ID: 43, Title: "fresh"}}
	drain(t, a, latest)
	other := newFake()
	a.api = other
	f.errs["Search"] = outcomeUnknown("")
	drain(t, a, old)
	eq(t, other.count("Search"), 0)
	eq(t, a.banner.text, "")
	if !strings.Contains(viewText(a), "fresh") {
		t.Fatal("stale reply replaced results")
	}
	a.api = f
	for _, tc := range []struct {
		err    error
		banner string
	}{
		{notSent(), "cannot reach the Taskronin server"},
		{outcomeUnknown(""), "server did not answer"},
	} {
		f.errs["Search"] = tc.err
		before := f.count("Search")
		drain(t, a, s.load(a))
		eq(t, f.count("Search"), before+1)
		eq(t, a.banner.text, tc.banner)
		eq(t, a.banner.readError, true)
		eq(t, f.count("Today"), 1)
	}
	before := f.count("Search")
	f.setToday(api.Today{Clock: f.clock, TriageCount: 1}, "changed")
	drain(t, a, func() tea.Msg { return pollMsg{} })
	eq(t, f.count("Search"), before) // CF02: even changed polls leave results alone.
	searchFor(t, a, "second")
	eq(t, f.mustLast(t, "Search").Q.(client.SearchQuery).Q, "second")
}

func TestSearchEmptyAndClipped(t *testing.T) {
	f := newFake()
	a := newTestApp(t, f)
	searchFor(t, a, "missing")
	press(t, a, "j", "k", "enter")
	eq(t, f.count("GetTask"), 0)
	if !strings.Contains(viewText(a), "no results") {
		t.Fatal(viewText(a))
	}
	f.hits = []api.SearchHit{{Kind: "entry", ID: 9, Title: "bad\x1b\u009b\u202e" + strings.Repeat("宽", 100)}}
	drain(t, a, a.current().load(a))
	for _, size := range []tea.WindowSizeMsg{{Width: 20, Height: 5}, {Width: 1, Height: 1}, {}} {
		a.Update(size)
		v := viewText(a)
		if strings.ContainsAny(v, "\x1b\u009b\u202e") {
			t.Fatalf("unsafe output %q", v)
		}
		for _, line := range strings.Split(v, "\n") {
			if lipgloss.Width(line) > size.Width {
				t.Fatalf("oversized line %q", line)
			}
		}
	}
}

func TestSearchSelectionSurvivesReloadWithOverlappingIDs(t *testing.T) {
	f := newFake()
	f.hits = []api.SearchHit{{Kind: "task", ID: 9, Title: "task"}, {Kind: "entry", ID: 9, Title: "entry"}}
	a := newTestApp(t, f)
	searchFor(t, a, "query")
	press(t, a, "j")
	f.hits[0], f.hits[1] = f.hits[1], f.hits[0]
	drain(t, a, a.current().load(a))
	press(t, a, "enter")
	eq(t, a.banner.text, "standalone entry e9")
	eq(t, f.count("GetTask"), 0)
}

func TestSearchGlobalInModesAndFocusedQuery(t *testing.T) {
	for _, mode := range []Mode{ModeTriage, ModePlan, ModeRecap} {
		f := newFake()
		a := newTestApp(t, f, func(c *Config) { c.StartMode = mode })
		searchFor(t, a, "uA/")
		eq(t, f.mustLast(t, "Search").Q.(client.SearchQuery).Q, "uA/")
		eq(t, f.count("Undo")+f.count("AIChanges")+f.count("AcceptNew"), 0)
		eq(t, a.mode, mode)
		press(t, a, "esc")
		eq(t, a.current() == a.modeScreen, true)
	}
}

func TestSearchStackedLoadsRemainIndependent(t *testing.T) { // M1.
	for _, lowerFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(lowerFirst), func(t *testing.T) {
			f := newFake()
			f.hits = []api.SearchHit{{Kind: "task", ID: 42, Title: "first result"}}
			a := newTestApp(t, f)
			press(t, a, "/")
			typeText(t, a, "first query")
			_, firstLoad := a.Update(key("enter"))
			first := a.current()
			firstReply := firstLoad()
			press(t, a, "/")
			typeText(t, a, "second query")
			_, secondLoad := a.Update(key("enter"))
			f.hits = []api.SearchHit{{Kind: "task", ID: 60, Title: "second result"}}
			secondReply := secondLoad()
			replies := []tea.Msg{secondReply, firstReply}
			if lowerFirst {
				replies = []tea.Msg{firstReply, secondReply}
			}
			for _, reply := range replies {
				drain(t, a, func() tea.Msg { return reply })
			}
			if !strings.Contains(viewText(a), "second result") {
				t.Fatal("upper search lost its reply")
			}
			press(t, a, "esc")
			eq(t, a.current() == first, true)
			if !strings.Contains(viewText(a), "first result") || strings.Contains(viewText(a), "second result") {
				t.Fatal("lower search lost its reply", viewText(a))
			}
			press(t, a, "enter")
			eq(t, f.mustLast(t, "GetTask").ID, int64(42))
		})
	}
}

func TestSearchRevealedAfterOwnOpReloadsQuery(t *testing.T) { // M2.
	for _, k := range []string{"u", "ctrl+r", "R"} {
		t.Run(k, func(t *testing.T) {
			f := newFake()
			f.hits = []api.SearchHit{{Kind: "task", ID: 42, Title: "old result"}}
			f.aiPages = []api.AIChanges{{Ops: []api.OpSummary{aiOp(12)}}}
			a := newTestApp(t, f)
			searchFor(t, a, "original query")
			// Exercise hidden-screen invalidation independently of the A key guard.
			drain(t, a, a.openOverlay(newAIOverlay()))
			f.hits = []api.SearchHit{{Kind: "task", ID: 60, Title: "fresh result"}}
			press(t, a, k)
			eq(t, f.count("Search"), 1)
			press(t, a, "esc")
			eq(t, f.count("Search"), 2)
			eq(t, f.mustLast(t, "Search").Q, client.SearchQuery{Q: "original query", Kind: "all", PageQuery: client.PageQuery{Limit: 50}})
			if !strings.Contains(viewText(a), "fresh result") || strings.Contains(viewText(a), "old result") {
				t.Fatal("revealed stale search rows", viewText(a))
			}
		})
	}
}

func TestSearchOmitsEmptySnippetSeparator(t *testing.T) { // M6.
	f := newFake()
	f.hits = []api.SearchHit{{Kind: "task", ID: 42, Title: "no snippet"}}
	a := newTestApp(t, f)
	searchFor(t, a, "query")
	v := viewText(a)
	if !strings.Contains(v, "task #42 no snippet") || strings.Contains(v, " — ") {
		t.Fatal("empty snippet has a dangling separator", v)
	}
}

func TestSearchFailedLoadCanRetry(t *testing.T) { // M6.
	for _, loaded := range []bool{false, true} {
		t.Run(fmt.Sprint(loaded), func(t *testing.T) {
			f := newFake()
			f.hits = []api.SearchHit{{Kind: "task", ID: 42, Title: "result"}}
			a := newTestApp(t, f)
			if !loaded {
				f.errs["Search"] = notSent()
			}
			searchFor(t, a, "retry this query")
			if loaded {
				f.errs["Search"] = notSent()
				drain(t, a, a.current().load(a))
			}
			if !strings.Contains(viewText(a), "r retries") {
				t.Fatal("missing search retry hint", viewText(a))
			}
			if loaded && (!strings.Contains(viewText(a), "previous results") || !strings.Contains(viewText(a), "task #42 result")) {
				t.Fatal("failed reload must label retained search results", viewText(a))
			}
			before := f.count("Search")
			_, retry := a.Update(key("r"))
			press(t, a, "r")
			eq(t, f.count("Search"), before)
			drain(t, a, retry)
			eq(t, f.count("Search"), before+1)
			eq(t, f.mustLast(t, "Search").Q.(client.SearchQuery).Q, "retry this query")
			if !strings.Contains(viewText(a), "task #42 result") {
				t.Fatal("retry did not load results")
			}
			eq(t, f.count("Today"), 1)
		})
	}
}
