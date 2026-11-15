package web

import "net/http"

func (a *app) today(w http.ResponseWriter, r *http.Request) {
	a.renderPage(w, r, http.StatusOK, "today", a.newPage(r, "Today", "today", nil))
}
