package web_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/testutil"
)

func TestTaskDetailOpen(t *testing.T) {
	ta := newTestApp(t).login()
	const notes = "**bold** [x](https://example.com)"
	id := ta.addTask(service.AddTaskIn{Title: "Database work", Notes: notes, Tags: []string{"db", "infra"},
		Project: "infra.kc", Priority: domain.PriorityH, Due: ptrMoment(testutil.Moment("10-30"))})
	block := ta.mustOp(ta.Svc.Block(t.Context(), ta.meta(), service.BlockIn{TaskID: id, Expect: ta.taskExpect(id),
		Span: domain.BlockSpan{Date: testutil.D("10-29"), Start: 13 * 60, End: 15 * 60}})).Block
	entry := ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{
		TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}, Title: "Investigated schema", Notes: "**entry notes**"})).Entry
	before := ta.opKinds()
	body := ta.viewBody("/ui/tasks/1")
	assertInert(t, body)
	requireText(t, body, "Database work", "infra.kc", "H", "db", "infra", "0/0", "Fri 2026-10-30", "13:00", "15:00", "Investigated schema")
	requireElementText(t, body, "strong", "bold")
	requireElementText(t, body, "strong", "entry notes")
	link, history := false, false
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			if attr(n, "href") == "https://example.com" {
				link = attr(n, "rel") == "noreferrer noopener"
			}
			history = history || attr(n, "href") == "/ui/tasks/1/history"
		}
	})
	if !link || !history {
		t.Error("missing safe markdown link or task history link")
	}
	etag := domain.FormatETag(ta.Svc.Generation(), ta.task(id).Version)
	for _, action := range []string{"edit", "tags", "due", "defer", "place", "block", "triage", "complete", "drop", "delete"} {
		for _, form := range requireTaskForms(t, body, "/ui/tasks/1/"+action) {
			if form.Get("etag") != etag {
				t.Errorf("%s ETag = %v", action, form)
			}
		}
	}
	edit := scrapeForm(t, body, "/ui/tasks/1/edit")
	for key, want := range map[string]string{"title": "Database work", "notes": notes, "project": "infra.kc", "priority": "H"} {
		if !reflect.DeepEqual(edit[key], []string{want}) {
			t.Errorf("edit %s = %v", key, edit[key])
		}
	}
	var priorities []string
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Data == "select" && attr(n, "name") == "priority" {
			walkHTML(n, func(option *html.Node) {
				if option.Data == "option" {
					priorities = append(priorities, attr(option, "value"))
				}
			})
		}
	})
	if !reflect.DeepEqual(priorities, []string{"", "H", "M", "L"}) {
		t.Errorf("priority options = %v", priorities)
	}
	if forms := scrapeForms(t, body, "/ui/tasks/1/tags"); len(forms) != 3 {
		t.Errorf("tag forms = %d, want add plus two removals", len(forms))
	}
	scrapeFormWhere(t, body, "/ui/tasks/1/tags", url.Values{"add": {""}})
	for _, tag := range []string{"db", "infra"} {
		scrapeFormWhere(t, body, "/ui/tasks/1/tags", url.Values{"remove": {tag}})
	}
	for _, how := range []string{"keep", "top", "bottom", "before", "after"} {
		form := scrapeFormWhere(t, body, "/ui/tasks/1/place", url.Values{"how": {how}})
		if (how == "before" || how == "after") && !form.Has("ref") {
			t.Errorf("%s lacks ref", how)
		}
	}
	for action, fields := range map[string][]string{
		"due": {"due"}, "defer": {"until"}, "block": {"span"}, "complete": {"as", "date", "log", "notes"},
	} {
		form := scrapeForm(t, body, "/ui/tasks/1/"+action)
		for _, field := range fields {
			if !form.Has(field) {
				t.Errorf("%s lacks %s", action, field)
			}
		}
	}
	if got := scrapeForm(t, body, "/ui/tasks/1/due").Get("due"); got != "2026-10-30" {
		t.Errorf("due = %q", got)
	}
	scrapeFormWhere(t, body, "/ui/focus", url.Values{"task_id": {"1"}})
	for _, action := range []string{"drop", "delete"} {
		requireConfirm(t, body, "/ui/tasks/1/"+action)
	}
	for _, action := range []string{"reopen", "undelete"} {
		if len(scrapeForms(t, body, "/ui/tasks/1/"+action)) != 0 {
			t.Errorf("open task offers %s", action)
		}
	}
	// DF20: this future block does not need reblocking, but still offers C17.
	form := scrapeForm(t, body, "/ui/blocks/1/reblock")
	if form.Get("etag") != domain.FormatETag(ta.Svc.Generation(), block.Version) || form.Get("task_etag") != etag || !form.Has("span") {
		t.Errorf("reblock form = %v", form)
	}
	scrapeForm(t, body, "/ui/blocks/1/unblock")
	form = scrapeForm(t, body, "/ui/entries/1/edit")
	if form.Get("etag") != domain.FormatETag(ta.Svc.Generation(), entry.Version) || form.Get("notes") != entry.Notes {
		t.Errorf("entry form = %v", form)
	}
	if !reflect.DeepEqual(before, ta.opKinds()) {
		t.Error("task GET created a user operation")
	}
}

func TestTaskDetailDoneAndDeleted(t *testing.T) {
	for _, status := range []string{"done", "dropped"} {
		t.Run(status, func(t *testing.T) {
			ta := newTestApp(t).login()
			id := ta.addTask(service.AddTaskIn{Title: "Closed task"})
			ta.mustOp(ta.Svc.Block(t.Context(), ta.meta(), service.BlockIn{TaskID: id, Expect: ta.taskExpect(id),
				Span: domain.BlockSpan{Date: testutil.D("10-29"), Start: 13 * 60, End: 14 * 60}}))
			ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{
				TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}, Title: "Retained work"}))
			if status == "done" {
				ta.mustOp(ta.Svc.Complete(t.Context(), ta.meta(), service.CompleteIn{ID: id, Expect: ta.taskExpect(id)}))
			} else {
				ta.mustOp(ta.Svc.Drop(t.Context(), ta.meta(), service.TaskRef{ID: id, Expect: ta.taskExpect(id)}))
			}
			body := ta.viewBody("/ui/tasks/1")
			requireText(t, body, status)
			form := scrapeForm(t, body, "/ui/tasks/1/reopen")
			if form.Get("etag") != domain.FormatETag(ta.Svc.Generation(), ta.task(id).Version) || form.Has("remove_entry") {
				t.Errorf("reopen ETag or default remove_entry = %v", form)
			}
			requireConfirm(t, body, "/ui/tasks/1/reopen")
			checkbox := false
			walkHTML(parseHTML(t, body), func(n *html.Node) {
				if n.Data == "form" && attr(n, "action") == "/ui/tasks/1/reopen" {
					walkHTML(n, func(field *html.Node) {
						if field.Data == "input" && attr(field, "name") == "remove_entry" {
							checkbox = attr(field, "type") == "checkbox" && attr(field, "value") == "1"
						}
					})
				}
			})
			if !checkbox || len(scrapeForms(t, body, "/ui/tasks/1/complete")) != 0 {
				t.Error("closed task lacks remove-entry checkbox or offers completion")
			}
			ta.mustOp(ta.Svc.Delete(t.Context(), ta.meta(), service.TaskRef{ID: id, Expect: ta.taskExpect(id)}))
			body = ta.viewBody("/ui/tasks/1")
			requireText(t, body, "deleted", "Retained work")
			if len(dataIDs(t, body, "data-block")) != 1 || len(dataIDs(t, body, "data-entry")) == 0 {
				t.Error("deleted task lost related records")
			}
			form = scrapeForm(t, body, "/ui/tasks/1/undelete")
			if form.Get("etag") != domain.FormatETag(ta.Svc.Generation(), ta.task(id).Version) {
				t.Errorf("undelete ETag = %v", form)
			}
			walkHTML(parseHTML(t, body), func(n *html.Node) {
				if n.Data == "form" && attr(n, "method") == "post" {
					action := attr(n, "action")
					if action != "/ui/logout" && action != "/ui/tasks/1/undelete" {
						t.Errorf("deleted task exposes mutation %s", action)
					}
				}
			})
		})
	}
}

func TestTaskDetailNotFound(t *testing.T) {
	ta := newTestApp(t).login()
	for _, path := range []string{"/ui/tasks/999", "/ui/tasks/abc", "/ui/tasks/0", "/ui/tasks/abc/history"} {
		w := ta.get(path)
		if w.Code != http.StatusNotFound || !slices.Contains(dataIDs(t, w.Body.String(), "data-code"), "not_found") {
			t.Errorf("GET %s = %d %s", path, w.Code, w.Body)
		}
	}
}

func TestDetailEscapesAndRendersMarkdown(t *testing.T) {
	ta := newTestApp(t).login()
	const title = `"><img src=x onerror=alert(1)>`
	// AIDEV-NOTE: DF05 separates the HTML block from the Markdown paragraph.
	const notes = "<script>x</script>\n\n[c](javascript:alert(1)) **b**"
	ta.addTask(service.AddTaskIn{Title: title, Notes: notes})
	body := ta.viewBody("/ui/tasks/1")
	assertInert(t, body)
	requireElementText(t, body, "strong", "b")
	form := scrapeForm(t, body, "/ui/tasks/1/edit")
	if !reflect.DeepEqual(form["title"], []string{title}) || !reflect.DeepEqual(form["notes"], []string{notes}) {
		t.Errorf("hostile input did not round-trip: %v", form)
	}
	requireConfirm(t, body, "/ui/tasks/1/drop")
	body = ta.viewBody("/ui/tasks/1/history")
	assertInert(t, body)
	found := false
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "pre" && strings.Contains(nodeText(n), "onerror=alert(1)") {
			found = true
		}
	})
	if !found {
		t.Error("history lost hostile title text")
	}
}

func TestHistoryPage(t *testing.T) {
	ta := newTestApp(t).login()
	id := ta.addTask(service.AddTaskIn{Title: "Original"})
	title := "Updated"
	ta.mustOp(ta.Svc.UpdateTask(t.Context(), ta.meta(), service.UpdateTaskIn{ID: id, Expect: ta.taskExpect(id), Title: &title}))
	complete := ta.mustOp(ta.Svc.Complete(t.Context(), ta.meta(), service.CompleteIn{ID: id, Expect: ta.taskExpect(id)}))
	before := ta.opKinds()
	body := ta.viewBody("/ui/tasks/1/history")
	assertInert(t, body)
	kinds := slices.Compact(dataIDs(t, body, "data-kind"))
	if !reflect.DeepEqual(kinds, []string{"task.complete", "task.update", "task.add"}) {
		t.Errorf("history kinds = %v", kinds)
	}
	page, err := ta.Svc.History(t.Context(), testutil.Owner(), service.HistoryIn{TaskID: id, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	checkHistoryEvents(t, body, page.Items)
	action := fmt.Sprintf("/ui/ops/%s/revert", complete.OpID)
	if len(scrapeForms(t, body, action)) != 1 {
		t.Error("completion needs exactly one revert form despite multiple events")
	}
	requireConfirm(t, body, action)
	if !reflect.DeepEqual(before, ta.opKinds()) {
		t.Error("history GET created a user operation")
	}
	ta.mustOp(ta.Svc.Revert(t.Context(), ta.meta(), service.RevertIn{OpID: complete.OpID}))
	body = ta.viewBody("/ui/tasks/1/history")
	if len(scrapeForms(t, body, action)) != 0 {
		t.Error("reverted operation still offers revert")
	}
	// Active system operations are visible but are not revertible.
	due := testutil.Moment("10-29")
	ta.mustOp(ta.Svc.SetDue(t.Context(), ta.meta(), service.SetDueIn{ID: id, Expect: ta.taskExpect(id), Due: &due}))
	ta.Clock.Set(testutil.Local(t, "2026-10-29 10:00"))
	if err := ta.Svc.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	page, err = ta.Svc.History(t.Context(), testutil.Owner(), service.HistoryIn{TaskID: id, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(page.Items, func(event service.EventView) bool {
		return event.Op.State == domain.OpActive && !event.Op.Revertible
	}) {
		t.Fatal("fixture lacks an active non-revertible operation")
	}
	checkHistoryEvents(t, ta.viewBody("/ui/tasks/1/history"), page.Items)
}

func TestHistoryPagePaging(t *testing.T) {
	ta := newTestApp(t).login()
	id := ta.addTask(service.AddTaskIn{Title: "First title"})
	for i := range 101 {
		title := fmt.Sprintf("Revision %d", i)
		ta.mustOp(ta.Svc.UpdateTask(t.Context(), ta.meta(), service.UpdateTaskIn{ID: id, Expect: ta.taskExpect(id), Title: &title}))
	}
	path := "/ui/tasks/1/history?keep=a%26b"
	body := ta.viewBody(path)
	ids := dataIDs(t, body, "data-event")
	if len(ids) != 100 {
		t.Fatalf("first page events = %d", len(ids))
	}
	more := moreLink(t, body)
	u, err := url.Parse(more)
	if err != nil || u.Path != "/ui/tasks/1/history" || u.Query().Get("cursor") == "" || u.Query().Get("keep") != "a&b" {
		t.Fatalf("More link = %q, %v", more, err)
	}
	body = ta.viewBody(more)
	page, err := ta.Svc.History(t.Context(), testutil.Owner(), service.HistoryIn{TaskID: id, Limit: 100, Cursor: u.Query().Get("cursor")})
	if err != nil {
		t.Fatal(err)
	}
	checkHistoryEvents(t, body, page.Items)
	if len(page.Items) == 0 || moreLink(t, body) != "" {
		t.Error("last page empty or has a More link")
	}
	for _, id := range dataIDs(t, body, "data-event") {
		if slices.Contains(ids, id) {
			t.Errorf("event %s repeated across pages", id)
		}
	}
	w := ta.get("/ui/tasks/1/history?cursor=garbage")
	if w.Code != http.StatusBadRequest || !slices.Contains(dataIDs(t, w.Body.String(), "data-code"), "malformed") {
		t.Errorf("invalid cursor = %d %s", w.Code, w.Body)
	}
}

func TestTaskDetailDeferAndInterruptions(t *testing.T) {
	ta := newTestApp(t).login()
	id := ta.addTask(service.AddTaskIn{Title: "Interrupted"})
	other := ta.addTask(service.AddTaskIn{Title: "Next"})
	ta.mustOp(ta.Svc.SetFocus(t.Context(), ta.meta(), service.SetFocusIn{TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}}))
	ta.mustOp(ta.Svc.SetFocus(t.Context(), ta.meta(), service.SetFocusIn{TaskID: sql.Null[domain.TaskID]{V: other, Valid: true}, Interrupt: true}))
	until := testutil.Moment("10-30 09:00")
	ta.mustOp(ta.Svc.Defer(t.Context(), ta.meta(), service.DeferIn{ID: id, Expect: ta.taskExpect(id), Until: &until}))
	body := ta.viewBody("/ui/tasks/1")
	requireText(t, body, "Fri 2026-10-30", "09:00")
	if got := scrapeForm(t, body, "/ui/tasks/1/defer").Get("until"); got != "2026-10-30 09:00" {
		t.Errorf("defer = %q", got)
	}
	found := false
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Data == "section" && attr(n, "aria-label") == "Interruptions" {
			text := nodeText(n)
			found = strings.Contains(text, "#1") && strings.Contains(text, "#2") && strings.Contains(text, "10:00")
		}
	})
	if !found {
		t.Error("missing interruption time and task links")
	}
}

func requireTaskForms(t *testing.T, body, action string) []url.Values {
	t.Helper()
	forms := scrapeForms(t, body, action)
	if len(forms) == 0 {
		t.Fatalf("missing %s form", action)
	}
	return forms
}

func requireElementText(t *testing.T, body, element, text string) {
	t.Helper()
	found := false
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == element && nodeText(n) == text {
			found = true
		}
	})
	if !found {
		t.Errorf("missing <%s> with text %q", element, text)
	}
}

func checkHistoryEvents(t *testing.T, body string, events []service.EventView) {
	t.Helper()
	var rows []*html.Node
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Type == html.ElementNode && hasAttr(n, "data-event") {
			rows = append(rows, n)
		}
	})
	if len(rows) != len(events) {
		t.Fatalf("history rows = %d, want %d", len(rows), len(events))
	}
	seen := make(map[domain.OpID]bool)
	for i, event := range events {
		row := rows[i]
		if attr(row, "data-event") != fmt.Sprint(event.ID) {
			t.Errorf("event order at %d: %s, want %d", i, attr(row, "data-event"), event.ID)
		}
		text := nodeText(row)
		for _, value := range []string{string(event.Op.Kind), event.Op.Actor, string(event.Op.Channel), string(event.Entity), string(event.Action), event.Op.At.In(testutil.Zone.Location()).Format("Mon 2006-01-02 15:04")} {
			if !strings.Contains(text, value) {
				t.Errorf("event %d lacks %q", event.ID, value)
			}
		}
		var jsonText []string
		forms := 0
		walkHTML(row, func(n *html.Node) {
			if n.Type == html.ElementNode && n.Data == "pre" {
				jsonText = append(jsonText, nodeText(n))
			}
			if n.Data == "form" {
				forms++
				if attr(n, "action") != fmt.Sprintf("/ui/ops/%s/revert", event.Op.OpID) || attr(n, "hx-confirm") == "" {
					t.Errorf("event %d has wrong revert form", event.ID)
				}
			}
		})
		if !reflect.DeepEqual(jsonText, []string{string(event.Before), string(event.After)}) {
			t.Errorf("event %d JSON = %q", event.ID, jsonText)
		}
		want := 0
		if !seen[event.Op.OpID] && event.Op.Revertible && event.Op.State == domain.OpActive {
			want = 1
		}
		if forms != want {
			t.Errorf("event %d revert forms = %d, want %d", event.ID, forms, want)
		}
		seen[event.Op.OpID] = true
	}
}
