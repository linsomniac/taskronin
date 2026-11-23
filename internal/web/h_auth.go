package web

import (
	"net/http"
	"net/netip"
	"time"

	"github.com/linsomniac/taskronin/internal/service"
)

func (a *app) loginPage(w http.ResponseWriter, r *http.Request) {
	a.renderPage(w, r, http.StatusOK, "login", a.newPage(r, "Log in", "", nil))
}

func (a *app) login(w http.ResponseWriter, r *http.Request) {
	old, _ := sessionFrom(r.Context())
	s, err := a.o.Svc.Login(r.Context(), service.LoginIn{
		RemoteKey: clientIP(r), Password: r.PostFormValue("password"), OldCookie: old.Cookie,
	})
	if err != nil {
		a.renderLoginError(w, r, err)
		return
	}
	setCookie(w, SessionCookie, s.CookieValue, "/", s.ExpiresAt, 0)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/ui/", http.StatusSeeOther)
}

func (a *app) renderLoginError(w http.ResponseWriter, r *http.Request, err error) {
	de, status := a.viewError(w, r, err)
	p := a.newPage(r, "Log in", "", nil)
	p.Err = de
	// Login is unboosted and credential-bearing: preserve its error status,
	// and render only an empty password field, never a resubmit form or nav.
	p.CSRF = ""
	a.renderPage(w, r, status, "login", p)
}

func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	s, _ := sessionFrom(r.Context())
	if err := a.o.Svc.Logout(r.Context(), s.Cookie); err != nil {
		a.renderError(w, r, err)
		return
	}
	setCookie(w, SessionCookie, "", "/", time.Time{}, -1)
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
