package server_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/testutil"
)

func TestWebUIOverTCP(t *testing.T) {
	// The standard cookie jar uses wall time. Keep the fake session expiry in
	// the future so this deterministic browser flow does not expire in 2026.
	srv := testutil.StartServer(t, testutil.WithWeb(), testutil.WithClock("2099-10-28 10:00"))
	browser := newWebBrowser(t, srv.WebURL)
	resp, body := browser.request(http.MethodGet, "/", nil)
	assertWebPage(t, resp, http.StatusOK, "/ui/login")
	scrapeWebHiddenForm(t, body, "/ui/login")

	// DF01: native form navigation under no-referrer sends Origin: null.
	login := url.Values{"password": {testutil.Password}}
	resp, _ = browser.request(http.MethodPost, "/ui/login", login, "Origin", "null")
	assertWebPage(t, resp, http.StatusForbidden, "/ui/login")
	resp, body = browser.request(http.MethodPost, "/ui/login", login,
		"Origin", "null", "Sec-Fetch-Site", "same-origin")
	assertWebPage(t, resp, http.StatusOK, "/ui/")
	today := parseWebHTML(t, body)
	foundToday := false
	walkWebHTML(today, func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "h1" && webNodeText(n) == "Today" {
			foundToday = true
		}
	})
	if !foundToday || !strings.Contains(webNodeText(today), "2099-10-28") {
		t.Fatal("login did not render Today using the injected clock")
	}

	form := scrapeWebHiddenForm(t, body, "/ui/tasks")
	form.Set("title", "From the browser")
	resp, body = browser.request(http.MethodPost, "/ui/tasks", form)
	assertWebPage(t, resp, http.StatusOK, "/ui/")
	if !strings.Contains(webNodeText(parseWebHTML(t, body)), "From the browser") {
		t.Fatal("added task is missing from Today")
	}
	tasks, err := srv.Client.ListTasks(t.Context(), client.TaskQuery{View: "triage"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks.Items) != 1 || tasks.Items[0].Title != "From the browser" {
		t.Fatalf("socket triage = %+v", tasks.Items)
	}
	task := tasks.Items[0]
	path := fmt.Sprintf("/ui/tasks/%d", task.ID)
	resp, body = browser.request(http.MethodGet, path, nil)
	assertWebPage(t, resp, http.StatusOK, path)
	stale := scrapeWebHiddenForm(t, body, path+"/complete")
	if stale.Get("etag") != task.ETag {
		t.Fatalf("page ETag = %q, socket ETag = %q", stale.Get("etag"), task.ETag)
	}
	stale.Set("as", "My unsaved browser title")
	_, err = srv.Client.UpdateTask(t.Context(), client.Mut{IfMatch: task.ETag}, task.ID,
		api.UpdateTaskRequest{Title: api.Opt[string]{Set: true, V: "Changed from CLI"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, htmx := range []bool{false, true} {
		t.Run(fmt.Sprintf("stale/htmx=%t", htmx), func(t *testing.T) {
			b := *browser
			b.t = t
			var headers []string
			status := http.StatusPreconditionFailed
			if htmx {
				headers = []string{"HX-Request", "true", "HX-Boosted", "true"}
				status = http.StatusOK
			}
			resp, body := b.request(http.MethodPost, path+"/complete", stale, headers...)
			assertWebPage(t, resp, status, path+"/complete")
			if htmx && (resp.header.Get("HX-Push-Url") != "false" || resp.header.Get("HX-Retarget") != "body" || resp.header.Get("HX-Reswap") != "innerHTML") {
				t.Fatalf("htmx error headers = %v", resp.header)
			}
			doc := parseWebHTML(t, body)
			found := false
			walkWebHTML(doc, func(n *html.Node) {
				if n.Type == html.ElementNode && n.Data == "section" && webAttr(n, "data-code") == "precondition_failed" {
					found = strings.Contains(webNodeText(n), "changed elsewhere")
				}
			})
			if !found {
				t.Fatal("stale form did not show the changed elsewhere error")
			}
			savedTitle, reload := false, false
			walkWebHTML(doc, func(n *html.Node) {
				if n.Type != html.ElementNode {
					return
				}
				if n.Data == "input" && webAttr(n, "name") == "as" && webAttr(n, "value") == stale.Get("as") {
					savedTitle = true
				}
				if n.Data == "a" && webAttr(n, "href") == path && webNodeText(n) == "reload" {
					reload = true
				}
			})
			if !savedTitle || !reload {
				t.Fatal("stale error lost the typed input or reload link")
			}
		})
	}
	current, err := srv.Client.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Title != "Changed from CLI" || current.Status != "open" || current.Version != task.Version+1 {
		t.Fatalf("stale form changed the task: %+v", current.Task)
	}

	resp, body = browser.request(http.MethodGet, "/ui/", nil)
	assertWebPage(t, resp, http.StatusOK, "/ui/")
	logout := scrapeWebHiddenForm(t, body, "/ui/logout")
	resp, _ = browser.request(http.MethodPost, "/ui/logout", logout,
		"Origin", "null", "Sec-Fetch-Site", "same-origin")
	assertWebPage(t, resp, http.StatusOK, "/ui/login")
	browser.client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	resp, _ = browser.request(http.MethodGet, "/ui/", nil)
	if resp.status != http.StatusSeeOther || resp.header.Get("Location") != "/ui/login" {
		t.Fatalf("GET after logout = %d, location %q", resp.status, resp.header.Get("Location"))
	}

	resp, _ = browser.request(http.MethodGet, "/ui/", nil, "Host", "evil.example")
	assertWebPage(t, resp, http.StatusBadRequest, "/ui/")
	resp, body = browser.request(http.MethodGet, "/healthz", nil)
	assertWebPage(t, resp, http.StatusOK, "/healthz")
	if strings.TrimSpace(body) != "ok" {
		t.Fatalf("health body = %q", body)
	}
}

func TestSCP04_WebAPIOverTCP(t *testing.T) {
	srv := testutil.StartServer(t, testutil.WithWeb())
	browser := newWebBrowser(t, srv.WebURL)
	token, err := os.ReadFile(srv.Paths.OwnerToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Client.WhoAmI(t.Context()); err != nil {
		t.Fatalf("owner token over unix socket: %v", err)
	}
	for _, path := range []string{"/api/v1", "/api/v1/whoami"} {
		resp, body := browser.request(http.MethodGet, path, nil, "Authorization", "Bearer "+strings.TrimSpace(string(token)))
		if resp.status != http.StatusUnauthorized || resp.header.Get("Content-Type") != "application/problem+json" || resp.header.Get("Cache-Control") != "no-store" {
			t.Fatalf("web API response = %d %v", resp.status, resp.header)
		}
		var problem api.Problem
		if err := json.Unmarshal([]byte(body), &problem); err != nil {
			t.Fatal(err)
		}
		if problem.Type != "urn:taskronin:problem:unauthenticated" || problem.Title != "Unauthorized" || problem.Status != 401 || problem.Code != "unauthenticated" {
			t.Fatalf("web API problem = %+v", problem)
		}
		if problem.RequestID == "" || problem.RequestID != resp.header.Get("X-Request-ID") {
			t.Fatalf("problem request ID = %q, header = %q", problem.RequestID, resp.header.Get("X-Request-ID"))
		}
	}
}

func TestWebOptionsStarOverTCP(t *testing.T) {
	srv := testutil.StartServer(t, testutil.WithWeb())
	browser := newWebBrowser(t, srv.WebURL)
	resp, _ := browser.request(http.MethodOptions, "*", nil)
	if resp.status != http.StatusNotFound {
		t.Fatalf("OPTIONS * = %d, want web route-class 404", resp.status)
	}
	resp, _ = browser.request(http.MethodOptions, "*", nil, "Host", "evil.example")
	if resp.status != http.StatusBadRequest {
		t.Fatalf("OPTIONS * with bad Host = %d, want 400", resp.status)
	}
}

type webBrowser struct {
	t      *testing.T
	client *http.Client
	base   string
}

type webResponse struct {
	status int
	header http.Header
	path   string
}

func newWebBrowser(t *testing.T, base string) *webBrowser {
	t.Helper()
	if base == "" {
		t.Fatal("web listener did not start: WebURL is empty")
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	t.Cleanup(transport.CloseIdleConnections)
	return &webBrowser{t: t, client: &http.Client{Jar: jar, Transport: transport, Timeout: 10 * time.Second}, base: base}
}

func (b *webBrowser) request(method, path string, form url.Values, headers ...string) (webResponse, string) {
	b.t.Helper()
	target := b.base + path
	if path == "*" {
		target = b.base
	}
	req, err := http.NewRequestWithContext(b.t.Context(), method, target, strings.NewReader(form.Encode()))
	if err != nil {
		b.t.Fatal(err)
	}
	if path == "*" {
		req.URL.Path = "*" // Send the asterisk request-target over real TCP.
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", b.base)
	}
	for i := 0; i < len(headers); i += 2 {
		if headers[i] == "Host" {
			req.Host = headers[i+1]
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		b.t.Fatal(err)
	}
	for name, want := range map[string]string{
		"Content-Security-Policy":    "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'",
		"X-Content-Type-Options":     "nosniff",
		"Referrer-Policy":            "no-referrer",
		"Cross-Origin-Opener-Policy": "same-origin",
		"Permissions-Policy":         "camera=(), geolocation=(), microphone=()",
	} {
		if got := resp.Header.Get(name); got != want {
			b.t.Errorf("%s %s: %s = %q, want %q", method, path, name, got, want)
		}
	}
	return webResponse{status: resp.StatusCode, header: resp.Header, path: resp.Request.URL.Path}, string(body)
}

func assertWebPage(t *testing.T, resp webResponse, status int, path string) {
	t.Helper()
	if resp.status != status || resp.path != path {
		t.Fatalf("response = %d %s, want %d %s", resp.status, resp.path, status, path)
	}
}

// C1, C9 and C24 carry all concurrency/CSRF state in hidden inputs. Scrape it
// from the real DOM instead of manufacturing tokens, nonces or generations.
func scrapeWebHiddenForm(t *testing.T, body, action string) url.Values {
	t.Helper()
	var form url.Values
	walkWebHTML(parseWebHTML(t, body), func(n *html.Node) {
		if n.Type != html.ElementNode || n.Data != "form" || webAttr(n, "action") != action {
			return
		}
		if form != nil || webAttr(n, "method") != "post" {
			t.Fatalf("expected one POST form for %s", action)
		}
		form = make(url.Values)
		walkWebHTML(n, func(field *html.Node) {
			if field.Type == html.ElementNode && field.Data == "input" && webAttr(field, "type") == "hidden" {
				form.Add(webAttr(field, "name"), webAttr(field, "value"))
			}
		})
	})
	if form == nil {
		t.Fatalf("missing form for %s", action)
	}
	if action != "/ui/login" {
		for _, name := range []string{"_csrf", "_nonce", "_gen", "_return"} {
			if form.Get(name) == "" {
				t.Fatalf("%s form is missing %s", action, name)
			}
		}
	}
	return form
}

func parseWebHTML(t *testing.T, body string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func walkWebHTML(n *html.Node, visit func(*html.Node)) {
	visit(n)
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		walkWebHTML(child, visit)
	}
}

func webAttr(n *html.Node, key string) string {
	for _, attr := range n.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

func webNodeText(n *html.Node) string {
	var text strings.Builder
	walkWebHTML(n, func(n *html.Node) {
		if n.Type == html.TextNode {
			text.WriteString(n.Data)
		}
	})
	return text.String()
}

func TestWebAccessLogRouteAndActor(t *testing.T) {
	srv := testutil.StartServer(t, testutil.WithWeb(), testutil.WithClock("2099-10-28 10:00"))
	browser := newWebBrowser(t, srv.WebURL)
	resp, _ := browser.request(http.MethodGet, "/ui/login", nil)
	loginID := resp.header.Get("X-Request-ID")
	browser.request(http.MethodPost, "/ui/login", url.Values{"password": {testutil.Password}})
	resp, _ = browser.request(http.MethodGet, "/ui/tasks/987?secret=not-for-logs", nil)
	taskID := resp.header.Get("X-Request-ID")
	// Stop joins all serving goroutines before inspecting their access-log records.
	srv.Stop()
	found := map[string]bool{}
	for line := range strings.SplitSeq(srv.Log.String(), "\n") {
		var record struct {
			Message      string `json:"msg"`
			ID           string `json:"request_id"`
			Route, Actor string
		}
		if json.Unmarshal([]byte(line), &record) != nil || record.Message != "HTTP request" {
			continue
		}
		var route, actor string
		switch record.ID {
		case loginID:
			route = "GET /ui/login"
		case taskID:
			route, actor = "GET /ui/tasks/{id}", "owner"
		default:
			continue
		}
		found[record.ID] = true
		if record.Route != route || record.Actor != actor {
			t.Errorf("access log = %+v, want %s / %s", record, route, actor)
		}
	}
	if !found[loginID] || !found[taskID] {
		t.Error("missing access records")
	}
	if strings.Contains(srv.Log.String(), "not-for-logs") {
		t.Error("query leaked into access log")
	}
}
