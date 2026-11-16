package web

import (
	"crypto/subtle"
	"net/http"

	"taskronin/internal/domain"
)

// AIDEV-NOTE: Every POST needs both origin protection and a constant-time
// CSRF-token check. Login is exempt only from the token, not the origin rule.
// DF01: no-referrer browser forms send Origin: null; same-origin fetch
// metadata permits those, but never overrides an explicitly foreign origin.
func (a *app) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if _, present := r.Header["Origin"]; present && origin != "null" && !a.origins[origin] {
		return false
	}
	if _, present := r.Header["Sec-Fetch-Site"]; present {
		return r.Header.Get("Sec-Fetch-Site") == "same-origin"
	}
	return a.origins[origin]
}

func (a *app) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path == "/ui/login" {
			next.ServeHTTP(w, r)
			return
		}
		s, _ := sessionFrom(r.Context())
		token := r.PostForm.Get("_csrf")
		if token == "" {
			token = r.Header.Get("X-CSRF-Token")
		}
		if !a.checkOrigin(r) {
			a.renderError(w, r, domain.Errorf(domain.CodeForbidden, "cross-site request refused"))
			return
		}
		if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.CSRF)) != 1 {
			// AIDEV-NOTE: DF29: only a same-origin token mismatch may echo
			// typed input, read-only. Cross-site refusals never preserve it.
			a.renderErrorWithInput(w, r, domain.Errorf(domain.CodeForbidden,
				"Your session changed (you logged in again) — reload, then paste"), true)
			return
		}
		next.ServeHTTP(w, r)
	})
}
