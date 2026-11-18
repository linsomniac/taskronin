package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/client"
)

func TestListSelectionFollowsID(t *testing.T) { // Review Focus 1.
	l := &taskList{}
	l.set([]api.Task{task(1, "a"), task(2, "b"), task(3, "c")})
	l.move(1)
	l.set([]api.Task{task(4, "d"), task(1, "a"), task(2, "b")})
	got, ok := l.selected()
	eq(t, ok, true)
	eq(t, got.ID, int64(2))
	l.set([]api.Task{task(4, "d"), task(1, "a")})
	got, _ = l.selected()
	eq(t, got.ID, int64(1))
	eq(t, l.selID, int64(1))
	l.set([]api.Task{task(1, "a"), task(4, "d")})
	got, _ = l.selected()
	eq(t, got.ID, int64(1)) // The fallback row becomes the selection identity.
	l.set(nil)
	if _, ok := l.selected(); ok {
		t.Fatal("empty list has no selection")
	}
	l.move(1)
	l.move(-1)
	l.set([]api.Task{task(3, "c")})
	got, _ = l.selected()
	eq(t, got.ID, int64(3))
}

func TestListScrollAndClamp(t *testing.T) {
	a := newTestApp(t, newFake())
	l := &taskList{}
	for i := range 10 {
		l.items = append(l.items, task(int64(i+1), "row"))
	}
	l.set(l.items)
	row := func(v api.Task, w int) string { return taskRow(a, v, w) }
	l.move(100)
	eq(t, l.cursor, 9)
	out := l.render(a, 20, 3, row)
	if !strings.Contains(out, "› #10 row") || strings.Count(out, "\n") > 2 {
		t.Fatal(out)
	}
	l.move(-1)
	if !strings.Contains(l.render(a, 20, 1, row), "› #9 row") {
		t.Fatal("shrinking the viewport hid the selection")
	}
	l.move(-100)
	eq(t, l.cursor, 0)
	if !strings.Contains(l.render(a, 20, 3, row), "› #1 row") {
		t.Fatal("upward scrolling hid the selection")
	}
	eq(t, l.render(a, 0, 3, row), "")
	eq(t, l.render(a, 20, 0, row), "")
	l.set(nil)
	eq(t, l.render(a, 20, 3, row), "")
}

func TestListTaskRow(t *testing.T) {
	a := newTestApp(t, newFake())
	v := task(42, "Title", "slipped", "due")
	v.Slips.Lifetime = 2
	v.Project, v.Tags = ptr("infra.keycloak"), []string{"db"}
	v.Due = &api.Moment{Date: civil.MustDate("2026-10-30")}
	eq(t, taskRow(a, v, 100), "#42 [slipped×2,due] Title  infra.keycloak +db  due Fri 10-30")
	v.DuePhase = "overdue"
	if !strings.Contains(taskRow(a, v, 100), "overdue") {
		t.Fatal("missing overdue marker")
	}
	v.Title, v.Project, v.Tags = "bad\x1b\u009b\u202e宽", ptr("p\x1b"), []string{"tag\u202e"}
	v.Reasons = append(v.Reasons, api.Reason{Reason: "bad\x1b"})
	for _, w := range []int{200, 20, 1, 0} {
		out := taskRow(a, v, w)
		if strings.ContainsAny(out, "\x1b\u009b\u202e") || lipgloss.Width(out) > w {
			t.Fatalf("unsafe or oversized row: %q", out)
		}
	}
	eq(t, taskRow(a, task(1, "simple"), 100), "#1 simple")
}

// Pagination belongs to the loader, so this fixture serves actual cursor pages
// without changing the shared fake's single-page behavior for other tests.
type taskPagesAPI struct {
	*fakeAPI
	pages      map[string]api.Page[api.Task]
	failCursor string
	err        error
}

func (f *taskPagesAPI) ListTasks(_ context.Context, q client.TaskQuery) (*api.Page[api.Task], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(call{Method: "ListTasks", Q: q}); err != nil {
		return nil, err
	}
	if f.err != nil && q.Cursor == f.failCursor {
		return nil, f.err
	}
	p := f.pages[q.Cursor]
	return &p, nil
}

func TestListLoadPagination(t *testing.T) {
	for _, pages := range []int{1, 3, 5, 6} {
		t.Run(fmt.Sprint(pages), func(t *testing.T) {
			f := &taskPagesAPI{fakeAPI: newFake(), pages: make(map[string]api.Page[api.Task])}
			cursor := ""
			for p := range pages {
				page := api.Page[api.Task]{}
				for i := range 500 {
					page.Items = append(page.Items, task(int64(p*500+i+1), "row"))
				}
				if p+1 < pages {
					page.NextCursor = ptr(fmt.Sprintf("cursor-%d", p+1))
				}
				f.pages[cursor] = page
				if page.NextCursor != nil {
					cursor = *page.NextCursor
				}
			}
			a := newTestApp(t, f.fakeAPI, func(c *Config) { c.API = f })
			cmd := a.loadTasks("backlog", "backlog")
			eq(t, f.count("ListTasks"), 0)
			msg := cmd().(tasksMsg)
			eq(t, msg.err, nil)
			eq(t, len(msg.tasks), min(pages, 5)*500)
			eq(t, msg.truncated, pages > 5)
			eq(t, msg.target, "backlog")
			for i, call := range f.calls[1:] { // Initial Today read precedes the list pages.
				q := call.Q.(client.TaskQuery)
				wantCursor := ""
				if i > 0 {
					wantCursor = fmt.Sprintf("cursor-%d", i)
				}
				eq(t, q, client.TaskQuery{View: "backlog", PageQuery: client.PageQuery{Limit: 500, Cursor: wantCursor}})
			}
			drain(t, a, func() tea.Msg { return msg })
			if pages > 5 {
				eq(t, a.banner.text, "showing the first 2500")
				f.pages[""] = api.Page[api.Task]{}
				drain(t, a, a.loadTasks("backlog", "backlog"))
				eq(t, a.banner.text, "")
			}
		})
	}
}

func TestListPartialLoadFailureKeepsPreviousRows(t *testing.T) {
	f := &taskPagesAPI{fakeAPI: newFake(), pages: map[string]api.Page[api.Task]{
		"": {Items: []api.Task{task(2, "original")}},
	}}
	a := newTestApp(t, f.fakeAPI, func(c *Config) { c.API, c.StartTab = f, TabBacklog })
	f.pages[""] = api.Page[api.Task]{Items: []api.Task{task(3, "partial")}, NextCursor: ptr("next")}
	f.failCursor, f.err = "next", outcomeUnknown("")
	before := f.count("ListTasks")
	drain(t, a, a.current().load(a))
	eq(t, f.count("ListTasks"), before+2)
	selectedID(t, a, 2)
	eq(t, a.banner.text, "server did not answer")
	if strings.Contains(viewText(a), "partial") {
		t.Fatal("a failed load replaced the complete list with partial data")
	}
}

func TestListLoadsUseSeparateTargets(t *testing.T) {
	f := newFake()
	f.tasks["triage"], f.tasks["backlog"] = []api.Task{task(1, "triage")}, []api.Task{task(2, "backlog")}
	a := newTestApp(t, f)
	triage := a.loadTasks("triage", "triage")
	backlog := a.loadTasks("backlog", "backlog")
	drain(t, a, backlog)
	drain(t, a, triage)
	for tab, want := range map[Tab]int64{TabTriage: 1, TabBacklog: 2} {
		id, _, ok := a.tabs[tab].selection()
		eq(t, ok, true)
		eq(t, id, want)
	}
	for _, target := range []string{"triage", "backlog"} {
		old := a.reqSeq[target]
		drain(t, a, a.loadTasks(target, target))
		drain(t, a, func() tea.Msg { return tasksMsg{req: old, target: target, truncated: true} })
		eq(t, a.banner.text, "")
	}
}
