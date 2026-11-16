package web_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/net/html"

	"taskronin/internal/datespec"
	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/testutil"
)

func TestAddTaskDefaultTriage(t *testing.T) {
	ta := newTestApp(t).login()
	w := ta.submit("/ui/triage", "/ui/tasks", url.Values{"title": {"Write report"}})
	assertRedirect(t, w, "/ui/triage")
	task := ta.task(1)
	if task.Title != "Write report" || task.View != domain.ViewTriage || len(task.Reasons) != 1 || task.Reasons[0].Reason != domain.ReasonNew {
		t.Fatalf("captured task = %+v", task)
	}
	ops, err := ta.Svc.ListOps(t.Context(), testutil.Owner(), service.ListOpsIn{})
	if err != nil || len(ops.Items) != 1 || ops.Items[0].Kind != domain.OpTaskAdd || ops.Items[0].Channel != domain.ChanWeb {
		t.Fatalf("ops = %+v, %v", ops, err)
	}
	cookie := responseCookie(t, w, "tr_flash")
	if cookie.Path != "/ui/" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge != 60 || cookie.Secure || cookie.Domain != "" {
		t.Errorf("flash cookie attributes = %+v", cookie)
	}
	w = ta.get("/ui/triage")
	csrf := scrapeForm(t, w.Body.String(), "/ui/tasks").Get("_csrf")
	if got := decodeFlash(t, cookie.Value, csrf); got != "#1 created\n#1 +new" {
		t.Errorf("flash text = %q", got)
	}
	if got := flashText(t, w.Body.String()); got != "#1 created\n#1 +new" {
		t.Errorf("visible flash = %q", got)
	}
	cleared := responseCookie(t, w, "tr_flash")
	if cleared.MaxAge >= 0 || cleared.Path != "/ui/" || ta.cookies["tr_flash"] != "" {
		t.Error("flash was not cleared at its cookie path")
	}
	if got := flashText(t, ta.get("/ui/triage").Body.String()); got != "" {
		t.Errorf("flash repeated: %q", got)
	}
}

func TestFlashTamperIgnored(t *testing.T) {
	ta := newTestApp(t).login()
	for _, value := range []string{
		base64.RawURLEncoding.EncodeToString([]byte("forged success")) + "." + base64.RawURLEncoding.EncodeToString(make([]byte, sha256.Size)),
		"garbage", "bad.bad", strings.Repeat("a", 2049),
	} {
		ta.cookies["tr_flash"] = value
		w := ta.get("/ui/triage")
		if w.Code != http.StatusOK || slices.Contains(dataIDs(t, w.Body.String(), "class"), "flash") || ta.cookies["tr_flash"] != "" {
			t.Errorf("invalid flash not ignored/cleared: %d %v", w.Code, w.Header())
		}
	}
}

func TestAddTaskPlacements(t *testing.T) {
	for _, place := range []string{"top", "bottom", "before", "after", "now", "defer", "block"} {
		t.Run(place, func(t *testing.T) {
			ta := newTestApp(t).login()
			ta.addTask(service.AddTaskIn{Title: "Existing", Place: &service.AddPlacement{How: "bottom"}})
			form := url.Values{"title": {"New"}, "place": {place}, "ref": {"1"}, "defer": {"fri 9:00"}, "span": {"13:00-15:00"}}
			if place == "before" {
				form.Set("ref", "#1")
			}
			assertRedirect(t, ta.submit("/ui/", "/ui/tasks", form), "/ui/")
			task := ta.task(2)
			if len(task.Reasons) != 0 {
				t.Errorf("placed task has reasons: %v", task.Reasons)
			}
			switch place {
			case "top", "bottom", "before", "after":
				if task.View != domain.ViewBacklog || ((place == "top" || place == "before") && task.Rank >= ta.task(1).Rank) || ((place == "bottom" || place == "after") && task.Rank <= ta.task(1).Rank) {
					t.Errorf("%s placement = %+v", place, task)
				}
			case "now":
				view, err := ta.Svc.Today(t.Context(), testutil.Owner())
				if err != nil || view.Focus == nil || view.Focus.ID != 2 {
					t.Fatalf("focus = %+v, %v", view.Focus, err)
				}
			case "defer":
				if task.View != domain.ViewUpcoming || !task.DeferDate.Valid || task.DeferDate.V != testutil.D("10-30") || !task.DeferMin.Valid || task.DeferMin.V != 540 {
					t.Errorf("deferred task = %+v", task)
				}
			case "block":
				if len(task.Blocks) != 1 || task.Blocks[0].Status != domain.BlockPlanned || task.Blocks[0].Span.Date != testutil.D("10-28") || task.Blocks[0].Span.Start != 780 || task.Blocks[0].Span.End != 900 {
					t.Errorf("blocks = %+v", task.Blocks)
				}
			}
		})
	}
}

func TestAddTaskFields(t *testing.T) {
	ta := newTestApp(t).login()
	assertRedirect(t, ta.submit("/ui/", "/ui/tasks", url.Values{
		"title": {"Fields"}, "due": {"fri"}, "tags": {"db, infra"}, "project": {"infra.kc"}, "priority": {"H"},
	}), "/ui/")
	task := ta.task(1)
	if !task.DueDate.Valid || task.DueDate.V != testutil.D("10-30") || !slices.Equal(task.Tags, []string{"db", "infra"}) || task.Project != "infra.kc" || task.Priority != domain.PriorityH {
		t.Errorf("task fields = %+v", task)
	}
}

func TestAddTaskNotesCRLF(t *testing.T) {
	ta := newTestApp(t).login()
	assertRedirect(t, ta.submit("/ui/", "/ui/tasks", url.Values{"title": {"Notes"}, "notes": {"line1\r\nline2\r\n"}}), "/ui/")
	if got := ta.task(1).Notes; got != "line1\nline2\n" {
		t.Errorf("notes = %q", got)
	}
}

func TestAddTaskDoubleSubmitReplays(t *testing.T) {
	ta := newTestApp(t).login()
	form := scrapeForm(t, ta.viewBody("/ui/"), "/ui/tasks")
	form.Set("title", "Only once")
	before := ta.opKinds()
	for range 2 {
		assertRedirect(t, ta.post("/ui/tasks", form), "/ui/")
	}
	list, err := ta.Svc.ListTasks(t.Context(), testutil.Owner(), service.ListTasksIn{View: domain.ViewAll})
	if err != nil || len(list.Items) != 1 || !slices.Equal(ta.opKinds(), append(before, "task.add")) {
		t.Fatalf("replay tasks=%+v err=%v ops=%v", list, err, ta.opKinds())
	}
	if got := flashText(t, ta.viewBody("/ui/")); got != "#1 created\n#1 +new\nalready applied" {
		t.Errorf("replay flash = %q", got)
	}
}

func TestAddTaskValidation(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		form        url.Values
	}{
		{"empty title", "title", url.Values{"title": {""}}},
		{"bad due", "due", url.Values{"due": {"someday"}}},
		{"bad place", "place", url.Values{"place": {"sideways"}}},
		{"missing defer", "defer", url.Values{"place": {"defer"}}},
		{"bad priority", "priority", url.Values{"priority": {"Z"}}},
		{"bad block", "span", url.Values{"place": {"block"}, "span": {"15:00-13:00"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ta := newTestApp(t).login()
			if !tc.form.Has("title") {
				tc.form.Set("title", "Invalid")
			}
			w := ta.submit("/ui/triage", "/ui/tasks", tc.form)
			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("validation = %d %s", w.Code, w.Body)
			}
			assertErrorCode(t, w.Body.String(), "validation")
			requireFieldErrors(t, w.Body.String(), tc.field)
			if len(ta.opKinds()) != 0 {
				t.Error("validation created an operation")
			}
			list, err := ta.Svc.ListTasks(t.Context(), testutil.Owner(), service.ListTasksIn{View: domain.ViewAll})
			if err != nil || len(list.Items) != 0 {
				t.Fatalf("validation created tasks: %+v, %v", list, err)
			}
		})
	}
}

func TestValidationKeepsInput(t *testing.T) {
	ta := newTestApp(t).login()
	w := ta.submit("/ui/triage", "/ui/tasks", url.Values{"title": {"Long one"}, "notes": {"para 1\n\npara 2"}, "due": {"someday"}})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("validation = %d %s", w.Code, w.Body)
	}
	requireFieldErrors(t, w.Body.String(), "due")
	form := scrapeForm(t, w.Body.String(), "/ui/tasks")
	if form.Get("title") != "Long one" || form.Get("notes") != "para 1\n\npara 2" || form.Get("due") != "someday" {
		t.Fatalf("lost input: %v", form)
	}
	form.Set("due", "fri")
	assertRedirect(t, ta.post("/ui/tasks", form), "/ui/triage")
	if task := ta.task(1); task.Notes != "para 1\n\npara 2" || task.DueDate.V != testutil.D("10-30") {
		t.Errorf("resubmitted task = %+v", task)
	}
}

func TestEditTask(t *testing.T) {
	ta := newTestApp(t).login()
	id := ta.addTask(service.AddTaskIn{Title: "Old", Project: "infra", Priority: domain.PriorityH})
	version := ta.task(id).Version
	assertRedirect(t, ta.submit("/ui/tasks/1", "/ui/tasks/1/edit", url.Values{"title": {"New"}, "project": {""}, "priority": {""}}), "/ui/tasks/1")
	if task := ta.task(id); task.Title != "New" || task.Project != "" || task.Priority != domain.PriorityNone || task.Version != version+1 {
		t.Errorf("edited task = %+v", task)
	}
	if got := flashText(t, ta.viewBody("/ui/tasks/1")); !strings.Contains(got, "#1 retitled 'Old' → 'New'") {
		t.Errorf("edit flash = %q", got)
	}
}

func TestEditTaskNotesCRLF(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: "Notes"})
	assertRedirect(t, ta.submit("/ui/tasks/1", "/ui/tasks/1/edit", url.Values{"notes": {"a\r\nb"}}), "/ui/tasks/1")
	if got := ta.task(1).Notes; got != "a\nb" {
		t.Errorf("notes = %q", got)
	}
}

func TestEditTaskPartialForm(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: "Old", Notes: "Keep notes", Project: "infra", Priority: domain.PriorityH})
	assertRedirect(t, ta.submit("/ui/tasks/1", "/ui/tasks/1/edit", url.Values{"title": {"New"}, "notes": nil, "project": nil, "priority": nil}), "/ui/tasks/1")
	if task := ta.task(1); task.Title != "New" || task.Notes != "Keep notes" || task.Project != "infra" || task.Priority != domain.PriorityH {
		t.Errorf("partial edit = %+v", task)
	}
}

func TestTags(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: "Tags"})
	assertRedirect(t, ta.submit("/ui/tasks/1", "/ui/tasks/1/tags", url.Values{"add": {"x y"}}), "/ui/tasks/1")
	if got := ta.task(1).Tags; !slices.Equal(got, []string{"x", "y"}) {
		t.Fatalf("added tags = %v", got)
	}
	form := scrapeFormWhere(t, ta.viewBody("/ui/tasks/1"), "/ui/tasks/1/tags", url.Values{"remove": {"x"}})
	assertRedirect(t, ta.post("/ui/tasks/1/tags", form), "/ui/tasks/1")
	if got := ta.task(1).Tags; !slices.Equal(got, []string{"y"}) {
		t.Errorf("remaining tags = %v", got)
	}
}

func TestDue(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: "Due"})
	for _, due := range []string{"fri", "none"} {
		assertRedirect(t, ta.submit("/ui/tasks/1", "/ui/tasks/1/due", url.Values{"due": {due}}), "/ui/tasks/1")
		task := ta.task(1)
		if (due == "fri" && (!task.DueDate.Valid || task.DueDate.V != testutil.D("10-30"))) || (due == "none" && (task.DueDate.Valid || task.DueMin.Valid)) {
			t.Errorf("due %q = %+v", due, task)
		}
	}
	for _, due := range []string{"yesterday", "", "someday"} {
		w := ta.submit("/ui/tasks/1", "/ui/tasks/1/due", url.Values{"due": {due}})
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("due %q = %d %s", due, w.Code, w.Body)
		}
		assertErrorCode(t, w.Body.String(), "validation")
		requireFieldErrors(t, w.Body.String(), "due")
	}
}

func TestDueRenderedRoundTrip(t *testing.T) {
	for _, moment := range []string{"10-30", "10-30 09:00"} {
		for _, path := range []string{"/ui/tasks/1", "/ui/upcoming"} {
			t.Run(moment+path, func(t *testing.T) {
				ta := newTestApp(t).login()
				due := testutil.Moment(moment)
				ta.addTask(service.AddTaskIn{Title: "Round trip", Due: &due})
				form := scrapeForm(t, ta.viewBody(path), "/ui/tasks/1/due")
				parsed, err := ta.App.FormMoment(form, "due", datespec.Future)
				if err != nil || parsed == nil || *parsed != due {
					t.Fatalf("rendered due %q = %v, %v", form.Get("due"), parsed, err)
				}
				before := ta.opKinds()
				assertRedirect(t, ta.post("/ui/tasks/1/due", form), path)
				if !slices.Equal(before, ta.opKinds()) || flashText(t, ta.viewBody(path)) != "no change" {
					t.Error("unchanged due did not succeed as no change")
				}
				assertRedirect(t, ta.post("/ui/tasks/1/due", form), path)
				if !slices.Equal(before, ta.opKinds()) || flashText(t, ta.viewBody(path)) != "no change\nalready applied" {
					t.Error("unchanged due replay did not preserve both flags")
				}
			})
		}
	}
}

func TestAddTaskDeferResubmitRoundTrip(t *testing.T) {
	ta := newTestApp(t).login()
	// C1 has no initial defer prefill; its error form preserves the typed value.
	w := ta.submit("/ui/", "/ui/tasks", url.Values{"title": {""}, "place": {"defer"}, "defer": {"2026-10-30 09:00"}})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("validation = %d %s", w.Code, w.Body)
	}
	requireFieldErrors(t, w.Body.String(), "title")
	form := scrapeForm(t, w.Body.String(), "/ui/tasks")
	parsed, err := ta.App.FormMoment(form, "defer", datespec.Future)
	if err != nil || parsed == nil || *parsed != testutil.Moment("10-30 09:00") {
		t.Fatalf("rendered defer %q = %v, %v", form.Get("defer"), parsed, err)
	}
	form.Set("title", "Corrected title")
	assertRedirect(t, ta.post("/ui/tasks", form), "/ui/")
	if task := ta.task(1); task.View != domain.ViewUpcoming || task.DeferDate.V != parsed.Date || task.DeferMin != parsed.Min {
		t.Errorf("resubmitted defer = %+v", task)
	}
}

func TestFlashEscapesAndTruncates(t *testing.T) {
	ta := newTestApp(t).login()
	const hostile = `"><img src=x onerror=alert(1)><script>x</script>`
	ta.addTask(service.AddTaskIn{Title: "Old"})
	assertRedirect(t, ta.submit("/ui/tasks/1", "/ui/tasks/1/edit", url.Values{"title": {hostile}}), "/ui/tasks/1")
	body := ta.viewBody("/ui/tasks/1")
	assertInert(t, body)
	if got := flashText(t, body); got != "#1 retitled 'Old' → '"+hostile+"'" {
		t.Errorf("literal flash = %q", got)
	}
	// A retitle can contain two 500-rune titles, larger than a flash cookie.
	assertRedirect(t, ta.submit("/ui/tasks/1", "/ui/tasks/1/edit", url.Values{"title": {strings.Repeat("界", 500)}}), "/ui/tasks/1")
	form := scrapeForm(t, ta.viewBody("/ui/tasks/1"), "/ui/tasks/1/edit")
	form.Set("title", strings.Repeat("語", 500))
	w := ta.post("/ui/tasks/1/edit", form)
	assertRedirect(t, w, "/ui/tasks/1")
	cookie := responseCookie(t, w, "tr_flash")
	if len(cookie.String()) > 2048 {
		t.Errorf("flash cookie is %d bytes", len(cookie.String()))
	}
	text := decodeFlash(t, cookie.Value, form.Get("_csrf"))
	if !utf8.ValidString(text) || !strings.HasPrefix(text, "#1 retitled '") || !strings.HasSuffix(text, "…") {
		t.Errorf("invalid truncation: %q", text)
	}
	if got := flashText(t, ta.viewBody("/ui/tasks/1")); got != text {
		t.Error("truncated flash did not round-trip")
	}
}

func TestFlashBoundToSession(t *testing.T) {
	ta := newTestApp(t).login()
	assertRedirect(t, ta.submit("/ui/", "/ui/tasks", url.Values{"title": {"Saved"}}), "/ui/")
	value := ta.cookies["tr_flash"]
	ta.login()
	ta.cookies["tr_flash"] = value
	if got := flashText(t, ta.viewBody("/ui/")); got != "" {
		t.Errorf("another session accepted flash: %q", got)
	}
}

func TestAddTaskSafeReturn(t *testing.T) {
	ta := newTestApp(t).login()
	for _, ret := range []string{"https://evil.example/", "//evil.example/", `/\evil.example`, "javascript:alert(1)", "/api/v1/tasks", "/ui/triage?tag=db"} {
		want := "/ui/"
		if ret == "/ui/triage?tag=db" {
			want = ret
		}
		assertRedirect(t, ta.submit("/ui/", "/ui/tasks", url.Values{"title": {"Return"}, "_return": {ret}}), want)
	}
}

func TestAddTaskServerRetryReplays(t *testing.T) {
	ta := newTestApp(t).login()
	form := scrapeForm(t, ta.viewBody("/ui/"), "/ui/tasks")
	form.Set("title", "Committed before error")
	assertRedirect(t, ta.post("/ui/tasks", form), "/ui/")
	// Model a failed response after commit using the actual DF25 error renderer.
	r := errorPost(t, form)
	w := httptest.NewRecorder()
	ta.App.RenderError(w, r, domain.Errorf(domain.CodeInternal, "response lost"))
	retry := scrapeForm(t, w.Body.String(), "/ui/tasks")
	if retry.Get("_nonce") != form.Get("_nonce") {
		t.Fatal("server retry changed nonce")
	}
	retry.Set("_csrf", form.Get("_csrf"))
	assertRedirect(t, ta.post("/ui/tasks", retry), "/ui/")
	retry.Set("title", "Edited retry")
	w = ta.post("/ui/tasks", retry)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("edited retry = %d %s", w.Code, w.Body)
	}
	assertErrorCode(t, w.Body.String(), "idempotency_mismatch")
	requireText(t, w.Body.String(), "earlier attempt may have been saved")
	if !slices.Equal(ta.opKinds(), []string{"task.add"}) {
		t.Errorf("retry ops = %v", ta.opKinds())
	}
}

func TestEditTaskPreconditions(t *testing.T) {
	ta := newTestApp(t).login()
	ta.addTask(service.AddTaskIn{Title: "Original"})
	for _, action := range []string{"edit", "tags", "due"} {
		path := "/ui/tasks/1/" + action
		form := scrapeForm(t, ta.viewBody("/ui/tasks/1"), path)
		form.Del("etag")
		w := ta.post(path, form)
		if w.Code != http.StatusPreconditionRequired {
			t.Fatalf("%s without etag = %d %s", action, w.Code, w.Body)
		}
		for _, id := range []string{"abc", "0", "999"} {
			form.Set("etag", domain.FormatETag(ta.Svc.Generation(), ta.task(1).Version))
			form.Set("due", "fri")
			w = ta.post(fmt.Sprintf("/ui/tasks/%s/%s", id, action), form)
			if w.Code != http.StatusNotFound {
				t.Fatalf("%s bad id %s = %d %s", action, id, w.Code, w.Body)
			}
		}
	}
	before := ta.opKinds()
	w := ta.submit("/ui/tasks/1", "/ui/tasks/1/edit", url.Values{"priority": {"Z"}})
	if w.Code != http.StatusUnprocessableEntity || !reflect.DeepEqual(before, ta.opKinds()) {
		t.Errorf("bad edit priority = %d, ops = %v", w.Code, ta.opKinds())
	}
	requireFieldErrors(t, w.Body.String(), "priority")
}

func responseCookie(t *testing.T, w *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	response := w.Result()
	defer response.Body.Close()
	for _, cookie := range response.Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("missing %s cookie", name)
	return nil
}

func decodeFlash(t *testing.T, value, csrf string) string {
	t.Helper()
	body, signature, ok := strings.Cut(value, ".")
	text, err := base64.RawURLEncoding.DecodeString(body)
	if !ok || err != nil {
		t.Fatalf("invalid flash text: %q, %v", value, err)
	}
	mac, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		t.Fatal(err)
	}
	h := hmac.New(sha256.New, []byte(csrf))
	_, _ = h.Write(text)
	if !hmac.Equal(mac, h.Sum(nil)) {
		t.Fatal("flash MAC does not authenticate its text with the session CSRF token")
	}
	return string(text)
}

func flashText(t *testing.T, body string) string {
	t.Helper()
	var lines []string
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Type == html.ElementNode && attr(n, "class") == "flash" {
			walkHTML(n, func(line *html.Node) {
				if line.Type == html.ElementNode && line.Data == "li" {
					lines = append(lines, nodeText(line))
				}
			})
		}
	})
	return strings.Join(lines, "\n")
}
