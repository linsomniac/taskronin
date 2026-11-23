package server

import (
	"database/sql"
	"net/http"
	"strconv"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/service"
)

func (s *Server) addEntry(w http.ResponseWriter, r *http.Request) {
	v, body, err := decodeBody[api.AddEntryRequest](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	taskID, err := requiredTaskID(v.TaskID)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	in := service.AddEntryIn{TaskID: taskID, Title: v.Title, Notes: v.Notes}
	if v.WorkDate != nil {
		in.WorkDate = *v.WorkDate
	}
	if v.Time != nil {
		in.At = sql.Null[civil.Minute]{V: *v.Time, Valid: true}
	}
	res, err := s.svc.AddEntry(r.Context(), s.meta(r, body), in)
	s.writeEntryOp(w, r, res, err)
}

func (s *Server) updateEntry(w http.ResponseWriter, r *http.Request) {
	v, body, ref, err := decodeEntryMutation[api.UpdateEntryRequest](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	for _, field := range []struct {
		name string
		null bool
	}{{"title", v.Title.Null}, {"notes", v.Notes.Null}, {"work_date", v.WorkDate.Null}} {
		if field.null {
			s.writeProblem(w, r, domain.ValidationError(domain.FieldError{Field: field.name, Message: "must not be null"}))
			return
		}
	}
	in := service.UpdateEntryIn{ID: ref.ID, Expect: ref.Expect}
	if v.Title.Set {
		in.Title = &v.Title.V
	}
	if v.Notes.Set {
		in.Notes = &v.Notes.V
	}
	if v.WorkDate.Set {
		in.WorkDate = &v.WorkDate.V
	}
	// AIDEV-NOTE: Missing time preserves the minute; explicit null clears it.
	if v.Time.Set {
		in.At = &sql.Null[civil.Minute]{V: v.Time.V, Valid: !v.Time.Null}
	}
	res, err := s.svc.UpdateEntry(r.Context(), s.meta(r, body), in)
	s.writeEntryOp(w, r, res, err)
}

func (s *Server) relinkEntry(w http.ResponseWriter, r *http.Request) {
	v, body, ref, err := decodeEntryMutation[api.RelinkRequest](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	taskID, err := requiredTaskID(v.TaskID)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	res, err := s.svc.RelinkEntry(r.Context(), s.meta(r, body), service.RelinkEntryIn{ID: ref.ID, Expect: ref.Expect, TaskID: taskID})
	s.writeEntryOp(w, r, res, err)
}

func (s *Server) deleteEntry(w http.ResponseWriter, r *http.Request) {
	_, body, ref, err := decodeEntryMutation[struct{}](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	res, err := s.svc.DeleteEntry(r.Context(), s.meta(r, body), ref)
	s.writeEntryOp(w, r, res, err)
}

func (s *Server) undeleteEntry(w http.ResponseWriter, r *http.Request) {
	_, body, ref, err := decodeEntryMutation[struct{}](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	res, err := s.svc.UndeleteEntry(r.Context(), s.meta(r, body), ref)
	s.writeEntryOp(w, r, res, err)
}

func decodeEntryMutation[T any](r *http.Request) (T, []byte, service.EntryRef, error) {
	v, body, err := decodeBody[T](r)
	if err != nil {
		return v, nil, service.EntryRef{}, err
	}
	id, err := pathInt(r, "id")
	if err != nil {
		return v, nil, service.EntryRef{}, err
	}
	expect, err := ifMatch(r)
	return v, body, service.EntryRef{ID: domain.EntryID(id), Expect: expect}, err
}

func requiredTaskID(v api.Opt[int64]) (sql.Null[domain.TaskID], error) {
	// AIDEV-NOTE: Omission must never inherit focus; null explicitly selects
	// standalone work (or clears focus), while a number selects a task.
	if !v.Set {
		return sql.Null[domain.TaskID]{}, domain.ValidationError(domain.FieldError{Field: "task_id", Message: "key is required"})
	}
	return sql.Null[domain.TaskID]{V: domain.TaskID(v.V), Valid: !v.Null}, nil
}

func (s *Server) writeEntryOp(w http.ResponseWriter, r *http.Request, res *service.OpResult, err error) {
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	location := ""
	if res.Created {
		location = "/api/v1/entries/" + strconv.FormatInt(int64(res.Entry.ID), 10)
	}
	s.writeOp(w, r, res, location)
}
