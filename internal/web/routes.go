package web

import (
	"bytes"
	"net/http"
	"strings"
	"time"

	"github.com/linsomniac/taskronin/internal/domain"
)

func (a *app) routes() *http.ServeMux {
	mux := http.NewServeMux()
	a.registerRoutes(mux)
	return mux
}

// AIDEV-NOTE: Keep all registrations here so the SCP-07/PRG-18 test recorder
// checks the same patterns that construct the real mux.
func (a *app) registerRoutes(mux interface {
	HandleFunc(string, func(http.ResponseWriter, *http.Request))
}) {
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
	mux.HandleFunc("POST /ui/tasks", a.addTask)
	mux.HandleFunc("POST /ui/tasks/{id}/edit", a.editTask)
	mux.HandleFunc("POST /ui/tasks/{id}/tags", a.taskTags)
	mux.HandleFunc("POST /ui/tasks/{id}/due", a.taskDue)
	mux.HandleFunc("POST /ui/tasks/{id}/place", a.placeTask)
	mux.HandleFunc("POST /ui/tasks/{id}/defer", a.deferTask)
	mux.HandleFunc("POST /ui/tasks/{id}/triage", a.triageTask)
	mux.HandleFunc("POST /ui/tasks/{id}/block", a.blockTask)
	mux.HandleFunc("POST /ui/tasks/{id}/complete", a.completeTask)
	mux.HandleFunc("POST /ui/tasks/{id}/reopen", a.reopenTask)
	mux.HandleFunc("POST /ui/tasks/{id}/drop", a.dropTask)
	mux.HandleFunc("POST /ui/tasks/{id}/delete", a.deleteTask)
	mux.HandleFunc("POST /ui/tasks/{id}/undelete", a.undeleteTask)
	mux.HandleFunc("POST /ui/triage/accept-new", a.acceptNew)
	mux.HandleFunc("POST /ui/entries", a.addEntry)
	mux.HandleFunc("POST /ui/entries/{id}/edit", a.editEntry)
	mux.HandleFunc("POST /ui/entries/{id}/relink", a.relinkEntry)
	mux.HandleFunc("POST /ui/entries/{id}/delete", a.deleteEntry)
	mux.HandleFunc("POST /ui/entries/{id}/undelete", a.undeleteEntry)
	mux.HandleFunc("POST /ui/blocks/{id}/reblock", a.reblock)
	mux.HandleFunc("POST /ui/blocks/{id}/unblock", a.unblock)
	mux.HandleFunc("POST /ui/blocks/{id}/outcome", a.setOutcome)
	mux.HandleFunc("POST /ui/focus", a.setFocus)
	mux.HandleFunc("POST /ui/undo", a.undo)
	mux.HandleFunc("POST /ui/redo", a.redo)
	mux.HandleFunc("POST /ui/ops/{op}/revert", a.revert)
	mux.HandleFunc("POST /ui/ai-changes/review", a.markAIReviewed)
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
}

func (a *app) dispatch(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" && strings.HasPrefix(r.URL.Path, "/ui/") {
		if _, pattern := a.mux.Handler(r); pattern == "" {
			// Like the API adapter, both absent method/path pairs use not_found;
			// the domain vocabulary has no method-not-allowed code.
			a.renderError(w, r, domain.Errorf(domain.CodeNotFound, "route not found"))
			return
		}
	}
	a.mux.ServeHTTP(w, r)
}
