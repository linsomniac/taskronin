package service_test

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/service"
	"github.com/linsomniac/taskronin/internal/store"
	"github.com/linsomniac/taskronin/internal/testutil"
)

func completionEntries(t *testing.T, s *testutil.Scenario, id domain.TaskID) []domain.LogEntry {
	t.Helper()
	entries, err := store.CompleteEntries(s.Ctx(), s.Store.Reader, id)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func requireCompleted(t *testing.T, s *testutil.Scenario, id domain.TaskID, date, title, notes string, version domain.Version) {
	t.Helper()
	v, r := s.Task(id), s.Last
	if v.Status != domain.StatusDone || !v.CompletedAt.Valid || !v.CompletedAt.V.Equal(s.Clock.Now()) ||
		!v.CompletedDate.Valid || v.CompletedDate.V != testutil.D(date) {
		t.Fatalf("completed task = %+v", v)
	}
	requirePlaced(t, s, id, version)
	if r.Task == nil || r.Task.ID != id || r.Entry == nil || r.Created || r.NoChange ||
		!slices.Equal(r.Refs, []domain.EntityRef{{Entity: domain.EntTask, ID: int64(id)}, {Entity: domain.EntEntry, ID: int64(r.Entry.ID)}}) {
		t.Fatalf("completion result = %+v", r)
	}
	e := r.Entry
	if e.TaskID != (sql.Null[domain.TaskID]{V: id, Valid: true}) || e.Kind != domain.EntryComplete ||
		e.Title != title || e.Notes != notes || e.WorkDate != testutil.D(date) || e.OccurredMin.Valid ||
		e.Reopened || e.DeletedAt.Valid || e.Version != 1 || !e.CreatedAt.Equal(s.Clock.Now()) || !e.UpdatedAt.Equal(s.Clock.Now()) {
		t.Fatalf("completion entry = %+v", e)
	}
	if op := s.Op(r.OpID); op.Kind != domain.OpTaskComplete || !op.Revertible || !slices.Contains(s.Stack(s.Actor.Key(), store.StackUndo), r.OpID) {
		t.Fatalf("completion op = %+v", op)
	}
	if !slices.ContainsFunc(r.Effects, func(e service.Effect) bool { return e.Kind == service.EffCompleted }) ||
		slices.ContainsFunc(r.Effects, func(e service.Effect) bool { return e.Kind == service.EffUpdated }) {
		t.Fatalf("completion effects = %+v", r.Effects)
	}
}

func requireReopened(t *testing.T, s *testutil.Scenario, before domain.TaskView) {
	t.Helper()
	v := s.Task(before.ID)
	if v.Status != domain.StatusOpen || v.CompletedAt.Valid || v.CompletedDate.Valid ||
		v.DueAck != domain.DueNone || v.DueAckDate.Valid || v.LastPlacedAt != before.LastPlacedAt ||
		v.LastPlacedSeq != before.LastPlacedSeq || v.Rank != before.Rank || v.Version != before.Version+1 {
		t.Fatalf("reopened task = %+v, before = %+v", v, before)
	}
	if s.Last.Task == nil || s.Last.Task.ID != before.ID || s.Last.Created || s.Last.NoChange {
		t.Fatalf("reopen result = %+v", s.Last)
	}
	if op := s.Op(s.Last.OpID); op.Kind != domain.OpTaskReopen || !op.Revertible || !slices.Contains(s.Stack(s.Actor.Key(), store.StackUndo), op.ID) {
		t.Fatalf("reopen op = %+v", op)
	}
}

func TestTRI15(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	s.Done(id)
	requireCompleted(t, s, id, "10-28", "A", "", 2)
	requireEventKeys(t, s, s.Last.OpID, "task:update 1", "entry:insert 1", "triage:remove 1 new", "task:update 1")
}

func TestLIF01(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("Set up keycloak")
	s.SetFocus(id, false)
	before := s.Task(id)
	s.Done(id)
	requireCompleted(t, s, id, "10-28", "Set up keycloak", "", before.Version+1)
	requireFocus(t, s, 0)
	requireEventKeys(t, s, s.Last.OpID, "task:update 1", "entry:insert 1", "triage:remove 1 new", "task:update 1", "focus:set")
	var changed map[string]any
	if err := json.Unmarshal(s.Events(s.Last.OpID)[0].After, &changed); err != nil {
		t.Fatal(err)
	}
	if len(changed) != 3 || changed["status"] != "done" || changed["completed_date"] != "2026-10-28" || changed["completed_at"] != float64(s.Clock.Now().UnixMilli()) {
		t.Fatalf("completion event = %v", changed)
	}
}

func TestLIF02(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("Run ansible against dev qt cluster")
	title := "Add svc-cluster weencrypt name to certs"
	s.Done(id, func(in *service.CompleteIn) { in.As = &title })
	requireCompleted(t, s, id, "10-28", title, "", 2)
	if s.Task(id).Title != title {
		t.Fatal("task was not retitled")
	}
	e := s.Events(s.Last.OpID)[0]
	if string(e.Before) != `{"title":"Run ansible against dev qt cluster"}` || string(e.After) != `{"title":"Add svc-cluster weencrypt name to certs"}` {
		t.Fatalf("retitle images = %s / %s", e.Before, e.After)
	}
	requireEventKeys(t, s, s.Last.OpID, "task:update 1", "task:update 1", "entry:insert 1", "triage:remove 1 new", "task:update 1")
	// F8: Task 34 adds the report rendering assertion for this same catalog ID.
}

func TestLIF03(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	s.Done(id, func(in *service.CompleteIn) { in.LogTitle, in.LogNotes = "Failover tested", "notes" })
	requireCompleted(t, s, id, "10-28", "Failover tested", "notes", 2)
	if s.Task(id).Title != "A" {
		t.Fatal("log title changed task title")
	}
}

func completionDay(t *testing.T) (*testutil.Scenario, domain.TaskID, []domain.BlockID) {
	t.Helper()
	s := testutil.NewScenario(t).At("10-28 09:00")
	id := s.Add("A")
	blocks := []domain.BlockID{s.Block(id, "10-28 13:00-15:00"), s.Block(id, "10-28 16:00-17:00"), s.Block(id, "10-29 09:00-10:00")}
	s.At("10-28 14:00")
	return s, id, blocks
}

func TestLIF04(t *testing.T) {
	s, id, blocks := completionDay(t)
	before := s.Task(id)
	s.Done(id)
	requireCompleted(t, s, id, "10-28", "A", "", before.Version+1)
	for i, b := range blocks {
		want := "{cancelled, –, 0, task_closed}"
		if i == 0 {
			want = "{done, auto, 0, –}"
		}
		requireBlockState(t, s, b, want)
		v := s.BlockView(b)
		if v.Version != 2 || !v.ResolvedAt.Valid || !v.ResolvedAt.V.Equal(s.Clock.Now()) || v.ResolvedSeq.V != s.Op(s.Last.OpID).Seq {
			t.Fatalf("resolved block = %+v", v)
		}
	}
	requireSlips(t, s, id, 0, 0)
	requireEventKeys(t, s, s.Last.OpID, "task:update 1", "entry:insert 1", "block:update 1", "block:update 2", "block:update 3", "task:update 1")
}

func backdatedCompletion(t *testing.T, userMon bool) (*testutil.Scenario, domain.TaskID, []domain.BlockID) {
	t.Helper()
	s := testutil.NewScenario(t).At("10-26 08:00")
	id := s.Add("A")
	blocks := []domain.BlockID{s.Block(id, "10-26 09:00-10:00"), s.Block(id, "10-27 09:00-10:00"), s.Block(id, "10-28 09:00-10:00"),
		s.Block(id, "10-29 14:00-16:00"), s.Block(id, "10-30 09:00-10:00")}
	s.At("10-27 08:00").Log(id, "worked tue", "10-27")
	s.At("10-29 10:00").Reconcile()
	if userMon {
		s.Outcome(blocks[0], domain.SetMissed)
	}
	// AIDEV-NOTE: F14 fixture shortcut: the future planned blocks suppress
	// continue, but LIF-05/06 explicitly require {slipped, continue} as Given.
	seq := latestOp(t, s).Seq
	s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), []domain.EntityRef{{Entity: domain.EntTask, ID: int64(id)}}, func(q store.Q) error {
		_, err := store.AddReason(s.Ctx(), q, domain.TriageRow{TaskID: id, Reason: domain.ReasonContinue, Since: s.Clock.Now(), Seq: seq})
		return err
	}))
	requireDueReasons(t, s, id, domain.ReasonSlipped, domain.ReasonContinue)
	return s, id, blocks
}

func checkBackdatedCompletion(t *testing.T, userMon bool) {
	t.Helper()
	s, id, blocks := backdatedCompletion(t, userMon)
	before, mon, tue := s.Task(id), s.BlockView(blocks[0]).TimeBlock, s.BlockView(blocks[1]).TimeBlock
	s.Done(id, func(in *service.CompleteIn) { in.Date = testutil.D("10-26") })
	requireCompleted(t, s, id, "10-26", "A", "", before.Version+1)
	wantMon, slips := "{done, auto, 0, –}", 0
	if userMon {
		wantMon, slips = "{missed, user, 1, –}", 1
		if s.BlockView(blocks[0]).TimeBlock != mon {
			t.Fatal("user outcome changed")
		}
	}
	requireBlockState(t, s, blocks[0], wantMon)
	requireBlockState(t, s, blocks[1], "{worked, auto, 0, –}")
	if s.BlockView(blocks[1]).TimeBlock != tue {
		t.Fatal("worked block changed")
	}
	for _, b := range blocks[2:] {
		requireBlockState(t, s, b, "{cancelled, –, 0, task_closed}")
	}
	requireSlips(t, s, id, slips, 0)
}

func TestLIF05(t *testing.T) { checkBackdatedCompletion(t, false) }

func TestLIF06(t *testing.T) { checkBackdatedCompletion(t, true) }

func TestBLK22(t *testing.T) {
	s := testutil.NewScenario(t).At("10-27 09:00")
	id := s.Add("A")
	b := s.Block(id, "10-27 13:00-15:00")
	s.At("10-27 14:00").Done(id, func(in *service.CompleteIn) { in.Date = testutil.D("10-27") })
	requireBlockState(t, s, b, "{done, auto, 0, –}")
	before, block, count := s.Task(id), s.BlockView(b).TimeBlock, s.OpCount()
	s.At("10-28 00:00").Reconcile()
	if !reflect.DeepEqual(s.Task(id), before) || s.BlockView(b).TimeBlock != block || s.OpCount() != count {
		t.Fatal("reconcile changed an already completed block")
	}
}

func TestSLP09(t *testing.T) {
	s := testutil.NewScenario(t).At("10-26 08:00")
	id := s.Add("A")
	b := s.Block(id, "10-26 09:00-10:00")
	s.At("10-26 10:00").Log(id, "work", "10-26")
	s.At("10-28 10:00").Reconcile()
	requireBlockState(t, s, b, "{worked, auto, 0, –}")
	requireDueReasons(t, s, id, domain.ReasonContinue)
	s.Done(id, func(in *service.CompleteIn) { in.Date = testutil.D("10-26") })
	requireBlockState(t, s, b, "{done, auto, 0, –}")
	requireDueReasons(t, s, id)
	requireSlips(t, s, id, 0, 0)
	if s.Last.Entry.WorkDate != testutil.D("10-26") {
		t.Fatal("completion date differs from the work date")
	}
	requireEventKeys(t, s, s.Last.OpID, "task:update 1", "entry:insert 2", "triage:remove 1 continue", "task:update 1", "block:update 1 [derived]")
}

func TestSLP16(t *testing.T) {
	s := testutil.NewScenario(t).At("10-26 08:00")
	id := s.Add("A")
	b := s.Block(id, "10-26 09:00-10:00")
	s.At("10-27 10:00").Reconcile()
	requireDueReasons(t, s, id, domain.ReasonSlipped)
	s.Done(id)
	requireBlockState(t, s, b, "{missed, auto, 1, –}")
	requireSlips(t, s, id, 1, 0)
	s.At("10-28 10:00").Log(id, "x", "10-26")
	requireBlockState(t, s, b, "{worked, auto, 0, –}")
	requireSlips(t, s, id, 0, 0)
	requireDueReasons(t, s, id)
	if !slices.Equal(s.Last.Warnings, []string{"logged against a done task"}) {
		t.Fatalf("warnings = %v", s.Last.Warnings)
	}
	requireEventKeys(t, s, s.Last.OpID, "entry:insert 2", "block:update 1 [derived]")
}

func TestLIF12(t *testing.T) {
	s := testutil.NewScenario(t).At("10-27 08:00")
	id := s.Add("A")
	b := s.Block(id, "10-27 09:00-10:00")
	s.At("10-28 10:00").Reconcile()
	s.SetFocus(id, false)
	s.Done(id)
	before, entry := s.Task(id), *s.Last.Entry
	s.Reopen(id, false)
	requireReopened(t, s, before)
	requireDueReasons(t, s, id, domain.ReasonReopened)
	requireSlips(t, s, id, 1, 0)
	requireBlockState(t, s, b, "{missed, auto, 1, –}")
	e := completionEntries(t, s, id)[0]
	if !e.Reopened || e.DeletedAt.Valid || e.Version != entry.Version+1 || e.Title != entry.Title || e.WorkDate != entry.WorkDate {
		t.Fatalf("reopened entry = %+v", e)
	}
	status, err := service.DeriveForTest(s.Ctx(), s.Store.Reader, id, testutil.D("10-28"))
	if err != nil || status != domain.BlockDone {
		t.Fatalf("reopened completion derives %s: %v", status, err)
	}
	requireFocus(t, s, 0)
	requireEventKeys(t, s, s.Last.OpID, "task:update 1", "entry:update 1", "triage:add 1 reopened")
}

func TestLIF13(t *testing.T) {
	s, id, blocks := completionDay(t)
	s.Done(id)
	before := s.Task(id)
	rows, err := store.BlocksOfTask(s.Ctx(), s.Store.Reader, id)
	if err != nil {
		t.Fatal(err)
	}
	s.Reopen(id, false)
	requireReopened(t, s, before)
	requireBlockState(t, s, blocks[0], "{done, auto, 0, –}")
	for i, b := range blocks {
		if s.BlockView(b).TimeBlock != rows[i] {
			t.Fatal("reopen changed a block")
		}
	}
}

func TestLIF14(t *testing.T) {
	for _, actor := range []domain.Actor{testutil.Owner(), testutil.AI()} {
		t.Run(actor.Key(), func(t *testing.T) {
			s, id, blocks := completionDay(t)
			s.Done(id)
			before, entry, count := s.Task(id), *s.Last.Entry, s.OpCount()
			a := s.As(actor)
			r, err := a.Svc.Reopen(a.Ctx(), a.Meta(), service.ReopenIn{ID: id, Expect: a.Expect(id), RemoveEntry: true})
			if actor.Kind == domain.ActorToken {
				testutil.RequireCode(t, err, domain.CodeForbidden)
				if s.OpCount() != count || !reflect.DeepEqual(s.Task(id), before) || completionEntries(t, s, id)[0] != entry.LogEntry {
					t.Fatal("forbidden reopen changed state")
				}
				return
			}
			s.Must(r, err)
			requireReopened(t, s, before)
			e := completionEntries(t, s, id)[0]
			if !e.Reopened || !e.DeletedAt.Valid || !e.DeletedAt.V.Equal(s.Clock.Now()) || e.Version != entry.Version+1 {
				t.Fatalf("removed completion = %+v", e)
			}
			requireBlockState(t, s, blocks[0], "{missed, auto, 1, –}")
			b := s.BlockView(blocks[0])
			if !b.SlippedAt.Valid || !b.SlippedAt.V.Equal(s.Clock.Now()) || b.SlippedSeq.V <= before.LastPlacedSeq.V {
				t.Fatalf("new slip = %+v", b)
			}
			for _, b := range blocks[1:] {
				requireBlockState(t, s, b, "{cancelled, –, 0, task_closed}")
			}
			requireDueReasons(t, s, id, domain.ReasonSlipped, domain.ReasonReopened)
			requireSlips(t, s, id, 1, 1)
			requireEventKeys(t, s, s.Last.OpID, "task:update 1", "entry:update 1", "entry:tombstone 1", "triage:add 1 reopened", "block:update 1 [derived]", "triage:add 1 slipped [derived]")
		})
	}
}

func TestLIF15(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	s.Done(id)
	e := s.Last.Entry.ID
	_, err := s.Svc.DeleteEntry(s.Ctx(), s.Meta(), service.EntryRef{ID: e, Expect: s.EntryExpect(e)})
	testutil.RequireCode(t, err, domain.CodeConflict)
	if !strings.Contains(err.Error(), "use reopen --remove-entry") {
		t.Fatalf("delete error = %v", err)
	}
	for _, field := range []string{"title", "date"} {
		in := service.UpdateEntryIn{ID: e, Expect: s.EntryExpect(e)}
		if field == "title" {
			title := "edited"
			in.Title = &title
		} else {
			date := testutil.D("10-27")
			in.WorkDate = &date
		}
		_, err := s.Svc.UpdateEntry(s.Ctx(), s.Meta(), in)
		testutil.RequireCode(t, err, domain.CodeValidation)
	}
	notes := "edited notes"
	_, err = s.Svc.UpdateEntry(s.Ctx(), s.As(testutil.AI()).Meta(), service.UpdateEntryIn{ID: e, Expect: s.EntryExpect(e), Notes: &notes})
	testutil.RequireCode(t, err, domain.CodeForbidden)
	s.EditEntry(e, func(in *service.UpdateEntryIn) { in.Notes = &notes })
	if completionEntries(t, s, id)[0].Notes != notes {
		t.Fatal("notes edit was lost")
	}
	s.Reopen(id, false)
	s.DeleteEntry(e)
	if entry := completionEntries(t, s, id)[0]; !entry.Reopened || !entry.DeletedAt.Valid {
		t.Fatalf("completion after delete = %+v", entry)
	}
}

func TestLIF17(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	s.Done(id)
	s.Reopen(id, false)
	c1 := completionEntries(t, s, id)[0]
	version := s.Task(id).Version
	s.At("10-29 10:00").Done(id)
	requireCompleted(t, s, id, "10-29", "A", "", version+1)
	entries := completionEntries(t, s, id)
	if len(entries) != 2 || entries[0] != c1 || !entries[0].Reopened || entries[0].WorkDate != testutil.D("10-28") || entries[1].Reopened || entries[1].WorkDate != testutil.D("10-29") {
		t.Fatalf("completions = %+v", entries)
	}
	// F8: Task 34 adds report lines, including C1's reopened annotation.
}

func TestLIF18(t *testing.T) {
	s := testutil.NewScenario(t).At("10-26 08:00")
	id := s.Add("A", testutil.Due("10-26"))
	s.At("10-28 10:00").Reconcile()
	s.Done(id)
	if s.Task(id).DueAck != domain.DueOverdue {
		t.Fatal("completion did not acknowledge overdue")
	}
	s.Reopen(id, false)
	if s.Last.Task.DueAck != domain.DueNone || s.Last.Task.DueAckDate.Valid {
		t.Fatal("reopen did not clear due acknowledgement")
	}
	s.Reconcile()
	requireDueReasons(t, s, id, domain.ReasonOverdue, domain.ReasonDue, domain.ReasonReopened)
}

func TestLIF19(t *testing.T) {
	s := testutil.NewScenario(t)
	id, other := s.Add("A"), s.Add("B")
	s.Done(id)
	e := s.Last.Entry.ID
	s.Reopen(id, false)
	before, count := completionEntries(t, s, id)[0], s.OpCount()
	_, err := s.Svc.RelinkEntry(s.Ctx(), s.Meta(), service.RelinkEntryIn{ID: e, Expect: s.EntryExpect(e), TaskID: sql.Null[domain.TaskID]{V: other, Valid: true}})
	testutil.RequireCode(t, err, domain.CodeValidation)
	if s.OpCount() != count || completionEntries(t, s, id)[0] != before {
		t.Fatal("rejected relink changed completion")
	}
}

func TestLIF20(t *testing.T) {
	s, id, blocks := backdatedCompletion(t, false)
	s.Done(id, func(in *service.CompleteIn) { in.Date = testutil.D("10-26") })
	placed := s.Task(id).LastPlacedSeq
	s.At("10-30 09:00").Reopen(id, false)
	s.Reconcile()
	requireDueReasons(t, s, id, domain.ReasonReopened)
	requireSlips(t, s, id, 0, 0)
	requireBlockState(t, s, blocks[0], "{done, auto, 0, –}")
	requireBlockState(t, s, blocks[2], "{cancelled, –, 0, task_closed}")
	// AIDEV-NOTE: F14 fixture shortcut: Block is a placement and would clear
	// reopened. LIF-20 requires a fresh block without another placement.
	b := domain.TimeBlock{TaskID: id, Span: blockInput(s, id, "10-30 13:00-14:00").Span, Status: domain.BlockPlanned, CreatedAt: s.Clock.Now()}
	s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), nil, func(q store.Q) error { return store.InsertBlock(s.Ctx(), q, &b) }))
	s.At("10-30 10:00").Unblock(b.ID)
	requireSlips(t, s, id, 1, 1)
	requireDueReasons(t, s, id, domain.ReasonSlipped, domain.ReasonReopened)
	if s.Task(id).LastPlacedSeq != placed || s.BlockView(b.ID).SlippedSeq.V <= placed.V {
		t.Fatal("new slip was acknowledged by reopen")
	}
}

func TestIDM07(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	m := s.Meta()
	in := service.CompleteIn{ID: id, Expect: s.Expect(id)}
	first := s.Must(s.Svc.Complete(s.Ctx(), m, in))
	s.Reopen(id, false)
	before, entries, count := s.Task(id), completionEntries(t, s, id), s.OpCount()
	replay := s.Must(s.Svc.Complete(s.Ctx(), m, in))
	if !replay.Replayed || replay.OpID != first.OpID || replay.Task == nil || replay.Task.Status != domain.StatusOpen ||
		replay.Entry == nil || !replay.Entry.Reopened || !slices.Equal(replay.Effects, first.Effects) ||
		!reflect.DeepEqual(s.Task(id), before) || !slices.Equal(completionEntries(t, s, id), entries) || s.OpCount() != count {
		t.Fatalf("completion replay = %+v", replay)
	}
}

func TestREC11(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	s.SetFocus(id, false)
	a := s.As(testutil.AI())
	a.Done(id)
	requireFocus(t, s, 0)
	count := s.OpCount()
	s.Reconcile()
	requireFocus(t, s, 0)
	if s.OpCount() != count || s.Task(id).Status != domain.StatusDone || s.Op(a.Last.OpID).Channel != domain.ChanMCP {
		t.Fatal("reconcile changed the completed task/focus")
	}
}

func TestBLK41Done(t *testing.T) {
	s, id, blocks := completionDay(t)
	s.Done(id)
	s.Reopen(id, false)
	before, block := s.Task(id), s.BlockView(blocks[0]).TimeBlock
	next := s.Reblock(blocks[0], "10-29 09:00-10:00")
	if next == blocks[0] || s.BlockView(blocks[0]).TimeBlock != block {
		t.Fatal("reblock changed the done row")
	}
	requireBlockState(t, s, blocks[0], "{done, auto, 0, –}")
	requireReblocked(t, s, before, next, "10-29 09:00-10:00")
}

func TestCompleteValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		mod  func(*service.CompleteIn)
	}{
		{"future", func(in *service.CompleteIn) { in.Date = testutil.D("10-29") }},
		{"as_empty", func(in *service.CompleteIn) { v := " \t"; in.As = &v }},
		{"as_control", func(in *service.CompleteIn) { v := "bad\u202e"; in.As = &v }},
		{"log_empty", func(in *service.CompleteIn) { in.LogTitle = " \t" }},
		{"log_utf8", func(in *service.CompleteIn) { in.LogTitle = "\xff" }},
		{"log_long", func(in *service.CompleteIn) { in.LogTitle = strings.Repeat("é", 501) }},
		{"notes_control", func(in *service.CompleteIn) { in.LogNotes = "bad\u0085" }},
		{"notes_long", func(in *service.CompleteIn) { in.LogNotes = strings.Repeat("x", 65537) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, id, blocks := completionDay(t)
			before, block, count := s.Task(id), s.BlockView(blocks[0]).TimeBlock, s.OpCount()
			title := "retitled"
			in := service.CompleteIn{ID: id, Expect: s.Expect(id), As: &title}
			tc.mod(&in)
			_, err := s.Svc.Complete(s.Ctx(), s.Meta(), in)
			testutil.RequireCode(t, err, domain.CodeValidation)
			if !reflect.DeepEqual(s.Task(id), before) || s.BlockView(blocks[0]).TimeBlock != block || s.OpCount() != count || len(completionEntries(t, s, id)) != 0 {
				t.Fatal("invalid completion was not atomic")
			}
		})
	}
	t.Run("normalization", func(t *testing.T) {
		s := testutil.NewScenario(t)
		id := s.Add("A", testutil.DeferTo("10-29"))
		title := "  Finished  "
		s.Done(id, func(in *service.CompleteIn) { in.As, in.LogNotes = &title, "line 1\r\nline 2" })
		requireCompleted(t, s, id, "10-28", "Finished", "line 1\nline 2", 2)
	})
	t.Run("text_limits", func(t *testing.T) {
		s := testutil.NewScenario(t)
		id := s.Add("A")
		title, notes := strings.Repeat("é", 500), strings.Repeat("x", 65536)
		s.Done(id, func(in *service.CompleteIn) { in.As, in.LogNotes = &title, notes })
		requireCompleted(t, s, id, "10-28", title, notes, 2)
	})
}

func TestLifecyclePreconditions(t *testing.T) {
	for _, complete := range []bool{true, false} {
		for _, check := range []string{"missing", "stale", "scope", "status", "idem"} {
			t.Run(map[bool]string{true: "complete", false: "reopen"}[complete]+"/"+check, func(t *testing.T) {
				s := testutil.NewScenario(t)
				id := s.Add("A")
				if complete == (check == "status") {
					s.Done(id)
				}
				m, expect := s.Meta(), s.Expect(id)
				code := domain.CodeConflict
				switch check {
				case "missing":
					expect, code = domain.Expect{}, domain.CodePreconditionReq
				case "stale":
					expect.Version++
					code = domain.CodePrecondition
				case "scope":
					m.Actor.Scopes, code = domain.ScopeRead, domain.CodeForbidden
				case "idem":
					m.Idem.Key, code = "", domain.CodeIdemRequired
				}
				before, entries, count := s.Task(id), completionEntries(t, s, id), s.OpCount()
				var err error
				if complete {
					_, err = s.Svc.Complete(s.Ctx(), m, service.CompleteIn{ID: id, Expect: expect})
				} else {
					_, err = s.Svc.Reopen(s.Ctx(), m, service.ReopenIn{ID: id, Expect: expect})
				}
				testutil.RequireCode(t, err, code)
				if !reflect.DeepEqual(s.Task(id), before) || !slices.Equal(completionEntries(t, s, id), entries) || s.OpCount() != count {
					t.Fatal("failed lifecycle operation changed state")
				}
			})
		}
	}
}

func TestReopenPurgedCompletion(t *testing.T) {
	for _, live := range []bool{true, false} {
		for _, remove := range []bool{true, false} {
			t.Run(map[bool]string{true: "live", false: "purged"}[live]+"/"+map[bool]string{true: "remove", false: "keep"}[remove], func(t *testing.T) {
				s := testutil.NewScenario(t)
				id := s.Add("A")
				s.Done(id)
				current := s.Last.Entry.LogEntry
				// AIDEV-NOTE: F14 fixture shortcut: purge/ledger replay arrives
				// in Task 37 and can leave multiple purged unreopened completions.
				var newer domain.LogEntry
				s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), nil, func(q store.Q) error {
					newer = current
					newer.DeletedAt, newer.PurgedAt = sql.Null[time.Time]{V: s.Clock.Now(), Valid: true}, sql.Null[time.Time]{V: s.Clock.Now(), Valid: true}
					newer.Title, newer.Notes = "[purged]", ""
					// A lower work date ensures the fallback orders by ID, not date.
					newer.WorkDate = testutil.D("10-27")
					if err := store.InsertEntry(s.Ctx(), q, &newer); err != nil {
						return err
					}
					ignored := newer
					ignored.PurgedAt = sql.Null[time.Time]{}
					if err := store.InsertEntry(s.Ctx(), q, &ignored); err != nil {
						return err
					}
					ignored = newer
					ignored.Reopened = true
					if err := store.InsertEntry(s.Ctx(), q, &ignored); err != nil {
						return err
					}
					if !live {
						_, err := store.ScrubEntities(s.Ctx(), q, nil, []domain.EntryID{current.ID}, s.Clock.Now())
						return err
					}
					return nil
				}))
				before := completionEntries(t, s, id)
				s.Reopen(id, remove)
				after := completionEntries(t, s, id)
				selected := 1
				if live {
					selected = 0
				}
				for i, e := range after {
					if i != selected {
						if e != before[i] {
							t.Fatal("unselected completion changed")
						}
						continue
					}
					if !e.Reopened || e.Version != before[i].Version+1 || e.Title != before[i].Title || e.Notes != before[i].Notes || e.PurgedAt != before[i].PurgedAt ||
						(!live && e.DeletedAt != before[i].DeletedAt) || (live && e.DeletedAt.Valid != remove) {
						t.Fatalf("selected completion = %+v, before = %+v", e, before[i])
					}
				}
				if !live {
					requireEventKeys(t, s, s.Last.OpID, "task:update 1", "entry:update 2", "triage:add 1 reopened")
				}
			})
		}
	}
}

func TestReopenDropped(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "keep", true: "remove"}[remove], func(t *testing.T) {
			s := testutil.NewScenario(t)
			id := s.Add("A", testutil.Bottom())
			// AIDEV-NOTE: F14 fixture shortcut: Drop is implemented in Task 29.
			s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), []domain.EntityRef{{Entity: domain.EntTask, ID: int64(id)}}, func(q store.Q) error {
				task, err := store.GetTask(s.Ctx(), q, id)
				if err != nil {
					return err
				}
				task.Status = domain.StatusDropped
				return store.UpdateTask(s.Ctx(), q, task)
			}))
			before := s.Task(id)
			badTitle := " "
			_, err := s.Svc.Complete(s.Ctx(), s.Meta(), service.CompleteIn{ID: id, Expect: s.Expect(id), Date: testutil.D("10-29"), As: &badTitle})
			testutil.RequireCode(t, err, domain.CodeConflict)
			s.Reopen(id, remove)
			requireReopened(t, s, before)
			requireDueReasons(t, s, id, domain.ReasonReopened)
			if len(completionEntries(t, s, id)) != 0 {
				t.Fatal("reopening a dropped task created an entry")
			}
			requireEventKeys(t, s, s.Last.OpID, "task:update 1", "triage:add 1 reopened")
		})
	}
}

func TestCompleteBlockBoundaries(t *testing.T) {
	for _, tc := range []struct{ now, want string }{
		{"10-28 12:59", "{cancelled, –, 0, task_closed}"},
		{"10-28 13:00", "{done, auto, 0, –}"},
		{"10-28 15:00", "{done, auto, 0, –}"},
	} {
		t.Run(tc.now, func(t *testing.T) {
			s := testutil.NewScenario(t).At("10-28 09:00")
			id := s.Add("A")
			b := s.Block(id, "10-28 13:00-15:00")
			s.At(tc.now).Done(id)
			requireBlockState(t, s, b, tc.want)
			requireSlips(t, s, id, 0, 0)
		})
	}
	t.Run("started_after_backdate", func(t *testing.T) {
		s := testutil.NewScenario(t).At("10-28 09:00")
		id := s.Add("A")
		b := s.Block(id, "10-28 13:00-15:00")
		s.At("10-28 14:00").Done(id, func(in *service.CompleteIn) { in.Date = testutil.D("10-27") })
		requireBlockState(t, s, b, "{cancelled, –, 0, task_closed}")
		requireSlips(t, s, id, 0, 0)
	})
}

func TestCompletePreservesResolvedBlocks(t *testing.T) {
	s := testutil.NewScenario(t).At("10-26 08:00")
	id := s.Add("A")
	missed := s.Block(id, "10-27 09:00-10:00")
	worked := s.Block(id, "10-27 10:00-11:00")
	done := s.Block(id, "10-27 11:00-12:00")
	s.At("10-27 12:00").Outcome(missed, domain.SetMissed)
	s.Outcome(worked, domain.SetWorked)
	s.Done(id)
	s.Reopen(id, false)
	before, err := store.BlocksOfTask(s.Ctx(), s.Store.Reader, id)
	if err != nil {
		t.Fatal(err)
	}
	s.At("10-28 10:00").Done(id, func(in *service.CompleteIn) { in.Date = testutil.D("10-26") })
	requireBlockState(t, s, missed, "{missed, user, 1, –}")
	requireBlockState(t, s, worked, "{worked, user, 0, –}")
	requireBlockState(t, s, done, "{done, auto, 0, –}")
	after, err := store.BlocksOfTask(s.Ctx(), s.Store.Reader, id)
	if err != nil || !slices.Equal(after, before) {
		t.Fatalf("resolved blocks changed: %+v, %v", after, err)
	}
	requireSlips(t, s, id, 1, 0)
}

func TestReopenReplay(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	s.Done(id)
	m := s.As(testutil.AI()).Meta()
	in := service.ReopenIn{ID: id, Expect: s.Expect(id)}
	first := s.Must(s.Svc.Reopen(s.Ctx(), m, in))
	s.Done(id)
	before, entries, count := s.Task(id), completionEntries(t, s, id), s.OpCount()
	replay := s.Must(s.Svc.Reopen(s.Ctx(), m, in))
	if !replay.Replayed || replay.OpID != first.OpID || replay.Task.Status != domain.StatusDone ||
		!reflect.DeepEqual(s.Task(id), before) || !slices.Equal(completionEntries(t, s, id), entries) || s.OpCount() != count {
		t.Fatalf("reopen replay = %+v", replay)
	}
}
