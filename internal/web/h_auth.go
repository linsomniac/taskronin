package web

import (
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"taskronin/internal/domain"
	"taskronin/internal/service"
)

func (a *app) loginPage(w http.ResponseWriter, r *http.Request) {
	a.renderPage(w, r, http.StatusOK, "login", a.newPage(r, "Log in", "", nil))
}

func (a *app) login(w http.ResponseWriter, r *http.Request) {
	if !a.checkOrigin(r) {
		a.renderError(w, r, domain.Errorf(domain.CodeForbidden, "cross-site request refused"))
		return
	}
	var oldCookie string
	if cookie, err := r.Cookie(SessionCookie); err == nil {
		oldCookie = cookie.Value
	}
	s, err := a.o.Svc.Login(r.Context(), service.LoginIn{
		RemoteKey: clientIP(r), Password: r.PostFormValue("password"), OldCookie: oldCookie,
	})
	if err != nil {
		de, _ := domain.AsError(err)
		status := statusFor(de.Code)
		p := a.newPage(r, "Log in", "", nil)
		p.Err = de
		if status == http.StatusInternalServerError {
			diagnostic := error(de)
			if de.Err != nil {
				diagnostic = de.Err
			}
			a.logError(w, r, diagnostic)
			p.Err = domain.Errorf(domain.CodeInternal, "internal error")
		}
		if de.RetryAfter > 0 {
			seconds := de.RetryAfter / time.Second
			if de.RetryAfter%time.Second != 0 {
				seconds++
			}
			w.Header().Set("Retry-After", strconv.FormatInt(int64(seconds), 10))
		}
		// Login is unboosted and credential-bearing: preserve its error status,
		// and render only an empty password field, never a resubmit form.
		a.renderPage(w, r, status, "login", p)
		return
	}
	setCookie(w, SessionCookie, s.CookieValue, s.ExpiresAt, 0)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/ui/", http.StatusSeeOther)
}

func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	s, _ := sessionFrom(r.Context())
	if err := a.o.Svc.Logout(r.Context(), s.Cookie); err != nil {
		a.renderError(w, r, err)
		return
	}
	setCookie(w, SessionCookie, "", time.Time{}, -1)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
}

func clientIP(r *http.Request) string {
	addr, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return addr.Addr().String()
}
