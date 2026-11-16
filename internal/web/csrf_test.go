package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"taskronin/internal/domain"
	"taskronin/internal/web"
)

func TestCSRF(t *testing.T) {
	for _, tc := range []struct {
		name, origin, site, token string
		status                    int
	}{
		{"1 scraped form", "http://127.0.0.1:7878", "", "form", 303},
		{"2 header only", "", "same-origin", "header", 303},
		{"3 localhost alias", "http://localhost:7878", "", "form", 303},
		{"4 missing token", "http://127.0.0.1:7878", "", "missing", 403},
		{"5 wrong token", "http://127.0.0.1:7878", "", "wrong", 403},
		{"6 another session token", "http://127.0.0.1:7878", "", "other", 403},
		{"7 foreign origin", "http://evil.example", "", "form", 403},
		{"8 HTTPS origin", "https://127.0.0.1:7878", "", "form", 403},
		{"9 Origin null no Sec-Fetch-Site", "null", "", "form", 403},
		{"10 absent headers", "", "", "form", 403},
		{"11 cross-site without Origin", "", "cross-site", "form", 403},
		{"12 allowlisted origin same-site", "http://127.0.0.1:7878", "same-site", "form", 403},
		{"13 empty form uses header", "http://127.0.0.1:7878", "", "empty with header", 303},
		{"browser same-origin", "null", "same-origin", "form", 303},
		{"browser cross-site", "null", "cross-site", "form", 403},
		{"foreign origin overrides metadata", "http://evil.example", "same-origin", "form", 403},
		{"empty form without header", "http://127.0.0.1:7878", "", "empty", 403},
		{"nonempty form overrides header", "http://127.0.0.1:7878", "", "wrong with header", 403},
		{"valid form overrides wrong header", "http://127.0.0.1:7878", "", "form with wrong header", 303},
		{"token in query only", "http://127.0.0.1:7878", "", "query", 403},
		{"metadata none", "http://127.0.0.1:7878", "none", "form", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ta := newTestApp(t).login()
			form := scrapeForm(t, ta.get("/ui/").Body.String(), "/ui/logout")
			token, header, path := form.Get("_csrf"), "", "/ui/logout"
			switch tc.token {
			case "missing":
				form.Del("_csrf")
			case "header":
				form.Del("_csrf")
				header = token
			case "empty":
				form.Set("_csrf", "")
			case "empty with header":
				form.Set("_csrf", "")
				header = token
			case "wrong", "wrong with header":
				form.Set("_csrf", strings.Repeat("x", len(token)))
				if tc.token == "wrong with header" {
					header = token
				}
			case "form with wrong header":
				header = "wrong"
			case "other":
				old := ta.cookies[web.SessionCookie]
				delete(ta.cookies, web.SessionCookie)
				ta.login()
				other := scrapeForm(t, ta.get("/ui/").Body.String(), "/ui/logout").Get("_csrf")
				ta.cookies[web.SessionCookie] = old
				form.Set("_csrf", other)
			case "query":
				form.Del("_csrf")
				path += "?_csrf=" + url.QueryEscape(token)
			}
			w := ta.post(path, form, "Origin", tc.origin, "Sec-Fetch-Site", tc.site, "X-CSRF-Token", header)
			if w.Code != tc.status {
				t.Fatalf("CSRF response = %d %v %s, want %d", w.Code, w.Header(), w.Body, tc.status)
			}
			if tc.status == http.StatusForbidden {
				assertErrorCode(t, w.Body.String(), "forbidden")
				if w := ta.get("/ui/"); w.Code != http.StatusOK {
					t.Errorf("refused request invalidated session: %d", w.Code)
				}
			} else {
				assertRedirect(t, w, "/ui/login")
			}
		})
	}
}

func TestCSRFAllPOSTPaths(t *testing.T) {
	ta := newTestApp(t)
	for _, path := range []string{"/", "/healthz", "/static/app.css", "/static/../ui/logout"} {
		w := ta.post(path, nil)
		if w.Code != http.StatusForbidden {
			t.Errorf("POST %s = %d, want 403 before mux", path, w.Code)
		}
	}
}

func TestCSRFHTMX(t *testing.T) {
	ta := newTestApp(t).login()
	w := ta.post("/ui/logout", nil, "HX-Request", "true")
	assertHTMXError(t, w, domain.CodeForbidden)
	if w := ta.get("/ui/"); w.Code != http.StatusOK {
		t.Error("htmx CSRF refusal logged out the session")
	}
}

func TestCSRFPresentEmptyHeaders(t *testing.T) {
	for _, name := range []string{"Origin", "Sec-Fetch-Site"} {
		t.Run(name, func(t *testing.T) {
			ta := newTestApp(t).login()
			form := scrapeForm(t, ta.get("/ui/").Body.String(), "/ui/logout")
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ui/logout", strings.NewReader(form.Encode()))
			r.Host = testListen
			r.AddCookie(&http.Cookie{Name: web.SessionCookie, Value: ta.cookies[web.SessionCookie]})
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", "http://"+testListen)
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set(name, "")
			w := httptest.NewRecorder()
			ta.H.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				t.Errorf("present empty %s = %d, want 403", name, w.Code)
			}
		})
	}
}

func TestStaleSessionInput(t *testing.T) {
	ta := newTestApp(t).login()
	form := scrapeForm(t, ta.get("/ui/").Body.String(), "/ui/tasks")
	form.Set("title", `Typed <script>alert(1)</script>`)
	form.Set("notes", "\nTyped notes\n<script>x</script>")
	form.Set("password", "pw-SECRET-zz9")
	ta.login()
	for _, origin := range []string{"http://" + testListen, "http://evil.example"} {
		for _, hx := range []string{"", "true"} {
			w := ta.post("/ui/tasks", form, "Origin", origin, "HX-Request", hx)
			if hx == "true" {
				assertHTMXError(t, w, domain.CodeForbidden)
			} else if w.Code != http.StatusForbidden {
				t.Fatalf("CSRF = %d", w.Code)
			}
			body := resubmitBody(t, w.Body.String())
			if origin == "http://"+testListen {
				requireText(t, body, "Your session changed (you logged in again) — reload, then paste")
				saved := readOnlyInput(t, body)
				if saved.Get("title") != form.Get("title") || saved.Get("notes") != form.Get("notes") {
					t.Error("stale session lost typed text")
				}
			} else {
				requireText(t, body, "cross-site request refused")
				walkHTML(parseHTML(t, body), func(n *html.Node) {
					if n.Type == html.ElementNode && (n.Data == "input" || n.Data == "textarea") {
						t.Error("cross-site request echoed input")
					}
				})
			}
			for _, secret := range []string{form.Get("_csrf"), form.Get("_nonce"), "pw-SECRET-zz9"} {
				if strings.Contains(body, secret) || strings.Contains(ta.Logs.String(), secret) {
					t.Error("stale credential echoed or logged")
				}
			}
			assertInert(t, w.Body.String())
		}
	}
	if len(ta.opKinds()) != 0 {
		t.Error("stale CSRF performed an operation")
	}
}

func TestRouteTableSessionAndCSRF(t *testing.T) {
	ta := newTestApp(t)
	public := map[string]bool{"GET /ui/login": true, "POST /ui/login": true, "GET /{$}": true, "GET /healthz": true, "GET /static/{name}": true}
	pathFor := func(pattern string) string {
		_, path, ok := strings.Cut(pattern, " ")
		if !ok {
			t.Fatalf("route lacks method: %s", pattern)
		}
		return strings.NewReplacer("{$}", "", "{id}", "1", "{op}", strings.Repeat("a", 32), "{name}", "app.css").Replace(path)
	}
	for _, pattern := range web.RoutePatterns() {
		if public[pattern] {
			continue
		}
		path := pathFor(pattern)
		t.Run("session/"+pattern, func(t *testing.T) {
			assertRedirect(t, ta.get(path), "/ui/login")
			assertSessionExpired(t, ta.post(path, nil))
		})
	}
	ta.login()
	for _, pattern := range web.RoutePatterns() {
		if !strings.HasPrefix(pattern, "POST ") || pattern == "POST /ui/login" {
			continue
		}
		t.Run("csrf/"+pattern, func(t *testing.T) {
			w := ta.post(pathFor(pattern), nil)
			if w.Code != http.StatusForbidden {
				t.Errorf("missing CSRF = %d, want 403", w.Code)
			}
		})
	}
}
