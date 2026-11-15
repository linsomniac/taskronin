// Package web serves Taskronin's loopback browser interface.
package web

import (
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"taskronin/internal/civil"
	"taskronin/internal/clock"
	"taskronin/internal/service"
)

// Browser protocol constants and the pinned, embedded htmx asset.
const (
	SessionCookie = "tr_session"
	HTMXAsset     = "htmx-2.0.11.min.js"
	HTMXSHA256    = "d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717"
	HTMXSize      = 52182
	CSP           = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"
)

//go:embed static
var staticFS embed.FS

//go:embed templates
var templateFS embed.FS

// Options supplies the service and configuration for the bound loopback listener.
type Options struct {
	Svc       *service.Service
	Clock     clock.Clock
	Zone      civil.Zone
	WeekStart time.Weekday
	Listen    string
	Logger    *slog.Logger
}

type app struct {
	o         Options
	hosts     map[string]bool
	origins   map[string]bool
	mux       *http.ServeMux
	templates map[string]*template.Template
}

// New constructs the web handler, rejecting Listen unless it has a numeric port.
func New(o Options) (http.Handler, error) {
	a, err := newApp(o)
	if err != nil {
		return nil, err
	}
	return a.handler(), nil
}

func newApp(o Options) (*app, error) {
	hosts, err := hostAllowlist(o.Listen)
	if err != nil {
		return nil, err
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	a := &app{o: o, hosts: hosts, origins: make(map[string]bool, len(hosts))}
	for host := range hosts {
		a.origins["http://"+host] = true
	}
	if err := a.parseTemplates(); err != nil {
		return nil, err
	}
	a.mux = a.routes()
	return a, nil
}

func (a *app) handler() http.Handler {
	return securityHeaders(a.checkHost(routeClass(a.bodyLimit(readDeadline(a.mux)))))
}
