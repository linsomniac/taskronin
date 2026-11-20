package service_test

import (
	"reflect"
	"slices"
	"testing"

	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/store"
	"taskronin/internal/testutil"
)

func revertRefusal(t *testing.T, s *testutil.Scenario, target domain.OpID, code domain.Code) *domain.Error {
	t.Helper()
	before, count := s.StateDigest(), s.OpCount()
	undo, redo := s.Stack(s.Actor.Key(), store.StackUndo), s.Stack(s.Actor.Key(), store.StackRedo)
	_, err := s.Svc.Revert(s.Ctx(), s.Meta(), service.RevertIn{OpID: target})
	testutil.RequireCode(t, err, code)
	if s.StateDigest() != before || s.OpCount() != count || !slices.Equal(undo, s.Stack(s.Actor.Key(), store.StackUndo)) || !slices.Equal(redo, s.Stack(s.Actor.Key(), store.StackRedo)) {
		t.Fatal("refused revert changed state, operations or stacks")
	}
	e, _ := domain.AsError(err)
	return e
}

func aiLogFixture(t *testing.T) (*testutil.Scenario, domain.BlockID, domain.EntryID, domain.OpID) {
	t.Helper()
	s := testutil.NewScenario(t).At("10-26 09:00")
	id := s.Add("A")
	b := s.Block(id, "10-26 13:00-15:00")
	s.At("10-27 00:00").Reconcile()
	s.At("10-28 10:00")
	ai := s.As(testutil.AI())
	e := ai.Log(id, "Did it", "10-26")
	requireBlockState(t, s, b, "{worked, auto, 0, –}")
	if !slices.Equal(s.Reasons(id), []domain.TriageReason{domain.ReasonContinue}) {
		t.Fatal(s.Reasons(id))
	}
	return s, b, e, ai.Last.OpID
}

func TestSLP05(t *testing.T) {
	for _, placed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unplaced", true: "placed"}[placed], func(t *testing.T) {
			s, b, entry, x := aiLogFixture(t)
			if placed {
				s.Place(1, domain.PlaceKeep)
			}
			s.At("10-28 11:00")
			r := s.Revert(x)
			requireBlockState(t, s, b, "{missed, auto, 1, –}")
			requireSlips(t, s, 1, 1, 1)
			v := s.BlockView(b)
			if v.SlippedSeq.V != s.Op(r.OpID).Seq || !v.SlippedAt.V.Equal(s.Clock.Now()) || v.SlippedSeq.V <= s.Task(1).LastPlacedSeq.V {
				t.Fatalf("slip metadata = %+v", v)
			}
			reasons := s.Task(1).Reasons
			if len(reasons) != 1 || reasons[0].Reason != domain.ReasonSlipped || reasons[0].Seq != s.Op(r.OpID).Seq || !reasons[0].Since.Equal(s.Clock.Now()) {
				t.Fatal(reasons)
			}
			e, err := store.GetEntry(s.Ctx(), s.Store.Reader, entry)
			if err != nil || !e.DeletedAt.Valid || s.Op(x).State != domain.OpReverted {
				t.Fatalf("entry = %+v; %v", e, err)
			}
			want := []string{"entry:tombstone 1", "block:update 1 [derived]", "triage:add 1 slipped [derived]"}
			if !placed {
				want = append(want, "triage:remove 1 continue [derived]")
			}
			requireEventKeys(t, s, r.OpID, want...)
			requireAIOpState(t, s, x, domain.OpReverted)
		})
	}
}

func TestUND06(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	s.Place(id, domain.PlaceTop)
	a := s.Last.OpID
	title := "B"
	s.Update(id, func(in *service.UpdateTaskIn) { in.Title = &title })
	b := s.Last.OpID
	e := revertRefusal(t, s, a, domain.CodeRevertConflict)
	if len(e.Conflicts) != 1 || e.Conflicts[0].OpID != b || e.Conflicts[0].Entity != domain.EntTask || e.Conflicts[0].EntityID != int64(id) {
		t.Fatal(e)
	}
}

func TestUND09(t *testing.T) {
	s := testutil.NewScenario(t)
	a, b := s.Add("A"), s.Add("B")
	block := s.Block(a, "10-29 09:00-10:00")
	s.Unblock(block)
	target := s.Last.OpID
	s.Block(b, "10-29 09:00-10:00")
	_ = revertRefusal(t, s, target, domain.CodeConflict)
}

func TestUND10(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	b := s.Block(id, "10-29 09:00-10:00")
	s.Unblock(b)
	a := s.Last.OpID
	s.Defer(id, "11-02")
	_ = revertRefusal(t, s, a, domain.CodeConflict)
}

func TestUND11(t *testing.T) {
	s, b, _, x := aiLogFixture(t)
	// AIDEV-NOTE: F14 fixture shortcut: Undo (Task 31) is not available to
	// populate redo yet. Only the stack Given uses a raw store fixture.
	s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), nil, func(q store.Q) error {
		return store.PushStack(s.Ctx(), q, s.Actor.Key(), store.StackRedo, x)
	}))
	r := s.Revert(x)
	op := s.Op(r.OpID)
	if op.Kind != domain.OpRevert || op.Actor != "owner" || op.Via != "revert" || op.RevertsOpID.V != x || op.Scopes != s.Op(x).Scopes || !op.Revertible || op.State != domain.OpActive {
		t.Fatalf("revert = %+v", op)
	}
	stack := s.Stack("owner", store.StackUndo)
	if stack[len(stack)-1] != r.OpID || len(s.Stack("owner", store.StackRedo)) != 0 || s.Op(x).State != domain.OpReverted {
		t.Fatal("incorrect state or stacks")
	}
	requireBlockState(t, s, b, "{missed, auto, 1, –}")
	if s.BlockView(b).SlippedSeq.V != op.Seq || !slices.Equal(s.Reasons(1), []domain.TriageReason{domain.ReasonSlipped}) {
		t.Fatal("missing derived slip")
	}
	requireEventKeys(t, s, r.OpID, "entry:tombstone 1", "block:update 1 [derived]", "triage:add 1 slipped [derived]", "triage:remove 1 continue [derived]")
	requireAIOpState(t, s, x, domain.OpReverted)
}

func TestUND12(t *testing.T) {
	s := testutil.NewScenario(t)
	a, b := s.Add("A"), s.Add("B")
	s.Drop(a)
	d := s.Last.OpID
	s.Place(b, domain.PlaceTop)
	p := s.Last.OpID
	ai := s.As(testutil.AI())
	_ = revertRefusal(t, ai, d, domain.CodeForbidden)
	_ = revertRefusal(t, ai, p, domain.CodeForbidden)
	ai.Log(b, "AI work", "")
	x := ai.Last.OpID
	ai.Revert(x)
	if ai.Op(ai.Last.OpID).Scopes != domain.ScopeCapture {
		t.Fatal("revert did not inherit capture scope")
	}
	s.Revert(d)
	if s.Task(a).Status != domain.StatusOpen {
		t.Fatal("owner revert failed")
	}
}

func TestUND14(t *testing.T) {
	s := testutil.NewScenario(t)
	x, target, y := s.Add("X", testutil.Bottom()), s.Add("T", testutil.Bottom()), s.Add("Y", testutil.Bottom())
	s.Place(target, domain.PlaceTop)
	a := s.Last.OpID
	z := s.Add("Z", testutil.Bottom())
	if err := store.RenormalizeRanks(s.Ctx(), s.Store.Writer); err != nil {
		t.Fatal(err)
	}
	s.Revert(a)
	if !slices.Equal(s.Backlog(), []domain.TaskID{x, target, y, z}) {
		t.Fatal(s.Backlog())
	}
}

func TestUND21(t *testing.T) {
	s := testutil.NewScenario(t).At("10-26 09:00")
	id := s.Add("A")
	s.Block(id, "13:00-14:00")
	s.At("10-27 00:00").Reconcile()
	system := latestOp(t, s)
	if system.Kind != domain.OpSystemReconcile {
		t.Fatal(system)
	}
	_ = revertRefusal(t, s, system.ID, domain.CodeNotRevertible)
}

func TestUND23(t *testing.T) {
	s := testutil.NewScenario(t)
	a, b := s.Add("A", testutil.Due("10-28 17:00")), s.Add("B")
	before := s.Task(a)
	block := s.Block(a, "10-29 09:00-10:00")
	target := s.Last.OpID
	s.Revert(target)
	after := s.Task(a)
	v := s.BlockView(block)
	if !v.DeletedAt.Valid || v.Slipped || v.Version != 2 || after.LastPlacedAt != before.LastPlacedAt || after.LastPlacedSeq != before.LastPlacedSeq || after.DueAck != before.DueAck || after.DueAckDate != before.DueAckDate || !reflect.DeepEqual(before.Reasons, after.Reasons) || after.Version != before.Version+2 {
		t.Fatalf("restored task/block = %+v / %+v", after, v)
	}
	requireEventKeys(t, s, s.Last.OpID, "task:update 1", "triage:add 1 new", "triage:add 1 due", "block:update 1")
	s.Block(b, "10-29 09:00-10:00")
}

func TestUND24(t *testing.T) {
	s, b, e, x := aiLogFixture(t)
	second := s.Log(1, "b", "10-26")
	before := s.Task(1).Reasons
	s.Revert(x)
	requireEventKeys(t, s, s.Last.OpID, "entry:tombstone 1")
	requireBlockState(t, s, b, "{worked, auto, 0, –}")
	firstRow, err := store.GetEntry(s.Ctx(), s.Store.Reader, e)
	if err != nil || !firstRow.DeletedAt.Valid {
		t.Fatal(firstRow, err)
	}
	secondRow, err := store.GetEntry(s.Ctx(), s.Store.Reader, second)
	if err != nil || secondRow.DeletedAt.Valid || !reflect.DeepEqual(before, s.Task(1).Reasons) {
		t.Fatal(secondRow, err)
	}
}

func TestRevertConflictsWithSystemEvent(t *testing.T) {
	s := testutil.NewScenario(t).At("10-26 09:00")
	id := s.Add("A")
	b := s.Block(id, "13:00-14:00")
	target := s.Last.OpID
	s.At("10-27 00:00").Reconcile()
	system := latestOp(t, s)
	e := revertRefusal(t, s, target, domain.CodeRevertConflict)
	if len(e.Conflicts) != 1 || e.Conflicts[0].OpID != system.ID || e.Conflicts[0].Actor != "system" || e.Conflicts[0].EntityID != int64(b) {
		t.Fatal(e)
	}
}

func TestRevertAlreadyReverted(t *testing.T) {
	s := testutil.NewScenario(t)
	s.Add("A")
	x := s.Last.OpID
	s.Revert(x)
	e := revertRefusal(t, s, x, domain.CodeRevertConflict)
	if e.Detail != "already reverted by owner" {
		t.Fatal(e)
	}
}

func TestRevertRunsRemovalChecks(t *testing.T) {
	s := testutil.NewScenario(t).At("10-27 08:00")
	id := s.Add("A")
	b1 := s.Block(id, "10-28 13:00-14:00")
	b2 := s.Block(id, "10-27 13:00-14:00")
	s.At("10-28 00:00").Reconcile()
	s.At("10-28 10:00").Unblock(b1)
	x := s.Last.OpID
	requireEventKeys(t, s, x, "block:update 1")
	s.Log(id, "work", "10-27")
	requireBlockState(t, s, b2, "{worked, auto, 0, –}")
	s.Revert(x)
	requireBlockState(t, s, b1, "{planned, –, 0, –}")
	if !slices.Equal(s.Reasons(id), []domain.TriageReason{domain.ReasonContinue}) {
		t.Fatal(s.Reasons(id))
	}
}

func TestRevertKeepsJustifiedReason(t *testing.T) {
	s := testutil.NewScenario(t).At("10-28 09:00")
	id := s.Add("A")
	b1, b2 := s.Block(id, "13:00-14:00"), s.Block(id, "15:00-16:00")
	s.At("10-28 10:00").Unblock(b1)
	u1 := s.Last.OpID
	s.Unblock(b2)
	requireEventKeys(t, s, s.Last.OpID, "block:update 2")
	s.Revert(u1)
	requireBlockState(t, s, b1, "{planned, –, 0, –}")
	if !slices.Equal(s.Reasons(id), []domain.TriageReason{domain.ReasonSlipped}) {
		t.Fatal(s.Reasons(id))
	}
	requireSlips(t, s, id, 1, 1)
	row := s.Task(id).Reasons[0]
	if row.Seq != s.Op(s.Last.OpID).Seq || !row.Since.Equal(s.Clock.Now()) {
		t.Fatal(row)
	}
}

func TestRevertEveryOpKind(t *testing.T) {
	task := func(s *testutil.Scenario) { s.Add("A") }
	block := func(s *testutil.Scenario) { task(s); s.Block(1, "10-29 13:00-14:00") }
	entry := func(s *testutil.Scenario) { task(s); s.Log(1, "work", "") }
	for _, tc := range []struct {
		kind      domain.OpKind
		setup, op func(*testutil.Scenario)
	}{
		{domain.OpTaskAdd, func(_ *testutil.Scenario) {}, func(s *testutil.Scenario) { s.Add("A", func(in *service.AddTaskIn) { in.Tags = []string{"infra"} }) }},
		{domain.OpTaskUpdate, task, func(s *testutil.Scenario) {
			s.Update(1, func(in *service.UpdateTaskIn) { title := "B"; in.Title = &title; in.TagsAdd = []string{"infra"} })
		}},
		{domain.OpTaskDue, task, func(s *testutil.Scenario) { s.SetDue(1, "10-30") }},
		{domain.OpTaskPlace, task, func(s *testutil.Scenario) { s.Place(1, domain.PlaceTop) }},
		{domain.OpTaskAcceptNew, task, func(s *testutil.Scenario) {
			s.Must(s.Svc.AcceptNew(s.Ctx(), s.Meta(), service.AcceptNewIn{Items: []service.AcceptItem{{ID: 1, Expect: s.Expect(1)}}}))
		}},
		{domain.OpTaskDefer, task, func(s *testutil.Scenario) { s.Defer(1, "11-02") }},
		{domain.OpTaskManual, func(s *testutil.Scenario) { s.Add("A", testutil.Bottom()) }, func(s *testutil.Scenario) { s.MarkManual(1) }},
		{domain.OpTaskComplete, block, func(s *testutil.Scenario) { s.Done(1) }},
		{domain.OpTaskReopen, func(s *testutil.Scenario) { task(s); s.Done(1) }, func(s *testutil.Scenario) { s.Reopen(1, true) }},
		{domain.OpTaskDrop, block, func(s *testutil.Scenario) { s.Drop(1) }},
		{domain.OpTaskDelete, block, func(s *testutil.Scenario) { s.Delete(1) }},
		{domain.OpTaskUndelete, func(s *testutil.Scenario) { task(s); s.Delete(1) }, func(s *testutil.Scenario) { s.Undelete(1) }},
		{domain.OpBlockCreate, task, func(s *testutil.Scenario) { s.Block(1, "10-29 13:00-14:00") }},
		{domain.OpBlockReblock, block, func(s *testutil.Scenario) { s.Reblock(1, "10-30 13:00-14:00") }},
		{domain.OpBlockUnblock, block, func(s *testutil.Scenario) { s.Unblock(1) }},
		{domain.OpBlockOutcome, func(s *testutil.Scenario) {
			s.At("10-27 09:00")
			task(s)
			s.Block(1, "13:00-14:00")
			s.At("10-28 10:00").Reconcile()
		}, func(s *testutil.Scenario) { s.Outcome(1, domain.SetWorked) }},
		{domain.OpEntryAdd, task, func(s *testutil.Scenario) { s.Log(1, "work", "") }},
		{domain.OpEntryUpdate, entry, func(s *testutil.Scenario) {
			s.EditEntry(1, func(in *service.UpdateEntryIn) { title := "edited"; in.Title = &title })
		}},
		{domain.OpEntryRelink, entry, func(s *testutil.Scenario) { s.Relink(1, 0) }},
		{domain.OpEntryDelete, entry, func(s *testutil.Scenario) { s.DeleteEntry(1) }},
		{domain.OpEntryUndelete, func(s *testutil.Scenario) { entry(s); s.DeleteEntry(1) }, func(s *testutil.Scenario) { s.UndeleteEntry(1) }},
		{domain.OpFocusSet, func(s *testutil.Scenario) { task(s); s.Add("B"); s.SetFocus(2, false) }, func(s *testutil.Scenario) { s.SetFocus(1, true) }},
		{domain.OpRevert, func(s *testutil.Scenario) { task(s); s.Place(1, domain.PlaceTop) }, func(s *testutil.Scenario) { s.Revert(s.Last.OpID) }},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			s := testutil.NewScenario(t)
			tc.setup(s)
			before := s.StateDigest()
			tc.op(s)
			target := s.Last.OpID
			if s.Op(target).Kind != tc.kind {
				t.Fatal("wrong op kind")
			}
			s.Revert(target)
			if after := s.StateDigest(); after != before {
				t.Fatalf("state after revert differs:\n%s\n---\n%s", before, after)
			}
		})
	}
}

func TestRevertedEffectsNameOriginalCompletion(t *testing.T) {
	s := testutil.NewScenario(t)
	s.Done(s.Add("A"))
	completion := s.Last.OpID
	check := func(effects []service.Effect, via string) {
		t.Helper()
		want := service.Effect{Kind: service.EffReverted, Via: via, From: completion.String()[:8], To: "task.complete"}
		if len(effects) == 0 || effects[0] != want {
			t.Fatalf("effects = %+v, want first effect %+v", effects, want)
		}
	}
	check(s.Undo().Effects, "undo")
	m := s.Meta()
	redo := s.Must(s.Svc.Redo(s.Ctx(), m))
	check(redo.Effects, "redo")
	check(s.Must(s.Svc.Redo(s.Ctx(), m)).Effects, "redo")
	detail, err := s.Svc.GetOp(s.Ctx(), s.Actor, redo.OpID)
	if err != nil {
		t.Fatal(err)
	}
	check(detail.Op.Effects, "redo")
	page, err := s.Svc.ListOps(s.Ctx(), s.Actor, service.ListOpsIn{OpPrefix: redo.OpID.String()})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("redo op page = %+v, err = %v", page, err)
	}
	check(page.Items[0].Effects, "redo")
	check(s.Revert(completion).Effects, "revert")
	restored := s.Undo()
	check(restored.Effects, "redo")
	detail, err = s.Svc.GetOp(s.Ctx(), s.Actor, restored.OpID)
	if err != nil {
		t.Fatal(err)
	}
	check(detail.Op.Effects, "redo")
	check(s.Redo().Effects, "undo")
}

func TestRevertOfUndoRestoresCompletion(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	s.Done(id)
	root := s.Last.OpID
	undo := s.Undo()
	res := s.Revert(undo.OpID)
	want := service.Effect{Kind: service.EffReverted, Via: "redo", From: root.String()[:8], To: "task.complete"}
	if len(res.Effects) == 0 || res.Effects[0] != want || s.Task(id).Status != domain.StatusDone {
		t.Fatalf("revert of undo: effects=%+v, status=%s", res.Effects, s.Task(id).Status)
	}
}

func TestUndoSurvivesRootLookupError(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	s.Done(id)
	target := s.Last.OpID
	// Make only the final, decorative GetOp fail to decode the target.
	_, err := s.Store.Writer.ExecContext(s.Ctx(), `CREATE TEMP TRIGGER break_root_decode
		AFTER UPDATE OF state ON ops WHEN NEW.state = 'reverted'
		BEGIN UPDATE ops SET scopes = 'unknown' WHERE op_id = NEW.op_id; END`)
	if err != nil {
		t.Fatal(err)
	}
	res := s.Undo()
	want := service.Effect{Kind: service.EffReverted, Via: "undo", From: target.String()[:8]}
	if len(res.Effects) == 0 || res.Effects[0] != want || s.Task(id).Status != domain.StatusOpen {
		t.Fatalf("undo: effects=%+v, status=%s", res.Effects, s.Task(id).Status)
	}
}

func TestRevertedEffectsRootHopLimit(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	s.Update(id, func(in *service.UpdateTaskIn) { in.Title = new("B") })
	root := s.Last.OpID
	for hop := 1; hop <= 9; hop++ {
		target := s.Last.OpID
		effects := s.Revert(target).Effects
		want := service.Effect{Kind: service.EffReverted, Via: "revert", From: root.String()[:8], To: "task.update"}
		if hop%2 == 0 {
			want.Via = "redo"
		}
		if hop == 9 {
			want.From, want.To = target.String()[:8], ""
		}
		if len(effects) == 0 || effects[0] != want {
			t.Fatalf("hop %d: effects = %+v, want first effect %+v", hop, effects, want)
		}
	}
}

func TestRevertNetZeroAndStateChain(t *testing.T) {
	s := testutil.NewScenario(t)
	s.Add("A")
	a := s.Last.OpID
	title := "B"
	s.Update(1, func(in *service.UpdateTaskIn) { in.Title = &title })
	b := s.Last.OpID
	s.Revert(b)
	r := s.Last.OpID
	s.Revert(r)
	u := s.Last.OpID
	s.Revert(u)
	d := s.Last.OpID
	if s.Op(b).State != domain.OpReverted || s.Op(r).State != domain.OpActive || s.Op(u).State != domain.OpReverted || s.Op(d).State != domain.OpActive {
		t.Fatal("incorrect alternating state chain")
	}
	s.Revert(a)
	if !s.Task(1).DeletedAt.Valid {
		t.Fatal("net-zero pairs caused conflict")
	}
}

func TestRevertFocusRulesAndEventless(t *testing.T) {
	s := testutil.NewScenario(t)
	a, b := s.Add("A"), s.Add("B")
	s.SetFocus(b, false)
	s.SetFocus(a, false)
	f := s.Last.OpID
	s.At("10-29 10:00").Reconcile()
	count := s.OpCount()
	r := s.Revert(f)
	if r.NoChange || r.OpID.IsZero() || len(s.Events(r.OpID)) != 0 || s.OpCount() != count+1 || s.Focus().TaskID.Valid || s.Op(f).State != domain.OpReverted {
		t.Fatal("eventless reversal was not committed")
	}
	s.SetFocus(a, false)
	f = s.Last.OpID
	s.SetFocus(b, false)
	s.Revert(f)
	if s.Focus().TaskID.Valid {
		t.Fatal("focus conflict handling failed")
	}
	s.Add("C")
	c := s.Last.OpID
	s.SetFocus(3, false)
	s.Revert(c)
	if s.Focus().TaskID.Valid {
		t.Fatal("invalidated focus was not cleared")
	}
}

func TestRevertReplayBeforeTargetAuthorization(t *testing.T) {
	s := testutil.NewScenario(t)
	ai := s.As(testutil.AI())
	ai.Log(0, "work", "")
	x := ai.Last.OpID
	m := ai.MetaWith("revert-key-1234567890", `{"op":"target"}`)
	first := ai.Must(ai.Svc.Revert(ai.Ctx(), m, service.RevertIn{OpID: x}))
	m.Actor.Scopes = 0
	replay := ai.Must(ai.Svc.Revert(ai.Ctx(), m, service.RevertIn{OpID: x}))
	if !replay.Replayed || replay.OpID != first.OpID {
		t.Fatal("replay selected or authorized target again")
	}
	_ = revertRefusal(t, s, domain.NewOpID(), domain.CodeNotFound)
}

func TestRevertTriageKeysAndVersions(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	s.MarkManual(id)
	x := s.Last.OpID
	s.SetDue(id, "10-28 17:00")
	s.Reconcile()
	before := s.Task(id)
	s.Revert(x)
	if !slices.Equal(s.Reasons(id), []domain.TriageReason{domain.ReasonDue, domain.ReasonNew}) || s.Task(id).Version != before.Version+1 {
		t.Fatal("triage reasons were not reversed as individual keys")
	}
	s.Update(id, func(in *service.UpdateTaskIn) { in.TagsAdd = []string{"infra"} })
	x = s.Last.OpID
	before = s.Task(id)
	s.Revert(x)
	if len(s.Task(id).Tags) != 0 || s.Task(id).Version != before.Version+1 {
		t.Fatal("tag revert did not bump task")
	}
}

func TestRevertKeepsJustifiedContinue(t *testing.T) {
	for _, later := range []bool{false, true} {
		t.Run(map[bool]string{false: "justified", true: "later_planned"}[later], func(t *testing.T) {
			s := testutil.NewScenario(t).At("10-28 09:00")
			id := s.Add("A")
			b1, b2 := s.Block(id, "13:00-14:00"), s.Block(id, "15:00-16:00")
			var future domain.BlockID
			if later {
				future = s.Block(id, "10-29 13:00-14:00")
				s.Unblock(future)
			}
			s.At("10-28 17:00")
			s.Outcome(b1, domain.SetWorked)
			x := s.Last.OpID
			s.Outcome(b2, domain.SetWorked)
			requireEventKeys(t, s, s.Last.OpID, "block:update 2")
			// Reversing the earlier cancellation restores a later planned block
			// without placing the task or touching the continue reason.
			if later {
				ops, err := store.ListOps(s.Ctx(), s.Store.Reader, store.OpQuery{Limit: 100})
				if err != nil {
					t.Fatal(err)
				}
				for _, op := range ops {
					if op.Kind == domain.OpBlockUnblock {
						s.Revert(op.ID)
						break
					}
				}
			}
			s.Revert(x)
			requireBlockState(t, s, b1, "{planned, –, 0, –}")
			requireBlockState(t, s, b2, "{worked, user, 0, –}")
			if later {
				if len(s.Reasons(id)) != 0 {
					t.Fatal(s.Reasons(id))
				}
			} else {
				rows := s.Task(id).Reasons
				if len(rows) != 1 || rows[0].Reason != domain.ReasonContinue || rows[0].Seq != s.Op(s.Last.OpID).Seq || !rows[0].Since.Equal(s.Clock.Now()) {
					t.Fatal(rows)
				}
			}
		})
	}
}

func TestRevertRunsUnworkRemoval(t *testing.T) {
	s := testutil.NewScenario(t).At("10-28 09:00")
	id := s.Add("A")
	b1, b2 := s.Block(id, "13:00-14:00"), s.Block(id, "15:00-16:00")
	s.At("10-28 17:00")
	s.Outcome(b1, domain.SetWorked)
	s.Outcome(b2, domain.SetWorked)
	x := s.Last.OpID
	requireEventKeys(t, s, x, "block:update 2")
	s.Outcome(b1, domain.SetMissed)
	s.Revert(x)
	requireBlockState(t, s, b2, "{planned, –, 0, –}")
	if !slices.Equal(s.Reasons(id), []domain.TriageReason{domain.ReasonSlipped}) {
		t.Fatal(s.Reasons(id))
	}
}

func TestRevertDerivedLaterEventConflicts(t *testing.T) {
	s := testutil.NewScenario(t).At("10-26 09:00")
	id := s.Add("A")
	b := s.Block(id, "13:00-14:00")
	s.At("10-27 10:00").Reconcile()
	s.Outcome(b, domain.SetWorked)
	s.Outcome(b, domain.SetAuto)
	target := s.Last.OpID
	s.Log(id, "work", "10-26")
	later := s.Last.OpID
	e := revertRefusal(t, s, target, domain.CodeRevertConflict)
	if !slices.ContainsFunc(e.Conflicts, func(c domain.RevertConflict) bool { return c.OpID == later && c.Entity == domain.EntBlock }) {
		t.Fatal(e)
	}
}

func TestRevertAlreadyRevertedNamesOriginalActor(t *testing.T) {
	s := testutil.NewScenario(t)
	ai := s.As(testutil.AI())
	ai.Log(0, "work", "")
	x := ai.Last.OpID
	s.Revert(x)
	e := revertRefusal(t, ai, x, domain.CodeRevertConflict)
	if e.Detail != "already reverted by owner" {
		t.Fatal(e)
	}
}

func TestRevertTriageSameReasonConflicts(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	s.MarkManual(id)
	target := s.Last.OpID
	s.Place(id, domain.PlaceKeep)
	later := s.Last.OpID
	e := revertRefusal(t, s, target, domain.CodeRevertConflict)
	if len(e.Conflicts) != 1 || e.Conflicts[0].OpID != later || e.Conflicts[0].Entity != domain.EntTriage {
		t.Fatal(e)
	}
}

func TestRevertInterruptionRoundTrip(t *testing.T) {
	s := testutil.NewScenario(t)
	a, b := s.Add("A"), s.Add("B")
	s.SetFocus(a, false)
	s.SetFocus(b, true)
	target := s.Last.OpID
	before := s.StateDigest()
	s.Revert(target)
	if len(s.Interruptions("10-28")) != 0 || s.Focus().TaskID.V != a {
		t.Fatal("interruption was not tombstoned")
	}
	s.Revert(s.Last.OpID)
	if s.StateDigest() != before {
		t.Fatal("interruption/focus restoration differs")
	}
}

func TestRevertRefusalPreservesReconcile(t *testing.T) {
	s := testutil.NewScenario(t).At("10-26 09:00")
	id := s.Add("A")
	b := s.Block(id, "13:00-14:00")
	target := s.Last.OpID
	count := s.OpCount()
	s.At("10-27 00:00")
	_, err := s.Svc.Revert(s.Ctx(), s.Meta(), service.RevertIn{OpID: target})
	testutil.RequireCode(t, err, domain.CodeRevertConflict)
	requireBlockState(t, s, b, "{missed, auto, 1, –}")
	if s.OpCount() != count+1 || latestOp(t, s).Actor != "system" || s.Op(target).State != domain.OpActive {
		t.Fatal("refusal discarded reconcile or kept failed revert")
	}
}

func TestRevertInactiveWithoutReversalIsConflict(t *testing.T) {
	s := testutil.NewScenario(t)
	s.Add("A")
	x := s.Last.OpID
	// Raw fixture (F14): an inactive op with no recorded reversal is unreachable through real ops.
	if _, err := s.Store.Writer.ExecContext(s.Ctx(), `UPDATE ops SET state = 'reverted' WHERE op_id = ?`, x); err != nil {
		t.Fatal(err)
	}
	_ = revertRefusal(t, s, x, domain.CodeRevertConflict)
}
