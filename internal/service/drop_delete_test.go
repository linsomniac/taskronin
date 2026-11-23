package service_test

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/service"
	"github.com/linsomniac/taskronin/internal/store"
	"github.com/linsomniac/taskronin/internal/testutil"
)

func requireTaskOp(t *testing.T, s *testutil.Scenario, id domain.TaskID, kind domain.OpKind, scopes domain.Scopes) {
	t.Helper()
	r := s.Last
	op := s.Op(r.OpID)
	if r.NoChange || r.Created || r.Task == nil || r.Task.ID != id ||
		!slices.Equal(r.Refs, []domain.EntityRef{{Entity: domain.EntTask, ID: int64(id)}}) ||
		op.Kind != kind || op.Scopes != scopes || !op.Revertible || !slices.Contains(s.Stack(s.Actor.Key(), store.StackUndo), op.ID) {
		t.Fatalf("task result = %+v, op = %+v", r, op)
	}
}

func TestTRI16(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	_, err := s.Svc.Drop(s.Ctx(), s.As(testutil.AI()).Meta(), service.TaskRef{ID: id, Expect: s.Expect(id)})
	testutil.RequireCode(t, err, domain.CodeForbidden)
	s.Drop(id)
	requirePlaced(t, s, id, 2)
	if s.Task(id).Status != domain.StatusDropped || s.Task(id).View != domain.ViewDropped {
		t.Fatal("task was not dropped")
	}
	requireTaskOp(t, s, id, domain.OpTaskDrop, domain.ScopeDestroy)
	requireEventKeys(t, s, s.Last.OpID, "task:update 1", "triage:remove 1 new", "task:update 1")
}

func TestTRI21(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A", testutil.Due("10-28"))
	s.Reconcile()
	requireDueReasons(t, s, id, domain.ReasonDue, domain.ReasonNew)
	before := s.Task(id)
	s.Delete(id)
	v := s.Task(id)
	if !v.DeletedAt.Valid || !v.DeletedAt.V.Equal(s.Clock.Now()) || v.View != domain.ViewDeleted || v.Version != before.Version+1 {
		t.Fatalf("deleted task = %+v", v)
	}
	requireNotPlaced(t, s, before)
	requireDueReasons(t, s, id)
	requireTaskOp(t, s, id, domain.OpTaskDelete, domain.ScopeDestroy)
	requireEventKeys(t, s, s.Last.OpID, "task:tombstone 1", "triage:remove 1 due", "triage:remove 1 new")
	deleted := v
	s.As(testutil.AI()).Undelete(id)
	// Read the persisted reason before a later read can reconcile today's due.
	rows, err := store.Reasons(s.Ctx(), s.Store.Reader, id)
	if err != nil || len(rows) != 1 || rows[0].Reason != domain.ReasonManual || !rows[0].Since.Equal(s.Clock.Now()) {
		t.Fatalf("undelete reasons = %+v, %v", rows, err)
	}
	op := latestOp(t, s)
	if rows[0].Seq != op.Seq || op.Kind != domain.OpTaskUndelete || op.Scopes != domain.ScopeWrite || !op.Revertible {
		t.Fatalf("undelete op = %+v, reason = %+v", op, rows)
	}
	task, err := store.GetTask(s.Ctx(), s.Store.Reader, id)
	if err != nil || task.DeletedAt.Valid || task.Status != domain.StatusOpen || task.Version != deleted.Version+1 {
		t.Fatalf("undeleted task = %+v, %v", task, err)
	}
	requireNotPlaced(t, s, deleted)
	requireEventKeys(t, s, op.ID, "task:untombstone 1", "triage:add 1 manual")
	if s.Task(id).View != domain.ViewTriage {
		t.Fatal("undeleted task is not in triage")
	}
}

func TestTRI23(t *testing.T) {
	for _, closeTask := range []struct {
		name  string
		close func(*testutil.Scenario, domain.TaskID)
	}{
		{"done", func(s *testutil.Scenario, id domain.TaskID) { s.Done(id) }},
		{"dropped", (*testutil.Scenario).Drop},
	} {
		t.Run(closeTask.name, func(t *testing.T) {
			s := testutil.NewScenario(t)
			id := s.Add("A")
			closeTask.close(s, id)
			s.Delete(id)
			before := s.Task(id)
			ai := s.As(testutil.AI())
			ai.Undelete(id)
			v := s.Task(id)
			if v.DeletedAt.Valid || v.Status != before.Status || v.CompletedAt != before.CompletedAt || v.CompletedDate != before.CompletedDate || v.Version != before.Version+1 {
				t.Fatalf("restored closed task = %+v", v)
			}
			requireNotPlaced(t, s, before)
			requireDueReasons(t, s, id)
			requireTaskOp(t, ai, id, domain.OpTaskUndelete, domain.ScopeWrite)
			requireEventKeys(t, s, ai.Last.OpID, "task:untombstone 1")
		})
	}
}

func TestTRI29(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A", testutil.DeferTo("11-02"))
	s.Delete(id)
	before := s.Task(id)
	s.Undelete(id)
	v := s.Task(id)
	if v.DeletedAt.Valid || v.DeferDate.Valid || v.DeferMin.Valid || v.View != domain.ViewTriage || v.Version != before.Version+1 {
		t.Fatalf("undeleted deferred task = %+v", v)
	}
	requireNotPlaced(t, s, before)
	requireDueReasons(t, s, id, domain.ReasonManual)
	requireEventKeys(t, s, s.Last.OpID, "task:untombstone 1", "triage:add 1 manual", "task:update 1")
}

func TestBLK08(t *testing.T) {
	s := testutil.NewScenario(t)
	done, dropped, deleted := s.Add("done"), s.Add("dropped"), s.Add("deleted")
	s.Done(done)
	s.Drop(dropped)
	s.Delete(deleted)
	for _, tc := range []struct {
		id   domain.TaskID
		code domain.Code
	}{
		{done, domain.CodeConflict}, {dropped, domain.CodeConflict}, {deleted, domain.CodeDeleted}, {999, domain.CodeNotFound},
	} {
		_, err := s.Svc.Block(s.Ctx(), s.Meta(), service.BlockIn{TaskID: tc.id, Expect: domain.Expect{Gen: s.Svc.Generation(), Version: 2},
			Span: domain.BlockSpan{Date: testutil.D("10-28"), Start: 13 * 60, End: 15 * 60}})
		testutil.RequireCode(t, err, tc.code)
	}
}

func TestBLK34(t *testing.T) {
	for name, closeTask := range map[string]func(*testutil.Scenario, domain.TaskID){
		"drop": (*testutil.Scenario).Drop, "delete": (*testutil.Scenario).Delete,
		"complete": func(s *testutil.Scenario, id domain.TaskID) { s.Done(id) },
	} {
		t.Run(name, func(t *testing.T) {
			s := testutil.NewScenario(t)
			id := s.Add("A")
			b := s.Block(id, "10-28 13:00-15:00")
			s.At("10-28 10:15")
			closeTask(s, id)
			requireBlockState(t, s, b, "{cancelled, –, 0, task_closed}")
			requireSlips(t, s, id, 0, 0)
		})
	}
}

func TestSLP10(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	b := s.Block(id, "10-28 13:00-15:00")
	s.Log(id, "work", "")
	s.At("10-28 14:00").Drop(id)
	requireBlockState(t, s, b, "{worked, auto, 0, –}")
	requireDueReasons(t, s, id)
	requireSlips(t, s, id, 0, 0)
}

func TestSLP11(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	b := s.Block(id, "10-28 13:00-15:00")
	s.At("10-28 14:00").Drop(id)
	requireBlockState(t, s, b, "{missed, auto, 1, –}")
	requireDueReasons(t, s, id)
	requireSlips(t, s, id, 1, 0)
	v := s.BlockView(b)
	if !v.SlippedAt.Valid || !v.SlippedAt.V.Equal(s.Clock.Now()) || !v.SlippedSeq.Valid || v.SlippedSeq.V != s.Op(s.Last.OpID).Seq {
		t.Fatalf("slip metadata = %+v", v)
	}
	requireEventKeys(t, s, s.Last.OpID, "task:update 1", "block:update 1", "task:update 1")
	requireHistoryOp(t, s, id, s.Last.OpID)
}

func TestLIF07(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	_, err := s.Svc.Complete(s.Ctx(), s.Meta(), service.CompleteIn{ID: id, Expect: s.Expect(id), Date: testutil.D("10-29")})
	testutil.RequireCode(t, err, domain.CodeValidation)
	s.Done(id)
	_, err = s.Svc.Complete(s.Ctx(), s.Meta(), service.CompleteIn{ID: id, Expect: s.Expect(id)})
	testutil.RequireCode(t, err, domain.CodeConflict)
	_, err = s.Svc.Drop(s.Ctx(), s.Meta(), service.TaskRef{ID: id, Expect: s.Expect(id)})
	testutil.RequireCode(t, err, domain.CodeConflict)
	s.Reopen(id, false)
	s.Drop(id)
	_, err = s.Svc.Complete(s.Ctx(), s.Meta(), service.CompleteIn{ID: id, Expect: s.Expect(id)})
	testutil.RequireCode(t, err, domain.CodeConflict)
	_, err = s.Svc.Drop(s.Ctx(), s.Meta(), service.TaskRef{ID: id, Expect: s.Expect(id)})
	testutil.RequireCode(t, err, domain.CodeConflict)
}

func dropDeleteDay(t *testing.T, work bool) (*testutil.Scenario, domain.TaskID, domain.BlockID, domain.BlockID) {
	t.Helper()
	s := testutil.NewScenario(t)
	id := s.Add("A")
	if work {
		s.Log(id, "yesterday", "10-27")
	}
	b1, b2 := s.Block(id, "10-28 16:00-17:00"), s.Block(id, "10-28 13:00-15:00")
	s.SetFocus(id, false)
	// AIDEV-NOTE: F14 fixture shortcut: Block places the task and clears new,
	// but LIF-08/09/10 require {new} alongside these planned blocks.
	seq := latestOp(t, s).Seq
	s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), []domain.EntityRef{{Entity: domain.EntTask, ID: int64(id)}}, func(q store.Q) error {
		_, err := store.AddReason(s.Ctx(), q, domain.TriageRow{TaskID: id, Reason: domain.ReasonNew, Since: s.Clock.Now(), Seq: seq})
		return err
	}))
	s.At("10-28 14:00")
	return s, id, b1, b2
}

func requireClosedBlocks(t *testing.T, s *testutil.Scenario, id domain.TaskID, b1, b2 domain.BlockID) {
	t.Helper()
	requireBlockState(t, s, b1, "{cancelled, –, 0, task_closed}")
	requireBlockState(t, s, b2, "{missed, auto, 1, –}")
	requireDueReasons(t, s, id)
	requireFocus(t, s, 0)
	if len(completionEntries(t, s, id)) != 0 || s.Task(id).CompletedAt.Valid || s.Task(id).CompletedDate.Valid {
		t.Fatal("drop/delete wrote completion state")
	}
	for _, b := range []domain.BlockID{b1, b2} {
		v := s.BlockView(b)
		if v.Version != 2 || !v.ResolvedAt.Valid || !v.ResolvedAt.V.Equal(s.Clock.Now()) || v.ResolvedSeq.V != s.Op(s.Last.OpID).Seq {
			t.Fatalf("resolution metadata = %+v", v)
		}
	}
}

func TestLIF08(t *testing.T) {
	s, id, b1, b2 := dropDeleteDay(t, false)
	before := s.Task(id)
	s.Drop(id)
	requirePlaced(t, s, id, before.Version+1)
	if s.Task(id).Status != domain.StatusDropped {
		t.Fatal("task is not dropped")
	}
	requireClosedBlocks(t, s, id, b1, b2)
	requireEventKeys(t, s, s.Last.OpID, "task:update 1", "block:update 2", "block:update 1", "triage:remove 1 new", "task:update 1", "focus:set")
}

func TestLIF09(t *testing.T) {
	s, id, b1, b2 := dropDeleteDay(t, true)
	before := s.Task(id)
	s.Delete(id)
	v := s.Task(id)
	if !v.DeletedAt.Valid || !v.DeletedAt.V.Equal(s.Clock.Now()) || v.View != domain.ViewDeleted || v.Status != domain.StatusOpen || v.Version != before.Version+1 {
		t.Fatalf("deleted task = %+v", v)
	}
	requireNotPlaced(t, s, before)
	requireClosedBlocks(t, s, id, b1, b2)
	requireSlips(t, s, id, 1, 1)
	detail, err := s.Svc.GetTask(s.Ctx(), s.Actor, id)
	if err != nil || len(detail.Entries) != 1 || detail.Entries[0].Title != "yesterday" || detail.Entries[0].DeletedAt.Valid {
		t.Fatalf("retained entries = %+v, %v", detail, err)
	}
	requireEventKeys(t, s, s.Last.OpID, "task:tombstone 1", "block:update 2", "block:update 1", "triage:remove 1 new", "focus:set")
	for name, call := range map[string]func() error{
		"block": func() error {
			_, err := s.Svc.Block(s.Ctx(), s.Meta(), service.BlockIn{TaskID: id, Expect: s.Expect(id), Span: domain.BlockSpan{Date: testutil.D("10-29"), Start: 600, End: 660}})
			return err
		},
		"focus": func() error {
			_, err := s.Svc.SetFocus(s.Ctx(), s.Meta(), service.SetFocusIn{TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}})
			return err
		},
		"entry": func() error {
			_, err := s.Svc.AddEntry(s.Ctx(), s.Meta(), service.AddEntryIn{TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}, Title: "work"})
			return err
		},
		"update": func() error {
			_, err := s.Svc.UpdateTask(s.Ctx(), s.Meta(), service.UpdateTaskIn{ID: id, Expect: s.Expect(id)})
			return err
		},
		"delete": func() error {
			_, err := s.Svc.Delete(s.Ctx(), s.Meta(), service.TaskRef{ID: id, Expect: s.Expect(id)})
			return err
		},
		"drop": func() error {
			_, err := s.Svc.Drop(s.Ctx(), s.Meta(), service.TaskRef{ID: id, Expect: s.Expect(id)})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) { testutil.RequireCode(t, call(), domain.CodeDeleted) })
	}
	// F8: Tasks 32 and 34 add list/Today exclusion and report inclusion for LIF-09.
}

func TestLIF10(t *testing.T) {
	s, id, b1, b2 := dropDeleteDay(t, true)
	s.Delete(id)
	before, first, second, focus := s.Task(id), s.BlockView(b1).TimeBlock, s.BlockView(b2).TimeBlock, s.Focus()
	s.Undelete(id)
	v := s.Task(id)
	if v.DeletedAt.Valid || v.Status != domain.StatusOpen || v.Version != before.Version+1 ||
		s.BlockView(b1).TimeBlock != first || s.BlockView(b2).TimeBlock != second || s.Focus() != focus {
		t.Fatalf("undelete changed blocks/focus or did not restore task: %+v", v)
	}
	requireNotPlaced(t, s, before)
	requireDueReasons(t, s, id, domain.ReasonManual)
	requireEventKeys(t, s, s.Last.OpID, "task:untombstone 1", "triage:add 1 manual")
}

func TestLIF16(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A", testutil.Due("10-29"))
	s.Drop(id)
	before := s.Task(id)
	s.Reopen(id, false)
	requireReopened(t, s, before)
	requireDueReasons(t, s, id, domain.ReasonReopened)
	requireEventKeys(t, s, s.Last.OpID, "task:update 1", "triage:add 1 reopened")
}

func TestFOC03(t *testing.T) {
	s := testutil.NewScenario(t)
	done, deleted, deferred := s.Add("done"), s.Add("deleted"), s.Add("deferred", testutil.DeferTo("10-29"))
	s.Done(done)
	s.Delete(deleted)
	for _, tc := range []struct {
		id   domain.TaskID
		code domain.Code
	}{
		{done, domain.CodeConflict}, {deleted, domain.CodeDeleted}, {deferred, domain.CodeConflict}, {999, domain.CodeNotFound},
	} {
		_, err := s.Svc.SetFocus(s.Ctx(), s.Meta(), service.SetFocusIn{TaskID: sql.Null[domain.TaskID]{V: tc.id, Valid: true}})
		testutil.RequireCode(t, err, tc.code)
	}
	id := s.Add("open")
	m := s.Meta()
	m.Idem.Key = ""
	_, err := s.Svc.SetFocus(s.Ctx(), m, service.SetFocusIn{TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}})
	testutil.RequireCode(t, err, domain.CodeIdemRequired)
	s.SetFocus(id, false)
	requireFocus(t, s, id)
}

func TestREC05(t *testing.T) {
	for name, closeTask := range map[string]func(*testutil.Scenario, domain.TaskID){
		"defer":    func(s *testutil.Scenario, id domain.TaskID) { s.Defer(id, "10-29") },
		"complete": func(s *testutil.Scenario, id domain.TaskID) { s.Done(id) },
		"drop":     (*testutil.Scenario).Drop, "delete": (*testutil.Scenario).Delete,
	} {
		t.Run(name, func(t *testing.T) {
			s := testutil.NewScenario(t)
			id := s.Add("A")
			s.SetFocus(id, false)
			closeTask(s, id)
			if _, err := s.Svc.GetTask(s.Ctx(), s.Actor, id); err != nil {
				t.Fatal(err)
			}
			requireFocus(t, s, 0)
			if !slices.Contains(s.EventKeys(s.Last.OpID), "focus:set") {
				t.Fatal("missing focus clear event")
			}
		})
	}
}

func TestPlaceRefDeleted(t *testing.T) {
	s := testutil.NewScenario(t)
	id, deleted := s.Add("A"), s.Add("reference")
	s.Delete(deleted)
	count := s.OpCount()
	_, err := s.Svc.AddTask(s.Ctx(), s.Meta(), service.AddTaskIn{Title: "B", Place: &service.AddPlacement{How: "after", Ref: deleted}})
	testutil.RequireCode(t, err, domain.CodeDeleted)
	_, err = s.Svc.Place(s.Ctx(), s.Meta(), service.PlaceIn{ID: id, Expect: s.Expect(id), How: domain.PlaceBefore, Ref: deleted})
	testutil.RequireCode(t, err, domain.CodeDeleted)
	if s.OpCount() != count {
		t.Fatal("failed placement recorded an op")
	}
}

func TestUndeleteLiveTaskNoChange(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A", testutil.DeferTo("11-02"))
	before, count, stack := s.Task(id), s.OpCount(), s.Stack(s.Actor.Key(), store.StackUndo)
	s.Undelete(id)
	if !s.Last.NoChange || !s.Last.OpID.IsZero() || s.Last.Task == nil || s.Last.Task.ID != id || len(s.Last.Effects) != 0 ||
		!reflect.DeepEqual(s.Task(id), before) || s.OpCount() != count || !slices.Equal(s.Stack(s.Actor.Key(), store.StackUndo), stack) {
		t.Fatalf("live undelete = %+v", s.Last)
	}
}

func TestDropDeleteUndeleteContracts(t *testing.T) {
	for _, name := range []string{"drop", "delete", "undelete"} {
		t.Run(name, func(t *testing.T) {
			s := testutil.NewScenario(t)
			id := s.Add("A")
			call := s.Svc.Drop
			if name == "delete" {
				call = s.Svc.Delete
			}
			if name == "undelete" {
				s.Delete(id)
				call = s.Svc.Undelete
			}
			exp, count := s.Expect(id), s.OpCount()
			_, err := call(s.Ctx(), s.Meta(), service.TaskRef{ID: id})
			testutil.RequireCode(t, err, domain.CodePreconditionReq)
			_, err = call(s.Ctx(), s.Meta(), service.TaskRef{ID: id, Expect: domain.Expect{Gen: exp.Gen, Version: exp.Version + 1}})
			testutil.RequireCode(t, err, domain.CodePrecondition)
			_, err = call(s.Ctx(), s.Meta(), service.TaskRef{ID: 999, Expect: exp})
			testutil.RequireCode(t, err, domain.CodeNotFound)
			if name != "undelete" {
				_, err = call(s.Ctx(), s.As(testutil.AI()).Meta(), service.TaskRef{ID: id, Expect: exp})
				testutil.RequireCode(t, err, domain.CodeForbidden)
			}
			if s.OpCount() != count {
				t.Fatal("refused operation wrote an op")
			}
			m := s.Meta()
			first := s.Must(call(s.Ctx(), m, service.TaskRef{ID: id, Expect: exp}))
			before, count := s.Task(id), s.OpCount()
			replay := s.Must(call(s.Ctx(), m, service.TaskRef{ID: id, Expect: exp}))
			if !replay.Replayed || replay.OpID != first.OpID || !reflect.DeepEqual(s.Task(id), before) || s.OpCount() != count {
				t.Fatalf("replay = %+v", replay)
			}
		})
	}
}

func TestDropDeleteBlockBoundaries(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		for _, now := range []string{"10-28 12:59", "10-28 13:00", "10-28 15:00"} {
			t.Run(map[bool]string{false: "drop/", true: "delete/"}[deleted]+now, func(t *testing.T) {
				s := testutil.NewScenario(t)
				id := s.Add("A")
				b := s.Block(id, "10-28 13:00-15:00")
				s.At(now)
				if deleted {
					s.Delete(id)
				} else {
					s.Drop(id)
				}
				want := "{missed, auto, 1, –}"
				if now == "10-28 12:59" {
					want = "{cancelled, –, 0, task_closed}"
				}
				requireBlockState(t, s, b, want)
				requireDueReasons(t, s, id)
				if deleted {
					e := s.Events(s.Last.OpID)[0]
					var after map[string]int64
					if err := json.Unmarshal(e.After, &after); err != nil {
						t.Fatal(err)
					}
					if string(e.Before) != `{"deleted_at":null}` || len(after) != 1 || after["deleted_at"] != s.Clock.Now().UnixMilli() {
						t.Fatalf("tombstone images = %s / %s", e.Before, e.After)
					}
				}
			})
		}
	}
}

func TestDropDeferredTask(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A", testutil.DeferTo("11-02 12:00"))
	before := s.Task(id)
	s.Drop(id)
	requirePlaced(t, s, id, before.Version+1)
	if s.Task(id).Status != domain.StatusDropped {
		t.Fatal("deferred task was not dropped")
	}
}

func TestDeleteWorkedAndResolvedBlocks(t *testing.T) {
	s := testutil.NewScenario(t).At("10-27 08:00")
	id := s.Add("A")
	previous := s.Block(id, "10-27 09:00-10:00")
	started := s.Block(id, "10-28 13:00-15:00")
	future := s.Block(id, "10-29 09:00-10:00")
	s.At("10-28 10:00").Reconcile()
	resolved := s.BlockView(previous).TimeBlock
	s.Log(id, "work today", "")
	s.At("10-28 14:00").Delete(id)
	requireBlockState(t, s, started, "{worked, auto, 0, –}")
	requireBlockState(t, s, future, "{cancelled, –, 0, task_closed}")
	requireDueReasons(t, s, id)
	if s.BlockView(previous).TimeBlock != resolved {
		t.Fatal("delete changed an already resolved block")
	}
	other := s.Add("other focus")
	s.SetFocus(other, false)
	focus := s.Focus()
	s.Undelete(id)
	if s.Focus() != focus || s.BlockView(previous).TimeBlock != resolved {
		t.Fatal("undelete changed focus or an already resolved block")
	}
	requireDueReasons(t, s, id, domain.ReasonManual)
}
