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
		loginPost := path == "/ui/login" && r.Method == http.MethodPost
		if (path == "/ui/login" && !loginPost) || (path != "/ui" && !strings.HasPrefix(path, "/ui/")) {
			next.ServeHTTP(w, r)
			return
		}
		// AIDEV-NOTE: Cookie sessions are the only authentication for /ui;
		// routeClass strips bearer auth and never accepts cookies on /api/v1.
		// Login needs no session, but selects an optional one for regeneration.
		// Refuse its Origin before authentication or password-gate work.
		if loginPost && !a.checkOrigin(r) {
			a.renderError(w, r, domain.Errorf(domain.CodeForbidden, "cross-site request refused"))
			return
		}
		s, err := a.authenticateSession(r)
		if err != nil {
			if loginPost {
				a.renderLoginError(w, r, err)
			} else {
				a.renderError(w, r, err)
			}
			return
		}
		if s.Cookie != "" {
			r = r.WithContext(withSession(r.Context(), s))
		}
		if loginPost || s.Cookie != "" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			setCookie(w, SessionCookie, "", time.Time{}, -1)
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

func (a *app) authenticateSession(r *http.Request) (sessionInfo, error) {
	// AIDEV-NOTE: Cookies are not port-scoped: another loopback page can plant
	// a /ui/ cookie ahead of our root cookie. Try at most four, in wire order,
	// and carry the authenticated value through to login regeneration/logout.
	cookies := r.CookiesNamed(SessionCookie)
	for _, cookie := range cookies[:min(len(cookies), 4)] {
		wa, err := a.o.Svc.Authenticate(r.Context(), cookie.Value)
		if err == nil {
			return sessionInfo{Actor: wa.Actor, CSRF: wa.CSRFToken, Cookie: cookie.Value}, nil
		}
		de, _ := domain.AsError(err)
		if de.Code != domain.CodeUnauthenticated {
			return sessionInfo{}, err
		}
	}
	return sessionInfo{}, nil
}

//nolint:gosec // G124: plain-HTTP loopback v1 (A§7.5); Secure would drop the cookie; HttpOnly+SameSite=Lax set
func setCookie(w http.ResponseWriter, name, value string, expires time.Time, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Expires: expires, MaxAge: maxAge,
	})
}
