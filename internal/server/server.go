package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/clock"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/service"
)

// Server adapts the socket HTTP protocol to the service.
type Server struct {
	svc        *service.Service
	conv       api.Conv
	clock      clock.Clock
	log        *slog.Logger
	stop       context.Context
	cancelStop context.CancelFunc
}

// New constructs an API server. Nil clock and logger use real time and discard logging.
func New(svc *service.Service, conv api.Conv, clk clock.Clock, log *slog.Logger) *Server {
	if clk == nil {
		clk = clock.Real{}
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	stop, cancel := context.WithCancel(context.Background())
	return &Server{svc: svc, conv: conv, clock: clk, log: log, stop: stop, cancelStop: cancel}
}

// BeginShutdown cancels long requests; repeated calls are safe.
func (s *Server) BeginShutdown() { s.cancelStop() }

const longBudget = 10 * time.Minute

// longCtx combines the request lifetime, shutdown signal and long-operation budget.
func (s *Server) longCtx(r *http.Request) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(r.Context(), longBudget)
	stop := context.AfterFunc(s.stop, cancel)
	if s.stop.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}

type requestStateKey struct{}

// Shared by the wrappers so authentication and mux selection reach the access
// logger and the outer recoverer without modifying the caller's request.
type requestState struct {
	id    string
	actor domain.Actor
	route string
}

func state(r *http.Request) *requestState {
	return r.Context().Value(requestStateKey{}).(*requestState)
}

func (s *Server) meta(r *http.Request, canonical []byte) service.Meta {
	m := s.exemptMeta(r)
	m.Idem = service.Idem{Key: r.Header.Get(api.HeaderIdempotencyKey), Method: r.Method, Route: r.Method + " " + r.URL.Path, Body: canonical}
	return m
}

func (*Server) exemptMeta(r *http.Request) service.Meta {
	return service.Meta{Actor: state(r).actor, Gen: domain.Generation(r.Header.Get(api.HeaderGeneration))}
}
