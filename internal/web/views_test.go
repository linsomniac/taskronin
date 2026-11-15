package web_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/testutil"
)

func (ta *testApp) mustOp(out *service.OpResult, err error) *service.OpResult {
	ta.t.Helper()
	if err != nil {
		ta.t.Fatal(err)
	}
	return out
}

func (ta *testApp) taskExpect(id domain.TaskID) domain.Expect {
	ta.t.Helper()
	return domain.Expect{Gen: ta.Svc.Generation(), Version: ta.task(id).Version}
}

// viewBody checks the browser-visible form contract on every successful page.
func (ta *testApp) viewBody(path string) string {
	ta.t.Helper()
	w := ta.get(path)
	if w.Code != http.StatusOK {
		ta.t.Fatalf("GET %s = %d %s; logs: %s", path, w.Code, w.Body, ta.Logs)
	}
	body := w.Body.String()
	nonces := make(map[string]bool)
	csrf := ""
	walkHTML(parseHTML(ta.t, body), func(n *html.Node) {
		if n.Type != html.ElementNode || n.Data != "form" || attr(n, "method") != "post" {
			return
		}
		values := make(url.Values)
		walkHTML(n, func(field *html.Node) {
			if field.Type == html.ElementNode && field.Data == "input" && attr(field, "type") == "hidden" {
				values.Add(attr(field, "name"), attr(field, "value"))
			}
		})
		for _, key := range []string{"_csrf", "_gen", "_nonce", "_return"} {
			if len(values[key]) != 1 || values.Get(key) == "" {
				ta.t.Errorf("%s: %s missing one hidden %s", path, attr(n, "action"), key)
			}
		}
		if csrf == "" {
			csrf = values.Get("_csrf")
		}
		if values.Get("_csrf") != csrf || values.Get("_gen") != string(ta.Svc.Generation()) || values.Get("_return") != path {
			ta.t.Errorf("%s: wrong form context: %v", path, values)
		}
		nonce := values.Get("_nonce")
		if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(nonce) || nonces[nonce] {
			ta.t.Errorf("%s: invalid or reused nonce %q", path, nonce)
		}
		nonces[nonce] = true
		for _, key := range []string{"etag", "task_etag"} {
			for _, value := range values[key] {
				expect, err := domain.ParseETag(value)
				if err != nil || expect.Gen != ta.Svc.Generation() {
					ta.t.Errorf("%s: invalid %s %q", path, key, value)
				}
			}
		}
	})
	return body
}

func dataIDs(t *testing.T, body, key string) []string {
	t.Helper()
	var ids []string
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Type == html.ElementNode && hasAttr(n, key) {
			ids = append(ids, attr(n, key))
		}
	})
	return ids
}

func requireText(t *testing.T, body string, values ...string) {
	t.Helper()
	text := nodeText(parseHTML(t, body))
	for _, want := range values {
		if !strings.Contains(text, want) {
			t.Errorf("missing text %q", want)
		}
	}
}

func requireConfirm(t *testing.T, body, action string) {
	t.Helper()
	found := false
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "form" && attr(n, "action") == action && attr(n, "hx-confirm") != "" {
			found = true
		}
	})
	if !found {
		t.Errorf("missing hx-confirm on %s", action)
	}
}

func moreLink(t *testing.T, body string) string {
	t.Helper()
	var link string
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" && strings.TrimSpace(nodeText(n)) == "More" {
			if link != "" {
				t.Error("multiple More links")
			}
			link = attr(n, "href")
		}
	})
	return link
}

func TestTodayPage(t *testing.T) {
	ta := newTestApp(t).login()
	id := ta.addTask(service.AddTaskIn{Title: "Write report", Place: &service.AddPlacement{How: "top"}})
	ta.mustOp(ta.Svc.SetFocus(t.Context(), ta.meta(), service.SetFocusIn{TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}}))
	ta.mustOp(ta.Svc.Block(t.Context(), ta.meta(), service.BlockIn{TaskID: id, Expect: ta.taskExpect(id), Span: domain.BlockSpan{Date: testutil.D("10-28"), Start: 13 * 60, End: 15 * 60}}))
	ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}, Title: "Drafted intro"}))
	ta.addTask(service.AddTaskIn{Title: "Triage me"})
	ta.mustOp(ta.Svc.AddEntry(t.Context(), testutil.Meta(testutil.AI(), ta.Svc.Generation()), service.AddEntryIn{Title: "AI work"}))
	body := ta.viewBody("/ui/")
	if !slices.Contains(dataIDs(t, body, "data-task"), "1") || !reflect.DeepEqual(dataIDs(t, body, "data-block"), []string{"1"}) {
		t.Error("missing focus task or today's block")
	}
	requireText(t, body, "13:00", "15:00", "Drafted intro", "Triage (1)")
	if got := dataIDs(t, body, "data-banner"); !reflect.DeepEqual(got, []string{"backup_stale", "ai_changes"}) {
		t.Errorf("banners = %v, want service banners once each", got)
	}
	if got := scrapeForm(t, body, "/ui/entries")["task_id"]; !reflect.DeepEqual(got, []string{"1"}) {
		t.Errorf("log task = %v", got)
	}
	for _, action := range []string{"/ui/tasks", "/ui/focus", "/ui/tasks/1/complete", "/ui/blocks/1/unblock"} {
		scrapeForm(t, body, action)
	}
	scrapeFormWhere(t, body, "/ui/focus", url.Values{"task_id": {""}})
	if strings.Contains(nodeText(parseHTML(t, body)), "Overdue") || strings.Contains(nodeText(parseHTML(t, body)), "Due soon") {
		t.Error("empty due sections rendered")
	}
}

func TestTodayReblockFormCarriesTaskETag(t *testing.T) {
	ta := newTestApp(t)
	ta.Clock.Set(testutil.Local(t, "2026-10-28 07:00"))
	id := ta.addTask(service.AddTaskIn{Title: "Missed work"})
	block := ta.mustOp(ta.Svc.Block(t.Context(), ta.meta(), service.BlockIn{TaskID: id, Expect: ta.taskExpect(id), Span: domain.BlockSpan{Date: testutil.D("10-28"), Start: 8 * 60, End: 9 * 60}})).Block
	ta.Clock.Set(testutil.Local(t, "2026-10-28 10:00"))
	ta.login()
	body := ta.viewBody("/ui/")
	requireText(t, body, "reblock?")
	form := scrapeForm(t, body, "/ui/blocks/1/reblock")
	if form.Get("etag") != domain.FormatETag(ta.Svc.Generation(), block.Version) || form.Get("task_etag") != domain.FormatETag(ta.Svc.Generation(), ta.task(id).Version) || !form.Has("span") {
		t.Errorf("reblock form = %v", form)
	}
}

func TestTriagePage(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: "First", Tags: []string{"writing"}, Project: "Docs", Priority: domain.PriorityH})
	ta.addTask(service.AddTaskIn{Title: "Placed", Place: &service.AddPlacement{How: "bottom"}})
	ta.addTask(service.AddTaskIn{Title: "Third"})
	body := ta.viewBody("/ui/triage")
	if got := dataIDs(t, body, "data-task"); !reflect.DeepEqual(got, []string{"1", "3"}) {
		t.Errorf("triage rows = %v", got)
	}
	requireText(t, body, "new", "writing", "Docs", "H", "0/0")
	form := scrapeForm(t, body, "/ui/tasks")
	for _, key := range []string{"title", "notes", "due", "tags", "project", "priority", "place", "ref", "defer", "span"} {
		if !form.Has(key) {
			t.Errorf("add task lacks %s", key)
		}
	}
	for _, how := range []string{"keep", "top", "bottom", "before", "after"} {
		place := scrapeFormWhere(t, body, "/ui/tasks/1/place", url.Values{"how": {how}})
		if place.Get("etag") != domain.FormatETag(ta.Svc.Generation(), ta.task(1).Version) {
			t.Errorf("placement ETag = %v", place)
		}
		if (how == "before" || how == "after") && !place.Has("ref") {
			t.Errorf("%s lacks ref", how)
		}
	}
	for _, action := range []string{"defer", "block", "complete", "drop"} {
		form := scrapeForm(t, body, "/ui/tasks/1/"+action)
		if form.Get("etag") != domain.FormatETag(ta.Svc.Generation(), ta.task(1).Version) {
			t.Errorf("%s ETag = %v", action, form)
		}
	}
	requireConfirm(t, body, "/ui/tasks/1/drop")
	accept := scrapeForm(t, body, "/ui/triage/accept-new")
	want := []string{domain.FormatETag(ta.Svc.Generation(), ta.task(1).Version), domain.FormatETag(ta.Svc.Generation(), ta.task(3).Version)}
	if !reflect.DeepEqual(accept["id"], []string{"1", "3"}) || !reflect.DeepEqual(accept["etag"], want) {
		t.Errorf("accept-new = %v", accept)
	}
	// A new task with another pending reason must never enter accept-new.
	due := testutil.Moment("10-28")
	ta.mustOp(ta.Svc.SetDue(t.Context(), ta.meta(), service.SetDueIn{ID: 1, Expect: ta.taskExpect(1), Due: &due}))
	body = ta.viewBody("/ui/triage")
	if got := scrapeForm(t, body, "/ui/triage/accept-new")["id"]; !reflect.DeepEqual(got, []string{"3"}) {
		t.Errorf("accept-new included a mixed-reason task: %v", got)
	}
	ta.mustOp(ta.Svc.Place(t.Context(), ta.meta(), service.PlaceIn{ID: 3, Expect: ta.taskExpect(3), How: domain.PlaceBottom}))
	if forms := scrapeForms(t, ta.viewBody("/ui/triage"), "/ui/triage/accept-new"); len(forms) != 0 {
		t.Error("accept-new rendered without new-only tasks")
	}
}

func TestBacklogPage(t *testing.T) {
	ta := newTestApp(t).login()
	for _, title := range []string{"First", "Second", "Third"} {
		ta.addTask(service.AddTaskIn{Title: title, Place: &service.AddPlacement{How: "bottom"}})
	}
	body := ta.viewBody("/ui/backlog")
	if got := dataIDs(t, body, "data-task"); !reflect.DeepEqual(got, []string{"1", "2", "3"}) {
		t.Errorf("backlog rows = %v", got)
	}
	scrapeFormWhere(t, body, "/ui/tasks/2/place", url.Values{"how": {"before"}, "ref": {"1"}})
	scrapeFormWhere(t, body, "/ui/tasks/2/place", url.Values{"how": {"after"}, "ref": {"3"}})
	for _, tc := range []struct{ action, absent string }{{"/ui/tasks/1/place", "before"}, {"/ui/tasks/3/place", "after"}} {
		for _, form := range scrapeForms(t, body, tc.action) {
			if form.Get("how") == tc.absent {
				t.Errorf("unexpected %s on %s", tc.absent, tc.action)
			}
		}
	}
	for _, how := range []string{"top", "bottom"} {
		scrapeFormWhere(t, body, "/ui/tasks/2/place", url.Values{"how": {how}})
	}
	for _, action := range []string{"triage", "defer", "block", "complete", "drop"} {
		scrapeForm(t, body, "/ui/tasks/2/"+action)
	}
	scrapeFormWhere(t, body, "/ui/focus", url.Values{"task_id": {"2"}})
	requireConfirm(t, body, "/ui/tasks/2/drop")
}

func TestUpcomingPage(t *testing.T) {
	ta := newTestApp(t).login()
	id := ta.addTask(service.AddTaskIn{Title: "Deferred"})
	until := testutil.Moment("10-30 09:00")
	ta.mustOp(ta.Svc.Defer(t.Context(), ta.meta(), service.DeferIn{ID: id, Expect: ta.taskExpect(id), Until: &until}))
	id = ta.addTask(service.AddTaskIn{Title: "Deadline"})
	due := testutil.Moment("10-29")
	ta.mustOp(ta.Svc.SetDue(t.Context(), ta.meta(), service.SetDueIn{ID: id, Expect: ta.taskExpect(id), Due: &due}))
	body := ta.viewBody("/ui/upcoming")
	requireText(t, body, "Fri 2026-10-30", "09:00", "Due soon", "Deadline")
	scrapeFormWhere(t, body, "/ui/tasks/1/defer", url.Values{"until": {"none"}})
	if forms := scrapeForms(t, body, "/ui/tasks/1/defer"); len(forms) != 2 {
		t.Errorf("defer forms = %d, want change and keep", len(forms))
	}
	if form := scrapeForm(t, body, "/ui/tasks/2/due"); form.Get("etag") != domain.FormatETag(ta.Svc.Generation(), ta.task(id).Version) || form.Get("due") != "2026-10-29" {
		t.Errorf("due form = %v", form)
	}
}

func TestLogPage(t *testing.T) {
	ta := newTestApp(t).login()
	id := ta.addTask(service.AddTaskIn{Title: "Report"})
	entry := ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}, Title: "Today's work", Notes: "\n**Bold**\n\n[c](javascript:alert(1))", At: sql.Null[civil.Minute]{V: 9 * 60, Valid: true}})).Entry
	ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}, Title: "Yesterday's work", WorkDate: testutil.D("10-27")}))
	ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{Title: "Standalone work", WorkDate: testutil.D("10-27")}))
	body := ta.viewBody("/ui/log")
	text := nodeText(parseHTML(t, body))
	if !strings.Contains(text, "Wed 2026-10-28") || strings.Index(text, "Wed 2026-10-28") > strings.Index(text, "Tue 2026-10-27") {
		t.Error("log date groups missing or out of order")
	}
	requireText(t, body, "09:00", "standalone", "Report", "Bold")
	form := scrapeForm(t, body, "/ui/entries/1/edit")
	if form.Get("etag") != domain.FormatETag(ta.Svc.Generation(), entry.Version) || form.Get("title") != entry.Title || form.Get("date") != "2026-10-28" || form.Get("at") != "09:00" || form.Get("notes") != entry.Notes {
		t.Errorf("entry edit = %v", form)
	}
	for _, action := range []string{"/ui/entries/1/relink", "/ui/entries/1/delete", "/ui/entries"} {
		scrapeForm(t, body, action)
	}
	requireConfirm(t, body, "/ui/entries/1/delete")
	assertInert(t, body)
	body = ta.viewBody("/ui/log?from=2026-10-27&to=2026-10-27")
	if got := dataIDs(t, body, "data-entry"); !reflect.DeepEqual(got, []string{"3", "2"}) {
		t.Errorf("filtered entries = %v", got)
	}
	for _, tc := range []struct{ query, field string }{
		{"from=someday", "from"}, {"to=someday", "to"}, {"from=2026-10-28&to=2026-10-27", "to"},
	} {
		w := ta.get("/ui/log?" + tc.query)
		if w.Code != 422 || !strings.Contains(w.Body.String(), `data-code="validation"`) {
			t.Errorf("bad date %s = %d %s", tc.query, w.Code, w.Body)
		}
		requireText(t, w.Body.String(), tc.field)
	}
	// Completion entries have a notes-only edit form and cannot be relinked.
	complete := ta.mustOp(ta.Svc.Complete(t.Context(), ta.meta(), service.CompleteIn{ID: id, Expect: ta.taskExpect(id)})).Entry
	body = ta.viewBody("/ui/log")
	form = scrapeForm(t, body, fmt.Sprintf("/ui/entries/%d/edit", complete.ID))
	for _, key := range []string{"title", "date", "at"} {
		if form.Has(key) {
			t.Errorf("completion edit exposes %s", key)
		}
	}
	if !form.Has("notes") || len(scrapeForms(t, body, fmt.Sprintf("/ui/entries/%d/relink", complete.ID))) != 0 {
		t.Error("completion form contract violated")
	}
}

func TestListPaging(t *testing.T) {
	ta := newTestApp(t).login()
	id := ta.addTask(service.AddTaskIn{Title: "Report"})
	var entries []*domain.EntryView
	for i := range 102 {
		entries = append(entries, ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}, Title: fmt.Sprintf("Old work %d", i), WorkDate: testutil.D("10-20")})).Entry)
	}
	deleted := entries[0] // Oldest entry lands on the second page.
	ta.mustOp(ta.Svc.DeleteEntry(t.Context(), ta.meta(), service.EntryRef{ID: deleted.ID, Expect: domain.Expect{Gen: ta.Svc.Generation(), Version: deleted.Version}}))
	newer := ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{Title: "Outside range"})).Entry
	path := "/ui/log?from=2026-10-20&to=2026-10-20&include_deleted=1"
	body := ta.viewBody(path)
	if got := len(dataIDs(t, body, "data-entry")); got != 100 {
		t.Fatalf("first page rows = %d", got)
	}
	more := moreLink(t, body)
	u, err := url.Parse(more)
	if err != nil || u.Path != "/ui/log" || u.Query().Get("from") != "2026-10-20" || u.Query().Get("to") != "2026-10-20" || u.Query().Get("include_deleted") != "1" || u.Query().Get("cursor") == "" {
		t.Fatalf("More URL = %q, err %v", more, err)
	}
	body2 := ta.viewBody(more)
	if got := dataIDs(t, body2, "data-entry"); !reflect.DeepEqual(got, []string{"2", "1"}) || moreLink(t, body2) != "" {
		t.Errorf("last page = %v; More = %q", got, moreLink(t, body2))
	}
	scrapeForm(t, body2, "/ui/entries/1/undelete")
	if len(scrapeForms(t, body2, "/ui/entries/1/delete")) != 0 || len(scrapeForms(t, body2, "/ui/entries/1/edit")) != 0 {
		t.Error("deleted entry has live mutation forms")
	}
	for _, page := range []string{body, body2} {
		if slices.Contains(dataIDs(t, page, "data-entry"), fmt.Sprint(newer.ID)) {
			t.Error("page lost date filters")
		}
	}
	deleted = entries[1]
	ta.mustOp(ta.Svc.DeleteEntry(t.Context(), ta.meta(), service.EntryRef{ID: deleted.ID, Expect: domain.Expect{Gen: ta.Svc.Generation(), Version: deleted.Version}}))
	body = ta.viewBody("/ui/log?from=2026-10-20&to=2026-10-20")
	if len(dataIDs(t, body, "data-entry")) != 100 || moreLink(t, body) != "" {
		t.Error("exactly full final page must have no More link")
	}
	for _, path := range []string{"/ui/log", "/ui/triage", "/ui/backlog"} {
		w := ta.get(path + "?cursor=garbage")
		if w.Code != 400 || !strings.Contains(w.Body.String(), `data-code="malformed"`) {
			t.Errorf("hostile cursor on %s = %d %s", path, w.Code, w.Body)
		}
	}
	if len(ta.opKinds()) < 106 {
		t.Error("opKinds did not page through all operations")
	}
}

func TestViewsEscapeHostileTitle(t *testing.T) {
	ta := newTestApp(t).login()
	const title = `<script>alert(1)</script>"' onmouseover=x`
	ta.addTask(service.AddTaskIn{Title: title})
	ta.addTask(service.AddTaskIn{Title: title, Place: &service.AddPlacement{How: "bottom"}})
	ta.addTask(service.AddTaskIn{Title: title, Place: &service.AddPlacement{How: "defer", Defer: testutil.Moment("10-30 09:00")}})
	ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{Title: title}))
	for _, path := range []string{"/ui/", "/ui/triage", "/ui/backlog", "/ui/upcoming", "/ui/log"} {
		body := ta.viewBody(path)
		assertInert(t, body)
		requireText(t, body, title)
	}
}

func TestGETsPerformNoUserOps(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: "Due tomorrow", Due: ptrMoment(testutil.Moment("10-29"))})
	ta.addTask(service.AddTaskIn{Title: "Backlog", Place: &service.AddPlacement{How: "bottom"}})
	before := ta.opKinds()
	ta.Clock.Set(testutil.Local(t, "2026-10-29 10:00")) // GET may reconcile a new due reason.
	for range 2 {
		for _, path := range []string{"/ui/", "/ui/triage", "/ui/backlog", "/ui/upcoming", "/ui/log"} {
			ta.viewBody(path)
		}
	}
	after := ta.opKinds()
	if len(after) < len(before) || !reflect.DeepEqual(after[:len(before)], before) {
		t.Fatalf("GET rewrote operation history: %v -> %v", before, after)
	}
	for _, kind := range after[len(before):] {
		if !strings.HasPrefix(kind, "system.") {
			t.Errorf("GET created user op %q", kind)
		}
	}
}

func ptrMoment(m domain.Moment) *domain.Moment { return &m }

func TestTodayPageCurrentBlockAndPrefill(t *testing.T) {
	ta := newTestApp(t)
	ta.Clock.Set(testutil.Local(t, "2026-10-28 07:00"))
	ta.login()
	if form := scrapeForm(t, ta.viewBody("/ui/"), "/ui/entries"); form.Get("task_id") != "" {
		t.Error("empty Today did not default to standalone")
	}
	id := ta.addTask(service.AddTaskIn{Title: "Scheduled"})
	ta.mustOp(ta.Svc.Block(t.Context(), ta.meta(), service.BlockIn{TaskID: id, Expect: ta.taskExpect(id), Span: domain.BlockSpan{Date: testutil.D("10-28"), Start: 8 * 60, End: 9 * 60}}))
	focus := ta.addTask(service.AddTaskIn{Title: "Focus instead", Place: &service.AddPlacement{How: "bottom"}})
	for i := range 6 {
		ta.addTask(service.AddTaskIn{Title: fmt.Sprintf("Triage %d", i)})
	}
	ta.Clock.Set(testutil.Local(t, "2026-10-28 08:30"))
	body := ta.viewBody("/ui/")
	requireText(t, body, "Current block", "Triage (6)")
	if got := dataIDs(t, body, "data-task"); !reflect.DeepEqual(got, []string{"3", "4", "5", "6", "7"}) {
		t.Errorf("triage top five = %v", got)
	}
	if form := scrapeForm(t, body, "/ui/entries"); form.Get("task_id") != "1" {
		t.Error("Today did not prefill current block task")
	}
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if attr(n, "data-block") == "1" && attr(n, "aria-current") != "time" {
			t.Error("current block not highlighted")
		}
	})
	if len(scrapeForms(t, body, "/ui/blocks/1/reblock")) != 0 {
		t.Error("Today offers reblock before NeedsReblock")
	}
	ta.mustOp(ta.Svc.SetFocus(t.Context(), ta.meta(), service.SetFocusIn{TaskID: sql.Null[domain.TaskID]{V: focus, Valid: true}}))
	body = ta.viewBody("/ui/")
	if form := scrapeForm(t, body, "/ui/entries"); form.Get("task_id") != "2" {
		t.Error("focus did not take precedence over current block")
	}
	interruption := false
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Data == "section" && attr(n, "aria-label") == "Interruptions" {
			text := nodeText(n)
			interruption = strings.Contains(text, "08:30") && strings.Contains(text, "#1") && strings.Contains(text, "#2")
		}
	})
	if !interruption {
		t.Error("missing current-block interruption")
	}
}

func TestTaskListPaging(t *testing.T) {
	for _, view := range []string{"triage", "backlog"} {
		t.Run(view, func(t *testing.T) {
			ta := newTestApp(t).login()
			in := service.AddTaskIn{Tags: []string{"keep"}, Project: "Docs"}
			if view == "backlog" {
				in.Place = &service.AddPlacement{How: "bottom"}
			}
			for i := range 101 {
				in.Title = fmt.Sprintf("Task %d", i)
				ta.addTask(in)
			}
			in.Title, in.Tags = "Wrong tag", []string{"other"}
			ta.addTask(in)
			in.Title, in.Tags, in.Project = "Wrong project", []string{"keep"}, "Elsewhere"
			ta.addTask(in)
			path := "/ui/" + view + "?tag=keep&project=Docs"
			body := ta.viewBody(path)
			ids := dataIDs(t, body, "data-task")
			if len(ids) != 100 || ids[0] != "1" || ids[99] != "100" {
				t.Fatalf("first page task IDs = %v", ids)
			}
			u, err := url.Parse(moreLink(t, body))
			if err != nil || u.Path != "/ui/"+view || u.Query().Get("tag") != "keep" || u.Query().Get("project") != "Docs" || u.Query().Get("cursor") == "" {
				t.Fatalf("More link = %v, %v", u, err)
			}
			body = ta.viewBody(u.String())
			if got := dataIDs(t, body, "data-task"); !reflect.DeepEqual(got, []string{"101"}) || moreLink(t, body) != "" {
				t.Errorf("final page task IDs = %v", got)
			}
			if view == "backlog" {
				for _, form := range scrapeForms(t, body, "/ui/tasks/101/place") {
					if form.Get("how") == "before" || form.Get("how") == "after" {
						t.Error("single-row page offers a move to an unseen neighbour")
					}
				}
			}
		})
	}
}

func TestLogPageDefaultRangeAndPastDatespec(t *testing.T) {
	ta := newTestApp(t).login()
	for _, date := range []string{"10-14", "10-15", "10-27", "10-28"} {
		ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{Title: "Work " + date, WorkDate: testutil.D(date)}))
	}
	body := ta.viewBody("/ui/log")
	if got := dataIDs(t, body, "data-entry"); !reflect.DeepEqual(got, []string{"4", "3", "2"}) {
		t.Errorf("default date range = %v", got)
	}
	filter := scrapeForm(t, body, "/ui/log")
	if filter.Get("from") != "2026-10-15" || filter.Get("to") != "2026-10-28" {
		t.Errorf("default filters = %v", filter)
	}
	body = ta.viewBody("/ui/log?from=yesterday&to=yesterday")
	if got := dataIDs(t, body, "data-entry"); !reflect.DeepEqual(got, []string{"3"}) {
		t.Errorf("past datespec = %v", got)
	}
}
