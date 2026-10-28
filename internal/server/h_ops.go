package server

import (
	"net/http"

	"taskronin/internal/api"
	"taskronin/internal/domain"
	"taskronin/internal/service"
)

func (s *Server) setFocus(w http.ResponseWriter, r *http.Request) {
	v, body, err := decodeBody[api.FocusRequest](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	taskID, err := requiredTaskID(v.TaskID)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	res, err := s.svc.SetFocus(r.Context(), s.meta(r, body), service.SetFocusIn{TaskID: taskID, Interrupt: v.Interrupt})
	s.writeMutationOp(w, r, res, err)
}

func (s *Server) revert(w http.ResponseWriter, r *http.Request) {
	_, body, err := decodeBody[struct{}](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	id, err := domain.ParseOpID(r.PathValue("op_id"))
	if err != nil {
		s.writeProblem(w, r, domain.Errorf(domain.CodeMalformed, "invalid op_id"))
		return
	}
	res, err := s.svc.Revert(r.Context(), s.meta(r, body), service.RevertIn{OpID: id})
	s.writeMutationOp(w, r, res, err)
}

func (s *Server) undo(w http.ResponseWriter, r *http.Request) {
	_, body, err := decodeBody[struct{}](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	res, err := s.svc.Undo(r.Context(), s.meta(r, body))
	s.writeMutationOp(w, r, res, err)
}

func (s *Server) redo(w http.ResponseWriter, r *http.Request) {
	_, body, err := decodeBody[struct{}](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	res, err := s.svc.Redo(r.Context(), s.meta(r, body))
	s.writeMutationOp(w, r, res, err)
}

func (s *Server) reviewAIChanges(w http.ResponseWriter, r *http.Request) {
	v, body, err := decodeBody[api.ReviewRequest](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	res, err := s.svc.MarkAIReviewed(r.Context(), s.meta(r, body), service.MarkAIReviewedIn{Through: domain.OpSeq(v.ThroughSeq)})
	s.writeMutationOp(w, r, res, err)
}

func (s *Server) writeMutationOp(w http.ResponseWriter, r *http.Request, res *service.OpResult, err error) {
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeOp(w, r, res, "")
}
