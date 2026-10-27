package service_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/store"
	"taskronin/internal/testutil"
)

func requireFocus(t *testing.T, s *testutil.Scenario, task domain.TaskID) {
	t.Helper()
	f := s.Focus()
	if f.TaskID.Valid != (task != 0) || f.TaskID.V != task || f.Date.Valid != (task != 0) ||
		(task != 0 && f.Date.V != testutil.Zone.DateOf(s.Clock.Now())) {
		t.Fatalf("focus = %+v, want task %d today", f, task)
	}
}

func requireInterruption(t *testing.T, s *testutil.Scenario, from domain.TaskID, block domain.BlockID, to domain.TaskID) {
	t.Helper()
	rows := s.Interruptions(testutil.Zone.DateOf(s.Clock.Now()).String())
	if len(rows) == 0 {
		t.Fatal("missing interruption")
	}
	row := rows[len(rows)-1]
	if row.FromTaskID != (sql.Null[domain.TaskID]{V: from, Valid: true}) ||
		row.FromBlockID != (sql.Null[domain.BlockID]{V: block, Valid: block != 0}) || row.ToTaskID != to ||
		!row.At.Equal(s.Clock.Now().Truncate(time.Millisecond)) || row.Date != testutil.Zone.DateOf(s.Clock.Now()) ||
		row.OpID != s.Last.OpID || row.DeletedAt.Valid {
		t.Fatalf("interruption = %+v", row)
	}
	var found bool
	for _, ev := range s.Events(s.Last.OpID) {
		if ev.Entity != domain.EntInterruption {
			continue
		}
		found = true
		if ev.Action != domain.ActInsert || ev.EntityID != int64(row.ID) || ev.TaskID != (sql.Null[domain.TaskID]{V: to, Valid: true}) || ev.Derived || len(ev.Before) != 0 {
			t.Fatalf("interruption event = %+v", ev)
		}
		want := map[string]any{"at": float64(row.At.UnixMilli()), "date": row.Date.String(),
			"from_task_id": float64(from), "from_block_id": nil, "to_task_id": float64(to), "op_id": row.OpID.String(), "deleted_at": nil}
		if block != 0 {
			want["from_block_id"] = float64(block)
		}
		var got map[string]any
		if err := json.Unmarshal(ev.After, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("interruption image = %v, want %v", got, want)
		}
	}
	if !found || !slices.ContainsFunc(s.Last.Effects, func(e service.Effect) bool {
		return e.Kind == service.EffInterrupted && e.TaskID == to && e.From == fmt.Sprint(from)
	}) {
		t.Fatalf("missing interruption event/effect: %+v", s.Last)
	}
}

func interruptionFixture(t *testing.T) (*testutil.Scenario, domain.TaskID, domain.BlockID, domain.TaskID) {
	t.Helper()
	s := testutil.NewScenario(t).At("10-28 08:00")
	t42 := s.Add("Set up keycloak")
	b1 := s.Block(t42, "10-28 09:00-11:00")
	s.At("10-28 09:05").SetFocus(t42, false)
	s.At("10-28 10:20")
	t60 := s.Add("Restart stuck pg replica", testutil.Now())
	return s, t42, b1, t60
}

func checkAddNowInterruption(t *testing.T) {
	t.Helper()
	s, from, block, to := interruptionFixture(t)
	requireFocus(t, s, to)
	requirePlaced(t, s, to, 1)
	requireBacklog(t, s, to, from)
	requireBlockState(t, s, block, "{planned, –, 0, –}")
	requireSlips(t, s, from, 0, 0)
	if s.Task(to).Status != domain.StatusOpen || len(s.Interruptions("10-28")) != 1 || s.BlockView(block).Version != 1 {
		t.Fatal("add now changed block or omitted interruption")
	}
	requireInterruption(t, s, from, block, to)
	keys := s.EventKeys(s.Last.OpID)
	for _, key := range []string{fmt.Sprintf("task:insert %d", to), fmt.Sprintf("task:update %d", to), "focus:set", "interruption:insert 1"} {
		if !slices.Contains(keys, key) {
			t.Fatalf("events = %v, missing %s", keys, key)
		}
	}
	if len(keys) != 4 {
		t.Fatalf("events = %v", keys)
	}
}

func TestTRI07(t *testing.T) { checkAddNowInterruption(t) }
func TestINT01(t *testing.T) { checkAddNowInterruption(t) }

func TestFOC01(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	before, focus := s.Task(id), s.Focus()
	s.SetFocus(id, false)
	requireFocus(t, s, id)
	requireEventKeys(t, s, s.Last.OpID, "focus:set")
	f := s.Focus()
	if f.Version != focus.Version+1 || !f.SetAt.Valid || !f.SetAt.V.Equal(s.Clock.Now()) || !reflect.DeepEqual(s.Task(id), before) {
		t.Fatalf("focus changed task or has wrong metadata: %+v", f)
	}
	ev := s.Events(s.Last.OpID)[0]
	if string(ev.Before) != `{"date":null,"task_id":null}` || string(ev.After) != `{"date":"2026-10-28","task_id":1}` ||
		ev.TaskID.V != id || !ev.TaskID.Valid || ev.EntityID != 1 {
		t.Fatalf("focus event = %+v", ev)
	}
	op := s.Op(s.Last.OpID)
	if op.Kind != domain.OpFocusSet || !op.Revertible || op.Scopes != domain.ScopeWrite ||
		!slices.Contains(s.Stack(s.Actor.Key(), store.StackUndo), op.ID) {
		t.Fatalf("focus op = %+v", op)
	}
}

func TestFOC02(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	s.SetFocus(id, false)
	before := s.Focus()
	s.At("10-28 10:01").SetFocus(0, true)
	requireFocus(t, s, 0)
	requireEventKeys(t, s, s.Last.OpID, "focus:set")
	ev := s.Events(s.Last.OpID)[0]
	if string(ev.Before) != `{"date":"2026-10-28","task_id":1}` || string(ev.After) != `{"date":null,"task_id":null}` ||
		ev.TaskID.V != id || !ev.TaskID.Valid || s.Focus().Version != before.Version+1 ||
		!s.Focus().SetAt.Valid || !s.Focus().SetAt.V.Equal(s.Clock.Now()) || len(s.Interruptions("10-28")) != 0 {
		t.Fatalf("clear focus = %+v, event = %+v", s.Focus(), ev)
	}
}

func TestINT02(t *testing.T) {
	s := testutil.NewScenario(t).At("10-28 08:00")
	from := s.Add("A")
	b := s.Block(from, "09:00-11:00")
	s.At("10-28 10:20")
	to := s.Add("x", testutil.Now())
	requireFocus(t, s, to)
	requireInterruption(t, s, from, b, to)
}

func TestINT03(t *testing.T) {
	s := testutil.NewScenario(t)
	from := s.Add("A")
	s.SetFocus(from, false)
	before := s.Task(from)
	to := s.Add("x", testutil.Now())
	requireInterruption(t, s, from, 0, to)
	requireFocus(t, s, to)
	if !reflect.DeepEqual(s.Task(from), before) {
		t.Fatal("interruption changed source task")
	}
}

func TestINT04(t *testing.T) {
	s := testutil.NewScenario(t)
	to := s.Add("x", testutil.Now())
	requireFocus(t, s, to)
	requirePlaced(t, s, to, 1)
	if len(s.Interruptions("10-28")) != 0 {
		t.Fatal("interruption without a source")
	}
	requireEventKeys(t, s, s.Last.OpID, "task:insert 1", "task:update 1", "focus:set")
}

func TestINT05(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			s, from, b, _ := interruptionFixture(t)
			s.SetFocus(from, explicit)
			requireFocus(t, s, from)
			requireEventKeys(t, s, s.Last.OpID, "focus:set")
			requireBlockState(t, s, b, "{planned, –, 0, –}")
			if len(s.Interruptions("10-28")) != 1 {
				t.Fatal("returning to block task recorded an interruption")
			}
		})
	}
}

func TestINT07(t *testing.T) {
	for _, work := range []bool{false, true} {
		t.Run(fmt.Sprint(work), func(t *testing.T) {
			s, from, b, _ := interruptionFixture(t)
			if work {
				s.Log(from, "Configured keycloak", "10-28")
			}
			s.At("10-29 00:00").Reconcile()
			requireFocus(t, s, 0)
			if work {
				requireBlockState(t, s, b, "{worked, auto, 0, –}")
				requireDueReasons(t, s, from, domain.ReasonContinue)
				requireSlips(t, s, from, 0, 0)
			} else {
				requireBlockState(t, s, b, "{missed, auto, 1, –}")
				requireDueReasons(t, s, from, domain.ReasonSlipped)
				requireSlips(t, s, from, 1, 1)
			}
			op := latestOp(t, s)
			keys := s.EventKeys(op.ID)
			if op.Kind != domain.OpSystemReconcile || keys[len(keys)-1] != "focus:set" || len(s.Interruptions("10-28")) != 1 {
				t.Fatalf("midnight events = %v", keys)
			}
		})
	}
}

func TestINT10(t *testing.T) {
	s, from, b, _ := interruptionFixture(t)
	to := s.Add("third task")
	s.SetFocus(to, false)
	requireFocus(t, s, to)
	requireInterruption(t, s, from, b, to)
	if len(s.Interruptions("10-28")) != 2 {
		t.Fatal("missing third-task interruption")
	}
	// The same focus still records the current block's interruption, without
	// changing focus's version or set_at.
	before := s.Focus()
	s.At("10-28 10:21").SetFocus(to, false)
	requireInterruption(t, s, from, b, to)
	requireEventKeys(t, s, s.Last.OpID, "interruption:insert 3")
	if s.Last.NoChange || !reflect.DeepEqual(s.Focus(), before) {
		t.Fatal("interruption-only operation was treated as a focus change/no-op")
	}
}

func TestINT11(t *testing.T) {
	// The catalog's two calls are alternatives from the same Given focus.
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			s := testutil.NewScenario(t)
			from, to := s.Add("A"), s.Add("B")
			s.SetFocus(from, false)
			s.SetFocus(to, explicit)
			requireFocus(t, s, to)
			if explicit {
				requireInterruption(t, s, from, 0, to)
			} else if len(s.Interruptions("10-28")) != 0 {
				t.Fatal("implicit focus switch recorded an interruption")
			}
		})
	}
}

func TestINT12(t *testing.T) {
	s := testutil.NewScenario(t).At("10-28 08:00")
	previous, from := s.Add("previous focus"), s.Add("block task")
	b := s.Block(from, "09:00-11:00")
	s.SetFocus(previous, false)
	s.At("10-28 10:20")
	to := s.Add("x", testutil.Now())
	requireFocus(t, s, to)
	requireInterruption(t, s, from, b, to)
}

func TestINT13(t *testing.T) {
	s := testutil.NewScenario(t)
	to := s.Add("A")
	s.SetFocus(to, true)
	requireFocus(t, s, to)
	requireEventKeys(t, s, s.Last.OpID, "focus:set")
	if len(s.Interruptions("10-28")) != 0 {
		t.Fatal("explicit interruption without a source")
	}
}

func TestSetFocusNoChange(t *testing.T) {
	for _, focused := range []bool{false, true} {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprintf("focused=%t/interrupt=%t", focused, explicit), func(t *testing.T) {
				s := testutil.NewScenario(t)
				var id domain.TaskID
				if focused {
					id = s.Add("A")
					s.SetFocus(id, false)
				}
				before, count, stack := s.Focus(), s.OpCount(), s.Stack(s.Actor.Key(), store.StackUndo)
				s.At("10-28 11:00").SetFocus(id, explicit)
				if !s.Last.NoChange || !s.Last.OpID.IsZero() || len(s.Last.Effects) != 0 || s.OpCount() != count ||
					!reflect.DeepEqual(s.Focus(), before) || !slices.Equal(s.Stack(s.Actor.Key(), store.StackUndo), stack) {
					t.Fatalf("no-change result = %+v", s.Last)
				}
			})
		}
	}
}

func checkFocusMidnight(t *testing.T, setAt, beforeMidnight, midnight string) {
	t.Helper()
	s := testutil.NewScenario(t).At(setAt)
	id := s.Add("A")
	s.SetFocus(id, false)
	before, count := s.Focus(), s.OpCount()
	s.At(beforeMidnight)
	// A valid focus must leave the writer free: the need callback stays false.
	conn, err := s.Store.Writer.Conn(s.Ctx())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(s.Ctx(), time.Second)
	defer cancel()
	err = s.Svc.Reconcile(ctx)
	if closeErr := conn.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil || !reflect.DeepEqual(s.Focus(), before) || s.OpCount() != count {
		t.Fatalf("focus cleared before midnight or acquired writer: %v", err)
	}
	s.At(midnight).Reconcile()
	requireFocus(t, s, 0)
	op := latestOp(t, s)
	requireEventKeys(t, s, op.ID, "focus:set")
	if op.Kind != domain.OpSystemReconcile || op.Actor != "system" || op.Revertible ||
		s.OpCount() != count+1 || s.Focus().Version != before.Version+1 || !s.Focus().SetAt.V.Equal(s.Clock.Now()) {
		t.Fatalf("focus reconcile = %+v / %+v", op, s.Focus())
	}
	s.Reconcile()
	if s.OpCount() != count+1 {
		t.Fatal("repeated reconcile wrote an op")
	}
}

func TestREC03(t *testing.T) {
	checkFocusMidnight(t, "10-28 22:00", "10-28 23:59", "10-29 00:00")
}

func TestREC04(t *testing.T) {
	checkFocusMidnight(t, "2026-11-02T06:30Z", "2026-11-02T06:59Z", "2026-11-02T07:00Z")
}

func TestINTCurrentBlockInstants(t *testing.T) {
	for _, tc := range []struct {
		name, created, span, now string
		current                  bool
	}{
		{"before", "10-28 08:00", "10-28 09:00-11:00", "10-28 08:59", false},
		{"start", "10-28 08:00", "10-28 09:00-11:00", "10-28 09:00", true},
		{"end", "10-28 08:00", "10-28 09:00-11:00", "10-28 11:00", false},
		{"24:00", "10-28 08:00", "10-28 23:00-24:00", "10-28 23:59", true},
		{"fold start passed", "11-01 00:00", "11-01 01:30-02:30", "2026-11-01T08:15Z", true},
		{"fold end passed", "11-01 00:00", "11-01 00:30-01:30", "2026-11-01T08:15Z", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testutil.NewScenario(t).At(tc.created)
			from, to := s.Add("block"), s.Add("target")
			b := s.Block(from, tc.span)
			s.At(tc.now).SetFocus(to, false)
			requireFocus(t, s, to)
			if tc.current {
				requireInterruption(t, s, from, b, to)
			} else if len(s.Interruptions(testutil.Zone.DateOf(s.Clock.Now()).String())) != 0 {
				t.Fatal("non-current block caused interruption")
			}
		})
	}
}

func focusIneligibleFixture(t *testing.T, s *testutil.Scenario, state string) domain.TaskID {
	t.Helper()
	id := s.Add("ineligible", testutil.Top())
	// AIDEV-NOTE: F14 fixture shortcut: Defer/Complete/Drop/Delete arrive in
	// Tasks 27-29. Use store rows for the Given; exercise real focus ops below.
	s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), []domain.EntityRef{{Entity: domain.EntTask, ID: int64(id)}}, func(q store.Q) error {
		row, err := store.GetTask(s.Ctx(), q, id)
		if err != nil {
			return err
		}
		switch state {
		case "deleted":
			row.DeletedAt = sql.Null[time.Time]{V: s.Clock.Now(), Valid: true}
		case "deferred":
			row.DeferDate = sql.Null[civil.Date]{V: testutil.D("10-29"), Valid: true}
		case "dropped":
			row.Status = domain.StatusDropped
		case "done":
			row.Status = domain.StatusDone
			row.CompletedAt = sql.Null[time.Time]{V: s.Clock.Now(), Valid: true}
			row.CompletedDate = sql.Null[civil.Date]{V: testutil.D("10-28"), Valid: true}
			e := domain.LogEntry{TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}, Kind: domain.EntryComplete,
				Title: row.Title, WorkDate: row.CompletedDate.V, CreatedAt: s.Clock.Now(), UpdatedAt: s.Clock.Now()}
			if err := store.InsertEntry(s.Ctx(), q, &e); err != nil {
				return err
			}
		}
		return store.UpdateTask(s.Ctx(), q, row)
	}))
	return id
}

func TestSetFocusEligibility(t *testing.T) {
	for _, state := range []string{"missing", "deleted", "deferred", "dropped", "done"} {
		t.Run(state, func(t *testing.T) {
			s := testutil.NewScenario(t)
			id, code := domain.TaskID(999), domain.CodeNotFound
			if state != "missing" {
				id, code = focusIneligibleFixture(t, s, state), domain.CodeConflict
				if state == "deleted" {
					code = domain.CodeDeleted
				}
			}
			count, before := s.OpCount(), s.Focus()
			_, err := s.Svc.SetFocus(s.Ctx(), s.Meta(), service.SetFocusIn{TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}, Interrupt: true})
			testutil.RequireCode(t, err, code)
			if s.OpCount() != count || !reflect.DeepEqual(s.Focus(), before) || len(s.Interruptions("10-28")) != 0 {
				t.Fatal("rejected focus changed state")
			}
		})
	}
}

func TestSetFocusReconcileIneligible(t *testing.T) {
	for _, state := range []string{"deleted", "deferred", "dropped", "done"} {
		t.Run(state, func(t *testing.T) {
			s := testutil.NewScenario(t)
			id := focusIneligibleFixture(t, s, state)
			// AIDEV-NOTE: F14 fixture shortcut: invalid focus cannot survive a
			// real runner's I6 check. Seed it directly to test reconcile repair.
			f := domain.Focus{TaskID: sql.Null[domain.TaskID]{V: id, Valid: true},
				Date: sql.Null[civil.Date]{V: testutil.D("10-28"), Valid: true}, SetAt: sql.Null[time.Time]{V: s.Clock.Now(), Valid: true}}
			if err := store.PutFocus(s.Ctx(), s.Store.Writer, f); err != nil {
				t.Fatal(err)
			}
			count := s.OpCount()
			s.Reconcile()
			requireFocus(t, s, 0)
			requireEventKeys(t, s, latestOp(t, s).ID, "focus:set")
			if s.OpCount() != count+1 {
				t.Fatal("invalid focus did not trigger reconcile")
			}
		})
	}
}

func TestSetFocusAndAddNowReplayAndScopes(t *testing.T) {
	for _, add := range []bool{false, true} {
		t.Run(fmt.Sprintf("add=%t", add), func(t *testing.T) {
			s := testutil.NewScenario(t)
			from, to := s.Add("from"), s.Add("to")
			s.SetFocus(from, false)
			call := func(m service.Meta) (*service.OpResult, error) {
				if add {
					return s.Svc.AddTask(s.Ctx(), m, service.AddTaskIn{Title: "x", Place: &service.AddPlacement{How: "now"}})
				}
				return s.Svc.SetFocus(s.Ctx(), m, service.SetFocusIn{TaskID: sql.Null[domain.TaskID]{V: to, Valid: true}, Interrupt: true})
			}
			m := s.Meta()
			m.Idem.Key = ""
			_, err := call(m)
			testutil.RequireCode(t, err, domain.CodeIdemRequired)
			m = s.Meta()
			m.Actor = testutil.AI()
			if !add {
				m.Actor.Scopes = domain.ScopeRead | domain.ScopeCapture
			}
			_, err = call(m)
			testutil.RequireCode(t, err, domain.CodeForbidden)
			m = s.Meta()
			if !add {
				m.Actor = testutil.AI() // SetFocus is available to write-scoped tokens.
			}
			first := s.Must(call(m))
			count, before := s.OpCount(), s.Focus()
			second := s.Must(call(m))
			if !second.Replayed || second.OpID != first.OpID || !reflect.DeepEqual(first.Effects, second.Effects) ||
				s.OpCount() != count || !reflect.DeepEqual(s.Focus(), before) || len(s.Interruptions("10-28")) != 1 {
				t.Fatalf("replay = %+v", second)
			}
		})
	}
}
