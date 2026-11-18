package server

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"

	"taskronin/internal/civil"
	"taskronin/internal/clock"
	"taskronin/internal/service"
)

var (
	HandlerFor   = (*Server).handlerFor
	WithConnInfo = withConnInfo
	StatusFor    = statusFor
	IfMatch      = ifMatch
	WriteProblem = (*Server).writeProblem
	WriteJSON    = (*Server).writeJSON
	WriteOp      = (*Server).writeOp
	Meta         = (*Server).meta
	ExemptMeta   = (*Server).exemptMeta
	LongCtx      = (*Server).longCtx
	PathInt      = pathInt
	Authn        = (*Server).authn
)

// WithRequestState installs the per-request state that the outer wrappers
// (recoverer) create, for tests that drive one middleware on its own.
func WithRequestState(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestStateKey{}, &requestState{})))
	})
}

func DecodeBody[T any](r *http.Request) (T, []byte, error) { return decodeBody[T](r) }

// SetWebHandler replaces the adapter before Run; nil explicitly disables web.
func SetWebHandler(h func(*service.Service, clock.Clock, civil.Zone, time.Weekday, string, *slog.Logger) (http.Handler, error)) func() {
	old := newWebHandler
	newWebHandler = h
	return func() { newWebHandler = old }
}

// SetSchedulerObserver installs an observer before Run, restored after Stop.
func SetSchedulerObserver(f func(job, event string, at time.Time)) func() {
	old := schedulerObserver
	schedulerObserver = f
	return func() { schedulerObserver = old }
}

// SetWebListener supplies the listener used by Run in a lifecycle test.
func SetWebListener(ln net.Listener) func() {
	old := listenWeb
	listenWeb = func(context.Context, string, string) (net.Listener, error) { return ln, nil }
	return func() { listenWeb = old }
}
