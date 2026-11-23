package server

import (
	"net/http"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/service"
)

func (s *Server) reblock(w http.ResponseWriter, r *http.Request) {
	v, body, ref, err := decodeBlockMutation[api.ReblockRequest](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	taskExpect, err := api.ParseETag(v.TaskETag)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	res, err := s.svc.Reblock(r.Context(), s.meta(r, body), service.ReblockIn{BlockID: ref.BlockID, Expect: ref.Expect, TaskExpect: taskExpect, Span: taskBlockSpan(v.BlockRequest)})
	s.writeMutationOp(w, r, res, err)
}

func (s *Server) unblock(w http.ResponseWriter, r *http.Request) {
	_, body, ref, err := decodeBlockMutation[struct{}](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	res, err := s.svc.Unblock(r.Context(), s.meta(r, body), ref)
	s.writeMutationOp(w, r, res, err)
}

func (s *Server) setOutcome(w http.ResponseWriter, r *http.Request) {
	v, body, ref, err := decodeBlockMutation[api.OutcomeRequest](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	res, err := s.svc.SetOutcome(r.Context(), s.meta(r, body), service.SetOutcomeIn{BlockID: ref.BlockID, Expect: ref.Expect, Outcome: domain.UserOutcome(v.Outcome)})
	s.writeMutationOp(w, r, res, err)
}

func decodeBlockMutation[T any](r *http.Request) (T, []byte, service.BlockRef, error) {
	v, body, err := decodeBody[T](r)
	if err != nil {
		return v, nil, service.BlockRef{}, err
	}
	id, err := pathInt(r, "id")
	if err != nil {
		return v, nil, service.BlockRef{}, err
	}
	expect, err := ifMatch(r)
	return v, body, service.BlockRef{BlockID: domain.BlockID(id), Expect: expect}, err
}
