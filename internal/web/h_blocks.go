package web

import (
	"net/http"
	"net/url"

	"taskronin/internal/domain"
	"taskronin/internal/service"
)

func (a *app) blockRef(r *http.Request, form url.Values) (service.BlockRef, error) {
	id, err := pathInt(r, "id")
	if err != nil {
		return service.BlockRef{}, err
	}
	expect, err := a.expect(form, "etag")
	return service.BlockRef{BlockID: domain.BlockID(id), Expect: expect}, err
}

func (a *app) reblock(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, form url.Values) (*service.OpResult, error) {
		ref, err := a.blockRef(r, form)
		if err != nil {
			return nil, err
		}
		// AIDEV-NOTE: Both preconditions come from the rendered snapshot
		// (DF20/R4-7). Never refresh the task version before mutating (R1).
		taskExpect, err := a.expect(form, "task_etag")
		if err != nil {
			return nil, err
		}
		span, err := a.formSpan(form, "span")
		if err != nil {
			return nil, err
		}
		return a.o.Svc.Reblock(r.Context(), m, service.ReblockIn{BlockID: ref.BlockID,
			Expect: ref.Expect, TaskExpect: taskExpect, Span: span})
	})
}

func (a *app) unblock(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, form url.Values) (*service.OpResult, error) {
		ref, err := a.blockRef(r, form)
		if err != nil {
			return nil, err
		}
		return a.o.Svc.Unblock(r.Context(), m, ref)
	})
}

func (a *app) setOutcome(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, form url.Values) (*service.OpResult, error) {
		ref, err := a.blockRef(r, form)
		if err != nil {
			return nil, err
		}
		outcome := domain.UserOutcome(form.Get("outcome"))
		if !outcome.Valid() {
			return nil, domain.ValidationError(domain.FieldError{Field: "outcome", Message: "expected worked, missed, or auto"})
		}
		return a.o.Svc.SetOutcome(r.Context(), m, service.SetOutcomeIn{BlockID: ref.BlockID, Expect: ref.Expect, Outcome: outcome})
	})
}
