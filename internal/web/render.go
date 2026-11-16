package web

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/markdown"
	"taskronin/internal/textsafe"
)

type page struct {
	Title, Tab string
	CSRF       string
	Gen        domain.Generation
	Return     string
	Today      civil.Date
	Version    string // DF23: embedded CSS content hash, independent of build version.
	Data       any
	Err        *domain.Error
	Resubmit   *resubmit
	Flash      []string
}

type resubmit struct {
	Action   string
	Nonce    string // A valid submitted key reused only after a server error.
	CanRetry bool
	ReadOnly bool
	Keep     url.Values
	Fields   []resubmitField
}

type resubmitField struct {
	Name, Value string
	Long        bool
}

// AIDEV-NOTE: Row partials receive (row $ .), not just the row: use .P for
// page state (template "hidden" .P, etag .P.Gen .V.Version), and .V for data.
// Page forms use template "hidden" $; partials.html forms use "hidden" .P.
type rowCtx struct {
	P page
	V any
}

var cssVersion = func() string {
	css, err := staticFS.ReadFile("static/app.css")
	if err != nil {
		panic(fmt.Errorf("embedded stylesheet: %w", err))
	}
	sum := sha256.Sum256(css)
	return hex.EncodeToString(sum[:])[:12]
}()

func (a *app) parseTemplates() error {
	fm := template.FuncMap{
		"nonce": func() (string, error) {
			var b [16]byte
			if _, err := rand.Read(b[:]); err != nil {
				return "", err
			}
			return hex.EncodeToString(b[:]), nil
		},
		"etag": domain.FormatETag,
		"md":   markdown.Render,
		"day": func(d civil.Date) string {
			return d.Weekday().String()[:3] + " " + d.String()
		},
		"hhmm": func(v any) (string, error) {
			switch m := v.(type) {
			case civil.Minute:
				return m.String(), nil
			case civil.EndMinute:
				return m.String(), nil
			default:
				return "", fmt.Errorf("hhmm: unsupported type %T", v)
			}
		},
		"wall":     func(t time.Time) string { return t.In(a.o.Zone.Location()).Format("Mon 2006-01-02 15:04") },
		"short":    func(id domain.OpID) string { return id.String()[:8] },
		"jsontext": func(b json.RawMessage) string { return string(b) },
		"row":      func(p page, v any) rowCtx { return rowCtx{P: p, V: v} },
	}
	entries, err := templateFS.ReadDir("templates")
	if err != nil {
		return err
	}
	a.templates = make(map[string]*template.Template)
	for _, entry := range entries {
		file := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(file, ".html") || file == "layout.html" || file == "partials.html" {
			continue
		}
		name := strings.TrimSuffix(file, ".html")
		tmpl, err := template.New(name).Funcs(fm).ParseFS(templateFS,
			"templates/layout.html", "templates/partials.html", "templates/"+file)
		if err != nil {
			return fmt.Errorf("parse page %s: %w", name, err)
		}
		a.templates[name] = tmpl
	}
	return nil
}

func (a *app) newPage(r *http.Request, title, tab string, data any) page {
	s, _ := sessionFrom(r.Context())
	ret := r.URL.RequestURI()
	if r.Method == http.MethodPost {
		ret = safeReturn(r.PostForm.Get("_return"))
	}
	return page{Title: title, Tab: tab, CSRF: s.CSRF, Gen: a.o.Svc.Generation(),
		Return: ret, Today: a.o.Zone.DateOf(a.o.Clock.Now()), Version: cssVersion, Data: data}
}

func (a *app) renderPage(w http.ResponseWriter, r *http.Request, status int, name string, p page) {
	p.Flash = a.readFlash(w, r)
	var buf bytes.Buffer
	tmpl, ok := a.templates[name]
	var err error
	if !ok {
		err = fmt.Errorf("unknown page %q", name)
	} else {
		err = tmpl.ExecuteTemplate(&buf, "layout", p)
	}
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		a.logError(w, r, err)
		// A broken error template cannot safely recurse through renderError.
		// This constant HTML still uses the shared htmx status/header rule.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(errorStatus(w, r, http.StatusInternalServerError))
		_, _ = w.Write([]byte(`<!doctype html><html lang="en"><title>Internal Server Error</title><body><section class="error" data-code="internal"><h1>Internal Server Error</h1><p>internal error</p></section></body></html>`))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func (a *app) logError(w http.ResponseWriter, r *http.Request, err error) {
	safe := func(s string) string {
		// Never let a diagnostic that quotes a supplied password leak it.
		for _, password := range r.PostForm["password"] {
			if password != "" {
				s = strings.ReplaceAll(s, password, "[redacted]")
			}
		}
		return textsafe.SanitizeLine(s)
	}
	a.o.Logger.ErrorContext(r.Context(), "web request failed", "path", safe(r.URL.Path),
		"request_id", safe(w.Header().Get("X-Request-ID")), "error", safe(err.Error()))
}

// viewError shares diagnostics and retry metadata with login; callers retain
// their own status, navigation and resubmit behavior.
func (a *app) viewError(w http.ResponseWriter, r *http.Request, err error) (*domain.Error, int) {
	de, _ := domain.AsError(err)
	status := statusFor(de.Code)
	viewError := *de
	if status == http.StatusInternalServerError {
		diagnostic := error(de)
		if de.Err != nil {
			diagnostic = de.Err
		}
		a.logError(w, r, diagnostic)
		viewError = *domain.Errorf(domain.CodeInternal, "internal error")
	} else {
		viewError.Fields = slices.Clone(de.Fields)
		for i := range viewError.Fields {
			switch viewError.Fields[i].Field {
			case "work_date":
				viewError.Fields[i].Field = "date"
			case "time":
				viewError.Fields[i].Field = "at"
			}
		}
	}
	if de.RetryAfter > 0 {
		seconds := de.RetryAfter / time.Second
		if de.RetryAfter%time.Second != 0 {
			seconds++
		}
		w.Header().Set("Retry-After", strconv.FormatInt(int64(seconds), 10))
	}
	return &viewError, status
}

func (a *app) renderError(w http.ResponseWriter, r *http.Request, err error) {
	a.renderErrorWithInput(w, r, err, false)
}

// sessionChanged is set only after CSRF's same-origin check (DF29).
func (a *app) renderErrorWithInput(w http.ResponseWriter, r *http.Request, err error, sessionChanged bool) {
	de, status := a.viewError(w, r, err)
	p := a.newPage(r, http.StatusText(status), "", nil)
	p.Err = de
	_, hasSession := sessionFrom(r.Context())
	if hasSession && r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/ui/") &&
		r.URL.Path != "/ui/login" && r.URL.Path != "/ui/logout" &&
		de.Code != domain.CodeUnauthenticated && (de.Code != domain.CodeForbidden || sessionChanged) {
		input := submittedInput(r, status)
		input.ReadOnly = input.ReadOnly || sessionChanged
		// Stack 4xx errors need no retry form; retain stale preconditions for
		// read-only diagnostics (DF09), and the existing 5xx recovery display.
		if input.CanRetry || input.ReadOnly || status >= http.StatusInternalServerError {
			p.Resubmit = input
		}
	}
	a.renderPage(w, r, errorStatus(w, r, status), "error", p)
}

func errorStatus(w http.ResponseWriter, r *http.Request, status int) int {
	// AIDEV-NOTE: The fixed htmx config does not swap 4xx/5xx bodies. Return
	// a visible error page with 200 for boosted requests, without changing history.
	if r.Header.Get("HX-Request") == "true" {
		status = http.StatusOK
		w.Header().Set("HX-Push-Url", "false")
		w.Header().Set("HX-Retarget", "body")
		w.Header().Set("HX-Reswap", "innerHTML")
	}
	return status
}

func submittedInput(r *http.Request, status int) *resubmit {
	// AIDEV-NOTE: Never lose typed text on an error. Preserve original gen/ETags
	// for stale detection; DF09/DF29 input is copyable/read-only outside any form,
	// never offered to retry. Empty input gets no heading (including stack errors).
	// DF25: a 5xx can follow commit (service/op.go), so reuse a valid nonce to
	// replay rather than duplicate the op. Without it, do not offer undo/redo retry.
	// 4xx/invalid keys get fresh nonces; passwords and old CSRF are never echoed.
	input := &resubmit{Action: r.URL.EscapedPath(), CanRetry: true, ReadOnly: status == http.StatusPreconditionFailed, Keep: make(url.Values)}
	if status >= http.StatusInternalServerError {
		nonce := r.PostForm.Get("_nonce")
		// OpID's codec accepts exactly the nonce format: 32 lowercase hex.
		if _, err := domain.ParseOpID(nonce); err == nil {
			input.Nonce = nonce
		}
	}
	if r.URL.Path == "/ui/undo" || r.URL.Path == "/ui/redo" {
		// AIDEV-NOTE: DF26: a fresh-key retry after a 4xx can act on the next
		// stack operation. The nav supplies explicit Undo/Redo actions; only
		// a same-key retry after a 5xx is safe (DF25).
		input.CanRetry = status >= http.StatusInternalServerError && input.Nonce != ""
	}
	keys := make([]string, 0, len(r.PostForm))
	for key := range r.PostForm {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		values := r.PostForm[key]
		switch key {
		case "_csrf", "_nonce", "password":
			continue
		case "_gen", "_return", "etag", "task_etag", "id", "how", "remove", "remove_entry", "interrupt", "through", "outcome":
			input.Keep[key] = slices.Clone(values)
		default:
			for _, value := range values {
				input.Fields = append(input.Fields, resubmitField{Name: key, Value: value, Long: key == "notes"})
			}
		}
	}
	return input
}

// AIDEV-NOTE: Mirror internal/server/respond.go's statusFor. A§1.3 forbids
// importing server here; TestStatusForEveryCode pins the shared error contract.
func statusFor(c domain.Code) int {
	switch c {
	case domain.CodeMalformed:
		return http.StatusBadRequest
	case domain.CodeUnauthenticated:
		return http.StatusUnauthorized
	case domain.CodeForbidden, domain.CodePeerRejected, domain.CodePasswordRequired, domain.CodePasswordInvalid:
		return http.StatusForbidden
	case domain.CodeNotFound:
		return http.StatusNotFound
	case domain.CodeDeleted, domain.CodeConflict, domain.CodeRevertConflict, domain.CodeNotRevertible, domain.CodeNothingToUndo, domain.CodeNothingToRedo:
		return http.StatusConflict
	case domain.CodePrecondition, domain.CodeGenerationMismatch:
		return http.StatusPreconditionFailed
	case domain.CodeTooLarge:
		return http.StatusRequestEntityTooLarge
	case domain.CodeUnsupportedMedia:
		return http.StatusUnsupportedMediaType
	case domain.CodeValidation, domain.CodeIdemMismatch:
		return http.StatusUnprocessableEntity
	case domain.CodePreconditionReq, domain.CodeIdemRequired:
		return http.StatusPreconditionRequired
	case domain.CodeRateLimited:
		return http.StatusTooManyRequests
	case domain.CodeBusy:
		return http.StatusServiceUnavailable
	case domain.CodeInternal:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}
