package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/store"
)

// OpSummary describes a persisted operation and its user-visible consequences.
type OpSummary struct {
	Seq        domain.OpSeq
	OpID       domain.OpID
	Kind       domain.OpKind
	Actor      string
	Channel    domain.Channel
	At         time.Time
	State      domain.OpState
	Revertible bool
	Via        string
	TaskIDs    []domain.TaskID
	Effects    []Effect
}

// EventView pairs an audit event with its operation, omitting operation effects.
type EventView struct {
	ID            domain.EventID
	Op            OpSummary
	Entity        domain.EntityKind
	EntityID      int64
	Action        domain.Action
	Before, After json.RawMessage
}

// OpDetail contains an operation summary and its events in event ID order.
type OpDetail struct {
	Op     OpSummary
	Events []EventView
}

// HistoryIn selects a task's events with an optional event ID cursor.
type HistoryIn struct {
	TaskID domain.TaskID
	Limit  int
	Cursor string
}

// ListOpsIn filters operations by actor, op ID prefix and exclusive lower
// sequence bound.
type ListOpsIn struct {
	Actor    string
	OpPrefix string // 1 to 32 lowercase hex characters; empty means any.
	SinceSeq domain.OpSeq
	Limit    int
	Cursor   string
}

// History returns a task's audit events, including derived events, newest first.
func (s *Service) History(ctx context.Context, a domain.Actor, in HistoryIn) (Page[EventView], error) {
	limit, err := pageLimit(in.Limit)
	if err != nil {
		return Page[EventView]{}, err
	}
	cursor, err := decodeKeyset("history", in.Cursor)
	if err != nil {
		return Page[EventView]{}, err
	}
	var page Page[EventView]
	err = s.read(ctx, a, domain.ScopeRead, false, func(q store.Q, _ time.Time, _ civil.Date) error {
		if _, err := store.GetTask(ctx, q, in.TaskID); err != nil {
			return err
		}
		events, err := store.TaskEvents(ctx, q, in.TaskID, domain.EventID(cursor.ID), limit+1)
		if err != nil {
			return err
		}
		if len(events) > limit {
			events = events[:limit]
			page.NextCursor = domain.EncodeCursor(domain.Cursor{Keys: []any{"history"}, ID: int64(events[limit-1].ID)})
		}
		page.Items = make([]EventView, 0, len(events))
		ops := make(map[domain.OpID]OpSummary)
		for _, event := range events {
			summary, ok := ops[event.Op.ID]
			if !ok {
				summary, err = opSummary(ctx, q, event.Op)
				if err != nil {
					return err
				}
				ops[event.Op.ID] = summary
			}
			page.Items = append(page.Items, eventView(event.Event, summary))
		}
		return nil
	})
	if err != nil {
		return Page[EventView]{}, err
	}
	return page, nil
}

// ListOps returns matching operations in descending sequence order.
func (s *Service) ListOps(ctx context.Context, a domain.Actor, in ListOpsIn) (Page[OpSummary], error) {
	limit, err := pageLimit(in.Limit)
	if err != nil {
		return Page[OpSummary]{}, err
	}
	cursor, err := decodeKeyset("ops", in.Cursor)
	if err != nil {
		return Page[OpSummary]{}, err
	}
	if in.SinceSeq < 0 {
		return Page[OpSummary]{}, validation("since_seq", errors.New("must not be negative"))
	}
	if in.OpPrefix != "" && !domain.ValidOpIDPrefix(in.OpPrefix) {
		return Page[OpSummary]{}, validation("op_prefix", errors.New("must be 1 to 32 lowercase hex characters"))
	}
	var page Page[OpSummary]
	err = s.read(ctx, a, domain.ScopeRead, false, func(q store.Q, _ time.Time, _ civil.Date) error {
		var err error
		page, err = opPage(ctx, q, "ops", store.OpQuery{Actor: in.Actor, IDPrefix: in.OpPrefix, SinceSeq: in.SinceSeq,
			BeforeSeq: domain.OpSeq(cursor.ID), Limit: limit})
		return err
	})
	if err != nil {
		return Page[OpSummary]{}, err
	}
	return page, nil
}

// GetOp returns an operation and its ordered events, or not_found if absent.
func (s *Service) GetOp(ctx context.Context, a domain.Actor, id domain.OpID) (OpDetail, error) {
	var detail OpDetail
	err := s.read(ctx, a, domain.ScopeRead, false, func(q store.Q, _ time.Time, _ civil.Date) error {
		rec, err := store.GetOp(ctx, q, id)
		if err != nil {
			return err
		}
		detail.Op, err = opSummary(ctx, q, rec)
		if err != nil {
			return err
		}
		events, err := store.EventsOfOp(ctx, q, id)
		if err != nil {
			return err
		}
		detail.Op.Effects = effectsFromEvents(rec, events)
		detail.Events = make([]EventView, 0, len(events))
		for _, event := range events {
			detail.Events = append(detail.Events, eventView(event, detail.Op))
		}
		return nil
	})
	if err != nil {
		return OpDetail{}, err
	}
	return detail, nil
}

func opSummary(ctx context.Context, q store.Q, rec domain.OpRecord) (OpSummary, error) {
	ids, err := store.OpTaskIDs(ctx, q, rec.ID)
	if err != nil {
		return OpSummary{}, err
	}
	return OpSummary{Seq: rec.Seq, OpID: rec.ID, Kind: rec.Kind, Actor: rec.Actor,
		Channel: rec.Channel, At: rec.At, State: rec.State, Revertible: rec.Revertible,
		Via: rec.Via, TaskIDs: ids}, nil
}

func eventView(event domain.Event, summary OpSummary) EventView {
	summary.Effects = nil
	return EventView{ID: event.ID, Op: summary, Entity: event.Entity, EntityID: event.EntityID,
		Action: event.Action, Before: event.Before, After: event.After}
}

func opPage(ctx context.Context, q store.Q, view domain.View, query store.OpQuery) (Page[OpSummary], error) {
	limit := query.Limit
	query.Limit++
	records, err := store.ListOps(ctx, q, query)
	if err != nil {
		return Page[OpSummary]{}, err
	}
	var page Page[OpSummary]
	if len(records) > limit {
		records = records[:limit]
		page.NextCursor = domain.EncodeCursor(domain.Cursor{Keys: []any{string(view)}, ID: int64(records[limit-1].Seq)})
	}
	page.Items = make([]OpSummary, 0, len(records))
	for _, rec := range records {
		summary, err := opSummary(ctx, q, rec)
		if err != nil {
			return Page[OpSummary]{}, err
		}
		events, err := store.EventsOfOp(ctx, q, rec.ID)
		if err != nil {
			return Page[OpSummary]{}, err
		}
		summary.Effects = effectsFromEvents(rec, events)
		page.Items = append(page.Items, summary)
	}
	return page, nil
}
