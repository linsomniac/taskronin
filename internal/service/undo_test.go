package service_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/service"
	"github.com/linsomniac/taskronin/internal/store"
	"github.com/linsomniac/taskronin/internal/testutil"
)

func requireStacks(t *testing.T, s *testutil.Scenario, undo, redo []domain.OpID) {
	t.Helper()
	if got := s.Stack(s.Actor.Key(), store.StackUndo); !slices.Equal(got, undo) {
		t.Fatalf("undo = %v, want %v", got, undo)
	}
	if got := s.Stack(s.Actor.Key(), store.StackRedo); !slices.Equal(got, redo) {
		t.Fatalf("redo = %v, want %v", got, redo)
	}
}

func requireReversal(t *testing.T, s *testutil.Scenario, r *service.OpResult, target domain.OpID, kind domain.OpKind, via string) {
	t.Helper()
	op := s.Op(r.OpID)
	if r.NoChange || r.Created || r.OpID == target || op.Kind != kind || op.Via != via ||
		op.RevertsOpID != (sql.Null[domain.OpID]{V: target, Valid: true}) || op.Actor != s.Actor.Key() ||
		op.Channel != s.Actor.Channel || op.Scopes != s.Op(target).Scopes || !op.Revertible || op.State != domain.OpActive ||
		s.Op(target).State != domain.OpReverted {
		t.Fatalf("reversal = %+v; result = %+v", op, r)
	}
}

func requireNoIdem(t *testing.T, s *testutil.Scenario, m service.Meta) {
	t.Helper()
	if _, err := store.GetIdem(s.Ctx(), s.Store.Reader, m.Actor.Key(), m.Idem.Key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unexpected idempotency row: %v", err)
	}
}

func TestUND01(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	a, before := s.Last.OpID, s.Task(id)
	r := s.Undo()
	requireReversal(t, s, r, a, domain.OpUndo, "undo")
	requireStacks(t, s, nil, []domain.OpID{a})
	after := s.Task(id)
	if !after.DeletedAt.Valid || !after.DeletedAt.V.Equal(s.Clock.Now()) || after.Version != before.Version+1 || len(after.Reasons) != 0 {
		t.Fatalf("undone task = %+v", after)
	}
	requireEventKeys(t, s, r.OpID, "triage:remove 1 new", "task:tombstone 1")
}

func TestUND02(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	a, reasons := s.Last.OpID, s.Task(id).Reasons
	u := s.Undo()
	r := s.Redo()
	requireReversal(t, s, r, u.OpID, domain.OpRedo, "redo")
	requireStacks(t, s, []domain.OpID{a}, nil)
	after := s.Task(id)
	if after.DeletedAt.Valid || after.Version != 3 || !reflect.DeepEqual(after.Reasons, reasons) || s.Op(a).State != domain.OpActive {
		t.Fatalf("redone task = %+v", after)
	}
	requireEventKeys(t, s, r.OpID, "task:untombstone 1", "triage:add 1 new")
}

func TestUND03(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	a := s.Last.OpID
	s.Undo()
	s.Redo()
	r := s.Undo()
	requireReversal(t, s, r, a, domain.OpUndo, "undo")
	requireStacks(t, s, nil, []domain.OpID{a})
	if v := s.Task(id); !v.DeletedAt.Valid || v.Version != 4 || len(v.Reasons) != 0 {
		t.Fatal(v)
	}
}

func TestUND04(t *testing.T) {
	s := testutil.NewScenario(t)
	s.Add("A")
	s.Undo()
	s.Add("B")
	b, count, m := s.Last.OpID, s.OpCount(), s.Meta()
	_, err := s.Svc.Redo(s.Ctx(), m)
	testutil.RequireCode(t, err, domain.CodeNothingToRedo)
	requireStacks(t, s, []domain.OpID{b}, nil)
	requireNoIdem(t, s, m)
	if s.OpCount() != count {
		t.Fatal("empty redo stored an operation")
	}
}

func TestUND05(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	a := s.Last.OpID
	ai := s.As(testutil.AI())
	e := ai.Log(id, "work", "")
	b := ai.Last.OpID
	ai.Undo()
	requireStacks(t, ai, nil, []domain.OpID{b})
	requireStacks(t, s, []domain.OpID{a}, nil)
	entry, err := store.GetEntry(s.Ctx(), s.Store.Reader, e)
	if err != nil || !entry.DeletedAt.Valid || s.Task(id).DeletedAt.Valid || s.Op(a).State != domain.OpActive {
		t.Fatal(entry, err)
	}
	s.Undo()
	requireStacks(t, s, nil, []domain.OpID{a})
	if !s.Task(id).DeletedAt.Valid {
		t.Fatal("owner task not undone")
	}
}

func stackRefusal(t *testing.T, s *testutil.Scenario, redo bool, code domain.Code) *domain.Error {
	t.Helper()
	before, count, m := s.StateDigest(), s.OpCount(), s.Meta()
	u, r := s.Stack(s.Actor.Key(), store.StackUndo), s.Stack(s.Actor.Key(), store.StackRedo)
	var err error
	if redo {
		_, err = s.Svc.Redo(s.Ctx(), m)
	} else {
		_, err = s.Svc.Undo(s.Ctx(), m)
	}
	testutil.RequireCode(t, err, code)
	requireStacks(t, s, u, r)
	requireNoIdem(t, s, m)
	if s.StateDigest() != before || s.OpCount() != count {
		t.Fatal("refusal changed domain state or operations")
	}
	de, _ := domain.AsError(err)
	return de
}

func TestUND07(t *testing.T) {
	s := testutil.NewScenario(t).At("10-26 09:00")
	id := s.Add("A")
	b := s.Block(id, "10-26 13:00-15:00")
	s.At("10-27 00:00").Reconcile()
	system := latestOp(t, s)
	e := stackRefusal(t, s, false, domain.CodeRevertConflict)
	if len(e.Conflicts) != 1 || e.Conflicts[0].OpID != system.ID || e.Conflicts[0].Actor != "system" ||
		e.Conflicts[0].Entity != domain.EntBlock || e.Conflicts[0].EntityID != int64(b) {
		t.Fatal(e)
	}
}

func TestUND08(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	a := s.Last.OpID
	s.Place(id, domain.PlaceTop)
	b := s.Last.OpID
	s.Undo()
	s.Undo()
	requireStacks(t, s, nil, []domain.OpID{b, a})
	if !s.Task(id).DeletedAt.Valid {
		t.Fatal("net-zero pair blocked undo")
	}
}

func TestUND15(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	reasons := s.Task(id).Reasons
	s.Must(s.Svc.MarkManual(s.Ctx(), s.Meta(), service.MarkManualIn{ID: id, Expect: s.Expect(id)}))
	s.Done(id)
	s.Undo()
	requireDueReasons(t, s, id, domain.ReasonManual, domain.ReasonNew)
	if s.Task(id).Status != domain.StatusOpen {
		t.Fatal("completion was not undone")
	}
	s.Undo()
	if !reflect.DeepEqual(s.Task(id).Reasons, reasons) {
		t.Fatal("undo must remove only manual and retain original new metadata")
	}
	requireEventKeys(t, s, s.Last.OpID, "triage:remove 1 manual")
}

func undoUnblock(t *testing.T, nextDay bool) (*testutil.Scenario, domain.TaskID, domain.BlockID) {
	t.Helper()
	s, id, b := scheduledToday(t, "10-28 09:00")
	s.At("10-28 10:00").Unblock(b)
	if nextDay {
		s.At("10-29 10:00")
	}
	s.Undo()
	requireBlockState(t, s, b, "{planned, –, 0, –}")
	v := s.BlockView(b)
	if v.SlippedAt.Valid || v.SlippedSeq.Valid || v.ResolvedAt.Valid || v.ResolvedSeq.Valid {
		t.Fatalf("restored block metadata = %+v", v)
	}
	requireDueReasons(t, s, id)
	requireSlips(t, s, id, 0, 0)
	requireEventKeys(t, s, s.Last.OpID, "triage:remove 1 slipped", "block:update 1")
	for _, e := range s.Events(s.Last.OpID) {
		if e.Derived {
			t.Fatal("undo ran slip derivation")
		}
	}
	return s, id, b
}

func TestUND16(t *testing.T) {
	for _, nextDay := range []bool{false, true} {
		t.Run(map[bool]string{false: "same day", true: "next day"}[nextDay], func(t *testing.T) {
			undoUnblock(t, nextDay)
		})
	}
}

func TestREC10(t *testing.T) {
	s, id, b := undoUnblock(t, true)
	s.Reconcile()
	requireBlockState(t, s, b, "{missed, auto, 1, –}")
	requireDueReasons(t, s, id, domain.ReasonSlipped)
	requireSlips(t, s, id, 1, 1)
	op := latestOp(t, s)
	if op.Actor != "system" || op.Kind != domain.OpSystemReconcile || s.BlockView(b).SlippedSeq.V != op.Seq {
		t.Fatal(op)
	}
	requireEventKeys(t, s, op.ID, "block:update 1", "triage:add 1 slipped")
}

func TestUND17(t *testing.T) {
	for _, nextDay := range []bool{false, true} {
		t.Run(map[bool]string{false: "same day", true: "next day"}[nextDay], func(t *testing.T) {
			s, id, blocks := completionDay(t)
			s.SetFocus(id, false)
			before := s.Task(id)
			s.Done(id)
			entry := s.Last.Entry.ID
			if nextDay {
				s.At("10-29 00:00").Reconcile()
			}
			s.Undo()
			after := s.Task(id)
			if after.Status != domain.StatusOpen || after.CompletedAt.Valid || after.CompletedDate.Valid ||
				after.LastPlacedAt != before.LastPlacedAt || after.LastPlacedSeq != before.LastPlacedSeq ||
				after.DueAck != before.DueAck || after.DueAckDate != before.DueAckDate || !reflect.DeepEqual(after.Reasons, before.Reasons) {
				t.Fatalf("restored task = %+v", after)
			}
			for _, b := range blocks {
				requireBlockState(t, s, b, "{planned, –, 0, –}")
			}
			e, err := store.GetEntry(s.Ctx(), s.Store.Reader, entry)
			if err != nil || !e.DeletedAt.Valid {
				t.Fatal(e, err)
			}
			focus := id
			if nextDay {
				focus = 0
			}
			requireFocus(t, s, focus)
			requireSlips(t, s, id, 0, 0)
		})
	}
	// Complete can also remove new; its original since/seq must survive Undo.
	t.Run("original new reason", func(t *testing.T) {
		s := testutil.NewScenario(t)
		id := s.Add("A")
		reasons := s.Task(id).Reasons
		s.At("10-28 11:00").Done(id)
		s.Undo()
		if !reflect.DeepEqual(s.Task(id).Reasons, reasons) {
			t.Fatal("new reason metadata not restored")
		}
	})
}

func TestUND18(t *testing.T) {
	s := testutil.NewScenario(t)
	_ = stackRefusal(t, s, false, domain.CodeNothingToUndo)
	_ = stackRefusal(t, s, true, domain.CodeNothingToRedo)
}

func TestUND19(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	a := s.Last.OpID
	actor := testutil.Owner()
	actor.Channel = domain.ChanWeb
	web := s.As(actor)
	r := web.Undo()
	requireReversal(t, web, r, a, domain.OpUndo, "undo")
	requireStacks(t, s, nil, []domain.OpID{a})
	if !s.Task(id).DeletedAt.Valid {
		t.Fatal("web did not undo CLI operation")
	}
	s.Redo()
	requireStacks(t, web, []domain.OpID{a}, nil)
}

func requireCommittedReplay(t *testing.T, s *testutil.Scenario, redo bool, code domain.Code, detail string, undo, redos []domain.OpID) {
	t.Helper()
	m := s.MetaWith("undo-key-0123456789", "{}")
	before, count := s.StateDigest(), s.OpCount()
	var events int
	if err := s.Store.Reader.QueryRowContext(s.Ctx(), `SELECT COUNT(*) FROM events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	var first *domain.Error
	for i := 0; i < 2; i++ {
		var err error
		if redo {
			_, err = s.Svc.Redo(s.Ctx(), m)
		} else {
			_, err = s.Svc.Undo(s.Ctx(), m)
		}
		testutil.RequireCode(t, err, code)
		de, _ := domain.AsError(err)
		if i == 0 {
			first = de
		} else if !reflect.DeepEqual(first, de) {
			t.Fatalf("replayed error = %+v, want %+v", de, first)
		}
		if detail != "" && de.Detail != detail {
			t.Fatal(de)
		}
		requireStacks(t, s, undo, redos)
		var n int
		if err := s.Store.Reader.QueryRowContext(s.Ctx(), `SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if s.StateDigest() != before || s.OpCount() != count || n != events {
			t.Fatal("committed pop changed domain state, operations or events")
		}
	}
	row, err := store.GetIdem(s.Ctx(), s.Store.Reader, s.Actor.Key(), m.Idem.Key)
	if err != nil || row.Status != 409 || row.OpID.Valid {
		t.Fatalf("committed error row = %+v; %v", row, err)
	}
	var problem struct {
		Code   domain.Code `json:"code"`
		Detail string      `json:"detail"`
	}
	if err := json.Unmarshal(row.ErrorJSON, &problem); err != nil || problem.Code != first.Code || problem.Detail != first.Detail {
		t.Fatalf("stored problem = %s; %v", row.ErrorJSON, err)
	}
}

func TestUND20(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	ai := s.As(testutil.AI())
	ai.Log(id, "older", "")
	y := ai.Last.OpID
	ai.Log(id, "newer", "")
	x := ai.Last.OpID
	s.Revert(x)
	requireCommittedReplay(t, ai, false, domain.CodeRevertConflict, "already reverted by owner", []domain.OpID{y}, nil)
	ai.Undo()
	if ai.Op(y).State != domain.OpReverted {
		t.Fatal("fresh key did not undo older entry")
	}
	e := revertRefusal(t, s, x, domain.CodeRevertConflict)
	if e.Detail != "already reverted by owner" {
		t.Fatal(e)
	}
}

func TestUND22(t *testing.T) {
	s, id, b := scheduledToday(t, "10-28 09:00")
	before := s.Task(id)
	s.At("10-28 10:00").Defer(id, "10-30")
	requireBlockState(t, s, b, "{cancelled, –, 1, deferred}")
	s.Undo()
	after := s.Task(id)
	if after.DeferDate.Valid || after.DeferMin.Valid || after.LastPlacedAt != before.LastPlacedAt || after.LastPlacedSeq != before.LastPlacedSeq ||
		after.DueAck != before.DueAck || after.DueAckDate != before.DueAckDate || !reflect.DeepEqual(after.Reasons, before.Reasons) {
		t.Fatalf("restored task = %+v", after)
	}
	requireBlockState(t, s, b, "{planned, –, 0, –}")
	requireSlips(t, s, id, 0, 0)
}

func TestUND25(t *testing.T) {
	s := testutil.NewScenario(t)
	a, b := s.Add("A"), s.Add("B")
	s.SetFocus(b, false)
	s.At("10-28 22:00").SetFocus(a, false)
	f, stack := s.Last.OpID, s.Stack("owner", store.StackUndo)
	s.At("10-29 00:00").Reconcile()
	count := s.OpCount()
	r := s.Undo()
	requireReversal(t, s, r, f, domain.OpUndo, "undo")
	requireStacks(t, s, stack[:len(stack)-1], []domain.OpID{f})
	requireFocus(t, s, 0)
	if s.OpCount() != count+1 || len(s.Events(r.OpID)) != 0 || len(r.Effects) != 0 {
		t.Fatalf("eventless undo = %+v", r)
	}
}

func TestUND27(t *testing.T) {
	s := testutil.NewScenario(t)
	// The catalog leaves T0's actor open; AI capture gives owner the stated [R] stack.
	ai := s.As(testutil.AI())
	ai.Add("A")
	t0 := ai.Last.OpID
	r := s.Revert(t0)
	requireStacks(t, s, []domain.OpID{r.OpID}, nil)
	if s.Op(t0).State != domain.OpReverted || s.Op(r.OpID).State != domain.OpActive {
		t.Fatal("incorrect states after Revert")
	}
	u := s.Undo()
	requireStacks(t, s, nil, []domain.OpID{r.OpID})
	if s.Op(t0).State != domain.OpActive || s.Op(r.OpID).State != domain.OpReverted {
		t.Fatal("incorrect states after Undo")
	}
	d := s.Redo()
	requireReversal(t, s, d, u.OpID, domain.OpRedo, "redo")
	requireStacks(t, s, []domain.OpID{r.OpID}, nil)
	if s.Op(t0).State != domain.OpReverted || s.Op(r.OpID).State != domain.OpActive {
		t.Fatal("incorrect states after Redo")
	}
	_ = stackRefusal(t, s, true, domain.CodeNothingToRedo)
}

func TestRCP05(t *testing.T) {
	s := testutil.NewScenario(t).At("10-26 08:00")
	a, b := s.Add("A"), s.Add("B")
	b1, b2 := s.Block(a, "10-26 09:00-10:00"), s.Block(b, "10-26 11:00-12:00")
	e := s.Log(a, "did it", "")
	s.At("10-28 10:00").Reconcile()
	s.Relink(e, b)
	s.At("10-28 11:00").Undo()
	requireBlockState(t, s, b1, "{worked, auto, 0, –}")
	requireBlockState(t, s, b2, "{missed, auto, 1, –}")
	requireDueReasons(t, s, a, domain.ReasonContinue)
	requireDueReasons(t, s, b, domain.ReasonSlipped)
	requireSlips(t, s, a, 0, 0)
	requireSlips(t, s, b, 1, 1)
	if !s.BlockView(b2).SlippedAt.V.Equal(s.Clock.Now()) || s.BlockView(b2).SlippedSeq.V != s.Op(s.Last.OpID).Seq {
		t.Fatal("recompute did not record a new slip")
	}
	requireEventKeys(t, s, s.Last.OpID, "entry:update 1", "block:update 1 [derived]", "triage:remove 1 slipped [derived]",
		"triage:add 1 continue [derived]", "block:update 2 [derived]", "triage:add 2 slipped [derived]", "triage:remove 2 continue [derived]")
}

func TestINT09(t *testing.T) {
	for _, nextDay := range []bool{false, true} {
		t.Run(map[bool]string{false: "same day", true: "next day"}[nextDay], func(t *testing.T) {
			s, from, _, to := interruptionFixture(t)
			if nextDay {
				s.At("10-29 00:00").Reconcile()
			}
			s.Undo()
			v := s.Task(to)
			if !v.DeletedAt.Valid || v.LastPlacedAt.Valid || v.LastPlacedSeq.Valid || v.DueAck != domain.DueNone || v.DueAckDate.Valid || len(s.Interruptions("10-28")) != 0 {
				t.Fatalf("restored task/interruption = %+v", v)
			}
			interruption, err := store.GetInterruption(s.Ctx(), s.Store.Reader, 1)
			if err != nil || !interruption.DeletedAt.Valid {
				t.Fatal(interruption, err)
			}
			focus := from
			if nextDay {
				focus = 0
			}
			requireFocus(t, s, focus)
			keys := s.EventKeys(s.Last.OpID)
			if !slices.Contains(keys, "task:tombstone 2") || !slices.Contains(keys, "interruption:tombstone 1") {
				t.Fatal(keys)
			}
		})
	}
}

func TestSCP09(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	a := testutil.AI()
	a.Scopes = domain.ScopeRead | domain.ScopeCapture
	ai := s.As(a)
	ai.Log(id, "work", "")
	x := ai.Last.OpID
	r := ai.Undo()
	requireReversal(t, ai, r, x, domain.OpUndo, "undo")
	if ai.Op(r.OpID).Scopes != domain.ScopeCapture {
		t.Fatal("undo did not inherit capture scope")
	}
	ai.Redo()
	s.Drop(id)
	_ = revertRefusal(t, ai, s.Last.OpID, domain.CodeForbidden)
}

func TestUndoStaleGeneration(t *testing.T) {
	s := testutil.NewScenario(t)
	s.Add("A")
	a := s.Last.OpID
	m := s.Meta()
	m.Gen = domain.NewGeneration()
	count := s.OpCount()
	_, err := s.Svc.Undo(s.Ctx(), m)
	testutil.RequireCode(t, err, domain.CodeGenerationMismatch)
	requireStacks(t, s, []domain.OpID{a}, nil)
	requireNoIdem(t, s, m)
	if s.OpCount() != count || s.Op(a).State != domain.OpActive {
		t.Fatal("stale generation changed state")
	}
}

func TestUndoDepthTrim(t *testing.T) {
	s := testutil.NewScenario(t, func(o *service.Options) { o.UndoDepth = 3 })
	var ops []domain.OpID
	for _, title := range []string{"A", "B", "C", "D", "E"} {
		s.Add(title)
		ops = append(ops, s.Last.OpID)
	}
	requireStacks(t, s, ops[2:], nil)
	for range 3 {
		s.Undo()
	}
	requireStacks(t, s, nil, []domain.OpID{ops[4], ops[3], ops[2]})
	_ = stackRefusal(t, s, false, domain.CodeNothingToUndo)
	for range 3 {
		s.Redo()
	}
	requireStacks(t, s, ops[2:], nil)
}

func TestTRI18_Undo(t *testing.T) {
	s := testutil.NewScenario(t).At("10-28 09:00")
	a := s.Add("first")
	b := s.At("10-28 09:30").Add("second")
	c, d := s.Add("new and slipped"), s.Add("surfaced", testutil.Bottom())
	// Fixture shortcut: F5 allows TRI-18's reasons, which placement would clear.
	seq := s.Op(s.Last.OpID).Seq
	s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), nil, func(q store.Q) error {
		for _, row := range []domain.TriageRow{
			{TaskID: c, Reason: domain.ReasonSlipped, Since: s.Clock.Now(), Seq: seq},
			{TaskID: d, Reason: domain.ReasonSurfaced, Since: s.Clock.Now(), Seq: seq},
		} {
			if _, err := store.AddReason(s.Ctx(), q, row); err != nil {
				return err
			}
		}
		return nil
	}))
	for range 4 {
		s.Add("unaccepted")
	}
	nine := s.Add("backlog", testutil.Bottom())
	s.At(testutil.DefaultNow)
	before, stack := s.StateDigest(), s.Stack("owner", store.StackUndo)
	first, second, third, fourth := s.Task(a), s.Task(b), s.Task(c), s.Task(d)
	r := s.Must(s.Svc.AcceptNew(s.Ctx(), s.Meta(), service.AcceptNewIn{Items: []service.AcceptItem{
		{ID: a, Expect: s.Expect(a)}, {ID: b, Expect: s.Expect(b)},
	}}))
	requireBacklog(t, s, nine, a, b)
	if len(r.Tasks) != 2 || r.Tasks[0].ID != a || r.Tasks[1].ID != b || s.Op(r.OpID).Kind != domain.OpTaskAcceptNew {
		t.Fatal(r)
	}
	count := s.OpCount()
	s.Undo()
	requireStacks(t, s, stack, []domain.OpID{r.OpID})
	if s.OpCount() != count+1 || s.StateDigest() != before || !reflect.DeepEqual(s.Task(a).Reasons, first.Reasons) ||
		!reflect.DeepEqual(s.Task(b).Reasons, second.Reasons) || !reflect.DeepEqual(s.Task(c), third) || !reflect.DeepEqual(s.Task(d), fourth) {
		t.Fatal("bulk placement was not undone atomically with reason metadata intact")
	}
}

func TestUndoRedoCommittedErrors(t *testing.T) {
	for _, variant := range []string{"undo not revertible", "redo target not revertible", "redo reversal not revertible", "redo active", "redo no reversal"} {
		t.Run(variant, func(t *testing.T) {
			s := testutil.NewScenario(t)
			s.Add("older")
			older := s.Last.OpID
			s.Add("newer")
			newer := s.Last.OpID
			top := newer
			redo := variant != "undo not revertible"
			var reversal domain.OpID
			wantUndo, wantRedo := []domain.OpID{older}, []domain.OpID(nil)
			if redo {
				s.Undo()
				s.Undo()
				// Redo's top is now older; newer remains beneath it.
				top, reversal = older, s.Last.OpID
				wantUndo, wantRedo = nil, []domain.OpID{newer}
			}
			code := domain.CodeNotRevertible
			// Fixture shortcut: purge is Task 37; inconsistent terminal redo rows
			// cannot be made through the available service operations.
			s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), nil, func(q store.Q) error {
				switch variant {
				case "redo active":
					code = domain.CodeRevertConflict
					return store.SetOpState(s.Ctx(), q, top, domain.OpActive)
				case "redo no reversal":
					code = domain.CodeRevertConflict
					return store.SetOpState(s.Ctx(), q, reversal, domain.OpReverted)
				default:
					id := top
					if variant == "redo reversal not revertible" {
						id = reversal
					}
					_, err := q.ExecContext(s.Ctx(), `UPDATE ops SET revertible = 0 WHERE op_id = ?`, id)
					return err
				}
			}))
			requireCommittedReplay(t, s, redo, code, "", wantUndo, wantRedo)
			if redo {
				s.Redo()
			} else {
				s.Undo()
			}
		})
	}
}

func TestUndoRedoConflictKeepsStacks(t *testing.T) {
	for _, redo := range []bool{false, true} {
		t.Run(map[bool]string{false: "undo", true: "redo"}[redo], func(t *testing.T) {
			s := testutil.NewScenario(t)
			id := s.Add("A")
			title := "B"
			s.Update(id, func(in *service.UpdateTaskIn) { in.Title = &title })
			if redo {
				s.Undo()
			}
			ai := s.As(testutil.AI())
			title = "AI edit"
			ai.Update(id, func(in *service.UpdateTaskIn) { in.Title = &title })
			e := stackRefusal(t, s, redo, domain.CodeRevertConflict)
			if len(e.Conflicts) != 1 || e.Conflicts[0].OpID != ai.Last.OpID || e.Conflicts[0].EntityID != int64(id) {
				t.Fatal(e)
			}
		})
	}
	for _, redo := range []bool{false, true} {
		t.Run(map[bool]string{false: "undo overlap", true: "redo overlap"}[redo], func(t *testing.T) {
			s := testutil.NewScenario(t)
			a, b := s.Add("A"), s.Add("B")
			block := s.Block(a, "10-29 09:00-10:00")
			if redo {
				s.Undo()
			} else {
				s.Unblock(block)
			}
			s.As(testutil.AI()).Block(b, "10-29 09:00-10:00")
			_ = stackRefusal(t, s, redo, domain.CodeConflict)
		})
	}
}

func TestUndoRedoSuccessReplay(t *testing.T) {
	s := testutil.NewScenario(t)
	s.Add("A")
	s.Add("B")
	for _, redo := range []bool{false, true} {
		m := s.Meta()
		call := s.Svc.Undo
		if redo {
			call = s.Svc.Redo
		}
		first := s.Must(call(s.Ctx(), m))
		count, before := s.OpCount(), s.StateDigest()
		u, r := s.Stack("owner", store.StackUndo), s.Stack("owner", store.StackRedo)
		replay := s.Must(call(s.Ctx(), m))
		if !replay.Replayed || replay.OpID != first.OpID || !reflect.DeepEqual(replay.Effects, first.Effects) ||
			s.OpCount() != count || s.StateDigest() != before {
			t.Fatal("successful replay acted on the next stack entry")
		}
		requireStacks(t, s, u, r)
	}
}

func TestUndoRedoTargetScopes(t *testing.T) {
	for _, redo := range []bool{false, true} {
		t.Run(map[bool]string{false: "undo", true: "redo"}[redo], func(t *testing.T) {
			s := testutil.NewScenario(t)
			id := s.Add("A")
			ai := s.As(testutil.AI())
			ai.Place(id, domain.PlaceTop)
			if redo {
				ai.Undo()
			}
			ai.Actor.Scopes = domain.ScopeRead | domain.ScopeCapture
			_ = stackRefusal(t, ai, redo, domain.CodeForbidden)
		})
	}
}
