package testutil

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"taskronin/internal/civil"
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

// Expect returns the task's current version in the live database generation.
func (s *Scenario) Expect(id domain.TaskID) domain.Expect {
	s.T.Helper()
	return domain.Expect{Gen: s.Svc.Generation(), Version: s.Task(id).Version}
}

// EntryExpect returns the entry's current version in the live database generation.
func (s *Scenario) EntryExpect(id domain.EntryID) domain.Expect {
	s.T.Helper()
	e, err := s.Svc.GetEntry(s.Ctx(), s.Actor, id)
	if err != nil {
		s.T.Fatal(err)
	}
	return domain.Expect{Gen: s.Svc.Generation(), Version: e.Version}
}

// Log records untimed work; task 0 means standalone and date "" means today.
func (s *Scenario) Log(task domain.TaskID, text, date string) domain.EntryID {
	s.T.Helper()
	return s.LogAt(task, text, date, "")
}

// LogAt records work with an optional HH:MM time; task 0 is standalone, date "" is today.
func (s *Scenario) LogAt(task domain.TaskID, text, date, at string) domain.EntryID {
	s.T.Helper()
	in := service.AddEntryIn{TaskID: sql.Null[domain.TaskID]{V: task, Valid: task != 0}, Title: text}
	if date != "" {
		in.WorkDate = D(date)
	}
	if at != "" {
		minute, err := civil.ParseHHMM(at, false)
		if err != nil {
			s.T.Fatal(err)
		}
		in.At = sql.Null[civil.Minute]{V: minute, Valid: true}
	}
	r := s.Must(s.Svc.AddEntry(s.Ctx(), s.Meta(), in))
	if r.Entry == nil {
		s.T.Fatal("AddEntry returned no entry")
	}
	return r.Entry.ID
}

// Add captures a task after applying the supplied input modifiers.
func (s *Scenario) Add(title string, mods ...func(*service.AddTaskIn)) domain.TaskID {
	s.T.Helper()
	in := service.AddTaskIn{Title: title}
	for _, mod := range mods {
		mod(&in)
	}
	r := s.Must(s.Svc.AddTask(s.Ctx(), s.Meta(), in))
	if r.Task == nil {
		s.T.Fatal("AddTask returned no task")
	}
	return r.Task.ID
}

// Due sets an AddTask input's due moment from a catalog date and optional time.
func Due(m string) func(*service.AddTaskIn) {
	return func(in *service.AddTaskIn) { moment := Moment(m); in.Due = &moment }
}

// Top places a new task at the top of the global task order.
func Top() func(*service.AddTaskIn) {
	return func(in *service.AddTaskIn) { in.Place = &service.AddPlacement{How: "top"} }
}

// Bottom places a new task at the bottom of the global task order.
func Bottom() func(*service.AddTaskIn) {
	return func(in *service.AddTaskIn) { in.Place = &service.AddPlacement{How: "bottom"} }
}

// After places a new task immediately after id in the global task order.
func After(id domain.TaskID) func(*service.AddTaskIn) {
	return func(in *service.AddTaskIn) { in.Place = &service.AddPlacement{How: "after", Ref: id} }
}

// Before places a new task immediately before id in the global task order.
func Before(id domain.TaskID) func(*service.AddTaskIn) {
	return func(in *service.AddTaskIn) { in.Place = &service.AddPlacement{How: "before", Ref: id} }
}

// Place acknowledges triage and positions a task using its current precondition.
func (s *Scenario) Place(id domain.TaskID, how domain.PlaceHow, ref ...domain.TaskID) {
	s.T.Helper()
	in := service.PlaceIn{ID: id, Expect: s.Expect(id), How: how}
	if len(ref) > 0 {
		in.Ref = ref[0]
	}
	s.Must(s.Svc.Place(s.Ctx(), s.Meta(), in))
}

// MarkManual requests manual triage using the task's current precondition.
func (s *Scenario) MarkManual(id domain.TaskID) {
	s.T.Helper()
	s.Must(s.Svc.MarkManual(s.Ctx(), s.Meta(), service.MarkManualIn{ID: id, Expect: s.Expect(id)}))
}

// Reconcile applies pending time-dependent transitions at the current fake instant.
func (s *Scenario) Reconcile() {
	s.T.Helper()
	if err := s.Svc.Reconcile(s.Ctx()); err != nil {
		s.T.Fatal(err)
	}
}

// SetDue sets a task's due moment, or clears it when moment is empty.
func (s *Scenario) SetDue(id domain.TaskID, moment string) {
	s.T.Helper()
	in := service.SetDueIn{ID: id, Expect: s.Expect(id)}
	if moment != "" {
		due := Moment(moment)
		in.Due = &due
	}
	s.Must(s.Svc.SetDue(s.Ctx(), s.Meta(), in))
}

// Task returns the current task view through GetTask.
func (s *Scenario) Task(id domain.TaskID) domain.TaskView {
	s.T.Helper()
	detail, err := s.Svc.GetTask(s.Ctx(), s.Actor, id)
	if err != nil {
		s.T.Fatal(err)
	}
	return detail.TaskView
}

// Reasons returns a task's triage reasons in descending priority order.
func (s *Scenario) Reasons(id domain.TaskID) []domain.TriageReason {
	s.T.Helper()
	rows := s.Task(id).Reasons
	reasons := make([]domain.TriageReason, 0, len(rows))
	for _, row := range rows {
		reasons = append(reasons, row.Reason)
	}
	return reasons
}

// Backlog independently queries open, undeleted, undeferred tasks without reasons.
func (s *Scenario) Backlog() []domain.TaskID {
	s.T.Helper()
	rows, err := s.Store.Reader.QueryContext(s.Ctx(), `SELECT id FROM tasks
		WHERE status = 'open' AND deleted_at IS NULL AND defer_date IS NULL
		AND NOT EXISTS (SELECT 1 FROM task_triage WHERE task_id = tasks.id) ORDER BY rank, id`)
	if err != nil {
		s.T.Fatal(err)
	}
	defer rows.Close()
	var ids []domain.TaskID
	for rows.Next() {
		var id domain.TaskID
		if err := rows.Scan(&id); err != nil {
			s.T.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		s.T.Fatal(err)
	}
	return ids
}
