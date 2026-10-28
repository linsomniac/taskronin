package server

import (
	"database/sql"
	"net/http"
	"strconv"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/service"
)

func (s *Server) addTask(w http.ResponseWriter, r *http.Request) {
	v, body, err := decodeBody[api.AddTaskRequest](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	in := service.AddTaskIn{Title: v.Title, Notes: v.Notes, Due: taskMoment(v.Due), Tags: v.Tags, Project: v.Project, Priority: domain.Priority(v.Priority)}
	if v.Place != nil {
		in.Place = &service.AddPlacement{How: v.Place.How, Ref: domain.TaskID(v.Place.Ref)}
		if moment := taskMoment(v.Place.Defer); moment != nil {
			in.Place.Defer = *moment
		}
		if v.Place.Block != nil {
			in.Place.Block = taskBlockSpan(*v.Place.Block)
		}
	}
	res, err := s.svc.AddTask(r.Context(), s.meta(r, body), in)
	s.writeTaskOp(w, r, res, err)
}

func (s *Server) updateTask(w http.ResponseWriter, r *http.Request) {
	v, body, ref, err := decodeTaskMutation[api.UpdateTaskRequest](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	in := service.UpdateTaskIn{ID: ref.ID, Expect: ref.Expect, TagsAdd: v.TagsAdd, TagsRemove: v.TagsRemove}
	if v.Title.Null {
		s.writeProblem(w, r, domain.ValidationError(domain.FieldError{Field: "title", Message: "must not be null"}))
		return
	}
	if v.Notes.Null {
		s.writeProblem(w, r, domain.ValidationError(domain.FieldError{Field: "notes", Message: "must not be null"}))
		return
	}
	if v.Title.Set {
		in.Title = &v.Title.V
	}
	if v.Notes.Set {
		in.Notes = &v.Notes.V
	}
	// AIDEV-NOTE: Absence leaves metadata unchanged; explicit null clears it.
	// Priority uses the service's typed PriorityNone sentinel (BF10).
	if v.Project.Set {
		in.Project = new("")
		if !v.Project.Null {
			in.Project = &v.Project.V
		}
	}
	if v.Priority.Set {
		in.Priority = new(domain.PriorityNone)
		if !v.Priority.Null {
			in.Priority = new(domain.Priority(v.Priority.V))
		}
	}
	res, err := s.svc.UpdateTask(r.Context(), s.meta(r, body), in)
	s.writeTaskOp(w, r, res, err)
}

func (s *Server) setDue(w http.ResponseWriter, r *http.Request) {
	v, body, ref, err := decodeTaskMutation[api.DueRequest](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	due, err := requiredTaskMoment(v.Due, "due")
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	res, err := s.svc.SetDue(r.Context(), s.meta(r, body), service.SetDueIn{ID: ref.ID, Expect: ref.Expect, Due: due})
	s.writeTaskOp(w, r, res, err)
}

func (s *Server) placeTask(w http.ResponseWriter, r *http.Request) {
	v, body, ref, err := decodeTaskMutation[api.PlaceRequest](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	res, err := s.svc.Place(r.Context(), s.meta(r, body), service.PlaceIn{ID: ref.ID, Expect: ref.Expect, How: domain.PlaceHow(v.How), Ref: domain.TaskID(v.Ref)})
	s.writeTaskOp(w, r, res, err)
}

func (s *Server) deferTask(w http.ResponseWriter, r *http.Request) {
	v, body, ref, err := decodeTaskMutation[api.DeferRequest](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	until, err := requiredTaskMoment(v.Until, "until")
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	res, err := s.svc.Defer(r.Context(), s.meta(r, body), service.DeferIn{ID: ref.ID, Expect: ref.Expect, Until: until})
	s.writeTaskOp(w, r, res, err)
}

func (s *Server) blockTask(w http.ResponseWriter, r *http.Request) {
	v, body, ref, err := decodeTaskMutation[api.BlockRequest](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	res, err := s.svc.Block(r.Context(), s.meta(r, body), service.BlockIn{TaskID: ref.ID, Expect: ref.Expect, Span: taskBlockSpan(v)})
	s.writeTaskOp(w, r, res, err)
}

func (s *Server) markManual(w http.ResponseWriter, r *http.Request) {
	_, body, ref, err := decodeTaskMutation[struct{}](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	res, err := s.svc.MarkManual(r.Context(), s.meta(r, body), service.MarkManualIn(ref))
	s.writeTaskOp(w, r, res, err)
}

func (s *Server) completeTask(w http.ResponseWriter, r *http.Request) {
	v, body, ref, err := decodeTaskMutation[api.CompleteRequest](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	in := service.CompleteIn{ID: ref.ID, Expect: ref.Expect, As: v.As, LogTitle: v.Log, LogNotes: v.Notes}
	if v.Date != nil {
		in.Date = *v.Date
	}
	res, err := s.svc.Complete(r.Context(), s.meta(r, body), in)
	s.writeTaskOp(w, r, res, err)
}

func (s *Server) reopenTask(w http.ResponseWriter, r *http.Request) {
	v, body, ref, err := decodeTaskMutation[api.ReopenRequest](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	res, err := s.svc.Reopen(r.Context(), s.meta(r, body), service.ReopenIn{ID: ref.ID, Expect: ref.Expect, RemoveEntry: v.RemoveEntry})
	s.writeTaskOp(w, r, res, err)
}

func (s *Server) dropTask(w http.ResponseWriter, r *http.Request) {
	_, body, ref, err := decodeTaskMutation[struct{}](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	res, err := s.svc.Drop(r.Context(), s.meta(r, body), ref)
	s.writeTaskOp(w, r, res, err)
}

func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request) {
	_, body, ref, err := decodeTaskMutation[struct{}](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	res, err := s.svc.Delete(r.Context(), s.meta(r, body), ref)
	s.writeTaskOp(w, r, res, err)
}

func (s *Server) undeleteTask(w http.ResponseWriter, r *http.Request) {
	_, body, ref, err := decodeTaskMutation[struct{}](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	res, err := s.svc.Undelete(r.Context(), s.meta(r, body), ref)
	s.writeTaskOp(w, r, res, err)
}

func (s *Server) acceptNew(w http.ResponseWriter, r *http.Request) {
	v, body, err := decodeBody[api.AcceptNewRequest](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	in := service.AcceptNewIn{Items: make([]service.AcceptItem, len(v.Items))}
	for i, item := range v.Items {
		expect, err := api.ParseETag(item.ETag)
		if err != nil {
			s.writeProblem(w, r, err)
			return
		}
		in.Items[i] = service.AcceptItem{ID: domain.TaskID(item.ID), Expect: expect}
	}
	// BF12: Preserve the service's new-reason sequence order, including replays.
	res, err := s.svc.AcceptNew(r.Context(), s.meta(r, body), in)
	s.writeTaskOp(w, r, res, err)
}

func decodeTaskMutation[T any](r *http.Request) (T, []byte, service.TaskRef, error) {
	v, body, err := decodeBody[T](r)
	if err != nil {
		return v, nil, service.TaskRef{}, err
	}
	id, err := pathInt(r, "id")
	if err != nil {
		return v, nil, service.TaskRef{}, err
	}
	expect, err := ifMatch(r)
	return v, body, service.TaskRef{ID: domain.TaskID(id), Expect: expect}, err
}

func taskMoment(v *api.Moment) *domain.Moment {
	if v == nil {
		return nil
	}
	out := &domain.Moment{Date: v.Date}
	if v.Time != nil {
		out.Min = sql.Null[civil.Minute]{V: *v.Time, Valid: true}
	}
	return out
}

func requiredTaskMoment(v api.Opt[api.Moment], field string) (*domain.Moment, error) {
	if !v.Set {
		return nil, domain.ValidationError(domain.FieldError{Field: field, Message: "key is required"})
	}
	if v.Null {
		return nil, nil
	}
	return taskMoment(&v.V), nil
}

func taskBlockSpan(v api.BlockRequest) domain.BlockSpan {
	return domain.BlockSpan{Date: v.Date, Start: v.Start, End: v.End}
}

func (s *Server) writeTaskOp(w http.ResponseWriter, r *http.Request, res *service.OpResult, err error) {
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	location := ""
	if res.Created {
		location = "/api/v1/tasks/" + strconv.FormatInt(int64(res.Task.ID), 10)
	}
	s.writeOp(w, r, res, location)
}
