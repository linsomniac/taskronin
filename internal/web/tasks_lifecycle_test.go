package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/service"
	"github.com/linsomniac/taskronin/internal/testutil"
)

func TestTriageActions(t *testing.T) {
	for _, tc := range []struct {
		name, action string
		match, input url.Values
	}{
		{"keep", "place", url.Values{"how": {"keep"}}, nil},
		{"top", "place", url.Values{"how": {"top"}}, nil},
		{"bottom", "place", url.Values{"how": {"bottom"}}, nil},
		{"before", "place", url.Values{"how": {"before"}}, url.Values{"ref": {"2"}}},
		{"after", "place", url.Values{"how": {"after"}}, url.Values{"ref": {"2"}}},
		{"defer", "defer", nil, url.Values{"until": {"fri 9:00"}}},
		{"block", "block", nil, url.Values{"span": {"13:00-15:00"}}},
		{"complete", "complete", nil, nil},
		{"drop", "drop", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ta := newTestApp(t).login()
			ta.addTask(service.AddTaskIn{Title: "New task"})
			ta.addTask(service.AddTaskIn{Title: "Existing", Place: &service.AddPlacement{How: "bottom"}})
			rank := ta.task(1).Rank
			action := "/ui/tasks/1/" + tc.action
			// DF20: select the actual fixed-action form by its hidden value.
			form := scrapeFormWhere(t, ta.viewBody("/ui/triage"), action, tc.match)
			for key, values := range tc.input {
				form[key] = values
			}
			assertRedirect(t, ta.post(action, form), "/ui/triage")
			task := ta.task(1)
			if len(task.Reasons) != 0 {
				t.Errorf("placed task has reasons: %v", task.Reasons)
			}
			switch tc.name {
			case "keep", "top", "bottom", "before", "after":
				if task.View != domain.ViewBacklog || (tc.name == "keep" && task.Rank != rank) ||
					((tc.name == "top" || tc.name == "before") && task.Rank >= ta.task(2).Rank) ||
					((tc.name == "bottom" || tc.name == "after") && task.Rank <= ta.task(2).Rank) {
					t.Errorf("%s placement = %+v", tc.name, task)
				}
			case "defer":
				if task.View != domain.ViewUpcoming || !task.DeferDate.Valid || task.DeferDate.V != testutil.D("10-30") || !task.DeferMin.Valid || task.DeferMin.V != 540 {
					t.Errorf("deferred task = %+v", task)
				}
			case "block":
				if len(task.Blocks) != 1 || task.Blocks[0].Status != domain.BlockPlanned || task.Blocks[0].Span.Date != testutil.D("10-28") || task.Blocks[0].Span.Start != 780 || task.Blocks[0].Span.End != 900 {
					t.Errorf("blocks = %+v", task.Blocks)
				}
			case "complete":
				if task.Status != domain.StatusDone || !task.CompletedDate.Valid || task.CompletedDate.V != testutil.D("10-28") ||
					len(task.Entries) != 1 || task.Entries[0].Kind != domain.EntryComplete || task.Entries[0].WorkDate != testutil.D("10-28") || task.Entries[0].Title != "New task" {
					t.Errorf("completed task = %+v", task)
				}
			case "drop":
				if task.Status != domain.StatusDropped || task.CompletedDate.Valid || len(task.Entries) != 0 {
					t.Errorf("dropped task = %+v", task)
				}
			}
			ops, err := ta.Svc.ListOps(t.Context(), testutil.Owner(), service.ListOpsIn{})
			if err != nil || len(ops.Items) != 3 || ops.Items[0].Channel != domain.ChanWeb {
				t.Errorf("expected one web op: %+v, %v", ops, err)
			}
		})
	}
}

func TestDeferNone(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: "Deferred", Place: &service.AddPlacement{How: "defer", Defer: testutil.Moment("10-30")}})
	rank := ta.task(1).Rank
	form := scrapeFormWhere(t, ta.viewBody("/ui/upcoming"), "/ui/tasks/1/defer", url.Values{"until": {"none"}})
	assertRedirect(t, ta.post("/ui/tasks/1/defer", form), "/ui/upcoming")
	if task := ta.task(1); task.View != domain.ViewBacklog || task.Rank != rank || task.DeferDate.Valid || task.DeferMin.Valid || len(task.Reasons) != 0 {
		t.Errorf("undeferred task = %+v", task)
	}
}

func TestManualTriage(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: "Backlog", Place: &service.AddPlacement{How: "bottom"}})
	assertRedirect(t, ta.submit("/ui/tasks/1", "/ui/tasks/1/triage", nil), "/ui/tasks/1")
	if task := ta.task(1); task.View != domain.ViewTriage || len(task.Reasons) != 1 || task.Reasons[0].Reason != domain.ReasonManual {
		t.Errorf("manual triage = %+v", task)
	}
}

func TestCompleteFields(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: "Original"})
	for _, date := range []string{"2026-10-27 09:00", "tomorrow", "someday"} {
		before := ta.task(1)
		w := ta.submit("/ui/tasks/1", "/ui/tasks/1/complete", url.Values{"date": {date}, "as": {"Renamed"}})
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("date %q = %d %s", date, w.Code, w.Body)
		}
		assertErrorCode(t, w.Body.String(), "validation")
		requireFieldErrors(t, w.Body.String(), "date")
		if !reflect.DeepEqual(before, ta.task(1)) {
			t.Error("invalid completion changed task")
		}
	}
	assertRedirect(t, ta.submit("/ui/tasks/1", "/ui/tasks/1/complete", url.Values{
		"as": {"Renamed"}, "date": {"yesterday"}, "log": {"Did it"}, "notes": {"n"},
	}), "/ui/tasks/1")
	task := ta.task(1)
	if task.Title != "Renamed" || task.Status != domain.StatusDone || !task.CompletedDate.Valid || task.CompletedDate.V != testutil.D("10-27") ||
		len(task.Entries) != 1 || task.Entries[0].Kind != domain.EntryComplete || task.Entries[0].Title != "Did it" || task.Entries[0].Notes != "n" || task.Entries[0].WorkDate != testutil.D("10-27") {
		t.Errorf("completion fields = %+v", task)
	}
}

func TestCompleteEmptyDate(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: "Original"})
	assertRedirect(t, ta.submit("/ui/tasks/1", "/ui/tasks/1/complete", url.Values{"notes": {"a\r\nb"}}), "/ui/tasks/1")
	task := ta.task(1)
	if task.Title != "Original" || task.Status != domain.StatusDone || !task.CompletedDate.Valid || task.CompletedDate.V != testutil.D("10-28") ||
		len(task.Entries) != 1 || task.Entries[0].Title != "Original" || task.Entries[0].Notes != "a\nb" {
		t.Errorf("empty completion fields = %+v", task)
	}
}

func TestReopen(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: "Done"})
	ta.mustOp(ta.Svc.Complete(t.Context(), ta.meta(), service.CompleteIn{ID: 1, Expect: ta.taskExpect(1)}))
	body := ta.viewBody("/ui/tasks/1")
	requireConfirm(t, body, "/ui/tasks/1/reopen")
	checkbox := false
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "input" && attr(n, "name") == "remove_entry" && attr(n, "type") == "checkbox" && attr(n, "value") == "1" {
			checkbox = true
		}
	})
	if !checkbox {
		t.Fatal("missing remove_entry checkbox with value 1")
	}
	assertRedirect(t, ta.post("/ui/tasks/1/reopen", scrapeForm(t, body, "/ui/tasks/1/reopen")), "/ui/tasks/1")
	task := ta.task(1)
	if task.Status != domain.StatusOpen || task.CompletedDate.Valid || task.CompletedAt.Valid || len(task.Reasons) != 1 || task.Reasons[0].Reason != domain.ReasonReopened ||
		len(task.Entries) != 1 || !task.Entries[0].Reopened || task.Entries[0].DeletedAt.Valid {
		t.Fatalf("reopened task = %+v", task)
	}
	firstEntry := task.Entries[0].ID
	out := ta.mustOp(ta.Svc.Complete(t.Context(), ta.meta(), service.CompleteIn{ID: 1, Expect: ta.taskExpect(1)}))
	assertRedirect(t, ta.submit("/ui/tasks/1", "/ui/tasks/1/reopen", url.Values{"remove_entry": {"1"}}), "/ui/tasks/1")
	entry, err := ta.Svc.GetEntry(t.Context(), testutil.Owner(), out.Entry.ID)
	if err != nil || !entry.DeletedAt.Valid || !entry.Reopened {
		t.Errorf("removed completion entry = %+v, %v", entry, err)
	}
	if task = ta.task(1); task.Status != domain.StatusOpen || len(task.Reasons) != 1 || task.Reasons[0].Reason != domain.ReasonReopened || len(task.Entries) != 1 || task.Entries[0].ID != firstEntry {
		t.Errorf("reopen removed the wrong entry: %+v", task)
	}
	assertRedirect(t, ta.submit("/ui/tasks/1", "/ui/tasks/1/drop", nil), "/ui/tasks/1")
	assertRedirect(t, ta.submit("/ui/tasks/1", "/ui/tasks/1/reopen", nil), "/ui/tasks/1")
	if task = ta.task(1); task.Status != domain.StatusOpen || len(task.Reasons) != 1 || task.Reasons[0].Reason != domain.ReasonReopened {
		t.Errorf("reopened dropped task = %+v", task)
	}
}

func TestDeleteUndelete(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: `"><img src=x onerror=alert(1)>`})
	body := ta.viewBody("/ui/tasks/1")
	assertInert(t, body)
	requireConfirm(t, body, "/ui/tasks/1/delete")
	assertRedirect(t, ta.post("/ui/tasks/1/delete", scrapeForm(t, body, "/ui/tasks/1/delete")), "/ui/tasks/1")
	if task := ta.task(1); !task.DeletedAt.Valid || task.View != domain.ViewDeleted {
		t.Fatalf("deleted task = %+v", task)
	}
	assertRedirect(t, ta.submit("/ui/tasks/1", "/ui/tasks/1/undelete", nil), "/ui/tasks/1")
	if task := ta.task(1); task.DeletedAt.Valid || task.Status != domain.StatusOpen || task.View != domain.ViewTriage || len(task.Reasons) != 1 || task.Reasons[0].Reason != domain.ReasonManual {
		t.Errorf("undeleted task = %+v", task)
	}
}

func TestAcceptNew(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: "First"})
	ta.addTask(service.AddTaskIn{Title: "Second"})
	due := testutil.Moment("10-28")
	ta.addTask(service.AddTaskIn{Title: "Due", Due: &due})
	form := scrapeForm(t, ta.viewBody("/ui/triage"), "/ui/triage/accept-new")
	if !slices.Equal(form["id"], []string{"1", "2"}) || !slices.Equal(form["etag"], []string{
		domain.FormatETag(ta.Svc.Generation(), ta.task(1).Version), domain.FormatETag(ta.Svc.Generation(), ta.task(2).Version),
	}) {
		t.Fatalf("accept-new items = %v", form)
	}
	// A new eligible task arriving after render must not be swept into the batch.
	ta.addTask(service.AddTaskIn{Title: "Not on the page"})
	before := ta.opKinds()
	for range 2 {
		assertRedirect(t, ta.post("/ui/triage/accept-new", form), "/ui/triage")
	}
	first, second := ta.task(1), ta.task(2)
	if first.View != domain.ViewBacklog || second.View != domain.ViewBacklog || len(first.Reasons) != 0 || len(second.Reasons) != 0 || first.Rank >= second.Rank {
		t.Errorf("accepted tasks = %+v, %+v", first, second)
	}
	if ta.task(3).View != domain.ViewTriage || ta.task(4).View != domain.ViewTriage || !slices.Equal(ta.opKinds(), append(before, "task.accept_new")) {
		t.Errorf("batch accepted unlisted tasks or replayed twice: %v", ta.opKinds())
	}
}

func TestAcceptNewPageLimit(t *testing.T) {
	ta := newTestApp(t).login()
	for i := range 101 {
		ta.addTask(service.AddTaskIn{Title: fmt.Sprintf("Task %d", i+1)})
	}
	const path = "/ui/triage/accept-new"
	form := scrapeForm(t, ta.viewBody("/ui/triage"), path)
	if len(form["id"]) != 100 || len(form["etag"]) != 100 {
		t.Fatal("expected 100 rendered id/etag pairs")
	}
	before := ta.opKinds()
	form.Add("id", "101")
	form.Add("etag", domain.FormatETag(ta.Svc.Generation(), ta.task(101).Version))
	w := ta.post(path, form)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("101 pairs returned %d, want 400", w.Code)
	}
	assertErrorCode(t, w.Body.String(), "malformed")
	requireElementText(t, w.Body.String(), "p", "expected at most 100 id and etag pairs")
	if !slices.Equal(before, ta.opKinds()) {
		t.Fatal("oversize batch recorded an operation")
	}
	form["id"], form["etag"] = form["id"][:100], form["etag"][:100]
	assertRedirect(t, ta.post(path, form), "/ui/triage")
	if !slices.Equal(ta.opKinds(), append(before, "task.accept_new")) || ta.task(100).View != domain.ViewBacklog || ta.task(101).View != domain.ViewTriage {
		t.Fatal("100 pairs did not accept exactly the page")
	}
}

func TestAcceptNewInvalidLists(t *testing.T) {
	for _, tc := range []struct {
		name, code, field, message string
		status                     int
		input                      url.Values
	}{
		{"unequal lengths", "malformed", "", "expected equal nonempty id and etag lists", 400, url.Values{"id": {"1", "2"}, "etag": {"first"}}},
		{"empty lists", "malformed", "", "expected equal nonempty id and etag lists", 400, url.Values{"id": nil, "etag": nil}},
		{"extra etag", "malformed", "", "expected equal nonempty id and etag lists", 400, url.Values{"id": {"1"}, "etag": {"first", "second"}}},
		{"bad second id", "malformed", "id", "", 400, url.Values{"id": {"1", "bad"}}},
		{"bad second etag", "malformed", "", "invalid entity ETag", 400, url.Values{"etag": {"first", "bad"}}},
		{"missing second etag", "precondition_required", "", "missing etag", 428, url.Values{"etag": {"first", ""}}},
		{"stale second etag", "precondition_failed", "", "This changed elsewhere since the page was loaded — reload.", 412, url.Values{"etag": {"first", "stale"}}},
		{"duplicate", "validation", "", "duplicate task #1", 422, url.Values{"id": {"1", "1"}, "etag": {"first", "first"}}},
		{"ineligible", "conflict", "", "task #3 must have exactly the new reason", 409, url.Values{"id": {"1", "3"}, "etag": {"first", "third"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ta := newTestApp(t).login()
			ta.addTask(service.AddTaskIn{Title: "First"})
			ta.addTask(service.AddTaskIn{Title: "Second"})
			due := testutil.Moment("10-28")
			ta.addTask(service.AddTaskIn{Title: "Due", Due: &due})
			// Rendering reconciles the due reason, including its version/op.
			// Capture rejection baselines and ETags after that allowed GET work.
			ta.viewBody("/ui/triage")
			etags := map[string]string{
				"first":  domain.FormatETag(ta.Svc.Generation(), ta.task(1).Version),
				"second": domain.FormatETag(ta.Svc.Generation(), ta.task(2).Version),
				"third":  domain.FormatETag(ta.Svc.Generation(), ta.task(3).Version),
				"stale":  domain.FormatETag(ta.Svc.Generation(), ta.task(2).Version+1),
			}
			for i, value := range tc.input["etag"] {
				if etag, ok := etags[value]; ok {
					tc.input["etag"][i] = etag
				}
			}
			before := ta.opKinds()
			first, second := ta.task(1), ta.task(2)
			w := ta.submit("/ui/triage", "/ui/triage/accept-new", tc.input)
			if w.Code != tc.status {
				t.Fatalf("invalid batch = %d %s", w.Code, w.Body)
			}
			body := w.Body.String()
			assertErrorCode(t, body, tc.code)
			if tc.field != "" {
				requireFieldErrors(t, body, tc.field)
			} else {
				requireFieldErrors(t, body)
				requireElementText(t, body, "p", tc.message)
			}
			if !slices.Equal(before, ta.opKinds()) || !reflect.DeepEqual(first, ta.task(1)) || !reflect.DeepEqual(second, ta.task(2)) {
				t.Error("invalid batch was partially applied")
			}
		})
	}
}

func TestTaskActionPreconditions(t *testing.T) {
	ta := newTestApp(t).login()
	open := ta.addTask(service.AddTaskIn{Title: "Open task"})
	closed := ta.addTask(service.AddTaskIn{Title: "Closed task"})
	deleted := ta.addTask(service.AddTaskIn{Title: "Deleted task"})
	ta.mustOp(ta.Svc.Place(t.Context(), ta.meta(), service.PlaceIn{ID: open, Expect: ta.taskExpect(open), How: domain.PlaceBottom}))
	ta.mustOp(ta.Svc.Drop(t.Context(), ta.meta(), service.TaskRef{ID: closed, Expect: ta.taskExpect(closed)}))
	ta.mustOp(ta.Svc.Delete(t.Context(), ta.meta(), service.TaskRef{ID: deleted, Expect: ta.taskExpect(deleted)}))

	for _, tc := range []struct {
		action string
		id     domain.TaskID
	}{
		{"place", open}, {"defer", open}, {"triage", open}, {"block", open},
		{"complete", open}, {"reopen", closed}, {"drop", open}, {"delete", open}, {"undelete", deleted},
	} {
		page := fmt.Sprintf("/ui/tasks/%d", tc.id)
		path := page + "/" + tc.action
		body := ta.viewBody(page)
		stale := domain.FormatETag(ta.Svc.Generation(), ta.task(tc.id).Version-1)
		for _, etag := range []struct {
			name, value, code string
			status            int
		}{
			{"missing", "", "precondition_required", http.StatusPreconditionRequired},
			{"stale", stale, "precondition_failed", http.StatusPreconditionFailed},
		} {
			t.Run(tc.action+"/"+etag.name, func(t *testing.T) {
				form := scrapeForm(t, body, path)
				if tc.action == "defer" {
					form.Set("until", "tomorrow")
				}
				if tc.action == "block" {
					form.Set("span", "13:00-15:00")
				}
				form.Del("etag")
				if etag.value != "" {
					form.Set("etag", etag.value)
				}
				before := ta.opKinds()
				w := ta.post(path, form)
				if w.Code != etag.status {
					t.Errorf("got status %d, want %d", w.Code, etag.status)
				}
				assertErrorCode(t, w.Body.String(), etag.code)
				if !slices.Equal(before, ta.opKinds()) {
					t.Error("rejected action recorded an operation")
				}
			})
		}
	}
}

func TestStaleETagShowsReloadError(t *testing.T) {
	for _, hx := range []bool{false, true} {
		for _, action := range []string{"place", "complete"} {
			t.Run(fmt.Sprintf("%s/htmx=%t", action, hx), func(t *testing.T) {
				ta := newTestApp(t).login()
				ta.addTask(service.AddTaskIn{Title: "Original"})
				pagePath := "/ui/triage"
				match := url.Values{"how": {"keep"}}
				if action == "complete" {
					pagePath, match = "/ui/tasks/1", nil
				}
				path := "/ui/tasks/1/" + action
				form := scrapeFormWhere(t, ta.viewBody(pagePath), path, match)
				if action == "complete" {
					form.Set("as", "Typed title")
					form.Set("log", "Typed log")
					form.Set("notes", "Typed notes\n<script>x</script>")
				}
				title := "Changed from CLI"
				ta.mustOp(ta.Svc.UpdateTask(t.Context(), testutil.Meta(testutil.Owner(), ta.Svc.Generation()), service.UpdateTaskIn{
					ID: 1, Expect: ta.taskExpect(1), Title: &title,
				}))
				before := ta.opKinds()
				var headers []string
				status := http.StatusPreconditionFailed
				if hx {
					headers = []string{"HX-Request", "true", "HX-Boosted", "true"}
					status = http.StatusOK
				}
				w := ta.post(path, form, headers...)
				if w.Code != status {
					t.Fatalf("stale submit = %d %s", w.Code, w.Body)
				}
				if hx && (w.Header().Get("HX-Push-Url") != "false" || w.Header().Get("HX-Retarget") != "body" || w.Header().Get("HX-Reswap") != "innerHTML") {
					t.Errorf("htmx error headers = %v", w.Header())
				}
				body := w.Body.String()
				assertErrorCode(t, body, "precondition_failed")
				requireText(t, body, "changed elsewhere", "reload")
				if action == "complete" {
					requireText(t, body, "Your input — reload, then paste")
				} else if strings.Contains(body, "Your input") {
					t.Error("button-only action shows empty input heading")
				}
				assertInert(t, body)
				saved := readOnlyInput(t, resubmitBody(t, body))
				for key, values := range form {
					if key != "_csrf" && key != "_nonce" && !slices.Equal(saved[key], values) {
						t.Errorf("lost stale input %s: %v, want %v", key, saved[key], values)
					}
				}
				reload := false
				readonly := make(map[string]string)
				walkHTML(parseHTML(t, body), func(n *html.Node) {
					if n.Type != html.ElementNode {
						return
					}
					if n.Data == "a" && nodeText(n) == "reload" && attr(n, "href") == pagePath {
						reload = true
					}
					if hasAttr(n, "readonly") {
						readonly[attr(n, "name")] = n.Data + ":" + attr(n, "type")
					}
				})
				if !reload {
					t.Error("missing reload link to original page")
				}
				if action == "complete" && (readonly["as"] != "input:text" || readonly["log"] != "input:text" || readonly["notes"] != "textarea:") {
					t.Errorf("typed input lacks visible read-only controls: %v", readonly)
				}
				if task := ta.task(1); task.Title != title || task.View != domain.ViewTriage || !slices.Equal(before, ta.opKinds()) {
					t.Errorf("stale action changed task: %+v", task)
				}
			})
		}
	}
}

func TestGenerationAndNonceErrors(t *testing.T) {
	for _, tc := range []struct {
		field, value, code string
		status             int
	}{
		{"_gen", "ffffffffffffffff", "generation_mismatch", 412},
		{"_nonce", "", "idempotency_key_required", 428},
		{"_nonce", "bad nonce!", "malformed", 400},
		{"etag", "", "precondition_required", 428},
	} {
		t.Run(tc.code, func(t *testing.T) {
			ta := newTestApp(t).login()
			ta.addTask(service.AddTaskIn{Title: "New"})
			form := scrapeFormWhere(t, ta.viewBody("/ui/triage"), "/ui/tasks/1/place", url.Values{"how": {"keep"}})
			form.Set(tc.field, tc.value)
			before := ta.opKinds()
			w := ta.post("/ui/tasks/1/place", form)
			if w.Code != tc.status {
				t.Fatalf("%s = %d %s", tc.code, w.Code, w.Body)
			}
			assertErrorCode(t, w.Body.String(), tc.code)
			if ta.task(1).View != domain.ViewTriage || !slices.Equal(before, ta.opKinds()) {
				t.Error("invalid preconditions applied the action")
			}
		})
	}
}

func TestPlacementValidation(t *testing.T) {
	for _, tc := range []struct {
		action, field, value, code string
		status                     int
	}{
		{"place", "how", "sideways", "validation", 422},
		{"place", "ref", "bad", "malformed", 400},
		{"defer", "until", "", "validation", 422},
		{"defer", "until", "someday", "validation", 422},
		{"block", "span", "15:00-13:00", "validation", 422},
	} {
		t.Run(tc.action+"/"+tc.field+"/"+tc.value, func(t *testing.T) {
			ta := newTestApp(t).login()
			ta.addTask(service.AddTaskIn{Title: "New"})
			path := "/ui/tasks/1/" + tc.action
			match := url.Values(nil)
			if tc.action == "place" {
				match = url.Values{"how": {"before"}}
			}
			form := scrapeFormWhere(t, ta.viewBody("/ui/triage"), path, match)
			form.Set(tc.field, tc.value)
			before := ta.opKinds()
			w := ta.post(path, form)
			if w.Code != tc.status {
				t.Fatalf("validation = %d %s", w.Code, w.Body)
			}
			assertErrorCode(t, w.Body.String(), tc.code)
			requireFieldErrors(t, w.Body.String(), tc.field)
			if ta.task(1).View != domain.ViewTriage || !slices.Equal(before, ta.opKinds()) {
				t.Error("invalid placement applied the action")
			}
		})
	}
}
