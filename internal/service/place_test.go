package service_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/store"
	"taskronin/internal/testutil"
)

func requirePlaced(t *testing.T, s *testutil.Scenario, id domain.TaskID, version domain.Version) {
	t.Helper()
	v := s.Task(id)
	if len(v.Reasons) != 0 || !v.LastPlacedAt.Valid || !v.LastPlacedAt.V.Equal(s.Clock.Now()) ||
		!v.LastPlacedSeq.Valid || v.LastPlacedSeq.V != s.Op(s.Last.OpID).Seq || v.DueAck != v.DuePhase ||
		!v.DueAckDate.Valid || v.DueAckDate.V != testutil.Zone.DateOf(s.Clock.Now()) ||
		v.DeferDate.Valid || v.DeferMin.Valid || v.Version != version {
		t.Fatalf("task is not PLACED: %+v", v)
	}
}

func requireBacklog(t *testing.T, s *testutil.Scenario, want ...domain.TaskID) {
	t.Helper()
	if got := s.Backlog(); !slices.Equal(got, want) {
		t.Fatalf("backlog = %v, want %v", got, want)
	}
}

func TestTRI02(t *testing.T) {
	s := testutil.NewScenario(t)
	a, b := s.Add("a", testutil.Bottom()), s.Add("b", testutil.Bottom())
	c := s.Add("B", testutil.Top())
	requireBacklog(t, s, c, a, b)
	requirePlaced(t, s, c, 1)
	if s.Task(c).Rank >= s.Task(a).Rank || s.Task(c).DueAck != domain.DueNone ||
		!slices.Equal(s.EventKeys(s.Last.OpID), []string{"task:insert 3", "task:update 3"}) {
		t.Fatalf("top add = %+v, events = %v", s.Task(c), s.EventKeys(s.Last.OpID))
	}
}

func TestTRI03(t *testing.T) {
	s := testutil.NewScenario(t)
	a, b := s.Add("a", testutil.Bottom()), s.Add("b", testutil.Bottom())
	c := s.Add("B", testutil.Bottom())
	requireBacklog(t, s, a, b, c)
	requirePlaced(t, s, c, 1)
	if s.Task(c).Rank <= s.Task(b).Rank {
		t.Fatal("bottom add did not rank below b")
	}
}

func TestTRI04(t *testing.T) {
	for _, how := range []string{"after", "before"} {
		t.Run(how, func(t *testing.T) {
			s := testutil.NewScenario(t)
			a, b := s.Add("a", testutil.Bottom()), s.Add("b", testutil.Bottom())
			mod := testutil.After(a)
			if how == "before" {
				mod = testutil.Before(b)
			}
			c := s.Add("B", mod)
			requireBacklog(t, s, a, c, b)
			requirePlaced(t, s, c, 1)
			count := s.OpCount()
			_, err := s.Svc.AddTask(s.Ctx(), s.Meta(), service.AddTaskIn{Title: "missing", Place: &service.AddPlacement{How: how, Ref: 99}})
			testutil.RequireCode(t, err, domain.CodeNotFound)
			if s.OpCount() != count {
				t.Fatal("missing reference wrote an op")
			}
			requireBacklog(t, s, a, c, b)
		})
	}
}

func TestTRI09(t *testing.T) {
	for _, how := range []string{"top", "bottom", "before", "after", "defer", "block", "now", "keep", "", "unknown"} {
		t.Run(how, func(t *testing.T) {
			for _, field := range []string{"ref", "defer", "block"} {
				if (field == "ref" && (how == "before" || how == "after")) || field == how {
					continue
				}
				t.Run(field, func(t *testing.T) {
					s := testutil.NewScenario(t)
					p := service.AddPlacement{How: how}
					switch field {
					case "ref":
						p.Ref = 1
					case "defer":
						p.Defer = testutil.Moment("10-29")
					case "block":
						p.Block = domain.BlockSpan{Date: testutil.D("10-29"), Start: 600, End: 660}
					}
					_, err := s.Svc.AddTask(s.Ctx(), s.Meta(), service.AddTaskIn{Title: "B", Place: &p})
					testutil.RequireCode(t, err, domain.CodeValidation)
					if s.OpCount() != 0 {
						t.Fatal("invalid placement wrote an op")
					}
				})
			}
		})
	}
	for _, how := range []string{"defer", "block", "keep", "unknown", "", "before", "after"} {
		t.Run("unsupported_or_missing/"+how, func(t *testing.T) {
			s := testutil.NewScenario(t)
			_, err := s.Svc.AddTask(s.Ctx(), s.Meta(), service.AddTaskIn{Title: "B", Place: &service.AddPlacement{How: how}})
			testutil.RequireCode(t, err, domain.CodeValidation)
			if how == "defer" {
				de, _ := domain.AsError(err)
				if len(de.Fields) != 1 || de.Fields[0].Field != "place" || de.Fields[0].Message != "unsupported placement" {
					t.Fatalf("error = %v", err)
				}
			}
		})
	}
}

func triageBetween(t *testing.T) (*testutil.Scenario, domain.TaskID, domain.TaskID, domain.TaskID) {
	t.Helper()
	s := testutil.NewScenario(t)
	a := s.Add("new")
	b, c := s.Add("before", testutil.Top()), s.Add("after", testutil.Bottom())
	return s, a, b, c
}

func TestTRI10(t *testing.T) {
	s, a, b, c := triageBetween(t)
	rank := s.Task(a).Rank
	s.Place(a, domain.PlaceKeep)
	requirePlaced(t, s, a, 2)
	requireBacklog(t, s, b, a, c)
	if s.Task(a).Rank != rank || !slices.Equal(s.EventKeys(s.Last.OpID), []string{"triage:remove 1 new", "task:update 1"}) {
		t.Fatalf("keep rank/events = %d / %v", s.Task(a).Rank, s.EventKeys(s.Last.OpID))
	}
}

func TestTRI11(t *testing.T) {
	for _, how := range []domain.PlaceHow{domain.PlaceTop, domain.PlaceBottom} {
		t.Run(string(how), func(t *testing.T) {
			s, a, b, c := triageBetween(t)
			s.Place(a, how)
			requirePlaced(t, s, a, 2)
			after := `{"after_id":null}`
			if how == domain.PlaceTop {
				requireBacklog(t, s, a, b, c)
			} else {
				requireBacklog(t, s, b, c, a)
				after = `{"after_id":3}`
			}
			evs := s.Events(s.Last.OpID)
			if !slices.Equal(s.EventKeys(s.Last.OpID), []string{"task:move 1", "triage:remove 1 new", "task:update 1"}) ||
				string(evs[0].Before) != `{"after_id":2}` || string(evs[0].After) != after {
				t.Fatalf("move events = %+v", evs)
			}
		})
	}
}

func TestTRI12(t *testing.T) {
	for _, how := range []domain.PlaceHow{domain.PlaceBefore, domain.PlaceAfter} {
		t.Run(string(how), func(t *testing.T) {
			s, a, b, c := triageBetween(t)
			s.Place(a, how, c)
			requirePlaced(t, s, a, 2)
			if how == domain.PlaceBefore {
				requireBacklog(t, s, b, a, c)
			} else {
				requireBacklog(t, s, b, c, a)
			}
			before, count := s.Task(a), s.OpCount()
			_, err := s.Svc.Place(s.Ctx(), s.Meta(), service.PlaceIn{ID: a, Expect: s.Expect(a), How: how, Ref: 99})
			testutil.RequireCode(t, err, domain.CodeNotFound)
			if !reflect.DeepEqual(s.Task(a), before) || s.OpCount() != count {
				t.Fatal("missing reference changed state")
			}
		})
	}
}

func TestTRI18(t *testing.T) {
	fixture := func(t *testing.T) (*testutil.Scenario, []domain.TaskID) {
		t.Helper()
		s := testutil.NewScenario(t)
		a := s.At("10-28 09:00").Add("first")
		b := s.At("10-28 09:30").Add("second")
		c := s.Add("new and slipped")
		d := s.Add("surfaced", testutil.Bottom())
		// F5 permits these otherwise unavailable TRI-18 reason fixtures.
		s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), []domain.EntityRef{{Entity: domain.EntTask, ID: int64(c)}, {Entity: domain.EntTask, ID: int64(d)}}, func(q store.Q) error {
			for _, row := range []domain.TriageRow{
				{TaskID: c, Reason: domain.ReasonSlipped, Since: s.Clock.Now(), Seq: s.Op(s.Last.OpID).Seq},
				{TaskID: d, Reason: domain.ReasonSurfaced, Since: s.Clock.Now(), Seq: s.Op(s.Last.OpID).Seq},
			} {
				if _, err := store.AddReason(s.Ctx(), q, row); err != nil {
					return err
				}
			}
			return nil
		}))
		for i := 5; i < 9; i++ {
			s.Add(fmt.Sprintf("unaccepted %d", i))
		}
		nine := s.Add("backlog", testutil.Bottom())
		s.At(testutil.DefaultNow)
		return s, []domain.TaskID{a, b, c, d, nine}
	}
	t.Run("success and sequence order", func(t *testing.T) {
		s, ids := fixture(t)
		c, d, count := s.Task(ids[2]), s.Task(ids[3]), s.OpCount()
		stack := s.Stack("owner", store.StackUndo)
		// Reverse request order: A§3.2 requires new-sequence order.
		in := service.AcceptNewIn{Items: []service.AcceptItem{{ID: ids[1], Expect: s.Expect(ids[1])}, {ID: ids[0], Expect: s.Expect(ids[0])}}}
		r := s.Must(s.Svc.AcceptNew(s.Ctx(), s.Meta(), in))
		requireBacklog(t, s, ids[4], ids[0], ids[1])
		for _, id := range ids[:2] {
			requirePlaced(t, s, id, 2)
		}
		if len(r.Tasks) != 2 || r.Tasks[0].ID != ids[0] || r.Tasks[1].ID != ids[1] || s.OpCount() != count+1 ||
			s.Op(r.OpID).Kind != domain.OpTaskAcceptNew || !s.Op(r.OpID).Revertible ||
			!slices.Equal(s.Stack("owner", store.StackUndo), append(stack, r.OpID)) ||
			!reflect.DeepEqual(s.Task(ids[2]), c) || !reflect.DeepEqual(s.Task(ids[3]), d) {
			t.Fatalf("bulk result/state = %+v", r)
		}
		if got := s.EventKeys(r.OpID); !slices.Equal(got, []string{"task:move 1", "triage:remove 1 new", "task:update 1", "task:move 2", "triage:remove 2 new", "task:update 2"}) {
			t.Fatalf("bulk events = %v", got)
		}
	})
	for _, variant := range []string{"mixed reasons", "surfaced", "stale", "empty", "duplicate"} {
		t.Run(variant, func(t *testing.T) {
			s, ids := fixture(t)
			before := []domain.TaskView{s.Task(ids[0]), s.Task(ids[1]), s.Task(ids[2]), s.Task(ids[3])}
			count, stack := s.OpCount(), s.Stack("owner", store.StackUndo)
			items := []service.AcceptItem{{ID: ids[0], Expect: s.Expect(ids[0])}, {ID: ids[1], Expect: s.Expect(ids[1])}}
			code, badID := domain.CodeValidation, domain.TaskID(0)
			switch variant {
			case "mixed reasons", "surfaced":
				badID = ids[2]
				if variant == "surfaced" {
					badID = ids[3]
				}
				items[1] = service.AcceptItem{ID: badID, Expect: s.Expect(badID)}
				code = domain.CodeConflict
			case "stale":
				items[1].Expect.Version++
				code = domain.CodePrecondition
			case "empty":
				items = nil
			case "duplicate":
				items[1] = items[0]
			}
			_, err := s.Svc.AcceptNew(s.Ctx(), s.Meta(), service.AcceptNewIn{Items: items})
			testutil.RequireCode(t, err, code)
			if badID != 0 && !strings.Contains(err.Error(), fmt.Sprintf("#%d", badID)) {
				t.Fatalf("conflict does not name item: %v", err)
			}
			for i, v := range before {
				if !reflect.DeepEqual(s.Task(ids[i]), v) {
					t.Fatal("failed batch changed a task")
				}
			}
			if s.OpCount() != count || !slices.Equal(s.Stack("owner", store.StackUndo), stack) {
				t.Fatal("failed batch wrote an op or stack")
			}
			requireBacklog(t, s, ids[4])
		})
	}
}

func TestTRI22(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("backlog", testutil.Bottom())
	first := s.Must(s.Svc.MarkManual(s.Ctx(), s.MetaWith("manual-K1-0123456", `{}`), service.MarkManualIn{ID: id, Expect: s.Expect(id)}))
	if s.Task(id).Version != 2 || !slices.Equal(s.Reasons(id), []domain.TriageReason{domain.ReasonManual}) ||
		!slices.Equal(s.EventKeys(first.OpID), []string{"triage:add 1 manual"}) {
		t.Fatalf("first manual = %+v", first)
	}
	before, count, stack := s.Task(id), s.OpCount(), s.Stack("owner", store.StackUndo)
	m := s.MetaWith("manual-K2-0123456", `{}`)
	in := service.MarkManualIn{ID: id, Expect: s.Expect(id)}
	second := s.Must(s.Svc.MarkManual(s.Ctx(), m, in))
	row, err := store.GetIdem(s.Ctx(), s.Store.Reader, m.Actor.Key(), m.Idem.Key)
	if err != nil || row.OpID.Valid || row.Status != 200 {
		t.Fatalf("idempotency = %+v, %v", row, err)
	}
	replay := s.Must(s.Svc.MarkManual(s.Ctx(), m, in))
	if !second.NoChange || !second.OpID.IsZero() || len(second.Effects) != 0 || !replay.Replayed || !replay.NoChange ||
		!replay.OpID.IsZero() || len(replay.Effects) != 0 || s.OpCount() != count ||
		!reflect.DeepEqual(s.Task(id), before) || !slices.Equal(s.Stack("owner", store.StackUndo), stack) {
		t.Fatalf("no-change/replay = %+v / %+v", second, replay)
	}
}

func TestIDM08(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("new")
	m := s.MetaWith("place-key-0123456", `{"how":"keep"}`)
	in := service.PlaceIn{ID: id, Expect: s.Expect(id), How: domain.PlaceKeep}
	first := s.Must(s.Svc.Place(s.Ctx(), m, in))
	s.MarkManual(id)
	count := s.OpCount()
	replay := s.Must(s.Svc.Place(s.Ctx(), m, in))
	if !replay.Replayed || replay.OpID != first.OpID || replay.Task.Version != 3 ||
		!slices.Equal(replay.Effects, first.Effects) || s.OpCount() != count ||
		!slices.Equal(s.Reasons(id), []domain.TriageReason{domain.ReasonManual}) {
		t.Fatalf("stale replay = %+v", replay)
	}
}

func TestSCP03(t *testing.T) {
	for _, actor := range []domain.Actor{testutil.TokenActor("t", domain.ScopeCapture|domain.ScopeWrite), testutil.AI()} {
		t.Run(actor.Key()+string(actor.Channel), func(t *testing.T) {
			s := testutil.NewScenario(t)
			token := s.As(actor)
			_, err := s.Svc.AddTask(s.Ctx(), token.Meta(), service.AddTaskIn{Title: "x", Place: &service.AddPlacement{How: "top"}})
			testutil.RequireCode(t, err, domain.CodeForbidden)
			if s.OpCount() != 0 {
				t.Fatal("forbidden creation wrote an op")
			}
			r := token.Must(s.Svc.AddTask(s.Ctx(), token.Meta(), service.AddTaskIn{Title: "x"}))
			if !r.Created || !slices.Equal(s.Reasons(r.Task.ID), []domain.TriageReason{domain.ReasonNew}) {
				t.Fatalf("token capture = %+v", r)
			}
			id := s.Add("x", testutil.Top())
			requirePlaced(t, s, id, 1)
		})
	}
}

func TestPlaceValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		how    domain.PlaceHow
		ref    domain.TaskID
		expect string
		code   domain.Code
	}{
		{"unknown", "bogus", 0, "valid", domain.CodeValidation},
		{"empty", "", 0, "valid", domain.CodeValidation},
		{"before missing ref", domain.PlaceBefore, 0, "valid", domain.CodeValidation},
		{"after missing ref", domain.PlaceAfter, 0, "valid", domain.CodeValidation},
		{"before self", domain.PlaceBefore, 1, "valid", domain.CodeValidation},
		{"after self", domain.PlaceAfter, 1, "valid", domain.CodeValidation},
		{"missing expect", domain.PlaceKeep, 0, "missing", domain.CodePreconditionReq},
		{"stale expect", domain.PlaceTop, 0, "stale", domain.CodePrecondition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testutil.NewScenario(t)
			id := s.Add("new")
			exp := s.Expect(id)
			if tc.expect == "missing" {
				exp = domain.Expect{}
			}
			if tc.expect == "stale" {
				exp.Version++
			}
			before := s.Task(id)
			_, err := s.Svc.Place(s.Ctx(), s.Meta(), service.PlaceIn{ID: id, Expect: exp, How: tc.how, Ref: tc.ref})
			testutil.RequireCode(t, err, tc.code)
			if s.OpCount() != 1 || !reflect.DeepEqual(s.Task(id), before) {
				t.Fatal("invalid placement changed state")
			}
		})
	}
}

func TestRankRenormalization(t *testing.T) {
	s := testutil.NewScenario(t)
	a, b := s.Add("a", testutil.Bottom()), s.Add("b", testutil.Bottom())
	beforeB := s.Task(b)
	var ids []domain.TaskID
	for i := 0; i < 20; i++ {
		ids = append(ids, s.Add(fmt.Sprintf("t%d", i), testutil.After(a)))
		if len(s.Events(s.Last.OpID)) != 2 {
			t.Fatalf("renormalization recorded events: %+v", s.Events(s.Last.OpID))
		}
	}
	slices.Reverse(ids)
	want := append(append([]domain.TaskID{a}, ids...), b)
	requireBacklog(t, s, want...)
	if v := s.Task(b); v.Version != 1 || !v.UpdatedAt.Equal(beforeB.UpdatedAt) || v.Rank != 18*service.Gap {
		t.Fatalf("renormalized b = %+v", v)
	}
	if s.OpCount() != 22 {
		t.Fatalf("ops = %d", s.OpCount())
	}
	for i := 1; i < len(want); i++ {
		if s.Task(want[i-1]).Rank >= s.Task(want[i]).Rank {
			t.Fatal("ranks are not strictly increasing")
		}
	}
}

func TestAcceptNewSingleReplay(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("new")
	m := s.MetaWith("accept-key-01234", `{}`)
	in := service.AcceptNewIn{Items: []service.AcceptItem{{ID: id, Expect: s.Expect(id)}}}
	first := s.Must(s.Svc.AcceptNew(s.Ctx(), m, in))
	if len(first.Tasks) != 1 || first.Tasks[0].ID != id {
		t.Fatalf("single bulk result = %+v", first)
	}
	replay := s.Must(s.Svc.AcceptNew(s.Ctx(), m, in))
	if !replay.Replayed || len(replay.Tasks) != 1 || replay.Tasks[0].ID != id || replay.OpID != first.OpID {
		t.Fatalf("single bulk replay = %+v", replay)
	}
}

func TestPlaceDueAcknowledgement(t *testing.T) {
	for _, tc := range []struct {
		due   string
		phase domain.DuePhase
	}{{"10-29", domain.DueNone}, {"10-28", domain.DueDue}, {"10-28 09:30", domain.DueOverdue}} {
		t.Run(tc.due, func(t *testing.T) {
			s := testutil.NewScenario(t)
			id := s.At("10-28 09:00").Add("due", testutil.Due(tc.due))
			s.At(testutil.DefaultNow).Reconcile()
			version := s.Task(id).Version
			s.Place(id, domain.PlaceKeep)
			requirePlaced(t, s, id, version+1)
			if s.Task(id).DueAck != tc.phase {
				t.Fatalf("due ack = %s", s.Task(id).DueAck)
			}
			updates := 0
			for _, e := range s.Events(s.Last.OpID) {
				if e.Entity == domain.EntTask && e.Action == domain.ActUpdate {
					updates++
					var cols map[string]any
					if err := json.Unmarshal(e.After, &cols); err != nil {
						t.Fatal(err)
					}
					if _, ok := cols["rank"]; ok {
						t.Fatal("placement update contains rank")
					}
				}
			}
			if updates != 1 {
				t.Fatalf("updates = %d", updates)
			}
		})
	}
}

func TestPositionalAddOwnerOnly(t *testing.T) {
	for _, actor := range []domain.Actor{{Kind: domain.ActorSystem, Scopes: domain.AllScopes}, testutil.TokenActor("all", domain.AllScopes),
		{Kind: domain.ActorOwner, Scopes: domain.ScopeCapture}, {Kind: domain.ActorOwner, Scopes: domain.ScopeWrite}} {
		t.Run(actor.Key()+fmt.Sprint(actor.Scopes), func(t *testing.T) {
			s := testutil.NewScenario(t)
			_, err := s.Svc.AddTask(s.Ctx(), s.As(actor).Meta(), service.AddTaskIn{Title: "x", Place: &service.AddPlacement{How: "top"}})
			testutil.RequireCode(t, err, domain.CodeForbidden)
			if s.OpCount() != 0 {
				t.Fatal("forbidden placement wrote an op")
			}
		})
	}
}

func TestPlaceAndAcceptScopes(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("new")
	capture := s.As(testutil.TokenActor("capture", domain.ScopeCapture))
	_, err := s.Svc.Place(s.Ctx(), capture.Meta(), service.PlaceIn{ID: id, Expect: s.Expect(id), How: domain.PlaceKeep})
	testutil.RequireCode(t, err, domain.CodeForbidden)
	_, err = s.Svc.AcceptNew(s.Ctx(), capture.Meta(), service.AcceptNewIn{Items: []service.AcceptItem{{ID: id, Expect: s.Expect(id)}}})
	testutil.RequireCode(t, err, domain.CodeForbidden)
	write := s.As(testutil.TokenActor("writer", domain.ScopeWrite))
	r := write.Must(s.Svc.Place(s.Ctx(), write.Meta(), service.PlaceIn{ID: id, Expect: s.Expect(id), How: domain.PlaceKeep}))
	if r.Task.Version != 2 || s.Op(r.OpID).Scopes != domain.ScopeWrite {
		t.Fatalf("write placement = %+v", r)
	}
	id2 := s.Add("accept")
	r = write.Must(s.Svc.AcceptNew(s.Ctx(), write.Meta(), service.AcceptNewIn{Items: []service.AcceptItem{{ID: id2, Expect: s.Expect(id2)}}}))
	if len(r.Tasks) != 1 || r.Tasks[0].Version != 2 || s.Op(r.OpID).Scopes != domain.ScopeWrite {
		t.Fatalf("write acceptance = %+v", r)
	}
}

func TestPlaceClearsDefer(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("deferred", testutil.Bottom())
	// This helper-level fixture exercises D5 before Task 27 supplies Defer.
	s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), []domain.EntityRef{{Entity: domain.EntTask, ID: int64(id)}}, func(q store.Q) error {
		task, err := store.GetTask(s.Ctx(), q, id)
		if err != nil {
			return err
		}
		task.DeferDate = sql.Null[civil.Date]{V: testutil.D("10-29"), Valid: true}
		task.DeferMin = sql.Null[civil.Minute]{V: 600, Valid: true}
		return store.UpdateTask(s.Ctx(), q, task)
	}))
	rank := s.Task(id).Rank
	s.Place(id, domain.PlaceKeep)
	requirePlaced(t, s, id, 3)
	requireBacklog(t, s, id)
	evs := s.Events(s.Last.OpID)
	if s.Task(id).Rank != rank || len(evs) != 1 || evs[0].Action != domain.ActUpdate {
		t.Fatalf("deferred keep = %+v", evs)
	}
	var before, after map[string]any
	if err := json.Unmarshal(evs[0].Before, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(evs[0].After, &after); err != nil {
		t.Fatal(err)
	}
	if before["defer_date"] != "2026-10-29" || before["defer_min"] != float64(600) {
		t.Fatalf("before = %v", before)
	}
	for _, key := range []string{"defer_date", "defer_min"} {
		if value, ok := after[key]; !ok || value != nil {
			t.Fatalf("after = %v", after)
		}
	}
}

func TestRankLimitsAndMoveRenormalization(t *testing.T) {
	for _, tc := range []struct {
		name          string
		ranks         [3]int64
		how           domain.PlaceHow
		ref           domain.TaskID
		want          []domain.TaskID
		before, after string
	}{
		{"top limit", [3]int64{-1 << 60, 0, service.Gap}, domain.PlaceTop, 0, []domain.TaskID{3, 1, 2}, `{"after_id":2}`, `{"after_id":null}`},
		{"bottom limit", [3]int64{0, service.Gap, 1 << 60}, domain.PlaceBottom, 0, []domain.TaskID{1, 2, 3}, `{"after_id":2}`, `{"after_id":2}`},
		{"before minimum", [3]int64{math.MinInt64, 0, service.Gap}, domain.PlaceBefore, 1, []domain.TaskID{3, 1, 2}, `{"after_id":2}`, `{"after_id":null}`},
		{"after maximum", [3]int64{0, math.MaxInt64, 1}, domain.PlaceAfter, 2, []domain.TaskID{1, 2, 3}, `{"after_id":1}`, `{"after_id":2}`},
		{"before tight gap", [3]int64{10, 11, 30}, domain.PlaceBefore, 2, []domain.TaskID{1, 3, 2}, `{"after_id":2}`, `{"after_id":1}`},
		{"after tight gap", [3]int64{10, 11, 30}, domain.PlaceAfter, 1, []domain.TaskID{1, 3, 2}, `{"after_id":2}`, `{"after_id":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testutil.NewScenario(t)
			for _, title := range []string{"a", "b", "moving"} {
				s.Add(title, testutil.Bottom())
			}
			// Boundary ranks are numeric helper fixtures, not past-dated catalog state.
			s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), nil, func(q store.Q) error {
				for i, rank := range tc.ranks {
					if err := store.SetRank(s.Ctx(), q, domain.TaskID(i+1), rank); err != nil {
						return err
					}
				}
				return nil
			}))
			s.Place(3, tc.how, tc.ref)
			requireBacklog(t, s, tc.want...)
			requirePlaced(t, s, 3, 2)
			evs := s.Events(s.Last.OpID)
			if len(evs) != 2 || evs[0].Action != domain.ActMove || string(evs[0].Before) != tc.before || string(evs[0].After) != tc.after {
				t.Fatalf("renormalized move = %+v", evs)
			}
			for _, id := range []domain.TaskID{1, 2} {
				if v := s.Task(id); v.Version != 1 || v.Rank <= 0 || v.Rank > 3*service.Gap || v.Rank%service.Gap != 0 {
					t.Fatalf("untouched task = %+v", v)
				}
			}
		})
	}
}

func TestRankGlobalOrderAndEdges(t *testing.T) {
	s := testutil.NewScenario(t)
	a := s.Add("a", testutil.Top())
	b := s.Add("b", testutil.After(a))
	c := s.Add("c", testutil.Before(a))
	if s.Task(b).Rank != s.Task(a).Rank+service.Gap || s.Task(c).Rank != s.Task(a).Rank-service.Gap {
		t.Fatal("no-neighbor spacing differs from Gap")
	}
	// Closed and tombstoned rows retain positions in the global rank order.
	s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), nil, func(q store.Q) error {
		closed, err := store.GetTask(s.Ctx(), q, b)
		if err != nil {
			return err
		}
		closed.Status = domain.StatusDropped
		if err := store.UpdateTask(s.Ctx(), q, closed); err != nil {
			return err
		}
		deleted, err := store.GetTask(s.Ctx(), q, c)
		if err != nil {
			return err
		}
		deleted.DeletedAt = sql.Null[time.Time]{V: s.Clock.Now(), Valid: true}
		return store.UpdateTask(s.Ctx(), q, deleted)
	}))
	d := s.Add("bottom", testutil.Bottom())
	e := s.Add("top", testutil.Top())
	f := s.Add("after a", testutil.After(a))
	g := s.Add("before a", testutil.Before(a))
	if s.Task(d).Rank != 3*service.Gap || s.Task(e).Rank != -service.Gap ||
		s.Task(f).Rank != service.Gap+service.Gap/2 || s.Task(g).Rank != service.Gap/2 {
		t.Fatal("rank helpers excluded closed or deleted neighbors")
	}
	requireBacklog(t, s, e, g, a, f, d)
}
