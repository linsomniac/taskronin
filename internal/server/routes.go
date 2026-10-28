package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"taskronin/internal/clock"
	"taskronin/internal/domain"
)

// Route is the shared routing and scope-matrix description. Authorization is
// enforced by the service; these flags document each operation's contract.
type Route struct {
	Method, Pattern                  string
	Scopes                           domain.Scopes
	OwnerOnly, Exempt, IfMatch, Long bool
	Handler                          http.HandlerFunc
}

// Routes returns the API endpoint table. Feature tasks extend this table.
func (*Server) Routes() []Route { return nil }

// APIHandler returns the socket-only API middleware chain and router.
func (s *Server) APIHandler() http.Handler { return s.handlerFor(s.Routes()) }

func (s *Server) handlerFor(routes []Route) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
	})
	for _, route := range routes {
		mux.Handle(route.Method+" "+route.Pattern, s.routeHandler(route))
	}
	// Resolve the pattern before guards so rejected requests also log a route
	// pattern, never a raw path or query containing user text.
	router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mux.ServeHTTP(w, r) })
	chain := s.socketGuard(s.routeClass(s.bodyLimit(s.authn(s.apiHeaders(router)))))
	logged := s.accessLog(chain)
	selectRoute := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, state(r).route = mux.Handler(r)
		logged.ServeHTTP(w, r)
	})
	return s.recoverer(s.requestID(selectRoute))
}

func (s *Server) routeHandler(route Route) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if route.Long {
			ctx, cancel := s.longCtx(r)
			defer cancel()
			controller := http.NewResponseController(w)
			// AIDEV-NOTE: Socket deadlines must use real time, independently of
			// the injected domain clock. Cancellation interrupts blocked export
			// writes on client disconnect, budget expiry AND server shutdown.
			if err := controller.SetWriteDeadline(clock.Real{}.Now().Add(longBudget)); err != nil && !errors.Is(err, http.ErrNotSupported) {
				s.writeProblem(w, r, err)
				return
			}
			done := make(chan struct{})
			stop := context.AfterFunc(ctx, func() {
				defer close(done)
				_ = controller.SetWriteDeadline(clock.Real{}.Now())
			})
			// Join a running callback before returning the connection to net/http.
			defer func() {
				if !stop() {
					<-done
				}
			}()
			route.Handler(w, r.WithContext(ctx))
			return
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			r = r.WithContext(ctx)
		}
		route.Handler(w, r)
	})
}
