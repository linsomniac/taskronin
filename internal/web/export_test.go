package web

import (
	"html/template"
	"net/http"
	"net/url"

	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/datespec"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/service"
)

type App = app
type Page = page
type SessionInfo = sessionInfo

var (
	StaticFS      = staticFS
	HostAllowlist = hostAllowlist
	NewApp        = newApp
	TemplateFS    = templateFS
	StatusFor     = statusFor
	SafeReturn    = safeReturn
	IdemBody      = idemBody
	MoreURL       = moreURL
	FormInt       = formInt
	PathInt       = pathInt
	FormMinute    = formMinute
	SplitTags     = splitTags
	WithSession   = withSession
	FlashLines    = flashLines
)

func (a *App) Handler() http.Handler { return a.handler() }

// RoutePatterns records the same registrations used to construct the real mux.
func RoutePatterns() []string {
	var patterns routePatterns
	new(app).registerRoutes(&patterns)
	return patterns
}

type routePatterns []string

func (p *routePatterns) HandleFunc(pattern string, _ func(http.ResponseWriter, *http.Request)) {
	*p = append(*p, pattern)
}

func (a *App) BodyLimit(next http.Handler) http.Handler { return a.bodyLimit(next) }

func (a *App) NewPage(r *http.Request, title, tab string, data any) Page {
	return a.newPage(r, title, tab, data)
}

func (a *App) RenderPage(w http.ResponseWriter, r *http.Request, status int, name string, p Page) {
	a.renderPage(w, r, status, name, p)
}

// Template clones a parsed page so tests can exercise its real function map.
func (a *App) Template(name string) (*template.Template, error) { return a.templates[name].Clone() }

func (a *App) RenderError(w http.ResponseWriter, r *http.Request, err error) {
	a.renderError(w, r, err)
}

func (a *App) Meta(r *http.Request) (service.Meta, error) { return a.meta(r) }

func (a *App) Mutate(w http.ResponseWriter, r *http.Request, fn func(service.Meta, url.Values) (*service.OpResult, error)) {
	a.mutate(w, r, fn)
}

func (a *App) Expect(form url.Values, key string) (domain.Expect, error) {
	return a.expect(form, key)
}

func (a *App) FormMoment(form url.Values, key string, m datespec.Mode) (*domain.Moment, error) {
	return a.formMoment(form, key, m)
}

func (a *App) FormSpan(form url.Values, key string) (domain.BlockSpan, error) {
	return a.formSpan(form, key)
}

func (a *App) FormDate(form url.Values, key string, m datespec.Mode) (civil.Date, error) {
	return a.formDate(form, key, m)
}

// NewWithProbe installs a test handler behind the complete web middleware chain.
func NewWithProbe(o Options, probe http.Handler) (http.Handler, error) {
	a, err := newApp(o)
	if err != nil {
		return nil, err
	}
	a.mux.Handle("/ui/probe", probe)
	return a.handler(), nil
}
