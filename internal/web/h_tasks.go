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
			return nil, emptyField("defer")
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

func (a *app) placeTask(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, form url.Values) (*service.OpResult, error) {
		ref, err := a.taskRef(r, form)
		if err != nil {
			return nil, err
		}
		in := service.PlaceIn{ID: ref.ID, Expect: ref.Expect, How: domain.PlaceHow(form.Get("how"))}
		switch in.How {
		case domain.PlaceKeep, domain.PlaceTop, domain.PlaceBottom:
		case domain.PlaceBefore, domain.PlaceAfter:
			id, err := formInt(form, "ref")
			if err != nil {
				return nil, err
			}
			in.Ref = domain.TaskID(id)
		default:
			return nil, domain.ValidationError(domain.FieldError{Field: "how", Message: "unknown placement"})
		}
		return a.o.Svc.Place(r.Context(), m, in)
	})
}

func (a *app) deferTask(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, form url.Values) (*service.OpResult, error) {
		ref, err := a.taskRef(r, form)
		if err != nil {
			return nil, err
		}
		in := service.DeferIn{ID: ref.ID, Expect: ref.Expect}
		if form.Get("until") != "none" {
			in.Until, err = a.formMoment(form, "until", datespec.Future)
			if err != nil {
				return nil, err
			}
			if in.Until == nil {
				return nil, domain.ValidationError(domain.FieldError{Field: "until", Message: "must not be empty (use none to clear)"})
			}
		}
		return a.o.Svc.Defer(r.Context(), m, in)
	})
}

func (a *app) triageTask(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, form url.Values) (*service.OpResult, error) {
		ref, err := a.taskRef(r, form)
		if err != nil {
			return nil, err
		}
		return a.o.Svc.MarkManual(r.Context(), m, service.MarkManualIn(ref))
	})
}

func (a *app) blockTask(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, form url.Values) (*service.OpResult, error) {
		ref, err := a.taskRef(r, form)
		if err != nil {
			return nil, err
		}
		span, err := a.formSpan(form, "span")
		if err != nil {
			return nil, err
		}
		return a.o.Svc.Block(r.Context(), m, service.BlockIn{TaskID: ref.ID, Expect: ref.Expect, Span: span})
	})
}

func (a *app) completeTask(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, form url.Values) (*service.OpResult, error) {
		ref, err := a.taskRef(r, form)
		if err != nil {
			return nil, err
		}
		date, err := a.formDate(form, "date", datespec.Past)
		if err != nil {
			return nil, err
		}
		in := service.CompleteIn{ID: ref.ID, Expect: ref.Expect, Date: date,
			LogTitle: form.Get("log"), LogNotes: form.Get("notes")}
		if as := form.Get("as"); as != "" {
			in.As = &as
		}
		return a.o.Svc.Complete(r.Context(), m, in)
	})
}

func (a *app) reopenTask(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, form url.Values) (*service.OpResult, error) {
		ref, err := a.taskRef(r, form)
		if err != nil {
			return nil, err
		}
		return a.o.Svc.Reopen(r.Context(), m, service.ReopenIn{ID: ref.ID, Expect: ref.Expect,
			RemoveEntry: form.Get("remove_entry") == "1"})
	})
}

func (a *app) dropTask(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, form url.Values) (*service.OpResult, error) {
		ref, err := a.taskRef(r, form)
		if err != nil {
			return nil, err
		}
		return a.o.Svc.Drop(r.Context(), m, ref)
	})
}

func (a *app) deleteTask(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, form url.Values) (*service.OpResult, error) {
		ref, err := a.taskRef(r, form)
		if err != nil {
			return nil, err
		}
		return a.o.Svc.Delete(r.Context(), m, ref)
	})
}

func (a *app) undeleteTask(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, form url.Values) (*service.OpResult, error) {
		ref, err := a.taskRef(r, form)
		if err != nil {
			return nil, err
		}
		return a.o.Svc.Undelete(r.Context(), m, ref)
	})
}

func (a *app) acceptNew(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, form url.Values) (*service.OpResult, error) {
		ids, etags := form["id"], form["etag"]
		if len(ids) == 0 || len(ids) != len(etags) {
			return nil, domain.Errorf(domain.CodeMalformed, "expected equal nonempty id and etag lists")
		}
		// AIDEV-NOTE: DF31 bounds C12 to one page of rendered pairs, without a fresh
		// triage read. Keep pair order and the original form intact for idempotency.
		if len(ids) > taskListPageSize {
			return nil, domain.Errorf(domain.CodeMalformed, "expected at most %d id and etag pairs", taskListPageSize)
		}
		in := service.AcceptNewIn{Items: make([]service.AcceptItem, 0, len(ids))}
		for i, raw := range ids {
			item := url.Values{"id": {raw}, "etag": {etags[i]}}
			id, err := formInt(item, "id")
			if err != nil {
				return nil, err
			}
			expect, err := a.expect(item, "etag")
			if err != nil {
				return nil, err
			}
			in.Items = append(in.Items, service.AcceptItem{ID: domain.TaskID(id), Expect: expect})
		}
		return a.o.Svc.AcceptNew(r.Context(), m, in)
	})
}
