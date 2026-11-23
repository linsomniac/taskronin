package web_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/testutil"
	"github.com/linsomniac/taskronin/internal/web"
)

func TestStatusForEveryCode(t *testing.T) {
	// Read the actual server switch, since web cannot import server and a
	// dependency's export_test.go is unavailable to this external test package.
	codes := make(map[domain.Code]string)
	file, err := parser.ParseFile(token.NewFileSet(), "../domain/errors.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(file, func(node ast.Node) bool {
		v, ok := node.(*ast.ValueSpec)
		if !ok {
			return true
		}
		typ, ok := v.Type.(*ast.Ident)
		if !ok || typ.Name != "Code" {
			return true
		}
		for i, name := range v.Names {
			lit, ok := v.Values[i].(*ast.BasicLit)
			if !ok {
				t.Fatalf("nonliteral error code %s", name.Name)
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatal(err)
			}
			codes[domain.Code(value)] = name.Name
		}
		return false
	})
	if len(codes) != len(domain.ErrorCodes()) {
		t.Fatalf("ErrorCodes has %d entries, declarations have %d", len(domain.ErrorCodes()), len(codes))
	}
	file, err = parser.ParseFile(token.NewFileSet(), "../server/respond.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]int{
		"StatusBadRequest": http.StatusBadRequest, "StatusUnauthorized": http.StatusUnauthorized,
		"StatusForbidden": http.StatusForbidden, "StatusNotFound": http.StatusNotFound,
		"StatusConflict": http.StatusConflict, "StatusPreconditionFailed": http.StatusPreconditionFailed,
		"StatusRequestEntityTooLarge": http.StatusRequestEntityTooLarge, "StatusUnsupportedMediaType": http.StatusUnsupportedMediaType,
		"StatusUnprocessableEntity": http.StatusUnprocessableEntity, "StatusPreconditionRequired": http.StatusPreconditionRequired,
		"StatusTooManyRequests": http.StatusTooManyRequests, "StatusInternalServerError": http.StatusInternalServerError,
		"StatusServiceUnavailable": http.StatusServiceUnavailable,
	}
	serverMapping := make(map[string]int)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "statusFor" {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			clause, ok := node.(*ast.CaseClause)
			if !ok {
				return true
			}
			ret, ok := clause.Body[0].(*ast.ReturnStmt)
			if !ok {
				t.Fatal("server statusFor case must return a status")
			}
			value, ok := ret.Results[0].(*ast.SelectorExpr)
			if !ok {
				t.Fatal("server statusFor must return an HTTP constant")
			}
			status, ok := statuses[value.Sel.Name]
			if !ok {
				t.Fatalf("add HTTP constant %s to status test", value.Sel.Name)
			}
			for _, expr := range clause.List {
				code, ok := expr.(*ast.SelectorExpr)
				if !ok {
					t.Fatal("server statusFor must switch on domain codes")
				}
				serverMapping[code.Sel.Name] = status
			}
			return false
		})
	}
	for _, code := range domain.ErrorCodes() {
		name, ok := codes[code]
		if !ok {
			t.Fatalf("duplicate or undeclared ErrorCodes entry %s", code)
		}
		delete(codes, code)
		want, ok := serverMapping[name]
		if !ok {
			t.Fatalf("server has no explicit mapping for %s", name)
		}
		if got := web.StatusFor(code); got != want {
			t.Errorf("StatusFor(%s) = %d; server = %d", code, got, want)
		}
	}
	if got := web.StatusFor(domain.Code("unknown")); got != http.StatusInternalServerError {
		t.Errorf("unknown status = %d", got)
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
		got := readOnlyInput(t, resubmitBody(t, w.Body.String()))
		for key, values := range form {
			if !reflect.DeepEqual(got[key], values) {
				t.Errorf("stale %s = %v", key, got[key])
			}
		}
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
	for _, hx := range []string{"", "true"} {
		for _, name := range []string{"error", "missing"} {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ui/", nil)
			r.Header.Set("HX-Request", hx)
			// A nil Err fails after the layout has written its head to the buffer.
			p := ta.App.NewPage(r, "private partial title", "", nil)
			w := httptest.NewRecorder()
			ta.App.RenderPage(w, r, http.StatusOK, name, p)
			want := http.StatusInternalServerError
			if hx == "true" {
				want = http.StatusOK
				assertHTMXError(t, w, domain.CodeInternal)
			}
			if w.Code != want || w.Header().Get("Content-Type") != "text/html; charset=utf-8" || w.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("failed render = %d %v", w.Code, w.Header())
			}
			assertErrorCode(t, w.Body.String(), "internal")
			if strings.Contains(w.Body.String(), "private partial title") {
				t.Error("partial template leaked")
			}
		}
	}
	if !strings.Contains(ta.Logs.String(), "error") || !strings.Contains(ta.Logs.String(), "missing") {
		t.Error("missing render diagnostics")
	}
}

func TestRecoverHTML(t *testing.T) {
	ta := newTestApp(t).login()
	var err error
	ta.H, err = web.NewWithProbe(web.Options{Svc: ta.Svc, Clock: ta.Clock, Zone: testutil.Zone, Listen: testListen,
		Logger: slog.New(slog.NewTextHandler(ta.Logs, nil))}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("panic diagnostic\n\x1b pw-SECRET-zz9")
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, hx := range []string{"", "true"} {
		t.Run("hx="+hx, func(t *testing.T) {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("panic escaped web chain: %v", p)
				}
			}()
			form := scrapeForm(t, ta.get("/ui/").Body.String(), "/ui/logout")
			form.Set("password", "pw-SECRET-zz9")
			form.Set("title", "Keep this typed title")
			w := ta.post("/ui/probe", form, "HX-Request", hx)
			want := http.StatusInternalServerError
			if hx == "true" {
				want = http.StatusOK
				assertHTMXError(t, w, domain.CodeInternal)
			}
			if w.Code != want || w.Header().Get("Content-Type") != "text/html; charset=utf-8" || w.Header().Get("Content-Security-Policy") != web.CSP {
				t.Errorf("panic response = %d %v", w.Code, w.Header())
			}
			assertErrorCode(t, w.Body.String(), "internal")
			retry := scrapeForm(t, w.Body.String(), "/ui/probe")
			if retry.Get("title") != form.Get("title") || retry.Get("_nonce") != form.Get("_nonce") {
				t.Error("panic lost typed input or same-key retry")
			}
			if strings.Contains(w.Body.String(), "panic diagnostic") || strings.Contains(ta.Logs.String(), "pw-SECRET-zz9") {
				t.Error("private panic leaked")
			}
			if !strings.Contains(ta.Logs.String(), "panic diagnostic�� [redacted]") {
				t.Error("missing sanitized panic log")
			}
		})
	}
}

func TestMuxErrorsHTMX(t *testing.T) {
	ta := newTestApp(t).login()
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/ui/missing", http.StatusNotFound},
		{http.MethodPut, "/ui/tasks/1", http.StatusMethodNotAllowed},
	} {
		for _, hx := range []string{"", "true"} {
			w := ta.request(tc.method, tc.path, nil, "HX-Request", hx)
			if hx == "true" {
				assertHTMXError(t, w, domain.CodeNotFound)
			} else if w.Code != tc.status {
				t.Errorf("mux status = %d, want %d", w.Code, tc.status)
			}
		}
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
					if strings.Contains(nodeText(parseHTML(t, resubmitBody(t, w.Body.String()))), "Your input") {
						t.Error("empty input heading")
					}
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

func TestRecoverHTMLAbortsCommittedResponse(t *testing.T) {
	ta := newTestApp(t).login()
	for _, committed := range []bool{false, true} {
		h, err := web.NewWithProbe(web.Options{Svc: ta.Svc, Clock: ta.Clock, Zone: testutil.Zone, Listen: testListen, Logger: slog.New(slog.NewTextHandler(ta.Logs, nil))}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if committed {
				_, _ = w.Write([]byte("partial"))
				panic("after write")
			}
			panic(http.ErrAbortHandler)
		}))
		if err != nil {
			t.Fatal(err)
		}
		ta.H = h
		func() {
			defer func() {
				p := recover()
				if err, ok := p.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
					t.Errorf("abort = %v", p)
				}
			}()
			ta.get("/ui/probe")
			t.Error("expected abort")
		}()
	}
}

// Check actual DOM ancestry so Enter cannot implicitly submit a saved input.
func readOnlyInput(t *testing.T, body string) url.Values {
	t.Helper()
	values := make(url.Values)
	walkHTML(parseHTML(t, body), func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		if n.Data == "form" || n.Data == "button" || (n.Data == "input" && attr(n, "type") == "submit") {
			t.Error("read-only input can submit")
		}
		if n.Data != "input" && n.Data != "textarea" {
			return
		}
		name := attr(n, "name")
		if name == "password" || name == "_csrf" || name == "_nonce" {
			t.Error("read-only credential field")
		}
		if attr(n, "type") != "hidden" && !hasAttr(n, "readonly") {
			t.Errorf("editable saved %s", name)
		}
		value := attr(n, "value")
		if n.Data == "textarea" {
			value = nodeText(n)
		}
		values.Add(name, value)
	})
	return values
}
