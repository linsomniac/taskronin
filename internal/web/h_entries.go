package web

import (
	"database/sql"
	"net/http"
	"net/url"
	"strings"

	"github.com/linsomniac/taskronin/internal/datespec"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/service"
)

func entryTaskID(form url.Values) (sql.Null[domain.TaskID], error) {
	if strings.TrimSpace(form.Get("task_id")) == "" {
		return sql.Null[domain.TaskID]{}, nil
	}
	id, err := formInt(form, "task_id")
	if err != nil {
		return sql.Null[domain.TaskID]{}, err
	}
	return sql.Null[domain.TaskID]{V: domain.TaskID(id), Valid: true}, nil
}

func (a *app) addEntry(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, form url.Values) (*service.OpResult, error) {
		taskID, err := entryTaskID(form)
		if err != nil {
			return nil, err
		}
		date, err := a.formDate(form, "date", datespec.Past)
		if err != nil {
			return nil, err
		}
		at, err := formMinute(form, "at")
		if err != nil {
			return nil, err
		}
		// AIDEV-NOTE: As with task notes, service validation normalizes CRLF
		// to LF (D18); leave the submitted notes intact for idempotency.
		return a.o.Svc.AddEntry(r.Context(), m, service.AddEntryIn{TaskID: taskID,
			Title: form.Get("title"), Notes: form.Get("notes"), WorkDate: date, At: at})
	})
}

func (a *app) entryRef(r *http.Request, form url.Values) (service.EntryRef, error) {
	id, err := pathInt(r, "id")
	if err != nil {
		return service.EntryRef{}, err
	}
	expect, err := a.expect(form, "etag")
	return service.EntryRef{ID: domain.EntryID(id), Expect: expect}, err
}

func (a *app) editEntry(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, form url.Values) (*service.OpResult, error) {
		ref, err := a.entryRef(r, form)
		if err != nil {
			return nil, err
		}
		in := service.UpdateEntryIn{ID: ref.ID, Expect: ref.Expect}
		// AIDEV-NOTE: Decode only present fields. Complete-entry forms send
		// notes only; service enforces that restriction without a pre-read (R1).
		if form.Has("title") {
			value := form.Get("title")
			in.Title = &value
		}
		if form.Has("notes") {
			value := form.Get("notes")
			in.Notes = &value
		}
		if form.Has("date") {
			date, err := a.formDate(form, "date", datespec.Past)
			if err != nil {
				return nil, err
			}
			if date.IsZero() {
				return nil, emptyField("date")
			}
			in.WorkDate = &date
		}
		if form.Has("at") {
			at, err := formMinute(form, "at")
			if err != nil {
				return nil, err
			}
			in.At = &at
		}
		return a.o.Svc.UpdateEntry(r.Context(), m, in)
	})
}

func (a *app) relinkEntry(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, form url.Values) (*service.OpResult, error) {
		ref, err := a.entryRef(r, form)
		if err != nil {
			return nil, err
		}
		taskID, err := entryTaskID(form)
		if err != nil {
			return nil, err
		}
		return a.o.Svc.RelinkEntry(r.Context(), m, service.RelinkEntryIn{ID: ref.ID, Expect: ref.Expect, TaskID: taskID})
	})
}

func (a *app) deleteEntry(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, form url.Values) (*service.OpResult, error) {
		ref, err := a.entryRef(r, form)
		if err != nil {
			return nil, err
		}
		return a.o.Svc.DeleteEntry(r.Context(), m, ref)
	})
}

func (a *app) undeleteEntry(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, func(m service.Meta, form url.Values) (*service.OpResult, error) {
		ref, err := a.entryRef(r, form)
		if err != nil {
			return nil, err
		}
		return a.o.Svc.UndeleteEntry(r.Context(), m, ref)
	})
}
