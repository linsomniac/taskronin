package web

import (
	"net/http"
	"strings"
	"time"

	"taskronin/internal/domain"
)

func (a *app) session(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/ui/login" || (path != "/ui" && !strings.HasPrefix(path, "/ui/")) {
			next.ServeHTTP(w, r)
			return
		}
		// AIDEV-NOTE: Cookie sessions are the only authentication for /ui;
		// routeClass strips bearer auth and never accepts cookies on /api/v1.
		cookie, err := r.Cookie(SessionCookie)
		if err == nil {
			wa, authErr := a.o.Svc.Authenticate(r.Context(), cookie.Value)
			if authErr == nil {
				ctx := withSession(r.Context(), sessionInfo{Actor: wa.Actor, CSRF: wa.CSRFToken, Cookie: cookie.Value})
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			de, _ := domain.AsError(authErr)
			if de.Code != domain.CodeUnauthenticated {
				a.renderError(w, r, authErr)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
			return
		}
		// DF11: retain the 401 even for htmx; plain form submissions also
		// need a visible way back to login, without authenticated navigation.
		w.Header().Set("HX-Redirect", "/ui/login")
		p := a.newPage(r, "Session expired", "", nil)
		p.Err = domain.Errorf(domain.CodeUnauthenticated, "Session expired — log in")
		a.renderPage(w, r, http.StatusUnauthorized, "error", p)
	})
}

//nolint:gosec // G124: plain-HTTP loopback v1 (A§7.5); Secure would drop the cookie; HttpOnly+SameSite=Lax set
func setCookie(w http.ResponseWriter, name, value string, expires time.Time, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Expires: expires, MaxAge: maxAge,
	})
}
