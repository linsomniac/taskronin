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
	"time"

	"golang.org/x/net/html"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/testutil"
	"taskronin/internal/web"
)

func TestRecapPage(t *testing.T) {
	ta := newTestApp(t)
	ta.Clock.Set(testutil.Local(t, "2026-10-27 10:00"))
	id := ta.addTask(service.AddTaskIn{Title: "Scheduled work"})
	ta.mustOp(ta.Svc.Block(t.Context(), ta.meta(), service.BlockIn{TaskID: id, Expect: ta.taskExpect(id),
		Span: domain.BlockSpan{Date: testutil.D("10-27"), Start: 13 * 60, End: 15 * 60}}))
	ta.Clock.Set(testutil.Local(t, "2026-10-28 10:00"))
	entry := ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{
		Title: "Standalone work", WorkDate: testutil.D("10-27")})).Entry
	ta.login()
	before := ta.opKinds()
	body := ta.viewBody("/ui/recap?date=yesterday")
	assertInert(t, body)
	requireText(t, body, "Tue 2026-10-27", "Scheduled work", "missed", "Standalone work")
	if got := dataIDs(t, body, "data-entry"); !reflect.DeepEqual(got, []string{"1"}) {
		t.Errorf("recap entries = %v, want standalone entry exactly once", got)
	}
	view, err := ta.Svc.Day(t.Context(), testutil.Owner(), testutil.D("10-27"))
	if err != nil {
		t.Fatal(err)
	}
	forms := scrapeForms(t, body, "/ui/blocks/1/outcome")
	if len(forms) != 3 {
		t.Fatalf("outcome forms = %d, want 3", len(forms))
	}
	for _, outcome := range []string{"worked", "missed", "auto"} {
		form := scrapeFormWhere(t, body, "/ui/blocks/1/outcome", url.Values{"outcome": {outcome}})
		if form.Get("etag") != domain.FormatETag(ta.Svc.Generation(), view.Blocks[0].Version) {
			t.Errorf("%s outcome ETag = %v", outcome, form)
		}
	}
	for _, action := range []string{"edit", "relink", "delete"} {
		form := scrapeForm(t, body, "/ui/entries/1/"+action)
		if form.Get("etag") != domain.FormatETag(ta.Svc.Generation(), entry.Version) {
			t.Errorf("entry %s ETag = %v", action, form)
		}
	}
	if form := scrapeForm(t, body, "/ui/entries/1/relink"); !reflect.DeepEqual(form["task_id"], []string{""}) {
		t.Errorf("standalone relink = %v", form)
	}
	if form := scrapeForm(t, body, "/ui/entries"); !reflect.DeepEqual(form["date"], []string{"2026-10-27"}) || form.Get("task_id") != "" {
		t.Errorf("recap log prefill = %v", form)
	}
	for _, action := range []string{"reblock", "unblock"} {
		if len(scrapeForms(t, body, "/ui/blocks/1/"+action)) != 0 {
			t.Errorf("resolved block offers %s", action)
		}
	}
	body = ta.viewBody("/ui/recap")
	requireElementText(t, body, "h1", "Recap — Wed 2026-10-28")
	if got := scrapeForm(t, body, "/ui/entries").Get("date"); got != "2026-10-28" {
		t.Errorf("default recap log date = %q", got)
	}
	if !reflect.DeepEqual(before, ta.opKinds()) {
		t.Error("recap GET created a user operation")
	}
}

func TestRecapPagePlannedBlocksAndEntries(t *testing.T) {
	ta := newTestApp(t).login()
	id := ta.addTask(service.AddTaskIn{Title: "Planned work"})
	block := ta.mustOp(ta.Svc.Block(t.Context(), ta.meta(), service.BlockIn{TaskID: id, Expect: ta.taskExpect(id),
		Span: domain.BlockSpan{Date: testutil.D("10-28"), Start: 13 * 60, End: 15 * 60}})).Block
	linked := ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{
		TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}, Title: "Linked work", Notes: "**progress**"})).Entry
	other := ta.addTask(service.AddTaskIn{Title: "Interrupting task"})
	ta.mustOp(ta.Svc.SetFocus(t.Context(), ta.meta(), service.SetFocusIn{TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}}))
	ta.mustOp(ta.Svc.SetFocus(t.Context(), ta.meta(), service.SetFocusIn{TaskID: sql.Null[domain.TaskID]{V: other, Valid: true}, Interrupt: true}))
	complete := ta.mustOp(ta.Svc.Complete(t.Context(), ta.meta(), service.CompleteIn{ID: other, Expect: ta.taskExpect(other)})).Entry
	body := ta.viewBody("/ui/recap")
	assertInert(t, body)
	for _, action := range []string{"reblock", "unblock"} {
		form := scrapeForm(t, body, "/ui/blocks/1/"+action)
		if form.Get("etag") != domain.FormatETag(ta.Svc.Generation(), block.Version) {
			t.Errorf("%s ETag = %v", action, form)
		}
		if action == "reblock" && (form.Get("task_etag") != domain.FormatETag(ta.Svc.Generation(), ta.task(id).Version) || !form.Has("span")) {
			t.Errorf("reblock lacks task ETag or span: %v", form)
		}
	}
	for _, entry := range []*domain.EntryView{linked, complete} {
		action := fmt.Sprintf("/ui/entries/%d", entry.ID)
		form := scrapeForm(t, body, action+"/edit")
		if form.Get("etag") != domain.FormatETag(ta.Svc.Generation(), entry.Version) || !form.Has("notes") {
			t.Errorf("entry edit = %v", form)
		}
		for _, field := range []string{"title", "date", "at"} {
			if form.Has(field) != (entry.Kind == domain.EntryWork) {
				t.Errorf("%s entry has incorrect %s field", entry.Kind, field)
			}
		}
		if len(scrapeForms(t, body, action+"/relink")) != 0 {
			t.Error("recap offers relink for a linked entry")
		}
	}
	requireElementText(t, body, "strong", "progress")
	interruptions := sectionText(t, body, "Interruptions")
	for _, want := range []string{"#1", "#2", "Wed 2026-10-28 10:00"} {
		if !strings.Contains(interruptions, want) {
			t.Errorf("interruptions lack %q: %s", want, interruptions)
		}
	}
}

func TestReportPage(t *testing.T) {
	ta := newTestApp(t).login()
	const title = "Write report"
	id := ta.addTask(service.AddTaskIn{Title: title})
	for _, date := range []string{"10-26", "10-27"} {
		ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{
			TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}, Title: "Work " + date, WorkDate: testutil.D(date),
			Notes: "**progress**", At: sql.Null[civil.Minute]{V: 9 * 60, Valid: true}}))
	}
	ta.mustOp(ta.Svc.Complete(t.Context(), ta.meta(), service.CompleteIn{ID: id, Expect: ta.taskExpect(id)}))
	before := ta.opKinds()
	body := ta.viewBody("/ui/report")
	assertInert(t, body)
	if got := reportHeadings(t, body); !reflect.DeepEqual(got, []string{"Mon 2026-10-26", "Tue 2026-10-27", "Wed 2026-10-28"}) {
		t.Errorf("day headings = %v", got)
	}
	if got := reportTaskLinks(t, body, id); !reflect.DeepEqual(got, []string{"#1 " + title, "#1", "#1"}) {
		t.Errorf("task annotations = %v", got)
	}
	requireText(t, body, "✓", "09:00")
	requireElementText(t, body, "strong", "progress")
	filter := scrapeForm(t, body, "/ui/report")
	if filter.Get("from") != "2026-10-26" || filter.Get("to") != "2026-11-01" || filter.Has("todo") || filter.Has("upcoming") {
		t.Errorf("default filters = %v", filter)
	}
	explicit := ta.viewBody("/ui/report?from=2026-10-26&to=2026-11-01")
	if nodeText(parseHTML(t, body)) != nodeText(parseHTML(t, explicit)) {
		t.Error("default and explicit current-week reports differ")
	}
	for _, path := range []string{"/ui/report?todo=1", "/ui/report?upcoming=1"} {
		body = ta.viewBody(path)
		want := []string{"Mon 2026-10-26", "Tue 2026-10-27", "Wed 2026-10-28"}
		if strings.HasSuffix(path, "todo=1") {
			want = append(want, "Triage", "Backlog")
		} else {
			want = append(want, "Upcoming")
		}
		if got := reportHeadings(t, body); !reflect.DeepEqual(got, want) {
			t.Errorf("%s headings = %v", path, got)
		}
	}
	if !reflect.DeepEqual(before, ta.opKinds()) {
		t.Error("report GET created a user operation")
	}
	ta.mustOp(ta.Svc.Reopen(t.Context(), ta.meta(), service.ReopenIn{ID: id, Expect: ta.taskExpect(id)}))
	body = ta.viewBody("/ui/report?from=yesterday&to=today")
	if got := reportTaskLinks(t, body, id); !reflect.DeepEqual(got, []string{"#1 " + title, "#1"}) {
		t.Errorf("first annotation did not reset with report range: %v", got)
	}
	requireText(t, body, " (reopened)")
}

func TestReportMarksDeletedTasks(t *testing.T) {
	ta := newTestApp(t).login()
	id := ta.addTask(service.AddTaskIn{Title: "Old task"})
	ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{
		TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}, Title: "Logged work"}))
	requireElementText(t, ta.viewBody("/ui/report"), "p", "Logged work [#1 Old task]")
	ta.mustOp(ta.Svc.Delete(t.Context(), ta.meta(), service.TaskRef{ID: id, Expect: ta.taskExpect(id)}))
	body := ta.viewBody("/ui/report")
	requireElementText(t, body, "p", "Logged work [#1 Old task] (deleted)")
	if links := reportTaskLinks(t, body, id); !slices.Equal(links, []string{"#1 Old task"}) {
		t.Errorf("deleted task links = %v", links)
	}
}

func TestReportFiltersAndSections(t *testing.T) {
	ta := newTestApp(t).login()
	for _, in := range []service.AddTaskIn{
		{Title: "Triage match"},
		{Title: "Backlog match", Place: &service.AddPlacement{How: "bottom"}},
		{Title: "Upcoming match", Place: &service.AddPlacement{How: "defer", Defer: testutil.Moment("10-30")}},
	} {
		in.Tags, in.Project = []string{"infra"}, "infra.web"
		id := ta.addTask(in)
		ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{
			TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}, Title: in.Title + " work"}))
	}
	for _, in := range []service.AddTaskIn{
		{Title: "Wrong tag", Tags: []string{"other"}, Project: "infra.web"},
		{Title: "Wrong project", Tags: []string{"infra"}, Project: "elsewhere"},
	} {
		id := ta.addTask(in)
		ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{
			TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}, Title: in.Title + " work"}))
	}
	ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{Title: "Standalone work"}))
	body := ta.viewBody("/ui/report?tag=infra&project=infra&todo=1&upcoming=1")
	filter := scrapeForm(t, body, "/ui/report")
	for key, want := range map[string]string{"tag": "infra", "project": "infra", "todo": "1", "upcoming": "1"} {
		if !reflect.DeepEqual(filter[key], []string{want}) {
			t.Errorf("filter %s = %v", key, filter[key])
		}
	}
	for section, title := range map[string]string{"Triage": "Triage match", "Backlog": "Backlog match", "Upcoming": "Upcoming match"} {
		if text := sectionText(t, body, section); !strings.Contains(text, title) {
			t.Errorf("%s lacks %q: %s", section, title, text)
		}
	}
	if got := dataIDs(t, body, "data-entry"); !reflect.DeepEqual(got, []string{"1", "2", "3"}) {
		t.Errorf("filtered report entries = %v", got)
	}
	for _, unwanted := range []string{"Wrong tag", "Wrong project", "Standalone work"} {
		if strings.Contains(nodeText(parseHTML(t, body)), unwanted) {
			t.Errorf("report filters retained %q", unwanted)
		}
	}
	if got := reportHeadings(t, body); !reflect.DeepEqual(got, []string{"Wed 2026-10-28", "Triage", "Backlog", "Upcoming"}) {
		t.Errorf("report section order = %v", got)
	}
}

func TestRecapReportEmpty(t *testing.T) {
	ta := newTestApp(t).login()
	recap := ta.viewBody("/ui/recap")
	requireText(t, recap, "No blocks on this date.", "No linked entries on this date.",
		"No unlinked entries on this date.", "No interruptions on this date.")

	report := ta.viewBody("/ui/report?todo=1&upcoming=1")
	requireText(t, report, "No entries in this date range.", "Nothing to triage.",
		"No backlog tasks.", "No upcoming tasks.")
}

func TestRecapReportDates(t *testing.T) {
	ta := newTestApp(t).login()
	for _, tc := range []struct{ path, field string }{
		{"/ui/recap?date=garbage", "date"}, {"/ui/recap?date=today+10:00", "date"},
		{"/ui/report?from=garbage", "from"}, {"/ui/report?to=garbage", "to"},
		{"/ui/report?from=today+10:00", "from"}, {"/ui/report?to=today+10:00", "to"},
		{"/ui/report?from=2026-10-28&to=2026-10-26", "to"},
		{"/ui/report?from=2025-10-26&to=2026-10-28", "to"}, // 367-day span
	} {
		w := ta.get(tc.path)
		if w.Code != http.StatusUnprocessableEntity || !slices.Contains(dataIDs(t, w.Body.String(), "data-code"), "validation") {
			t.Errorf("%s = %d %s", tc.path, w.Code, w.Body)
		}
		requireFieldErrors(t, w.Body.String(), tc.field)
		assertInert(t, w.Body.String())
	}
	// UTC is already Monday; the configured zone is still Sunday. WeekStart
	// must come from web options, including when resolving relative datespecs.
	ta.Clock.Set(testutil.Local(t, "2026-11-02T01:00Z"))
	var err error
	ta.H, err = web.New(web.Options{Svc: ta.Svc, Clock: ta.Clock, Zone: testutil.Zone,
		WeekStart: time.Sunday, Listen: testListen})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, from, to string }{
		{"/ui/report", "2026-11-01", "2026-11-07"},
		{"/ui/report?from=yesterday", "2026-10-31", "2026-11-01"},
		{"/ui/report?to=today", "2026-11-01", "2026-11-01"},
		{"/ui/report?from=mon&to=today", "2026-10-26", "2026-11-01"},
	} {
		form := scrapeForm(t, ta.viewBody(tc.path), "/ui/report")
		if form.Get("from") != tc.from || form.Get("to") != tc.to {
			t.Errorf("%s date filters = %v", tc.path, form)
		}
	}
	for _, tc := range []struct{ path, date string }{
		{"/ui/recap", "2026-11-01"}, {"/ui/recap?date=yesterday", "2026-10-31"}, {"/ui/recap?date=mon", "2026-10-26"},
	} {
		if got := scrapeForm(t, ta.viewBody(tc.path), "/ui/entries").Get("date"); got != tc.date {
			t.Errorf("%s log date = %s, want %s", tc.path, got, tc.date)
		}
	}
}

func TestReportEscapes(t *testing.T) {
	ta := newTestApp(t).login()
	const title = `<script>x</script>`
	const taskTitle = `"><img src=x onerror=alert(1)>`
	id := ta.addTask(service.AddTaskIn{Title: taskTitle})
	ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{Title: title, Notes: "[c](javascript:alert(1))"}))
	ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{
		TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}, Title: "Linked hostile task", Notes: "**safe**"}))
	for _, path := range []string{"/ui/report?todo=1&upcoming=1", "/ui/recap"} {
		body := ta.viewBody(path)
		assertInert(t, body)
		requireText(t, body, title, taskTitle)
		requireElementText(t, body, "strong", "safe")
	}
}

func TestRecapReportGETsOnlyReconcile(t *testing.T) {
	ta := newTestApp(t)
	ta.Clock.Set(testutil.Local(t, "2026-10-27 10:00"))
	id := ta.addTask(service.AddTaskIn{Title: "Missed block"})
	ta.mustOp(ta.Svc.Block(t.Context(), ta.meta(), service.BlockIn{TaskID: id, Expect: ta.taskExpect(id),
		Span: domain.BlockSpan{Date: testutil.D("10-27"), Start: 13 * 60, End: 15 * 60}}))
	ta.login()
	before := ta.opKinds()
	ta.Clock.Set(testutil.Local(t, "2026-10-28 10:00"))
	for range 2 {
		for _, path := range []string{"/ui/recap?date=yesterday", "/ui/report", "/ui/report?todo=1&upcoming=1"} {
			ta.viewBody(path)
		}
	}
	after := ta.opKinds()
	if len(after) <= len(before) || !reflect.DeepEqual(after[:len(before)], before) {
		t.Fatalf("GET did not preserve history and reconcile: %v -> %v", before, after)
	}
	for _, kind := range after[len(before):] {
		if !strings.HasPrefix(kind, "system.") {
			t.Errorf("GET created user op %q", kind)
		}
	}
}

func reportHeadings(t *testing.T, body string) []string {
	t.Helper()
	var headings []string
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "h2" {
			headings = append(headings, nodeText(n))
		}
	})
	return headings
}

func reportTaskLinks(t *testing.T, body string, id domain.TaskID) []string {
	t.Helper()
	var links []string
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" && attr(n, "href") == fmt.Sprintf("/ui/tasks/%d", id) {
			links = append(links, nodeText(n))
		}
	})
	return links
}

func sectionText(t *testing.T, body, label string) string {
	t.Helper()
	var text string
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "section" && attr(n, "aria-label") == label {
			text = nodeText(n)
		}
	})
	return text
}

func TestPageMarkdownBudget(t *testing.T) {
	ta := newTestApp(t).login()
	// Report reaches 256 KiB exactly with notes 0–3. Recap reads in reverse:
	// notes 5–2 use 255 KiB, so note 1 must fall back with 1 KiB still free.
	sizesKiB := []int{64, 64, 64, 64, 63, 64}
	for i, size := range sizesKiB {
		marker := fmt.Sprintf("**note %d**", i)
		ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{
			Title: "Work", Notes: marker + strings.Repeat("x", (size<<10)-len(marker)),
			At: sql.Null[civil.Minute]{V: civil.Minute(i), Valid: true},
		}))
	}
	// Recap renders the newest entry first via a row partial. Report renders oldest first.
	for _, path := range []string{"/ui/recap", "/ui/report", "/ui/recap"} {
		body := ta.viewBody(path)
		for i := range sizesKiB {
			marker := fmt.Sprintf("note %d", i)
			formatted := i < 4
			if path == "/ui/recap" {
				formatted = i >= 2
			}
			if strings.Contains(body, "<strong>"+marker+"</strong>") != formatted ||
				strings.Contains(body, "<pre class=\"notes-plain\">\n**"+marker+"**") == formatted {
				t.Errorf("%s: wrong format for %s", path, marker)
			}
		}
		if got := strings.Count(body, "Shown as plain text — too complex to format."); got != 2 {
			t.Errorf("%s: got %d fallback notices, want 2", path, got)
		}
	}
}
