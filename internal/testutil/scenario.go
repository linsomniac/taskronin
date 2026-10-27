package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"taskronin/internal/clock"
	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/store"
)

// Scenario holds a service fixture and the actor for fluent catalog operations.
type Scenario struct {
	T     testing.TB
	Svc   *service.Service
	Clock *clock.Fake
	Store *store.Store
	Actor domain.Actor
	Last  *service.OpResult
}

// NewScenario constructs an isolated fixture acting as the owner.
func NewScenario(t testing.TB, opts ...SvcOpt) *Scenario {
	t.Helper()
	svc, clk, st := newService(t, opts...)
	return &Scenario{T: t, Svc: svc, Clock: clk, Store: st, Actor: Owner()}
}

// Ctx returns the test's context.
func (s *Scenario) Ctx() context.Context { return s.T.Context() }

// At moves the shared fake clock to a catalog instant.
func (s *Scenario) At(when string) *Scenario { s.T.Helper(); s.Clock.Set(Local(s.T, when)); return s }

// As returns a shallow copy acting as a, retaining the shared fixture.
func (s *Scenario) As(a domain.Actor) *Scenario { next := *s; next.Actor = a; return &next }

// Meta returns a fresh request for the current actor and database generation.
func (s *Scenario) Meta() service.Meta { return Meta(s.Actor, s.Svc.Generation()) }

// MetaWith returns a request with an explicit idempotency key and canonical body.
func (s *Scenario) MetaWith(key, body string) service.Meta {
	m := s.Meta()
	m.Idem.Key, m.Idem.Body = key, []byte(body)
	return m
}

// Must requires success and remembers the latest fluent operation result.
func (s *Scenario) Must(r *service.OpResult, err error) *service.OpResult {
	s.T.Helper()
	if err != nil {
		s.T.Fatal(err)
	}
	s.Last = r
	return r
}

// Events returns an operation's persisted events in event order.
func (s *Scenario) Events(op domain.OpID) []domain.Event {
	s.T.Helper()
	evs, err := store.EventsOfOp(s.Ctx(), s.Store.Reader, op)
	if err != nil {
		s.T.Fatal(err)
	}
	return evs
}

// EventKeys returns compact entity/action labels with IDs, triage reasons and derivation.
func (s *Scenario) EventKeys(op domain.OpID) []string {
	s.T.Helper()
	keys := make([]string, 0)
	for _, ev := range s.Events(op) {
		key := string(ev.Entity) + ":" + string(ev.Action)
		if ev.Entity != domain.EntFocus && ev.EntityID != 0 {
			key += fmt.Sprintf(" %d", ev.EntityID)
		}
		if ev.Entity == domain.EntTriage {
			image := ev.After
			if ev.Action == domain.ActRemove {
				image = ev.Before
			}
			var cols struct {
				Reason domain.TriageReason `json:"reason"`
			}
			if err := json.Unmarshal(image, &cols); err != nil {
				s.T.Fatal(err)
			}
			key += " " + string(cols.Reason)
		}
		if ev.Derived {
			key += " [derived]"
		}
		keys = append(keys, key)
	}
	return keys
}

// OpCount counts all operations in the fixture.
func (s *Scenario) OpCount() int {
	s.T.Helper()
	var n int
	if err := s.Store.Reader.QueryRowContext(s.Ctx(), `SELECT COUNT(*) FROM ops`).Scan(&n); err != nil {
		s.T.Fatal(err)
	}
	return n
}

// Op loads a persisted operation.
func (s *Scenario) Op(id domain.OpID) domain.OpRecord {
	s.T.Helper()
	r, err := store.GetOp(s.Ctx(), s.Store.Reader, id)
	if err != nil {
		s.T.Fatal(err)
	}
	return r
}

// Stack returns the actor's stack from bottom to top.
func (s *Scenario) Stack(actor string, which store.Stack) []domain.OpID {
	s.T.Helper()
	ops, err := store.StackOps(s.Ctx(), s.Store.Reader, actor, which)
	if err != nil {
		s.T.Fatal(err)
	}
	return ops
}

// Exec runs raw fixture SQL only for scenarios explicitly permitting it.
func (s *Scenario) Exec(query string, args ...any) {
	s.T.Helper()
	if _, err := s.Store.Writer.ExecContext(s.Ctx(), query, args...); err != nil {
		s.T.Fatal(err)
	}
}
