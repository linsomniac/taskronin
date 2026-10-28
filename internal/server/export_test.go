package server

import (
	"log/slog"
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
)

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
