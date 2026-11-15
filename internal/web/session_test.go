package web_test

import (
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"taskronin/internal/auth"
	"taskronin/internal/clock"
	"taskronin/internal/service"
	"taskronin/internal/testutil"
	"taskronin/internal/web"
)

func TestLoginSuccess(t *testing.T) {
	for _, tc := range []struct{ name, origin, site string }{
		{"allowlisted origin", "http://127.0.0.1:7878", ""},
		{"browser form", "null", "same-origin"},
		{"fetch metadata only", "", "same-origin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ta := newTestApp(t)
			w := ta.get("/ui/login")
			if w.Code != http.StatusOK {
				t.Fatalf("login page = %d %s", w.Code, w.Body)
			}
			assertLoginForm(t, w.Body.String())
			w = ta.post("/ui/login", url.Values{"password": {testutil.Password}}, "Origin", tc.origin, "Sec-Fetch-Site", tc.site)
			assertRedirect(t, w, "/ui/")
			cookie, err := http.ParseSetCookie(w.Header().Get("Set-Cookie"))
			if err != nil {
				t.Fatal(err)
			}
			if cookie.Name != "tr_session" || cookie.Path != "/" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Secure || cookie.Domain != "" {
				t.Errorf("cookie attributes = %+v", cookie)
			}
			if !cookie.Expires.Equal(ta.Clock.Now().Add(30*24*time.Hour).Truncate(time.Second)) || !regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`).MatchString(cookie.Value) {
				t.Error("invalid cookie expiry or value")
			}
			w = ta.get("/ui/")
			if w.Code != http.StatusOK || !strings.Contains(nodeText(parseHTML(t, w.Body.String())), "Today") {
				t.Fatalf("home = %d %s", w.Code, w.Body)
			}
			form := scrapeForm(t, w.Body.String(), "/ui/logout")
			if form.Get("_csrf") == "" {
				t.Error("authenticated nav has no CSRF token")
			}
			assertInert(t, w.Body.String())
		})
	}
}

func TestLoginWrongPassword(t *testing.T) {
	for _, hx := range []string{"", "true"} {
		t.Run("hx="+hx, func(t *testing.T) {
			ta := newTestApp(t)
			password := rand.Text()
			w := ta.post("/ui/login", url.Values{"password": {password}}, "HX-Request", hx)
			if w.Code != http.StatusForbidden || w.Header().Get("Set-Cookie") != "" {
				t.Fatalf("bad password = %d %v", w.Code, w.Header())
			}
			assertErrorCode(t, w.Body.String(), "password_invalid")
			assertLoginForm(t, w.Body.String())
			if strings.Contains(w.Body.String(), password) {
				t.Error("submitted password echoed")
			}
		})
	}
}

func TestLoginCrossOrigin(t *testing.T) {
	for _, tc := range []struct{ name, origin, site string }{
		{"foreign origin", "http://evil.example", ""},
		{"absent headers", "", ""},
		{"null without metadata", "null", ""},
		{"null cross-site", "null", "cross-site"},
		{"foreign origin with metadata", "http://evil.example", "same-origin"},
		{"allowlisted origin same-site", "http://127.0.0.1:7878", "same-site"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ta := newTestApp(t)
			w := ta.post("/ui/login", url.Values{"password": {testutil.Password}}, "Origin", tc.origin, "Sec-Fetch-Site", tc.site)
			if w.Code != http.StatusForbidden || w.Header().Get("Set-Cookie") != "" {
				t.Fatalf("cross-origin login = %d %v", w.Code, w.Header())
			}
			assertErrorCode(t, w.Body.String(), "forbidden")
		})
	}
}

func TestLoginRegeneratesSession(t *testing.T) {
	ta := newTestApp(t).login()
	old := ta.cookies[web.SessionCookie]
	ta.login()
	current := ta.cookies[web.SessionCookie]
	if old == current {
		t.Fatal("login reused session cookie")
	}
	ta.cookies[web.SessionCookie] = old
	assertRedirect(t, ta.get("/ui/"), "/ui/login")
	ta.cookies[web.SessionCookie] = current
	if w := ta.get("/ui/"); w.Code != http.StatusOK {
		t.Fatalf("replacement session = %d", w.Code)
	}
}

func TestSCP05_BearerOnUI(t *testing.T) {
	ta := newTestApp(t)
	bearer := "Bearer tkr_1_" + strings.Repeat("A", 43)
	assertRedirect(t, ta.get("/ui/", "Authorization", bearer), "/ui/login")
	assertSessionExpired(t, ta.post("/ui/logout", nil, "Authorization", bearer))
}

func TestSessionIdleExpiry(t *testing.T) {
	ta := newTestApp(t).login()
	ta.Clock.Advance(7*24*time.Hour + time.Minute)
	assertRedirect(t, ta.get("/ui/"), "/ui/login")
	assertSessionExpired(t, ta.post("/ui/logout", nil))
	assertSessionExpired(t, ta.post("/ui/logout", nil, "HX-Request", "true"))
}

func TestSessionAbsoluteExpiry(t *testing.T) {
	ta := newTestApp(t).login()
	for range 4 {
		ta.Clock.Advance(6 * 24 * time.Hour)
		if w := ta.get("/ui/"); w.Code != http.StatusOK {
			t.Fatalf("active session = %d", w.Code)
		}
	}
	ta.Clock.Advance(6 * 24 * time.Hour)
	assertRedirect(t, ta.get("/ui/"), "/ui/login")
}

func TestSessionPathAndMethodScope(t *testing.T) {
	ta := newTestApp(t)
	for _, cookie := range []string{"", "invalid", strings.Repeat("A", 43)} {
		ta.cookies[web.SessionCookie] = cookie
		for _, path := range []string{"/ui", "/ui/", "/ui/missing", "/ui/login/", "/ui/login/child"} {
			for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
				w := ta.request(method, path, nil)
				if method == http.MethodGet || method == http.MethodHead {
					assertRedirect(t, w, "/ui/login")
				} else {
					assertSessionExpired(t, w)
				}
			}
		}
	}
	if w := ta.request(http.MethodPut, "/ui/login", nil); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("exact login exemption = %d", w.Code)
	}
}

func TestLogout(t *testing.T) {
	ta := newTestApp(t).login()
	old := ta.cookies[web.SessionCookie]
	w := ta.submit("/ui/", "/ui/logout", nil)
	assertRedirect(t, w, "/ui/login")
	cookie, err := http.ParseSetCookie(w.Header().Get("Set-Cookie"))
	if err != nil {
		t.Fatal(err)
	}
	if cookie.Name != "tr_session" || cookie.MaxAge >= 0 || cookie.Value != "" || cookie.Path != "/" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Secure {
		t.Errorf("cleared cookie = %+v", cookie)
	}
	ta.cookies[web.SessionCookie] = old
	assertRedirect(t, ta.get("/ui/"), "/ui/login")
}

func TestIDM04_LoginNotLogged(t *testing.T) {
	ta := newTestApp(t)
	bad := rand.Text()
	failed := ta.post("/ui/login", url.Values{"password": {bad}})
	success := ta.post("/ui/login", url.Values{"password": {testutil.Password}})
	if failed.Code != http.StatusForbidden || success.Code != http.StatusSeeOther {
		t.Fatalf("login statuses = %d, %d", failed.Code, success.Code)
	}
	for _, output := range []string{failed.Body.String(), success.Body.String(), ta.Logs.String()} {
		if strings.Contains(output, bad) || strings.Contains(output, testutil.Password) {
			t.Error("password exposed in response or logs")
		}
	}
}

func TestLoginBodyLimits(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
		status                  int
	}{
		{"too large", "application/x-www-form-urlencoded", strings.Repeat("a", 1<<20+1), http.StatusRequestEntityTooLarge},
		{"JSON", "application/json", `{}`, http.StatusUnsupportedMediaType},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ta := newTestApp(t)
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ui/login", strings.NewReader(tc.body))
			r.Host = testListen
			r.Header.Set("Content-Type", tc.contentType)
			r.Header.Set("Origin", "http://"+testListen)
			w := httptest.NewRecorder()
			ta.H.ServeHTTP(w, r)
			if w.Code != tc.status || w.Header().Get("Set-Cookie") != "" {
				t.Fatalf("body limit = %d %v", w.Code, w.Header())
			}
		})
	}
}

type loginGateClock struct {
	clock.Clock
	waits chan time.Duration
}

func (c loginGateClock) After(d time.Duration) <-chan time.Time {
	ch := c.Clock.After(d)
	c.waits <- d
	return ch
}

func TestLoginRateLimited(t *testing.T) {
	for _, tc := range []struct{ remote, key string }{
		{"127.0.0.1:54321", "127.0.0.1"},
		{"[::1]:54321", "::1"},
		{"unparsed-remote", "unparsed-remote"},
	} {
		t.Run(tc.remote, func(t *testing.T) {
			waits := make(chan time.Duration, 3)
			var gate *auth.PasswordGate
			ta := newTestApp(t, func(o *service.Options) {
				gate = auth.NewPasswordGate(loginGateClock{Clock: o.Clock, waits: waits})
				o.Passwords = gate
			})
			// Consume reservations without argon2 work; an invalid hash fails only
			// after the real gate admits the verification.
			for range 5 {
				_, _ = gate.Verify(t.Context(), tc.key, "", testutil.Password)
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{}, 3)
			pending := 0
			defer func() {
				cancel()
				for range pending {
					<-done
				}
			}()
			for _, want := range []time.Duration{5 * time.Second, 10 * time.Second, 15 * time.Second} {
				pending++
				go func() {
					_, _ = gate.Verify(ctx, tc.key, "", testutil.Password)
					done <- struct{}{}
				}()
				select {
				case delay := <-waits:
					if delay != want {
						t.Fatalf("gate delay = %v, want %v", delay, want)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("gate reservation timed out")
				}
			}
			requestCtx, requestCancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer requestCancel()
			r := httptest.NewRequestWithContext(requestCtx, http.MethodPost, "/ui/login", strings.NewReader(url.Values{"password": {testutil.Password}}.Encode()))
			r.Host, r.RemoteAddr = testListen, tc.remote
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", "http://"+testListen)
			r.Header.Set("X-Forwarded-For", "192.0.2.99")
			w := httptest.NewRecorder()
			ta.H.ServeHTTP(w, r)
			if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "20" || w.Header().Get("Set-Cookie") != "" {
				t.Fatalf("rate limited login = %d %v", w.Code, w.Header())
			}
			assertErrorCode(t, w.Body.String(), "rate_limited")
			assertLoginForm(t, w.Body.String())
			if strings.Contains(w.Body.String(), testutil.Password) {
				t.Error("rate limit page exposed password")
			}
		})
	}
}

func assertRedirect(t *testing.T, w *httptest.ResponseRecorder, location string) {
	t.Helper()
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != location {
		t.Fatalf("redirect = %d %v, want 303 %s", w.Code, w.Header(), location)
	}
}

func assertErrorCode(t *testing.T, body, code string) {
	t.Helper()
	found := false
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Type == html.ElementNode && attr(n, "data-code") == code {
			found = true
		}
	})
	if !found {
		t.Errorf("missing error code %s", code)
	}
}

func assertLoginForm(t *testing.T, body string) {
	t.Helper()
	form, password := false, false
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		switch n.Data {
		case "nav":
			t.Error("login has nav")
		case "form":
			form = attr(n, "action") == "/ui/login" && attr(n, "method") == "post" && attr(n, "hx-boost") == "false"
		case "input":
			if attr(n, "type") == "hidden" {
				t.Error("login has hidden input")
			}
			if attr(n, "name") == "password" {
				password = attr(n, "type") == "password" && attr(n, "autocomplete") == "current-password" && attr(n, "value") == ""
			}
		}
	})
	if !form || !password {
		t.Error("missing unboosted login form or empty password input")
	}
	assertInert(t, body)
}

func assertSessionExpired(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusUnauthorized || w.Header().Get("HX-Redirect") != "/ui/login" || w.Header().Get("Content-Type") != "text/html; charset=utf-8" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("expired session = %d %v %s", w.Code, w.Header(), w.Body)
	}
	doc := parseHTML(t, w.Body.String())
	if !strings.Contains(nodeText(doc), "Session expired — log in") {
		t.Error("missing session-expired explanation")
	}
	link := false
	walkHTML(doc, func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" && attr(n, "href") == "/ui/login" {
			link = true
		}
		if n.Type == html.ElementNode && n.Data == "nav" {
			t.Error("expired session has nav")
		}
	})
	if !link {
		t.Error("missing login link")
	}
}
