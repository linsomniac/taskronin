package web_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/linsomniac/taskronin/internal/clock"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/service"
	"github.com/linsomniac/taskronin/internal/testutil"
	"github.com/linsomniac/taskronin/internal/web"
)

const testListen = "127.0.0.1:7878"

type testApp struct {
	t       *testing.T
	App     *web.App
	H       http.Handler
	Svc     *service.Service
	Clock   *clock.Fake
	Logs    *bytes.Buffer
	cookies map[string]string
}

func newTestApp(t *testing.T, opts ...testutil.SvcOpt) *testApp {
	t.Helper()
	svc, clk := testutil.Service(t, opts...)
	logs := new(bytes.Buffer)
	a, err := web.NewApp(web.Options{Svc: svc, Clock: clk, Zone: testutil.Zone,
		WeekStart: time.Monday, Listen: testListen,
		Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	if err != nil {
		t.Fatal(err)
	}
	return &testApp{t: t, App: a, H: a.Handler(), Svc: svc, Clock: clk, Logs: logs, cookies: make(map[string]string)}
}

func (ta *testApp) meta() service.Meta {
	return testutil.Meta(testutil.Owner(), ta.Svc.Generation())
}

func (ta *testApp) addTask(in service.AddTaskIn) domain.TaskID {
	ta.t.Helper()
	out, err := ta.Svc.AddTask(ta.t.Context(), ta.meta(), in)
	if err != nil {
		ta.t.Fatal(err)
	}
	return out.Task.ID
}

func (ta *testApp) task(id domain.TaskID) domain.TaskDetail {
	ta.t.Helper()
	task, err := ta.Svc.GetTask(ta.t.Context(), testutil.Owner(), id)
	if err != nil {
		ta.t.Fatal(err)
	}
	return task
}

func (ta *testApp) opKinds() []string {
	ta.t.Helper()
	var kinds []string
	for cursor := ""; ; {
		p, err := ta.Svc.ListOps(ta.t.Context(), testutil.Owner(), service.ListOpsIn{Limit: 100, Cursor: cursor})
		if err != nil {
			ta.t.Fatal(err)
		}
		for _, op := range p.Items {
			kinds = append(kinds, string(op.Kind))
		}
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
	slices.Reverse(kinds)
	return kinds
}

func webOwner() domain.Actor {
	a := testutil.Owner()
	a.Channel = domain.ChanWeb
	return a
}

func (ta *testApp) get(path string, hdr ...string) *httptest.ResponseRecorder {
	ta.t.Helper()
	return ta.request(http.MethodGet, path, nil, hdr...)
}

func (ta *testApp) post(path string, form url.Values, hdr ...string) *httptest.ResponseRecorder {
	ta.t.Helper()
	return ta.request(http.MethodPost, path, form, hdr...)
}

// AIDEV-NOTE: ≤5 logins per testApp: PasswordGate burst on a fake clock
func (ta *testApp) login() *testApp {
	ta.t.Helper()
	w := ta.post("/ui/login", url.Values{"password": {testutil.Password}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/ui/" || ta.cookies[web.SessionCookie] == "" {
		ta.t.Fatalf("login = %d %v %s", w.Code, w.Header(), w.Body)
	}
	return ta
}

func (ta *testApp) submit(pagePath, action string, overrides url.Values) *httptest.ResponseRecorder {
	ta.t.Helper()
	w := ta.get(pagePath)
	if w.Code != http.StatusOK {
		ta.t.Fatalf("GET %s = %d %s", pagePath, w.Code, w.Body)
	}
	form := scrapeForm(ta.t, w.Body.String(), action)
	for key, values := range overrides {
		if len(values) == 0 {
			form.Del(key)
		} else {
			form[key] = slices.Clone(values)
		}
	}
	return ta.post(action, form)
}

func (ta *testApp) request(method, path string, form url.Values, hdr ...string) *httptest.ResponseRecorder {
	ta.t.Helper()
	r := httptest.NewRequestWithContext(ta.t.Context(), method, path, strings.NewReader(form.Encode()))
	r.Host = testListen
	for name, value := range ta.cookies {
		r.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "http://"+testListen)
	}
	if len(hdr)%2 != 0 {
		ta.t.Fatal("headers must be key/value pairs")
	}
	for i := 0; i < len(hdr); i += 2 {
		if hdr[i+1] == "" {
			r.Header.Del(hdr[i])
		} else {
			r.Header.Set(hdr[i], hdr[i+1])
		}
	}
	w := httptest.NewRecorder()
	ta.H.ServeHTTP(w, r)
	response := w.Result()
	defer response.Body.Close()
	for _, cookie := range response.Cookies() {
		// Keep both tr_session and tr_flash across the POST/redirect/GET cycle.
		if cookie.MaxAge < 0 {
			delete(ta.cookies, cookie.Name)
		} else {
			ta.cookies[cookie.Name] = cookie.Value
		}
	}
	return w
}

func parseHTML(t *testing.T, body string) *html.Node {
	t.Helper()
	n, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func walkHTML(n *html.Node, visit func(*html.Node)) {
	visit(n)
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		walkHTML(child, visit)
	}
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	walkHTML(n, func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
	})
	return b.String()
}

func scrapeForms(t *testing.T, body, action string) []url.Values {
	t.Helper()
	var forms []url.Values
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Type != html.ElementNode || n.Data != "form" || attr(n, "action") != action {
			return
		}
		form := make(url.Values)
		walkHTML(n, func(n *html.Node) {
			name := attr(n, "name")
			if n.Type != html.ElementNode || name == "" || hasAttr(n, "disabled") {
				return
			}
			switch n.Data {
			case "input":
				typ := strings.ToLower(attr(n, "type"))
				switch typ {
				case "submit", "button", "reset", "image", "file":
					// No control was clicked, and no file was selected by this helper.
					return
				}
				if (typ != "checkbox" && typ != "radio") || hasAttr(n, "checked") {
					form.Add(name, attr(n, "value"))
				}
			case "textarea":
				form.Add(name, nodeText(n))
			case "select":
				walkHTML(n, func(option *html.Node) {
					if option.Type == html.ElementNode && option.Data == "option" && hasAttr(option, "selected") {
						value := attr(option, "value")
						if !hasAttr(option, "value") {
							value = nodeText(option)
						}
						form.Add(name, value)
					}
				})
			}
		})
		forms = append(forms, form)
	})
	return forms
}

func scrapeForm(t *testing.T, body, action string) url.Values {
	t.Helper()
	forms := scrapeForms(t, body, action)
	if len(forms) == 0 {
		t.Fatalf("no form with action %q in %s", action, body)
	}
	return forms[0]
}

func scrapeFormWhere(t *testing.T, body, action string, match url.Values) url.Values {
	t.Helper()
	for _, form := range scrapeForms(t, body, action) {
		matches := true
		for key, values := range match {
			if !reflect.DeepEqual(form[key], values) {
				matches = false
				break
			}
		}
		if matches {
			return form
		}
	}
	t.Fatalf("no form with action %q matching %v", action, match)
	return nil
}

func assertInert(t *testing.T, body string) {
	t.Helper()
	for _, problem := range inertErrors(t, body) {
		t.Error(problem)
	}
}

// requireFieldErrors checks only the error list, never resubmit labels or values.
// Shared by action tests so field-name regressions cannot hide in saved input.
func requireFieldErrors(t *testing.T, body string, want ...string) {
	t.Helper()
	var got []string
	found := false
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Type != html.ElementNode || n.Data != "section" || !slices.Contains(strings.Fields(attr(n, "class")), "error") {
			return
		}
		found = true
		for list := n.FirstChild; list != nil; list = list.NextSibling {
			if list.Type != html.ElementNode || list.Data != "ul" {
				continue
			}
			for item := list.FirstChild; item != nil; item = item.NextSibling {
				if item.Type != html.ElementNode || item.Data != "li" {
					continue
				}
				if field, _, ok := strings.Cut(nodeText(item), ":"); ok {
					got = append(got, strings.TrimSpace(field))
				}
			}
		}
	})
	if !found {
		t.Fatal("missing error section")
	}
	want = slices.Clone(want)
	slices.Sort(want)
	slices.Sort(got)
	got, want = slices.Compact(got), slices.Compact(want)
	if !slices.Equal(got, want) {
		t.Errorf("error fields = %v, want %v", got, want)
	}
}

func TestRequireFieldErrorsScoping(t *testing.T) {
	const input = `<section class="flash"><ul><li>place: unrelated</li></ul></section>
<section class="error">
<ul><li>priority: invalid</li><li>due: bad date: someday</li><li>due: another error</li></ul>
<form><label>title</label><label>place</label><label>defer</label><input name="due" value="someday"></form>
</section>`
	requireFieldErrors(t, input, "due", "priority")
	requireFieldErrors(t, `<section class="error"><form><label>due</label><ul><li>due: saved input</li></ul></form></section>`)
}

func inertErrors(t *testing.T, body string) []string {
	t.Helper()
	var problems []string
	urlSpace := strings.NewReplacer("\t", "", "\n", "", "\r", "")
	scripts := 0
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		switch n.Data {
		case "script":
			scripts++
			if attr(n, "src") != "/static/"+web.HTMXAsset || nodeText(n) != "" {
				problems = append(problems, "unexpected script")
			}
		case "img", "iframe", "embed", "base", "object":
			problems = append(problems, fmt.Sprintf("unsafe element %s", n.Data))
		}
		for _, a := range n.Attr {
			key := strings.TrimPrefix(a.Key, "data-") // htmx also accepts data-hx-*.
			if strings.HasPrefix(a.Key, "on") || strings.HasPrefix(key, "hx-on") {
				problems = append(problems, fmt.Sprintf("event attribute %s", a.Key))
			}
			switch key {
			case "srcdoc":
				problems = append(problems, "unsafe srcdoc attribute")
			case "href", "src", "action", "formaction", "hx-get", "hx-post", "hx-put", "hx-patch", "hx-delete", "hx-push-url", "hx-replace-url":
				value := strings.ToLower(strings.TrimSpace(urlSpace.Replace(a.Val)))
				if strings.HasPrefix(value, "javascript:") || strings.HasPrefix(value, "vbscript:") || strings.HasPrefix(value, "data:") {
					problems = append(problems, fmt.Sprintf("unsafe %s = %q", a.Key, a.Val))
				}
			}
		}
	})
	if scripts != 1 {
		problems = append(problems, fmt.Sprintf("script count = %d, want only htmx", scripts))
	}

	return problems
}

func TestHarnessRequestsAndForms(t *testing.T) {
	ta := newTestApp(t)
	ta.H = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != testListen || r.Header.Get("X-Test") != "yes" {
			t.Errorf("request host/headers = %s %v", r.Host, r.Header)
		}
		if r.Method == http.MethodPost {
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.PostForm.Get("title") != "a&b" || r.Header.Get("Origin") != "http://"+testListen {
				t.Error("POST did not preserve form/default Origin")
			}
			http.SetCookie(w, &http.Cookie{Name: "test", Value: "saved"})
		} else {
			cookie, err := r.Cookie("test")
			if err != nil || cookie.Value != "saved" || r.Header.Get("Origin") != "" {
				t.Error("GET did not carry the cookie/delete the overridden header")
			}
			http.SetCookie(w, &http.Cookie{Name: "test", MaxAge: -1})
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if w := ta.post("/ui/test", url.Values{"title": {"a&b"}}, "X-Test", "yes"); w.Code != http.StatusNoContent {
		t.Fatal(w.Code)
	}
	if w := ta.get("/ui/test", "X-Test", "yes", "Origin", "ignored", "Origin", ""); w.Code != http.StatusNoContent {
		t.Fatal(w.Code)
	}
	if len(ta.cookies) != 0 {
		t.Errorf("deleted cookie retained: %v", ta.cookies)
	}
	body := `<form action="/ui/test"><input name="tag" value="a"><input name="tag" value="b">
<input type="checkbox" name="yes" value="1" checked><input type="checkbox" name="no" value="1">
<input type="radio" name="pick" value="a"><input type="radio" name="pick" value="b" checked>
<textarea name="notes">

first
second</textarea><select name="priority"><option value="H">High</option><option value="L" selected>Low</option></select></form>
<form action="/elsewhere"><input name="ignore" value="x"></form><form action="/ui/test"><input name="id" value="42"></form>`
	forms := scrapeForms(t, body, "/ui/test")
	want := []url.Values{{"tag": {"a", "b"}, "yes": {"1"}, "pick": {"b"}, "notes": {"\nfirst\nsecond"}, "priority": {"L"}}, {"id": {"42"}}}
	if !reflect.DeepEqual(forms, want) {
		t.Errorf("scraped forms = %#v", forms)
	}
	if got := scrapeFormWhere(t, body, "/ui/test", url.Values{"id": {"42"}}); !reflect.DeepEqual(got, want[1]) {
		t.Errorf("selected form = %v, want %v", got, want[1])
	}
}

func TestHarnessSubmitOverrides(t *testing.T) {
	ta := newTestApp(t)
	ta.H = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if r.URL.Path != "/ui/page" {
				t.Errorf("submit loaded %s", r.URL.Path)
			}
			_, _ = w.Write([]byte(`<form action="/ui/action"><input name="title" value="old"><input name="drop" value="x"><input name="id" value="1"><input name="id" value="2"></form>`))
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		want := url.Values{"title": {"new"}, "id": {"3", "4"}, "etag": {"etag-3", "etag-4"}}
		if r.URL.Path != "/ui/action" || !reflect.DeepEqual(r.PostForm, want) {
			t.Errorf("submit = %s %v, want /ui/action %v", r.URL.Path, r.PostForm, want)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if w := ta.submit("/ui/page", "/ui/action", url.Values{"title": {"new"}, "id": {"3", "4"}, "etag": {"etag-3", "etag-4"}, "drop": {}}); w.Code != http.StatusNoContent {
		t.Errorf("submit response = %d", w.Code)
	}
}

func TestAssertInertBrowserURLsAndElements(t *testing.T) {
	const script = `<script src="/static/htmx-2.0.11.min.js"></script>`
	for _, unsafe := range []string{
		"<a href=\"java\tscript:alert(1)\">x</a>", "<a href=\"java\nscript:alert(1)\">x</a>",
		"<form action=\"java\rscript:alert(1)\"></form>", `<button formaction="java&#9;script:alert(1)">x</button>`,
		`<a href=" VBSCRIPT:msgbox(1)">x</a>`, `<a href="data:text/html,x">x</a>`,
		`<embed src="/x">`, `<base href="/x">`, `<object data="/x"></object>`,
		`<div srcdoc="&lt;script&gt;x&lt;/script&gt;"></div>`,
		`<a hx-get="javascript:alert(1)">x</a>`, `<form hx-post="data:text/html,x"></form>`,
		`<a hx-push-url="java&#9;script:alert(1)">x</a>`, `<div hx-replace-url="data:text/html,x"></div>`,
		`<div hx-put="javascript:alert(1)"></div>`, `<div hx-patch="data:text/html,x"></div>`,
		`<div hx-delete="javascript:alert(1)"></div>`, `<div hx-on:click="alert(1)"></div>`,
		`<div data-hx-get="javascript:alert(1)"></div>`, `<div data-hx-on--load="alert(1)"></div>`,
	} {
		t.Run(unsafe, func(t *testing.T) {
			if problems := inertErrors(t, script+unsafe); len(problems) == 0 {
				t.Error("inert check accepted an active URL, element, or srcdoc attribute")
			}
		})
	}
	if problems := inertErrors(t, script+`<p>javascript: vbscript: data: &lt;embed&gt;</p><a href="/ui/">safe</a>`); len(problems) != 0 {
		t.Errorf("inert text rejected: %v", problems)
	}
}

func TestScrapeFormsSkipsInactiveControls(t *testing.T) {
	form := scrapeForm(t, `<form action="/ui/test">
<input name="title" value="kept"><input type="hidden" name="id" value="42">
<input type="submit" name="submit" value="bad"><input type="BUTTON" name="button" value="bad">
<input type="reset" name="reset" value="bad"><input type="image" name="image" value="bad">
<input type="file" name="file" value="bad"><button name="clicked" value="bad">Click</button>
</form>`, "/ui/test")
	if want := (url.Values{"title": {"kept"}, "id": {"42"}}); !reflect.DeepEqual(form, want) {
		t.Errorf("scraped inactive controls: %v", form)
	}
}
