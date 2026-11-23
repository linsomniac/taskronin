package web

import (
	"database/sql"
	"net/http"
	"net/url"

	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/service"
)

func (a *app) setFocus(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, form url.Values) (*service.OpResult, error) {
		if !form.Has("task_id") {
			return nil, &domain.Error{Code: domain.CodeMalformed, Detail: "missing task_id",
				Fields: []domain.FieldError{{Field: "task_id", Message: "required (use an empty value to clear focus)"}}}
		}
		in := service.SetFocusIn{Interrupt: form.Get("interrupt") == "1"}
		if form.Get("task_id") != "" {
			id, err := formInt(form, "task_id")
			if err != nil {
				return nil, err
			}
			in.TaskID = sql.Null[domain.TaskID]{V: domain.TaskID(id), Valid: true}
		}
		return a.o.Svc.SetFocus(r.Context(), m, in)
	})
}

func (a *app) undo(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, _ url.Values) (*service.OpResult, error) {
		return a.o.Svc.Undo(r.Context(), m)
	})
}

func (a *app) redo(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, _ url.Values) (*service.OpResult, error) {
		return a.o.Svc.Redo(r.Context(), m)
	})
}

func (a *app) revert(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, _ url.Values) (*service.OpResult, error) {
		op, err := domain.ParseOpID(r.PathValue("op"))
		if err != nil {
			return nil, domain.Errorf(domain.CodeNotFound, "not found")
		}
		return a.o.Svc.Revert(r.Context(), m, service.RevertIn{OpID: op})
	})
}

func (a *app) markAIReviewed(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, form url.Values) (*service.OpResult, error) {
		through, err := formInt(form, "through")
		if err != nil {
			return nil, err
		}
		// AIDEV-NOTE: Use only the posted watermark. A fresh list or maximum
		// could acknowledge AI work that arrived after this page was rendered.
		return a.o.Svc.MarkAIReviewed(r.Context(), m, service.MarkAIReviewedIn{Through: domain.OpSeq(through)})
	})
}
