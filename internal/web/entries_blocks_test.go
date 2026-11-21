package web_test

import (
	"database/sql"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/testutil"
)

func (ta *testApp) entry(id domain.EntryID) domain.EntryView {
	ta.t.Helper()
	entry, err := ta.Svc.GetEntry(ta.t.Context(), testutil.Owner(), id)
	if err != nil {
		ta.t.Fatal(err)
	}
	return entry
}

func (ta *testApp) block(id domain.BlockID) domain.BlockView {
	ta.t.Helper()
	block, err := ta.Svc.GetBlock(ta.t.Context(), testutil.Owner(), id)
	if err != nil {
		ta.t.Fatal(err)
	}
	return block
}

func TestAddEntryFromToday(t *testing.T) {
	for _, tc := range []struct {
		name, date string
		form       url.Values
		linked     bool
		at         sql.Null[civil.Minute]
	}{
		{"focus", "10-28", nil, true, sql.Null[civil.Minute]{}},
		{"standalone", "10-28", url.Values{"task_id": {""}}, false, sql.Null[civil.Minute]{}},
		{"whitespace", "10-28", url.Values{"task_id": {" \t "}}, false, sql.Null[civil.Minute]{}},
		{"yesterday", "10-27", url.Values{"task_id": {"1"}, "date": {"yesterday"}}, true, sql.Null[civil.Minute]{}},
		{"time", "10-28", url.Values{"task_id": {"#1"}, "at": {"9:30"}}, true, sql.Null[civil.Minute]{V: 570, Valid: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ta := newTestApp(t).login()
			ta.addTask(service.AddTaskIn{Title: "Replica", Place: &service.AddPlacement{How: "now"}})
			form := tc.form
			if form == nil {
				form = make(url.Values)
			}
			form.Set("title", "Fixed replica")
			assertRedirect(t, ta.submit("/ui/", "/ui/entries", form), "/ui/")
			entry := ta.entry(1)
			if entry.Title != "Fixed replica" || entry.Kind != domain.EntryWork || entry.TaskID.Valid != tc.linked ||
				(tc.linked && entry.TaskID.V != 1) || entry.WorkDate != testutil.D(tc.date) || entry.OccurredMin != tc.at {
				t.Errorf("added entry = %+v", entry)
			}
		})
	}
}

func TestAddEntryFromRecap(t *testing.T) {
	ta := newTestApp(t).login()
	assertRedirect(t, ta.submit("/ui/recap?date=yesterday", "/ui/entries", url.Values{"title": {"Yesterday's work"}}), "/ui/recap?date=yesterday")
	if entry := ta.entry(1); entry.WorkDate != testutil.D("10-27") || entry.TaskID.Valid {
		t.Errorf("recap entry = %+v", entry)
	}
}

func TestAddEntryValidation(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		form        url.Values
		status      int
	}{
		{"future time", "at", url.Values{"at": {"11:00"}}, http.StatusUnprocessableEntity},
		{"bad time", "at", url.Values{"at": {"25:00"}}, http.StatusUnprocessableEntity},
		{"future date", "date", url.Values{"date": {"2026-10-29"}}, http.StatusUnprocessableEntity},
		{"bad date", "date", url.Values{"date": {"someday"}}, http.StatusUnprocessableEntity},
		{"date with time", "date", url.Values{"date": {"yesterday 9:00"}}, http.StatusUnprocessableEntity},
		{"empty title", "title", url.Values{"title": {""}}, http.StatusUnprocessableEntity},
		{"bad task ID", "task_id", url.Values{"task_id": {"0"}}, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ta := newTestApp(t).login()
			if !tc.form.Has("title") {
				tc.form.Set("title", "Invalid")
			}
			w := ta.submit("/ui/log", "/ui/entries", tc.form)
			if w.Code != tc.status {
				t.Fatalf("validation = %d %s", w.Code, w.Body)
			}
			requireFieldErrors(t, w.Body.String(), tc.field)
			entries, err := ta.Svc.ListEntries(t.Context(), testutil.Owner(), service.ListEntriesIn{
				From: testutil.D("10-27"), To: testutil.D("10-28"), IncludeDeleted: true})
			if err != nil || len(entries.Items) != 0 || len(ta.opKinds()) != 0 {
				t.Fatalf("invalid entry persisted: %+v, %v; ops=%v", entries, err, ta.opKinds())
			}
		})
	}
}

func TestAddEntryWarningFlash(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: "Replica"})
	ta.mustOp(ta.Svc.Complete(t.Context(), ta.meta(), service.CompleteIn{ID: 1, Expect: ta.taskExpect(1)}))
	assertRedirect(t, ta.submit("/ui/log", "/ui/entries", url.Values{"task_id": {"1"}, "title": {"Follow-up"}}), "/ui/log")
	body := ta.viewBody("/ui/log")
	page := parseHTML(t, body)
	mark := requireElement(t, page, "span", "class", "flash-mark")
	if attr(mark, "aria-hidden") != "true" {
		t.Error("decorative flash mark is exposed to screen readers")
	}
	card := requireElement(t, page, "article", "data-entry", "1")
	if got := nodeText(requireElement(t, card, "span", "class", "ok")); got != "✓" {
		t.Errorf("completion mark = %q", got)
	}
	if got := nodeText(requireElement(t, page, "li", "class", "warn")); got != "⚠ logged against a done task" {
		t.Errorf("warning flash = %q", got)
	}
	if entry := ta.entry(2); !entry.TaskID.Valid || entry.TaskID.V != 1 || entry.Title != "Follow-up" {
		t.Errorf("closed task entry = %+v", entry)
	}
}

func TestAddEntryNotesCRLF(t *testing.T) {
	ta := newTestApp(t).login()
	assertRedirect(t, ta.submit("/ui/log", "/ui/entries", url.Values{"title": {"Notes"}, "notes": {"line1\r\nline2\r\n"}}), "/ui/log")
	if got := ta.entry(1).Notes; got != "line1\nline2\n" {
		t.Errorf("notes = %q", got)
	}
}

func TestAddEntryDoubleSubmitReplays(t *testing.T) {
	ta := newTestApp(t).login()
	form := scrapeForm(t, ta.viewBody("/ui/log"), "/ui/entries")
	form.Set("title", "Only once")
	form.Set("notes", "a\r\nb")
	for range 2 {
		assertRedirect(t, ta.post("/ui/entries", form), "/ui/log")
	}
	entries, err := ta.Svc.ListEntries(t.Context(), testutil.Owner(), service.ListEntriesIn{
		From: testutil.D("10-28"), To: testutil.D("10-28")})
	if err != nil || len(entries.Items) != 1 || !slices.Equal(ta.opKinds(), []string{"entry.add"}) || ta.entry(1).Notes != "a\nb" {
		t.Fatalf("replay entries=%+v, err=%v, ops=%v", entries, err, ta.opKinds())
	}
	if got := flashText(t, ta.viewBody("/ui/log")); !slices.Contains(strings.Split(got, "\n"), "already applied") {
		t.Errorf("replay flash = %q", got)
	}
}

func TestEditEntry(t *testing.T) {
	ta := newTestApp(t).login()
	ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{Title: "Old"}))
	assertRedirect(t, ta.submit("/ui/log", "/ui/entries/1/edit", url.Values{
		"title": {"Fixed replica"}, "notes": {"What changed"}, "date": {"yesterday"}, "at": {"8:15"},
	}), "/ui/log")
	entry := ta.entry(1)
	if entry.Title != "Fixed replica" || entry.Notes != "What changed" || entry.WorkDate != testutil.D("10-27") ||
		!entry.OccurredMin.Valid || entry.OccurredMin.V != 495 {
		t.Fatalf("edited entry = %+v", entry)
	}
	assertRedirect(t, ta.submit("/ui/log", "/ui/entries/1/edit", url.Values{"at": {""}}), "/ui/log")
	if entry := ta.entry(1); entry.OccurredMin.Valid {
		t.Errorf("time not cleared: %+v", entry)
	}
}

func TestEditEntryPartialForm(t *testing.T) {
	for _, field := range []string{"title", "notes", "date", "at"} {
		t.Run(field, func(t *testing.T) {
			ta := newTestApp(t).login()
			ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{
				Title: "Old", Notes: "Keep notes", At: sql.Null[civil.Minute]{V: 570, Valid: true},
			}))
			want := ta.entry(1).LogEntry
			form := url.Values{"title": nil, "notes": nil, "date": nil, "at": nil}
			switch field {
			case "title":
				form.Set(field, "New")
				want.Title = "New"
			case "notes":
				form.Set(field, "")
				want.Notes = ""
			case "date":
				form.Set(field, "yesterday")
				want.WorkDate = testutil.D("10-27")
			case "at":
				form.Set(field, "")
				want.OccurredMin = sql.Null[civil.Minute]{}
			}
			want.Version++
			assertRedirect(t, ta.submit("/ui/log", "/ui/entries/1/edit", form), "/ui/log")
			if got := ta.entry(1).LogEntry; !reflect.DeepEqual(got, want) {
				t.Errorf("partial %s edit = %+v, want %+v", field, got, want)
			}
		})
	}
}

func TestEditEntryNotesCRLF(t *testing.T) {
	ta := newTestApp(t).login()
	ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{Title: "Notes"}))
	assertRedirect(t, ta.submit("/ui/log", "/ui/entries/1/edit", url.Values{"notes": {"a\r\nb\r\n"}}), "/ui/log")
	if got := ta.entry(1).Notes; got != "a\nb\n" {
		t.Errorf("notes = %q", got)
	}
}

func TestEditEntryValidation(t *testing.T) {
	ta := newTestApp(t).login()
	ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{Title: "Unchanged"}))
	before, ops := ta.entry(1), ta.opKinds()
	for _, tc := range []struct{ field, value string }{
		{"date", ""}, {"date", "2026-10-29"}, {"date", "someday"}, {"date", "yesterday 9:00"},
		{"at", "11:00"}, {"at", "25:00"}, {"title", ""},
	} {
		w := ta.submit("/ui/log", "/ui/entries/1/edit", url.Values{tc.field: {tc.value}})
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s=%q = %d %s", tc.field, tc.value, w.Code, w.Body)
		}
		requireFieldErrors(t, w.Body.String(), tc.field)
		if !reflect.DeepEqual(ta.entry(1), before) || !slices.Equal(ta.opKinds(), ops) {
			t.Error("invalid edit changed entry or operations")
		}
	}
}

func TestEditCompleteEntryNotesOnly(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: "Completed"})
	ta.mustOp(ta.Svc.Complete(t.Context(), ta.meta(), service.CompleteIn{ID: 1, Expect: ta.taskExpect(1)}))
	form := scrapeForm(t, ta.viewBody("/ui/tasks/1"), "/ui/entries/1/edit")
	if !form.Has("notes") || form.Has("title") || form.Has("date") || form.Has("at") {
		t.Fatalf("complete entry edit fields = %v", form)
	}
	want := ta.entry(1).LogEntry
	want.Notes = "Completion notes"
	want.Version++
	form.Set("notes", want.Notes)
	assertRedirect(t, ta.post("/ui/entries/1/edit", form), "/ui/tasks/1")
	if got := ta.entry(1).LogEntry; !reflect.DeepEqual(got, want) {
		t.Errorf("complete entry edit = %+v, want %+v", got, want)
	}
	for _, field := range []string{"title", "date", "at"} {
		value := map[string]string{"title": "Forbidden", "date": "yesterday", "at": "8:15"}[field]
		w := ta.submit("/ui/tasks/1", "/ui/entries/1/edit", url.Values{field: {value}})
		if w.Code != http.StatusUnprocessableEntity || !reflect.DeepEqual(ta.entry(1).LogEntry, want) {
			t.Fatalf("complete entry accepted %s: %d %s", field, w.Code, w.Body)
		}
	}
}

func TestRelinkEntry(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: "Replica"})
	ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{Title: "Standalone"}))
	for _, standalone := range []string{"", " \t "} {
		assertRedirect(t, ta.submit("/ui/recap", "/ui/entries/1/relink", url.Values{"task_id": {"1"}}), "/ui/recap")
		if entry := ta.entry(1); !entry.TaskID.Valid || entry.TaskID.V != 1 {
			t.Fatalf("linked entry = %+v", entry)
		}
		assertRedirect(t, ta.submit("/ui/log", "/ui/entries/1/relink", url.Values{"task_id": {standalone}}), "/ui/log")
		if entry := ta.entry(1); entry.TaskID.Valid {
			t.Errorf("standalone entry = %+v", entry)
		}
	}
	w := ta.submit("/ui/log", "/ui/entries/1/relink", url.Values{"task_id": {"abc"}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad task ID = %d %s", w.Code, w.Body)
	}
	requireFieldErrors(t, w.Body.String(), "task_id")
}

func TestDeleteUndeleteEntry(t *testing.T) {
	ta := newTestApp(t).login()
	ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{Title: "Delete me"}))
	body := ta.viewBody("/ui/log")
	requireConfirm(t, body, "/ui/entries/1/delete")
	assertRedirect(t, ta.post("/ui/entries/1/delete", scrapeForm(t, body, "/ui/entries/1/delete")), "/ui/log")
	if !ta.entry(1).DeletedAt.Valid {
		t.Fatal("entry was not deleted")
	}
	if got := dataIDs(t, ta.viewBody("/ui/log"), "data-entry"); len(got) != 0 {
		t.Errorf("deleted entry still in log: %v", got)
	}
	assertRedirect(t, ta.submit("/ui/log?include_deleted=1", "/ui/entries/1/undelete", nil), "/ui/log?include_deleted=1")
	if ta.entry(1).DeletedAt.Valid {
		t.Error("entry was not restored")
	}
	if got := dataIDs(t, ta.viewBody("/ui/log"), "data-entry"); !slices.Equal(got, []string{"1"}) {
		t.Errorf("restored log entries = %v", got)
	}
}

func TestEntryHostileText(t *testing.T) {
	ta := newTestApp(t).login()
	const title = `"><img src=x onerror=alert(1)><script>x</script>`
	const notes = "<script>x</script>\n\n[c](javascript:alert(1)) **b**"
	assertRedirect(t, ta.submit("/ui/log", "/ui/entries", url.Values{"title": {title}, "notes": {notes}}), "/ui/log")
	body := ta.viewBody("/ui/log")
	assertInert(t, body)
	requireConfirmValue(t, body, "/ui/entries/1/delete", "Delete entry "+title+"?")
	form := scrapeForm(t, body, "/ui/entries/1/edit")
	if form.Get("title") != title || form.Get("notes") != notes {
		t.Errorf("lost literal entry text: %v", form)
	}
	form.Set("date", "")
	w := ta.post("/ui/entries/1/edit", form)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("error = %d %s", w.Code, w.Body)
	}
	assertInert(t, w.Body.String())
	requireFieldErrors(t, w.Body.String(), "date")
	kept := scrapeForm(t, w.Body.String(), "/ui/entries/1/edit")
	if kept.Get("title") != title || kept.Get("notes") != notes {
		t.Error("error page lost literal entry text")
	}
}

func TestReblockInPlace(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: "Scheduled"})
	staleTaskETag := domain.FormatETag(ta.Svc.Generation(), ta.task(1).Version)
	ta.mustOp(ta.Svc.Block(t.Context(), ta.meta(), service.BlockIn{TaskID: 1, Expect: ta.taskExpect(1),
		Span: domain.BlockSpan{Date: testutil.D("10-28"), Start: 780, End: 900}}))
	before := ta.block(1)
	form := scrapeForm(t, ta.viewBody("/ui/tasks/1"), "/ui/blocks/1/reblock")
	if form.Get("etag") != domain.FormatETag(ta.Svc.Generation(), before.Version) ||
		form.Get("task_etag") != domain.FormatETag(ta.Svc.Generation(), before.TaskVersion) {
		t.Fatalf("reblock preconditions = %v", form)
	}
	form.Set("span", "16:00-17:00")
	assertRedirect(t, ta.post("/ui/blocks/1/reblock", form), "/ui/tasks/1")
	block := ta.block(1)
	if block.ID != before.ID || block.Span != (domain.BlockSpan{Date: testutil.D("10-28"), Start: 960, End: 1020}) ||
		block.Status != domain.BlockPlanned || block.Version != before.Version+1 || len(ta.task(1).Blocks) != 1 {
		t.Fatalf("reblocked row = %+v", block)
	}
	ops := ta.opKinds()
	w := ta.submit("/ui/tasks/1", "/ui/blocks/1/reblock", url.Values{"span": {"17:00-18:00"}, "task_etag": {staleTaskETag}})
	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale task ETag = %d %s", w.Code, w.Body)
	}
	assertErrorCode(t, w.Body.String(), "precondition_failed")
	if !reflect.DeepEqual(ta.block(1), block) || !slices.Equal(ta.opKinds(), ops) {
		t.Error("stale reblock changed block or operations")
	}
}

func TestUnblock(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: "Scheduled", Place: &service.AddPlacement{How: "block",
		Block: domain.BlockSpan{Date: testutil.D("10-28"), Start: 780, End: 900}}})
	assertRedirect(t, ta.submit("/ui/", "/ui/blocks/1/unblock", nil), "/ui/")
	if block := ta.block(1); block.Status != domain.BlockCancelled || block.CancelReason != domain.CancelUser {
		t.Errorf("unblocked row = %+v", block)
	}
}

func TestOutcome(t *testing.T) {
	ta := newTestApp(t)
	ta.Clock.Set(testutil.Local(t, "2026-10-27 10:00"))
	ta.addTask(service.AddTaskIn{Title: "Scheduled work"})
	ta.mustOp(ta.Svc.Block(t.Context(), ta.meta(), service.BlockIn{TaskID: 1, Expect: ta.taskExpect(1),
		Span: domain.BlockSpan{Date: testutil.D("10-27"), Start: 780, End: 900}}))
	ta.Clock.Set(testutil.Local(t, "2026-10-28 10:00"))
	ta.login()
	const page = "/ui/recap?date=yesterday"
	const action = "/ui/blocks/1/outcome"
	for _, outcome := range []string{"worked", "auto", "missed"} {
		form := scrapeFormWhere(t, ta.viewBody(page), action, url.Values{"outcome": {outcome}})
		if outcome == "worked" && ta.block(1).Status != domain.BlockMissed {
			t.Fatal("fixture block was not missed")
		}
		assertRedirect(t, ta.post(action, form), page)
		block := ta.block(1)
		wantStatus, wantSource, wantReason := domain.BlockMissed, domain.OutcomeUser, domain.ReasonSlipped
		switch outcome {
		case "worked":
			wantStatus, wantReason = domain.BlockWorked, domain.ReasonContinue
		case "auto":
			wantSource = domain.OutcomeAuto
		}
		if block.Status != wantStatus || block.OutcomeSource != wantSource {
			t.Errorf("%s outcome = %+v", outcome, block)
		}
		if reasons := ta.task(1).Reasons; len(reasons) != 1 || reasons[0].Reason != wantReason {
			t.Errorf("%s reasons = %v", outcome, reasons)
		}
	}
	before, ops := ta.block(1), ta.opKinds()
	for _, outcome := range []string{"maybe", "", "done"} {
		w := ta.submit(page, action, url.Values{"outcome": {outcome}})
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("outcome %q = %d %s", outcome, w.Code, w.Body)
		}
		requireFieldErrors(t, w.Body.String(), "outcome")
		if !reflect.DeepEqual(ta.block(1), before) || !slices.Equal(ta.opKinds(), ops) {
			t.Error("invalid outcome changed block or operations")
		}
	}
}

func TestEntryBlockPreconditions(t *testing.T) {
	for _, action := range []string{
		"/ui/entries/1/edit", "/ui/entries/1/relink", "/ui/entries/1/delete", "/ui/entries/1/undelete",
		"/ui/blocks/1/reblock", "/ui/blocks/1/unblock", "/ui/blocks/1/outcome",
	} {
		t.Run(action, func(t *testing.T) {
			ta := newTestApp(t).login()
			ta.addTask(service.AddTaskIn{Title: "Scheduled", Place: &service.AddPlacement{How: "block",
				Block: domain.BlockSpan{Date: testutil.D("10-28"), Start: 780, End: 900}}})
			entry := ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{Title: "Unchanged"})).Entry
			page := "/ui/log"
			if strings.HasPrefix(action, "/ui/blocks/") {
				page = "/ui/recap"
			} else if strings.HasSuffix(action, "/undelete") {
				ta.mustOp(ta.Svc.DeleteEntry(t.Context(), ta.meta(), service.EntryRef{ID: entry.ID,
					Expect: domain.Expect{Gen: ta.Svc.Generation(), Version: entry.Version}}))
				page += "?include_deleted=1"
			}
			body := ta.viewBody(page)
			beforeEntry, beforeBlock, ops := ta.entry(1), ta.block(1), ta.opKinds()
			for _, tc := range []struct {
				name, key, value string
				status           int
			}{
				{"missing ETag", "etag", "", http.StatusPreconditionRequired},
				{"bad ETag", "etag", "bad", http.StatusBadRequest},
				{"stale ETag", "etag", domain.FormatETag(ta.Svc.Generation(), 999), http.StatusPreconditionFailed},
				{"bad path", "", "", http.StatusNotFound},
			} {
				form := scrapeForm(t, body, action)
				if strings.HasSuffix(action, "/reblock") {
					form.Set("span", "16:00-17:00")
				}
				path := action
				if tc.key == "" {
					path = strings.Replace(action, "/1/", "/bad/", 1)
				} else {
					form.Set(tc.key, tc.value)
				}
				w := ta.post(path, form)
				if w.Code != tc.status {
					t.Fatalf("%s = %d %s", tc.name, w.Code, w.Body)
				}
				if !reflect.DeepEqual(ta.entry(1), beforeEntry) || !reflect.DeepEqual(ta.block(1), beforeBlock) || !slices.Equal(ta.opKinds(), ops) {
					t.Errorf("%s changed state", tc.name)
				}
			}
		})
	}
}

func TestReblockValidation(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: "Scheduled", Place: &service.AddPlacement{How: "block",
		Block: domain.BlockSpan{Date: testutil.D("10-28"), Start: 780, End: 900}}})
	before, ops := ta.block(1), ta.opKinds()
	for _, tc := range []struct {
		form   url.Values
		status int
		field  string
	}{
		{url.Values{"span": {"16:00-17:00"}, "task_etag": {""}}, http.StatusPreconditionRequired, ""},
		{url.Values{"span": {"16:00-17:00"}, "task_etag": {"bad"}}, http.StatusBadRequest, ""},
		{url.Values{"span": {"17:00-16:00"}}, http.StatusUnprocessableEntity, "span"},
		{url.Values{"span": {""}}, http.StatusUnprocessableEntity, "span"},
	} {
		w := ta.submit("/ui/tasks/1", "/ui/blocks/1/reblock", tc.form)
		if w.Code != tc.status {
			t.Fatalf("%v = %d %s", tc.form, w.Code, w.Body)
		}
		if tc.field != "" {
			requireFieldErrors(t, w.Body.String(), tc.field)
		}
		if !reflect.DeepEqual(ta.block(1), before) || !slices.Equal(ta.opKinds(), ops) {
			t.Error("invalid reblock changed state")
		}
	}
}
