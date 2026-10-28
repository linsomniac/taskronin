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
func (s *Server) Routes() []Route {
	return []Route{
		{Method: http.MethodGet, Pattern: "/api/v1/clock", Handler: s.clockView},
		{Method: http.MethodGet, Pattern: "/api/v1/whoami", Handler: s.whoAmI},
		{Method: http.MethodGet, Pattern: "/api/v1/today", Scopes: domain.ScopeRead, Handler: s.today},
		{Method: http.MethodGet, Pattern: "/api/v1/status", Scopes: domain.ScopeRead, Handler: s.status},
		{Method: http.MethodGet, Pattern: "/api/v1/days/{date}", Scopes: domain.ScopeRead, Handler: s.day},
		{Method: http.MethodGet, Pattern: "/api/v1/upcoming", Scopes: domain.ScopeRead, Handler: s.upcoming},
		{Method: http.MethodGet, Pattern: "/api/v1/report", Scopes: domain.ScopeRead, Handler: s.report},
		{Method: http.MethodGet, Pattern: "/api/v1/search", Scopes: domain.ScopeRead, Handler: s.search},
		{Method: http.MethodGet, Pattern: "/api/v1/tags", Scopes: domain.ScopeRead, Handler: s.tags},
		{Method: http.MethodGet, Pattern: "/api/v1/tasks", Scopes: domain.ScopeRead, Handler: s.listTasks},
		{Method: http.MethodPost, Pattern: "/api/v1/tasks", Scopes: domain.ScopeCapture, Handler: s.addTask}, // Placement additionally requires write and owner; enforced by service.
		{Method: http.MethodGet, Pattern: "/api/v1/tasks/{id}", Scopes: domain.ScopeRead, Handler: s.getTask},
		{Method: http.MethodPatch, Pattern: "/api/v1/tasks/{id}", Scopes: domain.ScopeWrite, IfMatch: true, Handler: s.updateTask},
		{Method: http.MethodPost, Pattern: "/api/v1/tasks/{id}/due", Scopes: domain.ScopeWrite, IfMatch: true, Handler: s.setDue},
		{Method: http.MethodPost, Pattern: "/api/v1/tasks/{id}/place", Scopes: domain.ScopeWrite, IfMatch: true, Handler: s.placeTask},
		{Method: http.MethodPost, Pattern: "/api/v1/tasks/{id}/defer", Scopes: domain.ScopeWrite, IfMatch: true, Handler: s.deferTask},
		{Method: http.MethodPost, Pattern: "/api/v1/tasks/{id}/block", Scopes: domain.ScopeWrite, IfMatch: true, Handler: s.blockTask},
		{Method: http.MethodPost, Pattern: "/api/v1/tasks/{id}/triage", Scopes: domain.ScopeWrite, IfMatch: true, Handler: s.markManual},
		{Method: http.MethodPost, Pattern: "/api/v1/tasks/{id}/complete", Scopes: domain.ScopeWrite, IfMatch: true, Handler: s.completeTask},
		{Method: http.MethodPost, Pattern: "/api/v1/tasks/{id}/reopen", Scopes: domain.ScopeWrite, IfMatch: true, Handler: s.reopenTask}, // Removing the entry additionally requires destroy.
		{Method: http.MethodPost, Pattern: "/api/v1/tasks/{id}/drop", Scopes: domain.ScopeDestroy, IfMatch: true, Handler: s.dropTask},
		{Method: http.MethodPost, Pattern: "/api/v1/tasks/{id}/delete", Scopes: domain.ScopeDestroy, IfMatch: true, Handler: s.deleteTask},
		{Method: http.MethodPost, Pattern: "/api/v1/tasks/{id}/undelete", Scopes: domain.ScopeWrite, IfMatch: true, Handler: s.undeleteTask},
		{Method: http.MethodGet, Pattern: "/api/v1/tasks/{id}/history", Scopes: domain.ScopeRead, Handler: s.taskHistory},
		{Method: http.MethodPost, Pattern: "/api/v1/triage/accept-new", Scopes: domain.ScopeWrite, Handler: s.acceptNew},
		{Method: http.MethodGet, Pattern: "/api/v1/entries", Scopes: domain.ScopeRead, Handler: s.listEntries},
		{Method: http.MethodGet, Pattern: "/api/v1/entries/{id}", Scopes: domain.ScopeRead, Handler: s.getEntry},
		{Method: http.MethodGet, Pattern: "/api/v1/blocks", Scopes: domain.ScopeRead, Handler: s.listBlocks},
		{Method: http.MethodGet, Pattern: "/api/v1/blocks/{id}", Scopes: domain.ScopeRead, Handler: s.getBlock},
		{Method: http.MethodGet, Pattern: "/api/v1/ops", Scopes: domain.ScopeRead, Handler: s.listOps},
		{Method: http.MethodGet, Pattern: "/api/v1/ops/{op_id}", Scopes: domain.ScopeRead, Handler: s.getOp},
		{Method: http.MethodGet, Pattern: "/api/v1/ai-changes", Scopes: domain.ScopeRead, Handler: s.aiChanges},
		{Method: http.MethodGet, Pattern: "/api/v1/settings", Scopes: domain.ScopeRead, Handler: s.settings},
	}
}

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
	router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// AIDEV-NOTE: ServeMux returns no pattern for its default 404/405
		// handlers. A route is a method/path pair; absent pairs use not_found
		// because the domain vocabulary has no method-not-allowed code.
		if state(r).route == "" {
			s.writeProblem(w, r, domain.Errorf(domain.CodeNotFound, "route not found"))
			return
		}
		mux.ServeHTTP(w, r)
	})
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
