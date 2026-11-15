package web_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"testing"

	"golang.org/x/net/html"

	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/testutil"
)

func TestSearchPage(t *testing.T) {
	ta := newTestApp(t).login()
	id := ta.addTask(service.AddTaskIn{Title: "Set up keycloak"})
	body := ta.viewBody("/ui/search?q=keycloak")
	requireSearchLink(t, body, "Set up keycloak", "/ui/tasks/1")
	if form := scrapeForm(t, body, "/ui/search"); form.Get("q") != "keycloak" || form.Get("kind") != "all" {
		t.Errorf("search form = %v", form)
	}
	ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{
		Title: "Linked keycloak work", TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}}))
	ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{
		Title: "Standalone keycloak work", WorkDate: testutil.D("10-27")}))
	const hostile = `<b onclick=x>keycloak</b>`
	ta.addTask(service.AddTaskIn{Title: hostile})
	before := ta.opKinds()
	for _, kind := range []string{"all", "task", "entry"} {
		body = ta.viewBody("/ui/search?q=keycloak&kind=" + kind)
		assertInert(t, body)
		if form := scrapeForm(t, body, "/ui/search"); form.Get("kind") != kind {
			t.Errorf("selected kind = %v", form)
		}
		if kind != "entry" {
			requireSearchLink(t, body, hostile, "/ui/tasks/2")
			requireSearchLink(t, body, "Set up keycloak", "/ui/tasks/1")
		}
		if kind != "task" {
			requireSearchLink(t, body, "Linked keycloak work", "/ui/tasks/1")
			requireSearchLink(t, body, "Standalone keycloak work", "/ui/log?from=2026-10-27&to=2026-10-27")
		}
		rows := dataIDs(t, body, "data-search-kind")
		want := 2
		if kind == "all" {
			want = 4
		}
		if len(rows) != want || (kind != "all" && slices.ContainsFunc(rows, func(v string) bool { return v != kind })) {
			t.Errorf("%s result kinds = %v", kind, rows)
		}
	}
	for _, q := range []string{"", " \t\n\u2003"} {
		// Invalid paging/filter inputs would fail if the empty query called Search.
		body = ta.viewBody("/ui/search?q=" + url.QueryEscape(q) + "&cursor=garbage&kind=garbage")
		scrapeForm(t, body, "/ui/search")
		if len(dataIDs(t, body, "data-search-kind")) != 0 || moreLink(t, body) != "" || sectionText(t, body, "Search results") != "" {
			t.Error("empty query rendered search results")
		}
	}
	body = ta.viewBody("/ui/search?q=absentword")
	requireText(t, body, "No results.")
	for _, tc := range []struct {
		query string
		code  int
	}{
		{"q=*", http.StatusUnprocessableEntity},
		{"q=keycloak&kind=invalid", http.StatusUnprocessableEntity},
		{"q=keycloak&cursor=garbage", http.StatusBadRequest},
	} {
		w := ta.get("/ui/search?" + tc.query)
		if w.Code != tc.code {
			t.Errorf("search %s = %d, want %d", tc.query, w.Code, tc.code)
		}
	}
	if !reflect.DeepEqual(before, ta.opKinds()) {
		t.Error("search GETs created user operations")
	}
}

func TestSearchPageEscapesSnippets(t *testing.T) {
	ta := newTestApp(t).login()
	const notes = `<script>needle</script> "><img src=x onerror=alert(1)> javascript:alert(1) **bold**`
	ta.addTask(service.AddTaskIn{Title: "Task notes", Notes: notes})
	ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{Title: "Entry notes", Notes: notes}))
	list, err := ta.Svc.Search(t.Context(), testutil.Owner(), service.SearchIn{Query: "needle"})
	if err != nil || len(list.Items) != 2 {
		t.Fatalf("snippet fixture = %v, %v", list, err)
	}
	body := ta.viewBody("/ui/search?q=needle")
	assertInert(t, body)
	var snippets []string
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Type == html.ElementNode && hasAttr(n, "data-snippet") {
			snippets = append(snippets, nodeText(n))
			for child := n.FirstChild; child != nil; child = child.NextSibling {
				if child.Type == html.ElementNode {
					t.Error("search snippet rendered markup")
				}
			}
		}
	})
	if want := []string{list.Items[0].Snippet, list.Items[1].Snippet}; !reflect.DeepEqual(snippets, want) {
		t.Errorf("snippets = %q, want %q", snippets, want)
	}
	const query = `needle"><img src=x onerror=alert(1)>`
	body = ta.viewBody("/ui/search?q=" + url.QueryEscape(query))
	assertInert(t, body)
	if got := scrapeForm(t, body, "/ui/search").Get("q"); got != query {
		t.Errorf("query round-trip = %q", got)
	}
}

func TestSearchPagePaging(t *testing.T) {
	ta := newTestApp(t).login()
	for range 101 {
		ta.mustOp(ta.Svc.AddEntry(t.Context(), ta.meta(), service.AddEntryIn{Title: "keycloak audit"}))
	}
	ta.addTask(service.AddTaskIn{Title: "keycloak audit"})
	before := ta.opKinds()
	body := ta.viewBody("/ui/search?q=keycloak+audit&kind=entry&keep=a%26b")
	if got := len(dataIDs(t, body, "data-search-id")); got != 100 {
		t.Fatalf("first page rows = %d", got)
	}
	more := moreLink(t, body)
	u, err := url.Parse(more)
	if err != nil || u.Path != "/ui/search" || u.Query().Get("q") != "keycloak audit" || u.Query().Get("kind") != "entry" || u.Query().Get("keep") != "a&b" || u.Query().Get("cursor") == "" {
		t.Fatalf("More URL = %q, %v", more, err)
	}
	body = ta.viewBody(more)
	if got := dataIDs(t, body, "data-search-id"); !reflect.DeepEqual(got, []string{"101"}) || moreLink(t, body) != "" {
		t.Errorf("last page = %v, More = %q", got, moreLink(t, body))
	}
	if !reflect.DeepEqual(before, ta.opKinds()) {
		t.Error("search paging created user operations")
	}
}

func TestAIChangesPage(t *testing.T) {
	ta := newTestApp(t).login()
	id := ta.addTask(service.AddTaskIn{Title: "Set up keycloak"})
	op := ta.mustOp(ta.Svc.AddEntry(t.Context(), testutil.Meta(testutil.AI(), ta.Svc.Generation()), service.AddEntryIn{
		Title: "AI work", TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}}))
	detail, err := ta.Svc.GetOp(t.Context(), testutil.Owner(), op.OpID)
	if err != nil {
		t.Fatal(err)
	}
	before := ta.opKinds()
	body := aiChangesBody(t, ta, "/ui/ai-changes")
	assertInert(t, body)
	requireText(t, body, "token:ai", "entry.add", "active", "entry_logged", "Wed 2026-10-28 10:00", fmt.Sprint(detail.Op.Seq))
	requireSearchLink(t, body, "#1", "/ui/tasks/1")
	action := "/ui/ops/" + op.OpID.String() + "/revert"
	if len(scrapeForms(t, body, action)) != 1 {
		t.Error("expected exactly one revert form for the full operation ID")
	}
	requireConfirm(t, body, action)
	if form := scrapeForm(t, body, "/ui/ai-changes/review"); form.Get("through") != fmt.Sprint(detail.Op.Seq) {
		t.Errorf("review watermark = %v", form)
	}
	body = aiChangesBody(t, ta, "/ui/ai-changes?all=1")
	requireText(t, body, "token:ai")
	requireNoAIReview(t, body)
	for _, path := range []string{"/ui/ai-changes?all=0", "/ui/ai-changes?all="} {
		// The brief permits review only when the all parameter is unset.
		body = aiChangesBody(t, ta, path)
		requireText(t, body, "token:ai")
		requireNoAIReview(t, body)
	}
	if !reflect.DeepEqual(before, ta.opKinds()) {
		t.Error("AI changes GETs created user operations")
	}
	ta.mustOp(ta.Svc.MarkAIReviewed(t.Context(), ta.meta(), service.MarkAIReviewedIn{Through: detail.Op.Seq}))
	before = ta.opKinds()
	body = aiChangesBody(t, ta, "/ui/ai-changes")
	requireNoAIReview(t, body)
	if len(dataIDs(t, body, "data-op")) != 0 {
		t.Error("reviewed operation remains in unreviewed view")
	}
	body = aiChangesBody(t, ta, "/ui/ai-changes?all=1")
	requireText(t, body, "token:ai")
	requireNoAIReview(t, body)
	if !reflect.DeepEqual(before, ta.opKinds()) {
		t.Error("reviewed AI changes GETs created user operations")
	}
	ta.mustOp(ta.Svc.Revert(t.Context(), ta.meta(), service.RevertIn{OpID: op.OpID}))
	body = aiChangesBody(t, ta, "/ui/ai-changes?all=1")
	requireText(t, body, "reverted")
	if len(scrapeForms(t, body, action)) != 0 {
		t.Error("reverted operation still offers revert")
	}
	w := ta.get("/ui/ai-changes?cursor=garbage")
	if w.Code != http.StatusBadRequest || !slices.Contains(dataIDs(t, w.Body.String(), "data-code"), "malformed") {
		t.Errorf("invalid AI cursor = %d %s", w.Code, w.Body)
	}
}

func TestAIChangesPageEscapesEffects(t *testing.T) {
	ta := newTestApp(t).login()
	const old = `<b onclick=x>original</b>`
	title := `"><img src=x onerror=alert(1)> javascript:alert(1)`
	id := ta.addTask(service.AddTaskIn{Title: old})
	actor := testutil.TokenActor(`<script>bot</script>`, domain.MCPDefaultScopes)
	ta.mustOp(ta.Svc.UpdateTask(t.Context(), testutil.Meta(actor, ta.Svc.Generation()), service.UpdateTaskIn{
		ID: id, Expect: ta.taskExpect(id), Title: &title}))
	body := aiChangesBody(t, ta, "/ui/ai-changes")
	assertInert(t, body)
	requireText(t, body, "token:<script>bot</script>", old, title, "retitled")
}

func TestAIChangesPageNonRevertible(t *testing.T) {
	ta := newTestApp(t).login()
	requireNoAIReview(t, aiChangesBody(t, ta, "/ui/ai-changes"))
	daily := 3
	if _, err := ta.Svc.UpdateSettings(t.Context(), ta.meta(), service.UpdateSettingsIn{KeepDaily: &daily, Password: testutil.Password}); err != nil {
		t.Fatal(err)
	}
	changes, err := ta.Svc.ListAIChanges(t.Context(), testutil.Owner(), service.ListAIChangesIn{})
	if err != nil || len(changes.Ops) != 1 || changes.Ops[0].Revertible || changes.Ops[0].State != domain.OpActive {
		t.Fatalf("active non-revertible fixture = %v, %v", changes, err)
	}
	body := aiChangesBody(t, ta, "/ui/ai-changes")
	requireText(t, body, "settings.retention_lowered", "active")
	if len(scrapeForms(t, body, "/ui/ops/"+changes.Ops[0].OpID.String()+"/revert")) != 0 {
		t.Error("active non-revertible operation offers revert")
	}
}

func TestAIReviewNeverCoversUnseen(t *testing.T) {
	for _, count := range []int{101, 100} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			ta := newTestApp(t).login()
			actor := testutil.TokenActor("bot", domain.MCPDefaultScopes)
			var first, last domain.OpID
			for i := range count {
				op := ta.mustOp(ta.Svc.AddEntry(t.Context(), testutil.Meta(actor, ta.Svc.Generation()), service.AddEntryIn{Title: fmt.Sprintf("AI work %d", i)}))
				if i == 0 {
					first = op.OpID
				}
				last = op.OpID
			}
			oldest, err := ta.Svc.GetOp(t.Context(), testutil.Owner(), first)
			if err != nil {
				t.Fatal(err)
			}
			newest, err := ta.Svc.GetOp(t.Context(), testutil.Owner(), last)
			if err != nil {
				t.Fatal(err)
			}
			before := ta.opKinds()
			body := aiChangesBody(t, ta, "/ui/ai-changes?keep=a%26b")
			seqs := dataIDs(t, body, "data-seq")
			var want []string
			for i := range 100 {
				want = append(want, fmt.Sprint(newest.Op.Seq-domain.OpSeq(i)))
			}
			if !reflect.DeepEqual(seqs, want) {
				t.Fatalf("first page seqs = %v, want %v", seqs, want)
			}
			through := newest.Op.Seq
			more := moreLink(t, body)
			if count == 101 {
				requireNoAIReview(t, body)
				requireText(t, body, "older unreviewed changes continue on the next page")
				u, err := url.Parse(more)
				if err != nil || u.Path != "/ui/ai-changes" || u.Query().Get("cursor") == "" || u.Query().Get("keep") != "a&b" {
					t.Fatalf("More URL = %q, %v", more, err)
				}
				body = aiChangesBody(t, ta, more)
				through = oldest.Op.Seq
				if got := dataIDs(t, body, "data-seq"); !reflect.DeepEqual(got, []string{fmt.Sprint(through)}) {
					t.Errorf("oldest page seqs = %v", got)
				}
			} else if more != "" {
				t.Error("exactly 100 operations unexpectedly have a next page")
			}
			form := scrapeForm(t, body, "/ui/ai-changes/review")
			if form.Get("through") != fmt.Sprint(through) || moreLink(t, body) != "" {
				t.Errorf("oldest-page review = %v, More = %q", form, moreLink(t, body))
			}
			if !slices.Contains(dataIDs(t, body, "data-seq"), form.Get("through")) {
				t.Error("review watermark names an unseen operation")
			}
			allBody := aiChangesBody(t, ta, "/ui/ai-changes?all=1")
			requireNoAIReview(t, allBody)
			if count == 101 {
				allMore := moreLink(t, allBody)
				u, err := url.Parse(allMore)
				if err != nil || u.Query().Get("all") != "1" {
					t.Fatalf("all filter lost in More = %q, %v", allMore, err)
				}
				requireNoAIReview(t, aiChangesBody(t, ta, allMore))
			}
			if !reflect.DeepEqual(before, ta.opKinds()) {
				t.Error("AI paging GETs created user operations")
			}
			ta.mustOp(ta.Svc.MarkAIReviewed(t.Context(), ta.meta(), service.MarkAIReviewedIn{Through: through}))
			body = aiChangesBody(t, ta, form.Get("_return"))
			if count == 101 {
				if got := scrapeForm(t, body, "/ui/ai-changes/review").Get("through"); got != fmt.Sprint(newest.Op.Seq) || len(dataIDs(t, body, "data-op")) != 100 || moreLink(t, body) != "" {
					t.Error("reviewing the oldest page did not expose the remaining 100 for review")
				}
				// Revisiting the now-empty oldest page must not offer another review.
				requireNoAIReview(t, aiChangesBody(t, ta, more))
			} else {
				requireNoAIReview(t, body)
			}
		})
	}
}

func requireSearchLink(t *testing.T, body, text, href string) {
	t.Helper()
	found := false
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" && nodeText(n) == text && attr(n, "href") == href {
			found = true
		}
	})
	if !found {
		t.Errorf("missing link %q to %q", text, href)
	}
}

func requireNoAIReview(t *testing.T, body string) {
	t.Helper()
	if len(scrapeForms(t, body, "/ui/ai-changes/review")) != 0 {
		t.Error("unexpected AI review form")
	}
}

// AI review alone returns to the default view (DF17), so viewBody's universal
// current-URL assertion does not apply. Check the same contract with that exception.
func aiChangesBody(t *testing.T, ta *testApp, path string) string {
	t.Helper()
	w := ta.get(path)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s; logs: %s", path, w.Code, w.Body, ta.Logs)
	}
	body := w.Body.String()
	nonces := make(map[string]bool)
	csrf := scrapeForm(t, body, "/ui/logout").Get("_csrf")
	walkHTML(parseHTML(t, body), func(n *html.Node) {
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
				t.Errorf("%s lacks one hidden %s", attr(n, "action"), key)
			}
		}
		ret := path
		if attr(n, "action") == "/ui/ai-changes/review" {
			ret = "/ui/ai-changes"
			if len(values["through"]) != 1 || values.Get("through") == "" {
				t.Error("review lacks one hidden through value")
			}
		}
		if values.Get("_csrf") != csrf || values.Get("_gen") != string(ta.Svc.Generation()) || values.Get("_return") != ret {
			t.Errorf("%s has wrong form context: %v", attr(n, "action"), values)
		}
		nonce := values.Get("_nonce")
		if _, err := domain.ParseOpID(nonce); err != nil || nonces[nonce] {
			t.Errorf("invalid or reused nonce %q", nonce)
		}
		nonces[nonce] = true
	})
	return body
}
