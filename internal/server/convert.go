package server

import (
	"encoding/json"

	"taskronin/internal/api"
	"taskronin/internal/domain"
	"taskronin/internal/service"
)

func (s *Server) opResponse(res *service.OpResult) api.OpResponse {
	out := api.OpResponse{Replayed: res.Replayed, Effects: make([]api.Effect, len(res.Effects)), Warnings: append([]string{}, res.Warnings...)}
	if !res.OpID.IsZero() {
		out.OpID = new(res.OpID.String())
	}
	if res.Task != nil {
		out.Task = new(s.conv.Task(*res.Task))
	}
	if res.Entry != nil {
		out.Entry = new(s.conv.Entry(*res.Entry))
	}
	if res.Block != nil {
		out.Block = new(s.conv.Block(*res.Block))
	}
	if res.Tasks != nil {
		out.Tasks = make([]api.Task, len(res.Tasks))
		for i, task := range res.Tasks {
			out.Tasks[i] = s.conv.Task(task)
		}
	}
	for i, e := range res.Effects {
		out.Effects[i] = api.Effect{Kind: string(e.Kind), TaskID: int64(e.TaskID), BlockID: int64(e.BlockID), EntryID: int64(e.EntryID), Reason: string(e.Reason), From: e.From, To: e.To}
	}
	return out
}

func (s *Server) currentResponse(v any) (json.RawMessage, error) {
	var wire any
	switch v := v.(type) {
	case domain.TaskView:
		wire = s.conv.Task(v)
	case *domain.TaskView:
		wire = s.conv.Task(*v)
	case domain.EntryView:
		wire = s.conv.Entry(v)
	case *domain.EntryView:
		wire = s.conv.Entry(*v)
	case domain.BlockView:
		wire = s.conv.Block(v)
	case *domain.BlockView:
		wire = s.conv.Block(*v)
	default:
		return nil, domain.Errorf(domain.CodeInternal, "unsupported current representation")
	}
	return json.Marshal(wire)
}
