package web_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/testutil"
	"taskronin/internal/web"
)

func TestFocusSetAndClear(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: "First focus", Place: &service.AddPlacement{How: "bottom"}})
	ta.addTask(service.AddTaskIn{Title: "Second focus"})
	form := scrapeFormWhere(t, ta.viewBody("/ui/backlog"), "/ui/focus", url.Values{"task_id": {"1"}})
	assertRedirect(t, ta.post("/ui/focus", form), "/ui/backlog")
	today, err := ta.Svc.Today(t.Context(), testutil.Owner())
	if err != nil || today.Focus == nil || today.Focus.ID != 1 || len(today.Interruptions) != 0 {
		t.Fatalf("focus set = %+v, %v", today, err)
	}
	requireText(t, sectionText(t, ta.viewBody("/ui/"), "Focus"), "First focus")
	clearForm := scrapeFormWhere(t, ta.viewBody("/ui/"), "/ui/focus", url.Values{"task_id": {""}})
	assertRedirect(t, ta.post("/ui/focus", clearForm), "/ui/")
	today, err = ta.Svc.Today(t.Context(), testutil.Owner())
	if err != nil || today.Focus != nil {
		t.Fatalf("focus clear = %+v, %v", today, err)
	}
	requireText(t, ta.viewBody("/ui/"), "No focus. Set one by task ID, or use Focus on a task.")
	assertRedirect(t, ta.submit("/ui/tasks/1", "/ui/focus", nil), "/ui/tasks/1")
	// Select Today's set form (the clear form has the same action).
	body := ta.viewBody("/ui/")
	foundInterrupt := false
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "form" && attr(n, "action") == "/ui/focus" {
			walkHTML(n, func(input *html.Node) {
				if input.Type == html.ElementNode && attr(input, "name") == "interrupt" {
					foundInterrupt = true
					if attr(input, "type") != "checkbox" || attr(input, "value") != "1" {
						t.Error("interrupt must be a checkbox with value 1")
					}
				}
			})
		}
	})
	if !foundInterrupt {
		t.Fatal("missing interrupt checkbox")
	}
	forms := scrapeForms(t, body, "/ui/focus")
	form = forms[len(forms)-1]
	form.Set("task_id", "2")
	form.Set("interrupt", "1")
	assertRedirect(t, ta.post("/ui/focus", form), "/ui/")
	today, err = ta.Svc.Today(t.Context(), testutil.Owner())
	if err != nil || today.Focus == nil || today.Focus.ID != 2 || len(today.Interruptions) != 1 {
		t.Fatalf("interrupted focus = %+v, %v", today, err)
	}
	in := today.Interruptions[0]
	if !in.FromTaskID.Valid || in.FromTaskID.V != 1 || in.ToTaskID != 2 || in.FromBlockID.Valid {
		t.Errorf("interruption = %+v", in)
	}
}

func TestFocusInvalidTaskID(t *testing.T) {
	ta := newTestApp(t).login()
	for _, id := range []string{"bad", "0", "-1", "9223372036854775808"} {
		w := ta.submit("/ui/", "/ui/focus", url.Values{"task_id": {id}})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("focus %q = %d", id, w.Code)
		}
		assertErrorCode(t, w.Body.String(), "malformed")
		requireFieldErrors(t, w.Body.String(), "task_id")
	}
	if len(ta.opKinds()) != 0 {
		t.Error("invalid focus created an operation")
	}
}

func TestFocusInterruptRequiresOne(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: "First focus"})
	ta.addTask(service.AddTaskIn{Title: "Second focus"})
	for _, interrupt := range []string{"", "on", "1"} {
		ta.mustOp(ta.Svc.SetFocus(t.Context(), ta.meta(), service.SetFocusIn{
			TaskID: sql.Null[domain.TaskID]{V: 1, Valid: true}}))
		forms := scrapeForms(t, ta.viewBody("/ui/"), "/ui/focus")
		form := forms[len(forms)-1] // Today's set form follows the explicit clear form.
		if form.Has("interrupt") {
			t.Fatal("unchecked checkbox submitted a value")
		}
		form.Set("task_id", "2")
		if interrupt != "" {
			form.Set("interrupt", interrupt)
		}
		assertRedirect(t, ta.post("/ui/focus", form), "/ui/")
		today, err := ta.Svc.Today(t.Context(), testutil.Owner())
		want := 0
		if interrupt == "1" {
			want = 1
		}
		if err != nil || today.Focus == nil || today.Focus.ID != 2 || len(today.Interruptions) != want {
			t.Errorf("interrupt=%q: focus=%+v interruptions=%d, want focus #2 and %d interruptions; err=%v",
				interrupt, today.Focus, len(today.Interruptions), want, err)
		}
	}
}

func TestFocusTaskIDRequired(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: "Keep focused"})
	assertRedirect(t, ta.submit("/ui/tasks/1", "/ui/focus", nil), "/ui/tasks/1")
	before := ta.opKinds()
	w := ta.submit("/ui/", "/ui/focus", url.Values{"task_id": {}})
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing task_id = %d, want 400", w.Code)
	} else {
		assertErrorCode(t, w.Body.String(), "malformed")
		requireFieldErrors(t, w.Body.String(), "task_id")
	}
	today, err := ta.Svc.Today(t.Context(), testutil.Owner())
	if err != nil || today.Focus == nil || today.Focus.ID != 1 || !slices.Equal(before, ta.opKinds()) {
		t.Fatalf("missing task_id changed focus or history: focus=%+v err=%v", today.Focus, err)
	}
	clearForm := scrapeFormWhere(t, ta.viewBody("/ui/"), "/ui/focus", url.Values{"task_id": {""}})
	assertRedirect(t, ta.post("/ui/focus", clearForm), "/ui/")
	today, err = ta.Svc.Today(t.Context(), testutil.Owner())
	if err != nil || today.Focus != nil || len(ta.opKinds()) != len(before)+1 {
		t.Errorf("explicit clear failed: focus=%+v err=%v", today.Focus, err)
	}
}

func TestUND19_WebUndoRevertsCLIOp(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: "CLI task"})
	assertRedirect(t, ta.submit("/ui/", "/ui/undo", nil), "/ui/")
	if !ta.task(1).DeletedAt.Valid {
		t.Fatal("undo did not tombstone CLI task")
	}
	ops, err := ta.Svc.ListOps(t.Context(), testutil.Owner(), service.ListOpsIn{})
	if err != nil || len(ops.Items) != 2 || ops.Items[0].Via != "undo" || ops.Items[0].Channel != domain.ChanWeb || ops.Items[1].Channel != domain.ChanCLI {
		t.Fatalf("undo ops = %+v, %v", ops, err)
	}
	assertRedirect(t, ta.submit("/ui/", "/ui/redo", nil), "/ui/")
	if ta.task(1).DeletedAt.Valid {
		t.Error("redo did not restore CLI task")
	}
}

func TestUndoEmptyStack(t *testing.T) {
	ta := newTestApp(t).login()
	for _, action := range []string{"undo", "redo"} {
		w := ta.submit("/ui/", "/ui/"+action, nil)
		if w.Code != http.StatusConflict {
			t.Fatalf("%s empty = %d", action, w.Code)
		}
		assertErrorCode(t, w.Body.String(), "nothing_to_"+action)
		if len(scrapeForms(t, resubmitBody(t, w.Body.String()), "/ui/"+action)) != 0 {
			t.Errorf("empty %s stack offers a retry form", action)
		}
		if len(scrapeForms(t, w.Body.String(), "/ui/"+action)) != 1 {
			t.Errorf("expected only the explicit nav %s form", action)
		}
	}
}

func TestUndoStaleGeneration(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: "Keep until fresh undo"})
	w := ta.submit("/ui/", "/ui/undo", url.Values{"_gen": {"ffffffffffffffff"}})
	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale undo = %d", w.Code)
	}
	assertErrorCode(t, w.Body.String(), "generation_mismatch")
	if ta.task(1).DeletedAt.Valid || len(ta.opKinds()) != 1 {
		t.Fatal("stale undo changed task or history")
	}
	assertRedirect(t, ta.submit("/ui/", "/ui/undo", nil), "/ui/")
	if !ta.task(1).DeletedAt.Valid {
		t.Error("fresh undo did not succeed")
	}
}

func TestUndoRedoTopbarForms(t *testing.T) {
	ta := newTestApp(t).login()
	body := ta.viewBody("/ui/backlog?tag=work") // checks hidden fields and unique nonces
	var actions []string
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "div" && attr(n, "class") == "topbar-actions" {
			walkHTML(n, func(form *html.Node) {
				if form.Type == html.ElementNode && form.Data == "form" && attr(form, "method") == "post" {
					actions = append(actions, attr(form, "action"))
				}
			})
		}
	})
	for _, action := range []string{"/ui/undo", "/ui/redo"} {
		if !slices.Contains(actions, action) || len(scrapeForms(t, body, action)) != 1 {
			t.Errorf("top bar actions must contain one POST form for %s", action)
		}
	}
}

func TestUndoRedoDoubleSubmitAndServerRetry(t *testing.T) {
	for _, action := range []string{"undo", "redo"} {
		t.Run(action, func(t *testing.T) {
			ta := newTestApp(t).login()
			ta.addTask(service.AddTaskIn{Title: "First"})
			ta.addTask(service.AddTaskIn{Title: "Second"})
			if action == "redo" {
				ta.mustOp(ta.Svc.Undo(t.Context(), ta.meta()))
				ta.mustOp(ta.Svc.Undo(t.Context(), ta.meta()))
			}
			before := len(ta.opKinds())
			path := "/ui/" + action
			form := scrapeForm(t, ta.viewBody("/ui/"), path)
			for range 2 {
				assertRedirect(t, ta.post(path, form), "/ui/")
			}
			requireText(t, ta.viewBody("/ui/"), "already applied")
			// DF25: a response can fail after commit. Retry the actual error form.
			r := errorPost(t, form)
			r.URL.Path = path
			w := httptest.NewRecorder()
			ta.App.RenderError(w, r, domain.Errorf(domain.CodeInternal, "response lost"))
			retry := scrapeFormWhere(t, w.Body.String(), path, url.Values{"_nonce": {form.Get("_nonce")}})
			retry.Set("_csrf", form.Get("_csrf"))
			assertRedirect(t, ta.post(path, retry), "/ui/")
			if ta.task(1).DeletedAt.Valid || !ta.task(2).DeletedAt.Valid || len(ta.opKinds()) != before+1 {
				t.Errorf("%s replay advanced the stack: task1 deleted=%v task2 deleted=%v ops=%v", action,
					ta.task(1).DeletedAt.Valid, ta.task(2).DeletedAt.Valid, ta.opKinds())
			}
		})
	}
}

func TestRevertFromAIChanges(t *testing.T) {
	ta := newTestApp(t).login()
	id := ta.addTask(service.AddTaskIn{Title: "Task"})
	op := ta.mustOp(ta.Svc.AddEntry(t.Context(), testutil.Meta(testutil.AI(), ta.Svc.Generation()), service.AddEntryIn{
		Title: "AI work", TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}}))
	action := "/ui/ops/" + op.OpID.String() + "/revert"
	form := scrapeForm(t, aiChangesBody(t, ta, "/ui/ai-changes"), action)
	assertRedirect(t, ta.post(action, form), "/ui/ai-changes")
	if !ta.entry(op.Entry.ID).DeletedAt.Valid {
		t.Fatal("revert did not delete AI entry")
	}
	assertRedirect(t, ta.post(action, form), "/ui/ai-changes")
	form.Set("_nonce", scrapeForm(t, ta.viewBody("/ui/"), "/ui/logout").Get("_nonce"))
	w := ta.post(action, form)
	if w.Code != http.StatusConflict {
		t.Fatalf("second revert = %d", w.Code)
	}
	assertErrorCode(t, w.Body.String(), "revert_conflict")
	requireText(t, w.Body.String(), "already reverted")
}

func TestRevertFromHistoryAndPreconditions(t *testing.T) {
	ta := newTestApp(t).login()
	op := ta.mustOp(ta.Svc.AddTask(t.Context(), ta.meta(), service.AddTaskIn{Title: "History task"}))
	action := "/ui/ops/" + op.OpID.String() + "/revert"
	for _, tc := range []struct {
		field, value, code string
		status             int
	}{
		{"_gen", "ffffffffffffffff", "generation_mismatch", http.StatusPreconditionFailed},
		{"_gen", "", "precondition_required", http.StatusPreconditionRequired},
		{"_nonce", "", "idempotency_key_required", http.StatusPreconditionRequired},
	} {
		form := scrapeForm(t, ta.viewBody("/ui/tasks/1/history"), action)
		form.Set(tc.field, tc.value)
		w := ta.post(action, form)
		if w.Code != tc.status {
			t.Fatalf("revert %s=%q = %d", tc.field, tc.value, w.Code)
		}
		assertErrorCode(t, w.Body.String(), tc.code)
	}
	form := scrapeForm(t, ta.viewBody("/ui/tasks/1/history"), action)
	for _, id := range []string{"bad", "1", strings.Repeat("A", 32), strings.Repeat("g", 32), strings.Repeat("0", 32)} {
		w := ta.post("/ui/ops/"+id+"/revert", form)
		if w.Code != http.StatusNotFound {
			t.Fatalf("revert invalid/missing op %q = %d", id, w.Code)
		}
		assertErrorCode(t, w.Body.String(), "not_found")
	}
	if ta.task(1).DeletedAt.Valid || len(ta.opKinds()) != 1 {
		t.Fatal("rejected revert mutated history")
	}
	assertRedirect(t, ta.post(action, form), "/ui/tasks/1/history")
	if !ta.task(1).DeletedAt.Valid {
		t.Error("history revert did not delete task")
	}
}

func TestMarkAIReviewed(t *testing.T) {
	ta := newTestApp(t).login()
	ta.mustOp(ta.Svc.AddEntry(t.Context(), testutil.Meta(testutil.AI(), ta.Svc.Generation()), service.AddEntryIn{Title: "AI work"}))
	if !slices.Contains(dataIDs(t, ta.viewBody("/ui/"), "data-banner"), "ai_changes") {
		t.Fatal("missing AI changes banner before review")
	}
	changes := aiChanges(t, ta)
	for _, through := range []string{"bad", "", "0", "-1", "9223372036854775808", fmt.Sprint(changes.Ops[0].Seq + 10)} {
		form := scrapeForm(t, aiChangesBody(t, ta, "/ui/ai-changes"), "/ui/ai-changes/review")
		form.Set("through", through)
		w := ta.post("/ui/ai-changes/review", form)
		status := http.StatusBadRequest
		if through == fmt.Sprint(changes.Ops[0].Seq+10) {
			status = http.StatusUnprocessableEntity
		}
		if w.Code != status {
			t.Fatalf("review through %q = %d, want %d", through, w.Code, status)
		}
		requireFieldErrors(t, w.Body.String(), "through")
	}
	if got := aiChanges(t, ta); got.Unreviewed != 1 || got.ReviewedThrough != 0 {
		t.Fatal("invalid review advanced watermark")
	}
	assertRedirect(t, ta.submit("/ui/ai-changes", "/ui/ai-changes/review", nil), "/ui/ai-changes")
	if got := flashText(t, ta.viewBody("/ui/")); got != fmt.Sprintf("AI changes reviewed through #%d", changes.Ops[0].Seq) {
		t.Errorf("review flash = %q", got)
	}
	if aiChanges(t, ta).Unreviewed != 0 || slices.Contains(dataIDs(t, ta.viewBody("/ui/"), "data-banner"), "ai_changes") {
		t.Error("review did not clear AI changes and banner")
	}
	requireNoAIReview(t, aiChangesBody(t, ta, "/ui/ai-changes"))
}

func TestMarkAIReviewedPreservesPostedThrough(t *testing.T) {
	ta := newTestApp(t).login()
	meta := func() service.Meta { return testutil.Meta(testutil.AI(), ta.Svc.Generation()) }
	ta.mustOp(ta.Svc.AddEntry(t.Context(), meta(), service.AddEntryIn{Title: "Visible at render"}))
	seen := aiChanges(t, ta).Ops[0].Seq
	form := scrapeForm(t, aiChangesBody(t, ta, "/ui/ai-changes"), "/ui/ai-changes/review")
	added := ta.mustOp(ta.Svc.AddEntry(t.Context(), meta(), service.AddEntryIn{Title: "Arrived after render"}))
	assertRedirect(t, ta.post("/ui/ai-changes/review", form), "/ui/ai-changes")
	changes := aiChanges(t, ta)
	if changes.ReviewedThrough != seen || changes.Unreviewed != 1 || len(changes.Ops) != 1 || changes.Ops[0].OpID != added.OpID {
		t.Errorf("review acknowledged unseen work: %+v", changes)
	}
}

func TestMarkAIReviewedEmptyPage(t *testing.T) {
	ta := newTestApp(t).login()
	for _, path := range []string{"/ui/ai-changes", "/ui/ai-changes?all=1", "/ui/ai-changes?all=0"} {
		body := aiChangesBody(t, ta, path)
		requireNoAIReview(t, body)
		if strings.Contains(nodeText(parseHTML(t, body)), "older unreviewed changes continue on the next page") {
			t.Errorf("empty view has a paging hint: %s", path)
		}
	}
}

func TestMarkAIReviewedMultiPage(t *testing.T) {
	ta := newTestApp(t).login()
	actor := testutil.TokenActor("bot", domain.MCPDefaultScopes)
	for i := range 101 {
		ta.mustOp(ta.Svc.AddEntry(t.Context(), testutil.Meta(actor, ta.Svc.Generation()), service.AddEntryIn{Title: fmt.Sprintf("AI work %d", i)}))
	}
	newer := aiChanges(t, ta)
	body := aiChangesBody(t, ta, "/ui/ai-changes")
	requireNoAIReview(t, body)
	requireText(t, body, "older unreviewed changes continue on the next page")
	oldestURL := moreLink(t, body)
	if oldestURL == "" {
		t.Fatal("101 changes need a second page")
	}
	body = aiChangesBody(t, ta, oldestURL)
	if len(dataIDs(t, body, "data-op")) != 1 {
		t.Fatal("oldest page must contain exactly one change")
	}
	requireText(t, body, "Mark the changes on this page reviewed")
	form := scrapeForm(t, body, "/ui/ai-changes/review")
	assertRedirect(t, ta.post("/ui/ai-changes/review", form), "/ui/ai-changes")
	remaining := aiChanges(t, ta)
	if remaining.Unreviewed != 100 || len(remaining.Ops) != 100 || remaining.NextCursor != "" {
		t.Fatalf("after oldest review = %+v", remaining)
	}
	for i, op := range remaining.Ops {
		if op.OpID != newer.Ops[i].OpID || op.Seq <= remaining.ReviewedThrough {
			t.Errorf("newer change unexpectedly reviewed: %+v", op)
		}
	}
	body = aiChangesBody(t, ta, "/ui/ai-changes")
	form = scrapeForm(t, body, "/ui/ai-changes/review")
	if len(dataIDs(t, body, "data-op")) != 100 || moreLink(t, body) != "" || form.Get("through") != fmt.Sprint(newer.Ops[0].Seq) {
		t.Fatal("remaining 100 changes must fit one page with the newest review watermark")
	}
	requireNoAIReview(t, aiChangesBody(t, ta, oldestURL))
	assertRedirect(t, ta.post("/ui/ai-changes/review", form), "/ui/ai-changes")
	if aiChanges(t, ta).Unreviewed != 0 {
		t.Error("second review did not clear remaining changes")
	}
	for _, path := range []string{"/ui/ai-changes", oldestURL, "/ui/ai-changes?all=1"} {
		body = aiChangesBody(t, ta, path)
		requireNoAIReview(t, body)
		if strings.Contains(nodeText(parseHTML(t, body)), "older unreviewed changes continue on the next page") {
			t.Errorf("misleading unreviewed paging hint on %s", path)
		}
	}
}

func TestSCP07_NoAdminRoutes(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: "Task"})
	form := scrapeForm(t, ta.viewBody("/ui/"), "/ui/logout")
	for _, path := range []string{"/ui/tokens", "/ui/tokens/1/revoke", "/ui/settings", "/ui/password", "/ui/backups", "/ui/export"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			w := ta.request(method, path, form)
			if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s = %d", method, path, w.Code)
			}
		}
	}
	for _, path := range []string{"/ui/", "/ui/tasks/1", "/ui/ai-changes"} {
		body := aiChangesBody(t, ta, path)
		walkHTML(parseHTML(t, body), func(n *html.Node) {
			if n.Type == html.ElementNode {
				for _, key := range []string{"href", "action"} {
					if strings.HasPrefix(attr(n, key), "/ui/tokens") || strings.HasPrefix(attr(n, key), "/ui/settings") {
						t.Errorf("%s exposes admin %s=%q", path, key, attr(n, key))
					}
				}
			}
		})
	}
}

func TestPRG18_NoWebPurge(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: "Task"})
	ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{Title: "Entry"}))
	form := scrapeForm(t, ta.viewBody("/ui/"), "/ui/logout")
	for _, path := range []string{"/ui/tasks/1/purge", "/ui/entries/1/purge", "/ui/purge"} {
		w := ta.post(path, form)
		if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d", path, w.Code)
		}
	}
	for _, path := range []string{"/ui/tasks/1", "/ui/log"} {
		walkHTML(parseHTML(t, ta.viewBody(path)), func(n *html.Node) {
			if n.Type == html.ElementNode && n.Data == "form" && strings.Contains(attr(n, "action"), "purge") {
				t.Errorf("%s exposes purge form", path)
			}
		})
	}
}

func TestSCP07_PRG18_RegisteredRoutePatterns(t *testing.T) {
	patterns := web.RoutePatterns()
	for _, required := range []string{"GET /ui/{$}", "POST /ui/focus", "POST /ui/undo", "POST /ui/redo", "GET /static/{name}"} {
		if !slices.Contains(patterns, required) {
			t.Errorf("route inventory missing %s", required)
		}
	}
	for _, pattern := range patterns {
		for _, forbidden := range []string{"token", "setting", "password", "backup", "export", "import", "restore", "purge", "admin"} {
			if strings.Contains(strings.ToLower(pattern), forbidden) {
				t.Errorf("forbidden route pattern %q contains %q", pattern, forbidden)
			}
		}
	}
}

func aiChanges(t *testing.T, ta *testApp) service.AIChanges {
	t.Helper()
	changes, err := ta.Svc.ListAIChanges(t.Context(), testutil.Owner(), service.ListAIChangesIn{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return changes
}
