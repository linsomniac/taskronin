package web

import (
	"bytes"
	"net/http"
	"time"
)

func (a *app) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ui/login", a.loginPage)
	mux.HandleFunc("POST /ui/login", a.login)
	mux.HandleFunc("POST /ui/logout", a.logout)
	mux.HandleFunc("GET /ui/{$}", a.today)
	mux.HandleFunc("GET /ui/triage", a.triage)
	mux.HandleFunc("GET /ui/backlog", a.backlog)
	mux.HandleFunc("GET /ui/upcoming", a.upcoming)
	mux.HandleFunc("GET /ui/log", a.log)
	mux.HandleFunc("GET /ui/recap", a.recap)
	mux.HandleFunc("GET /ui/report", a.report)
	mux.HandleFunc("GET /ui/search", a.search)
	mux.HandleFunc("GET /ui/ai-changes", a.aiChanges)
	mux.HandleFunc("GET /ui/tasks/{id}", a.taskDetail)
	mux.HandleFunc("GET /ui/tasks/{id}/history", a.taskHistory)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusSeeOther)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /static/{name}", func(w http.ResponseWriter, r *http.Request) {
		var (
			b           []byte
			contentType string
			err         error
		)
		switch r.PathValue("name") {
		case HTMXAsset:
			b, err = staticFS.ReadFile("static/" + HTMXAsset)
			contentType = "text/javascript; charset=utf-8"
		case "app.css":
			b, err = staticFS.ReadFile("static/app.css")
			contentType = "text/css; charset=utf-8"
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "asset unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.ServeContent(w, r, r.PathValue("name"), time.Time{}, bytes.NewReader(b))
	})
	return mux
}
