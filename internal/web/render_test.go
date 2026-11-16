package web_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"taskronin/internal/domain"
	"taskronin/internal/testutil"
	"taskronin/internal/web"
)

func TestStatusForEveryCode(t *testing.T) {
	for code, want := range map[domain.Code]int{
		domain.CodeMalformed: 400, domain.CodeUnauthenticated: 401,
		domain.CodeForbidden: 403, domain.CodePeerRejected: 403, domain.CodePasswordRequired: 403, domain.CodePasswordInvalid: 403,
		domain.CodeNotFound: 404, domain.CodeDeleted: 409, domain.CodeConflict: 409, domain.CodeRevertConflict: 409,
		domain.CodeNotRevertible: 409, domain.CodeNothingToUndo: 409, domain.CodeNothingToRedo: 409,
		domain.CodePrecondition: 412, domain.CodeGenerationMismatch: 412, domain.CodeTooLarge: 413, domain.CodeUnsupportedMedia: 415,
		domain.CodeValidation: 422, domain.CodeIdemMismatch: 422, domain.CodePreconditionReq: 428, domain.CodeIdemRequired: 428,
		domain.CodeRateLimited: 429, domain.CodeInternal: 500, domain.CodeBusy: 503, domain.Code("zzz"): 500,
	} {
		if got := web.StatusFor(code); got != want {
			t.Errorf("StatusFor(%s) = %d, want %d", code, got, want)
		}
	}
}

func TestRenderErrorStatus(t *testing.T) {
	ta := newTestApp(t)
	for _, tc := range []struct {
		name             string
		err              error
		status           int
		code             domain.Code
		text, log, retry string
	}{
		{"validation", domain.ValidationError(domain.FieldError{Field: "due", Message: "bad date"}), 422, domain.CodeValidation, "bad date", "", ""},
		{"internal", &domain.Error{Code: domain.CodeInternal, Detail: "private detail", Err: errors.New("secret sauce")}, 500, domain.CodeInternal, "internal error", "secret sauce", ""},
		{"internal without cause", domain.Errorf(domain.CodeInternal, "nil cause diagnostic"), 500, domain.CodeInternal, "internal error", "internal: nil cause diagnostic", ""},
		{"plain error", errors.New("plain private error"), 500, domain.CodeInternal, "internal error", "plain private error", ""},
		{"rate limited", &domain.Error{Code: domain.CodeRateLimited, RetryAfter: 2500 * time.Millisecond}, 429, domain.CodeRateLimited, "Too Many Requests", "", "3"},
		{"revert conflict", &domain.Error{Code: domain.CodeRevertConflict, Conflicts: []domain.RevertConflict{{OpID: domain.OpID{0xab, 0xcd, 0xef, 0x12}, Kind: "task.update"}}}, 409, domain.CodeRevertConflict, "abcdef12", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ta.Logs.Reset()
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ui/triage", nil)
			w := httptest.NewRecorder()
			w.Header().Set("X-Request-ID", "request-123")
			ta.App.RenderError(w, r, tc.err)
			body := w.Body.String()
			if w.Code != tc.status || w.Header().Get("Content-Type") != "text/html; charset=utf-8" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Retry-After") != tc.retry {
				t.Fatalf("response = %d %v %s", w.Code, w.Header(), body)
			}
			if !strings.Contains(body, `data-code="`+string(tc.code)+`"`) || !strings.Contains(nodeText(parseHTML(t, body)), tc.text) {
				t.Errorf("missing code/text in %s", body)
			}
			if tc.log != "" && (!strings.Contains(ta.Logs.String(), tc.log) || !strings.Contains(ta.Logs.String(), "/ui/triage") || !strings.Contains(ta.Logs.String(), "request-123")) {
				t.Errorf("missing diagnostic context: %s", ta.Logs)
			}
			for _, secret := range []string{"secret sauce", "private detail", "nil cause diagnostic", "plain private error"} {
				if strings.Contains(body, secret) {
					t.Errorf("body exposes %q", secret)
				}
			}
			if tc.code == domain.CodeRevertConflict && !strings.Contains(body, "task.update") {
				t.Error("missing conflict kind")
			}
			assertInert(t, body)
		})
	}
}

func errorPost(t *testing.T, form url.Values) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(web.WithSession(t.Context(), web.SessionInfo{Actor: webOwner(), CSRF: "current-token"}), http.MethodPost, "/ui/tasks", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRenderErrorHTMX(t *testing.T) {
	ta := newTestApp(t)
	r := errorPost(t, url.Values{"title": {"typed title"}, "_return": {"/ui/triage"}})
	r.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	ta.App.RenderError(w, r, domain.Errorf(domain.CodePrecondition, "stale"))
	assertHTMXError(t, w, domain.CodePrecondition)
	if !strings.Contains(w.Body.String(), "changed elsewhere") {
		t.Error("missing reload explanation")
	}
	var reload bool
	walkHTML(parseHTML(t, w.Body.String()), func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" && attr(n, "href") == "/ui/triage" && strings.Contains(strings.ToLower(nodeText(n)), "reload") {
			reload = true
		}
	})
	if !reload {
		t.Error("missing safe return reload link")
	}
}

func assertHTMXError(t *testing.T, w *httptest.ResponseRecorder, code domain.Code) {
	t.Helper()
	if w.Code != http.StatusOK || w.Header().Get("HX-Push-Url") != "false" || w.Header().Get("HX-Retarget") != "body" || w.Header().Get("HX-Reswap") != "innerHTML" || !strings.Contains(w.Body.String(), `data-code="`+string(code)+`"`) {
		t.Fatalf("htmx response = %d %v %s", w.Code, w.Header(), w.Body)
	}
}

func TestRenderErrorKeepsInput(t *testing.T) {
	ta := newTestApp(t)
	form := url.Values{"title": {"T"}, "notes": {"line1\nline2"}, "due": {"someday"}, "etag": {`"0123456789abcdef-3"`},
		"_gen": {"0123456789abcdef"}, "_return": {"/ui/triage"}, "_csrf": {"old-token"}, "_nonce": {strings.Repeat("a", 32)}, "password": {"pw-SECRET-zz9"},
		"id": {"42", "43"}, "tags": {"a", "b"}}
	w := httptest.NewRecorder()
	ta.App.RenderError(w, errorPost(t, form), domain.ValidationError(domain.FieldError{Field: "due", Message: "bad date"}))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d", w.Code)
	}
	got := scrapeForm(t, w.Body.String(), "/ui/tasks")
	for key, values := range form {
		switch key {
		case "_csrf", "_nonce", "password":
			continue
		}
		if !reflect.DeepEqual(got[key], values) {
			t.Errorf("%s = %q, want %q", key, got[key], values)
		}
	}
	if got.Get("_csrf") != "current-token" || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(got.Get("_nonce")) || got.Get("_nonce") == form.Get("_nonce") {
		t.Errorf("csrf/nonce = %v", got)
	}
	if got.Has("password") || strings.Contains(w.Body.String(), "pw-SECRET-zz9") || strings.Contains(ta.Logs.String(), "pw-SECRET-zz9") {
		t.Error("password leaked")
	}
	assertInert(t, w.Body.String())
}

func TestRenderErrorStaleInputReadOnly(t *testing.T) {
	ta := newTestApp(t)
	for _, code := range []domain.Code{domain.CodePrecondition, domain.CodeGenerationMismatch} {
		w := httptest.NewRecorder()
		form := url.Values{"title": {"typed title"}, "notes": {"\nfirst line\nsecond"}, "_gen": {"old-generation"}, "etag": {"old-etag"}}
		ta.App.RenderError(w, errorPost(t, form), domain.Errorf(code, "stale"))
		if w.Code != http.StatusPreconditionFailed || !strings.Contains(nodeText(parseHTML(t, w.Body.String())), "Your input — reload, then paste") {
			t.Fatalf("stale response = %d %s", w.Code, w.Body)
		}
		got := scrapeForm(t, w.Body.String(), "/ui/tasks")
		for key, values := range form {
			if !reflect.DeepEqual(got[key], values) {
				t.Errorf("stale %s = %v", key, got[key])
			}
		}
		walkHTML(parseHTML(t, w.Body.String()), func(n *html.Node) {
			if n.Type != html.ElementNode || n.Data != "form" || attr(n, "action") != "/ui/tasks" {
				return
			}
			walkHTML(n, func(n *html.Node) {
				if n.Data == "button" || (n.Data == "input" && attr(n, "type") == "submit") {
					t.Error("stale form has a submit control")
				}
				if (n.Data == "input" && attr(n, "type") != "hidden") || n.Data == "textarea" {
					if !hasAttr(n, "readonly") {
						t.Error("stale input is editable")
					}
				}
			})
		})
	}
}

func TestRenderErrorEscapesAndMapsFields(t *testing.T) {
	ta := newTestApp(t)
	hostile := `"><img src=x onerror=alert(1)><script>x</script>javascript:alert(1)`
	r := errorPost(t, url.Values{"title": {hostile}, "notes": {"\n" + hostile}, "_return": {"https://evil.example/"}})
	w := httptest.NewRecorder()
	err := domain.ValidationError(domain.FieldError{Field: "work_date", Message: "bad date"}, domain.FieldError{Field: "time", Message: "bad clock"})
	ta.App.RenderError(w, r, err)
	assertInert(t, w.Body.String())
	got := scrapeForm(t, w.Body.String(), "/ui/tasks")
	if got.Get("title") != hostile || got.Get("notes") != "\n"+hostile {
		t.Error("escaped input did not round trip")
	}
	text := nodeText(parseHTML(t, w.Body.String()))
	if !strings.Contains(text, "date: bad date") || !strings.Contains(text, "at: bad clock") || strings.Contains(text, "work_date:") || strings.Contains(text, "time:") {
		t.Errorf("field labels = %s", text)
	}
	if err.Fields[0].Field != "work_date" || err.Fields[1].Field != "time" {
		t.Error("renderer mutated service error")
	}
}

func TestRenderErrorNoResubmitForAuth(t *testing.T) {
	ta := newTestApp(t)
	for _, tc := range []struct {
		path string
		code domain.Code
	}{
		{"/ui/login", domain.CodePasswordInvalid}, {"/ui/logout", domain.CodeInternal},
		{"/ui/tasks", domain.CodeUnauthenticated}, {"/ui/tasks", domain.CodeForbidden}, {"/elsewhere", domain.CodeValidation},
	} {
		r := errorPost(t, url.Values{"password": {"pw-SECRET-zz9"}, "title": {"T"}})
		r.URL.Path = tc.path
		w := httptest.NewRecorder()
		ta.App.RenderError(w, r, domain.Errorf(tc.code, "failed"))
		if len(scrapeForms(t, w.Body.String(), tc.path)) > 0 && tc.path != "/ui/logout" {
			t.Errorf("unexpected resubmit form for %s %s", tc.path, tc.code)
		}
		if strings.Contains(w.Body.String(), "Your input") || strings.Contains(w.Body.String(), "pw-SECRET-zz9") {
			t.Errorf("unexpected saved input for %s %s", tc.path, tc.code)
		}
	}
}

func TestBodyLimitHTMX(t *testing.T) {
	ta := newTestApp(t)
	for _, tc := range []struct {
		contentType, body string
		code              domain.Code
	}{
		{"application/json", `{}`, domain.CodeUnsupportedMedia},
		{"application/x-www-form-urlencoded", "title=kept&bad=%zz&password=pw-SECRET-zz9", domain.CodeMalformed},
		{"application/x-www-form-urlencoded", "notes=" + strings.Repeat("a", 1<<20), domain.CodeTooLarge},
	} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ui/tasks", strings.NewReader(tc.body))
		r.Host = testListen
		r.Header.Set("Content-Type", tc.contentType)
		r.Header.Set("HX-Request", "true")
		w := httptest.NewRecorder()
		ta.H.ServeHTTP(w, r)
		assertHTMXError(t, w, tc.code)
		assertInert(t, w.Body.String())
		if len(scrapeForms(t, w.Body.String(), "/ui/tasks")) != 0 {
			t.Error("body-limit error before session middleware must not offer a resubmit form")
		}
		if strings.Contains(w.Body.String(), "pw-SECRET-zz9") || strings.Contains(ta.Logs.String(), "pw-SECRET-zz9") {
			t.Error("body-limit password leaked")
		}
	}
}

func TestLayoutCSSHash(t *testing.T) {
	ta := newTestApp(t)
	css, err := web.StaticFS.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	want := "/static/app.css?v=" + fmt.Sprintf("%x", sha256.Sum256(css))[:12]
	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ui/", nil)
	ta.App.RenderError(w, r, domain.Errorf(domain.CodeNotFound, "missing"))
	var href string
	walkHTML(parseHTML(t, w.Body.String()), func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "link" && attr(n, "rel") == "stylesheet" {
			href = attr(n, "href")
		}
		if n.Type == html.ElementNode && n.Data == "nav" {
			t.Error("logged-out page has navigation")
		}
	})
	if href != want {
		t.Errorf("stylesheet = %q, want %q", href, want)
	}
}

func TestNewPageAndHiddenInputs(t *testing.T) {
	ta := newTestApp(t)
	r := httptest.NewRequestWithContext(web.WithSession(t.Context(), web.SessionInfo{Actor: webOwner(), CSRF: "csrf-token"}), http.MethodGet, "/ui/log?from=2026-10-20", nil)
	p := ta.App.NewPage(r, "Log", "log", "page data")
	if p.Title != "Log" || p.Tab != "log" || p.Data != "page data" || p.CSRF != "csrf-token" || p.Gen != ta.Svc.Generation() || p.Today != testutil.D("2026-10-28") || p.Return != "/ui/log?from=2026-10-20" {
		t.Fatalf("page = %+v", p)
	}
	p.Err = domain.Errorf(domain.CodeNotFound, "missing")
	w := httptest.NewRecorder()
	ta.App.RenderPage(w, r, http.StatusNotFound, "error", p)
	form := scrapeForm(t, w.Body.String(), "/ui/logout")
	if form.Get("_gen") != string(p.Gen) || form.Get("_csrf") != p.CSRF || form.Get("_return") != p.Return || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(form.Get("_nonce")) {
		t.Errorf("logout hidden fields = %v", form)
	}
	var active string
	walkHTML(parseHTML(t, w.Body.String()), func(n *html.Node) {
		if attr(n, "aria-current") == "page" {
			active = attr(n, "href")
		}
	})
	if active != "/ui/log" {
		t.Errorf("active nav = %q", active)
	}
	for _, ret := range []string{"/ui/triage", "https://evil.example/"} {
		p := ta.App.NewPage(errorPost(t, url.Values{"_return": {ret}}), "title", "", nil)
		if p.Return != web.SafeReturn(ret) {
			t.Errorf("POST return = %q", p.Return)
		}
	}
}

func TestRenderPageFailureIsBuffered(t *testing.T) {
	ta := newTestApp(t)
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ui/", nil)
	// A nil Err fails after layout has already produced its head into the buffer.
	p := ta.App.NewPage(r, "title", "", nil)
	for _, name := range []string{"error", "missing"} {
		w := httptest.NewRecorder()
		ta.App.RenderPage(w, r, http.StatusOK, name, p)
		if w.Code != http.StatusInternalServerError || w.Body.String() != "internal error\n" || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("failed render = %d %v %s", w.Code, w.Header(), w.Body)
		}
	}
	if !strings.Contains(ta.Logs.String(), "error") || !strings.Contains(ta.Logs.String(), "missing") {
		t.Errorf("missing render diagnostics: %s", ta.Logs)
	}
}

func TestRenderErrorSanitizesDiagnostics(t *testing.T) {
	ta := newTestApp(t)
	r := errorPost(t, url.Values{"password": {"pw-SECRET-zz9"}})
	w := httptest.NewRecorder()
	de := &domain.Error{Code: domain.CodeInternal, Err: errors.New("private\n\x1b diagnostic pw-SECRET-zz9")}
	ta.App.RenderError(w, r, de)
	if strings.Contains(w.Body.String(), "private") || strings.Contains(ta.Logs.String(), "pw-SECRET-zz9") || !strings.Contains(ta.Logs.String(), "private�� diagnostic [redacted]") {
		t.Errorf("unsafe diagnostic: body=%s logs=%s", w.Body, ta.Logs)
	}
}

func TestRenderErrorServerRetryKeepsNonce(t *testing.T) {
	ta := newTestApp(t)
	const nonce = "0123456789abcdef0123456789abcdef"
	const explanation = "Try again resends the same request — if it was already saved, it will not be saved twice."
	for _, path := range []string{"/ui/undo", "/ui/redo", "/ui/tasks"} {
		for _, code := range []domain.Code{domain.CodeBusy, domain.CodeInternal} {
			for _, hx := range []string{"", "true"} {
				t.Run(path+"/"+string(code)+"/hx="+hx, func(t *testing.T) {
					form := url.Values{"_nonce": {nonce}, "_gen": {"0123456789abcdef"}, "_return": {"/ui/triage"}, "password": {"pw-SECRET-zz9"}}
					if path == "/ui/tasks" {
						form.Set("title", "typed title")
						form.Set("notes", "line1\nline2")
					}
					r := errorPost(t, form)
					r.URL.Path = path
					r.Header.Set("HX-Request", hx)
					w := httptest.NewRecorder()
					ta.App.RenderError(w, r, domain.Errorf(code, "failed"))
					wantStatus := web.StatusFor(code)
					if hx == "true" {
						wantStatus = http.StatusOK
					}
					if w.Code != wantStatus {
						t.Errorf("server retry status = %d, want %d", w.Code, wantStatus)
					}
					got := scrapeForm(t, resubmitBody(t, w.Body.String()), path)
					if !reflect.DeepEqual(got["_nonce"], []string{nonce}) {
						t.Errorf("retry nonce = %v, want submitted key", got["_nonce"])
					}
					for _, key := range []string{"title", "notes", "_gen", "_return"} {
						if !reflect.DeepEqual(got[key], form[key]) {
							t.Errorf("retry %s = %v, want %v", key, got[key], form[key])
						}
					}
					if !strings.Contains(nodeText(parseHTML(t, w.Body.String())), explanation) {
						t.Error("missing same-request retry explanation")
					}
					if !resubmitHasButton(t, w.Body.String(), path) {
						t.Error("same-key retry has no submit button")
					}
					walkHTML(parseHTML(t, w.Body.String()), func(n *html.Node) {
						if (attr(n, "name") == "title" || attr(n, "name") == "notes") && hasAttr(n, "readonly") {
							t.Error("server-error input is read-only")
						}
					})
					logout := scrapeForm(t, w.Body.String(), "/ui/logout")
					if logout.Get("_nonce") == nonce {
						t.Error("retry key leaked into a different action's form")
					}
					if got.Has("password") || strings.Contains(w.Body.String(), "pw-SECRET-zz9") {
						t.Error("retry exposed password")
					}
				})
			}
		}
	}
}

func TestRenderErrorRetryFreshNonce(t *testing.T) {
	ta := newTestApp(t)
	for _, tc := range []struct {
		name, nonce string
		code        domain.Code
	}{
		{"validation", strings.Repeat("a", 32), domain.CodeValidation},
		{"edited retry", strings.Repeat("a", 32), domain.CodeIdemMismatch},
		{"missing", "", domain.CodeBusy}, {"short", strings.Repeat("a", 31), domain.CodeBusy},
		{"long", strings.Repeat("a", 33), domain.CodeBusy}, {"uppercase", strings.Repeat("A", 32), domain.CodeBusy},
		{"nonhex", strings.Repeat("g", 32), domain.CodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"/ui/tasks", "/ui/undo", "/ui/redo"} {
				r := errorPost(t, url.Values{"_nonce": {tc.nonce}, "title": {"edited title"}})
				r.URL.Path = path
				w := httptest.NewRecorder()
				ta.App.RenderError(w, r, domain.Errorf(tc.code, "failed"))
				if tc.code == domain.CodeIdemMismatch && !strings.Contains(nodeText(parseHTML(t, w.Body.String())), "earlier attempt may have been saved") {
					t.Error("idempotency mismatch must warn about the earlier attempt")
				}
				if web.StatusFor(tc.code) < 500 && path != "/ui/tasks" {
					if len(scrapeForms(t, resubmitBody(t, w.Body.String()), path)) != 0 {
						t.Errorf("%s 4xx offers a fresh-key retry form", path)
					}
					continue
				}
				got := scrapeForm(t, resubmitBody(t, w.Body.String()), path)
				if len(got["_nonce"]) != 1 || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(got.Get("_nonce")) || got.Get("_nonce") == tc.nonce {
					t.Errorf("%s nonce = %v, want fresh lowercase hex", path, got["_nonce"])
				}
				wantButton := path == "/ui/tasks"
				if resubmitHasButton(t, w.Body.String(), path) != wantButton {
					t.Errorf("%s retry button present, want %v", path, wantButton)
				}
			}
		})
	}
}

func TestRenderErrorUndoRedoClientErrorsNeverRetry(t *testing.T) {
	ta := newTestApp(t)
	for _, path := range []string{"/ui/undo", "/ui/redo"} {
		for _, code := range []domain.Code{
			domain.CodeMalformed, domain.CodeUnauthenticated, domain.CodeForbidden, domain.CodeNotFound,
			domain.CodeNothingToUndo, domain.CodeNothingToRedo, domain.CodeRevertConflict,
			domain.CodePrecondition, domain.CodeGenerationMismatch, domain.CodeTooLarge,
			domain.CodeUnsupportedMedia, domain.CodeValidation, domain.CodeIdemMismatch,
			domain.CodePreconditionReq, domain.CodeIdemRequired, domain.CodeRateLimited,
		} {
			for _, hx := range []string{"", "true"} {
				t.Run(path+"/"+string(code)+"/hx="+hx, func(t *testing.T) {
					r := errorPost(t, url.Values{"_nonce": {strings.Repeat("a", 32)}, "_gen": {string(ta.Svc.Generation())}})
					r.URL.Path = path
					r.Header.Set("HX-Request", hx)
					w := httptest.NewRecorder()
					ta.App.RenderError(w, r, domain.Errorf(code, "failed"))
					assertErrorCode(t, w.Body.String(), string(code))
					if resubmitHasButton(t, w.Body.String(), path) {
						t.Error("4xx stack error offers Try again")
					}
				})
			}
		}
	}
}

// The nav also has undo/redo forms; retry assertions must inspect the error section.
func resubmitBody(t *testing.T, body string) string {
	t.Helper()
	var buf strings.Builder
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "section" && hasAttr(n, "data-code") {
			if err := html.Render(&buf, n); err != nil {
				t.Fatal(err)
			}
		}
	})
	if buf.Len() == 0 {
		t.Fatal("missing error section")
	}
	return buf.String()
}

func resubmitHasButton(t *testing.T, body, action string) bool {
	t.Helper()
	found := false
	walkHTML(parseHTML(t, resubmitBody(t, body)), func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "form" && attr(n, "action") == action {
			walkHTML(n, func(control *html.Node) {
				if control.Type == html.ElementNode && control.Data == "button" && attr(control, "type") == "submit" {
					found = true
				}
			})
		}
	})
	return found
}

func TestRenderErrorEscapedAction(t *testing.T) {
	ta := newTestApp(t)
	r := errorPost(t, url.Values{"title": {"typed title"}})
	var err error
	r.URL, err = url.Parse("/ui/tasks/4%3Fx%23y/edit")
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	ta.App.RenderError(w, r, domain.Errorf(domain.CodeNotFound, "not found"))
	got := scrapeForm(t, w.Body.String(), "/ui/tasks/4%3Fx%23y/edit")
	if got.Get("title") != "typed title" {
		t.Error("escaped-action input lost")
	}
}

func TestRenderErrorWithoutSessionOmitsResubmit(t *testing.T) {
	ta := newTestApp(t)
	r := errorPost(t, url.Values{"title": {"typed title"}, "_nonce": {strings.Repeat("a", 32)}}).WithContext(t.Context())
	w := httptest.NewRecorder()
	ta.App.RenderError(w, r, domain.Errorf(domain.CodeBusy, "busy"))
	if len(scrapeForms(t, w.Body.String(), "/ui/tasks")) != 0 {
		t.Error("sessionless error must not offer a resubmit form")
	}
}
