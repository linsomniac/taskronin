package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"taskronin/internal/domain"
)

func hostAllowlist(listen string) (map[string]bool, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return nil, fmt.Errorf("web listen address: %w", err)
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return nil, fmt.Errorf("web listen port: %w", err)
	}
	// AIDEV-NOTE: DNS-rebinding guard derived from web.listen (arch §6.2).
	// Only the primary loopback addresses gain the localhost alias.
	allowed := map[string]bool{strings.ToLower(listen): true}
	if host == "127.0.0.1" || host == "::1" {
		allowed["localhost:"+port] = true
	}
	return allowed, nil
}

func (a *app) checkHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.hosts[strings.ToLower(r.Host)] {
			http.Error(w, "bad host", http.StatusBadRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", CSP)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), geolocation=(), microphone=()")
		next.ServeHTTP(w, r)
	})
}

func routeClass(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name := range r.Header {
			if strings.EqualFold(name, "Authorization") {
				delete(r.Header, name)
			}
		}
		path := r.URL.Path
		if path == "/api/v1" || strings.HasPrefix(path, "/api/v1/") {
			w.Header().Set("Content-Type", "application/problem+json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusUnauthorized)
			// AIDEV-NOTE: Mirror internal/api/problem.go's wire fields; A§1.3
			// forbids importing api here. TestSCP04 pins the decoded problem.
			_ = json.NewEncoder(w).Encode(struct {
				Type      string `json:"type"`
				Title     string `json:"title"`
				Status    int    `json:"status"`
				Detail    string `json:"detail"`
				Code      string `json:"code"`
				RequestID string `json:"request_id"`
			}{
				Type: "urn:taskronin:problem:unauthenticated", Title: "Unauthorized",
				Status: http.StatusUnauthorized, Detail: "the API is served only on the unix socket",
				Code: "unauthenticated", RequestID: w.Header().Get("X-Request-ID"),
			})
			return
		}
		if path != "/" && path != "/healthz" && path != "/ui" && !strings.HasPrefix(path, "/ui/") && !strings.HasPrefix(path, "/static/") {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *app) bodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if status := parsePostForm(w, r); status != 0 {
				// AIDEV-NOTE: Plan D DF10 routes htmx form errors through
				// renderError so the fixed htmx config displays the failure.
				if r.Header.Get("HX-Request") == "true" {
					code := domain.CodeMalformed
					switch status {
					case http.StatusRequestEntityTooLarge:
						code = domain.CodeTooLarge
					case http.StatusUnsupportedMediaType:
						code = domain.CodeUnsupportedMedia
					}
					a.renderError(w, r, domain.Errorf(code, "%s", http.StatusText(status)))
				} else {
					http.Error(w, http.StatusText(status), status)
				}
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// parsePostForm eagerly enforces the form limit and returns an HTTP error status,
// or zero on success. Response rendering stays separate for htmx error handling.
func parsePostForm(w http.ResponseWriter, r *http.Request) int {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/x-www-form-urlencoded" {
		return http.StatusUnsupportedMediaType
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return http.StatusRequestEntityTooLarge
		}
		return http.StatusBadRequest
	}
	return 0
}

func readDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			// AIDEV-NOTE: A§4.11 and Plan B BF31 put the 10-second read
			// budget in the HTTP adapter; POSTs keep the detached service budget.
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}
