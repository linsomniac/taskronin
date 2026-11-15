package web

import "net/http"

var (
	StaticFS      = staticFS
	HostAllowlist = hostAllowlist
	BodyLimit     = bodyLimit
)

// NewWithProbe installs a test handler behind the complete web middleware chain.
func NewWithProbe(o Options, probe http.Handler) (http.Handler, error) {
	a, err := newApp(o)
	if err != nil {
		return nil, err
	}
	a.mux.Handle("/ui/probe", probe)
	return a.handler(), nil
}
