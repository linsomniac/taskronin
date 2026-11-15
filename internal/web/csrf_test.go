package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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
