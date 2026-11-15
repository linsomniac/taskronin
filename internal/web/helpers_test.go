package web_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"taskronin/internal/clock"
	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/testutil"
	"taskronin/internal/web"
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

func newTestApp(t *testing.T) *testApp {
	t.Helper()
	svc, clk := testutil.Service(t)
	logs := new(bytes.Buffer)
	a, err := web.NewApp(web.Options{Svc: svc, Clock: clk, Zone: testutil.Zone,
		WeekStart: time.Monday, Listen: testListen,
		Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	if err != nil {
		t.Fatal(err)
	}
	return &testApp{t: t, App: a, H: a.Handler(), Svc: svc, Clock: clk, Logs: logs, cookies: make(map[string]string)}
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
				typ := attr(n, "type")
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

func assertInert(t *testing.T, body string) {
	t.Helper()
	scripts := 0
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		switch n.Data {
		case "script":
			scripts++
			if attr(n, "src") != "/static/"+web.HTMXAsset || nodeText(n) != "" {
				t.Error("unexpected script")
			}
		case "img", "iframe", "object":
			t.Errorf("unsafe element %s", n.Data)
		}
		for _, a := range n.Attr {
			if strings.HasPrefix(a.Key, "on") {
				t.Errorf("event attribute %s", a.Key)
			}
			switch a.Key {
			case "href", "src", "action", "formaction":
				value := strings.ToLower(strings.TrimSpace(a.Val))
				if strings.HasPrefix(value, "javascript:") || strings.HasPrefix(value, "data:") {
					t.Errorf("unsafe %s = %q", a.Key, a.Val)
				}
			}
		}
	})
	if scripts != 1 {
		t.Errorf("script count = %d, want only htmx", scripts)
	}
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
}
