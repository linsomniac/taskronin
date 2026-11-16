package web

import (
	"net/http"
	"net/url"

	"taskronin/internal/datespec"
	"taskronin/internal/domain"
	"taskronin/internal/service"
)

// mutate gives every action the same request identity, error recovery and flash.
// Each closure decodes its form and makes at most one service call (R1).
func (a *app) mutate(w http.ResponseWriter, r *http.Request, fn func(service.Meta, url.Values) (*service.OpResult, error)) {
	m, err := a.meta(r)
	if err != nil {
		a.renderError(w, r, err)
		return
	}
	res, err := fn(m, r.PostForm)
	if err != nil {
		// AIDEV-NOTE: Keep the submitted form/nonce intact: renderError owns
		// DF25 same-key retries after a possible commit followed by a 5xx.
		a.renderError(w, r, err)
		return
	}
	if lines := flashLines(res); len(lines) > 0 {
		a.setFlash(w, r, lines)
	}
	redirectReturn(w, r)
}

//nolint:gosec // G710: target is safeReturn-allowlisted to same-origin /ui/ paths (TestSafeReturn)
func redirectReturn(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, safeReturn(r.PostForm.Get("_return")), http.StatusSeeOther)
}

func (a *app) addTask(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, form url.Values) (*service.OpResult, error) {
		in := service.AddTaskIn{Title: form.Get("title"), Notes: form.Get("notes"),
			Tags: splitTags(form.Get("tags")), Project: form.Get("project"), Priority: domain.Priority(form.Get("priority"))}
		var err error
		in.Due, err = a.formMoment(form, "due", datespec.Future)
		if err != nil {
			return nil, err
		}
		in.Place, err = a.addPlacement(form)
		if err != nil {
			return nil, err
		}
		// AIDEV-NOTE: Notes remain byte-for-byte in the idempotency body;
		// service validation normalizes browser CRLF to LF before storage (D18).
		return a.o.Svc.AddTask(r.Context(), m, in)
	})
}

func (a *app) addPlacement(form url.Values) (*service.AddPlacement, error) {
	p := &service.AddPlacement{How: form.Get("place")}
	switch p.How {
	case "":
		return nil, nil
	case "top", "bottom", "now":
	case "before", "after":
		id, err := formInt(form, "ref")
		if err != nil {
			return nil, err
		}
		p.Ref = domain.TaskID(id)
	case "defer":
		moment, err := a.formMoment(form, "defer", datespec.Future)
		if err != nil {
			return nil, err
		}
		if moment == nil {
			return nil, domain.ValidationError(domain.FieldError{Field: "defer", Message: "must not be empty"})
		}
		p.Defer = *moment // DF19: AddPlacement.Defer is a value, not a pointer.
	case "block":
		span, err := a.formSpan(form, "span")
		if err != nil {
			return nil, err
		}
		p.Block = span
	default:
		return nil, domain.ValidationError(domain.FieldError{Field: "place", Message: "unknown placement"})
	}
	return p, nil
}

func (a *app) taskRef(r *http.Request, form url.Values) (service.TaskRef, error) {
	id, err := pathInt(r, "id")
	if err != nil {
		return service.TaskRef{}, err
	}
	expect, err := a.expect(form, "etag")
	return service.TaskRef{ID: domain.TaskID(id), Expect: expect}, err
}

func (a *app) editTask(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, form url.Values) (*service.OpResult, error) {
		ref, err := a.taskRef(r, form)
		if err != nil {
			return nil, err
		}
		in := service.UpdateTaskIn{ID: ref.ID, Expect: ref.Expect}
		if form.Has("title") {
			value := form.Get("title")
			in.Title = &value
		}
		if form.Has("notes") {
			value := form.Get("notes")
			in.Notes = &value
		}
		if form.Has("project") {
			value := form.Get("project")
			in.Project = &value
		}
		if form.Has("priority") {
			value := domain.Priority(form.Get("priority"))
			in.Priority = &value
		}
		return a.o.Svc.UpdateTask(r.Context(), m, in)
	})
}

func (a *app) taskTags(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, form url.Values) (*service.OpResult, error) {
		ref, err := a.taskRef(r, form)
		if err != nil {
			return nil, err
		}
		return a.o.Svc.UpdateTask(r.Context(), m, service.UpdateTaskIn{ID: ref.ID, Expect: ref.Expect,
			TagsAdd: splitTags(form.Get("add")), TagsRemove: form["remove"]})
	})
}

func (a *app) taskDue(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, form url.Values) (*service.OpResult, error) {
		ref, err := a.taskRef(r, form)
		if err != nil {
			return nil, err
		}
		in := service.SetDueIn{ID: ref.ID, Expect: ref.Expect}
		if form.Get("due") != "none" {
			in.Due, err = a.formMoment(form, "due", datespec.Future)
			if err != nil {
				return nil, err
			}
			if in.Due == nil {
				return nil, domain.ValidationError(domain.FieldError{Field: "due", Message: "must not be empty (use none to clear)"})
			}
		}
		return a.o.Svc.SetDue(r.Context(), m, in)
	})
}
