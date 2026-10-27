package service_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/store"
	"taskronin/internal/testutil"
)

func requireBlockState(t *testing.T, s *testutil.Scenario, id domain.BlockID, want string) {
	t.Helper()
	if got := s.BlockState(id); got != want {
		t.Fatalf("block %d = %s, want %s", id, got, want)
	}
}

func requireSlips(t *testing.T, s *testutil.Scenario, id domain.TaskID, lifetime, since int) {
	t.Helper()
	if l, st := s.Slips(id); l != lifetime || st != since {
		t.Fatalf("L/S = %d/%d, want %d/%d", l, st, lifetime, since)
	}
}

func latestOp(t *testing.T, s *testutil.Scenario) domain.OpRecord {
	t.Helper()
	var id domain.OpID
	if err := s.Store.Reader.QueryRowContext(s.Ctx(), `SELECT op_id FROM ops ORDER BY seq DESC LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return s.Op(id)
}

func requireEventKeys(t *testing.T, s *testutil.Scenario, op domain.OpID, want ...string) {
	t.Helper()
	if got := s.EventKeys(op); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestBLK17(t *testing.T) {
	s := testutil.NewScenario(t).At("10-28 09:00")
	id := s.Add("A")
	b := s.Block(id, "10-28 13:00-15:00")
	count := s.OpCount()
	s.At("10-28 23:59").Reconcile()
	requirePlanned(t, s, b)
	if s.OpCount() != count || !s.BlockView(b).NeedsReblock {
		t.Fatal("ended block must remain planned without events until midnight")
	}
}

func TestBLK18(t *testing.T) {
	s := testutil.NewScenario(t).At("10-28 09:00")
	id := s.Add("A")
	b := s.Block(id, "10-28 13:00-15:00")
	before, count := s.Task(id), s.OpCount()
	s.At("10-29 00:00").Reconcile()
	requireBlockState(t, s, b, "{missed, auto, 1, –}")
	requireDueReasons(t, s, id, domain.ReasonSlipped)
	requireSlips(t, s, id, 1, 1)
	op, block, task := latestOp(t, s), s.BlockView(b), s.Task(id)
	wantTime := testutil.Local(t, "2026-10-29T06:00Z")
	if s.OpCount() != count+1 || op.Kind != domain.OpSystemReconcile || op.Actor != "system" ||
		op.Revertible || op.Channel != domain.ChanSystem || task.Version != before.Version+1 ||
		!task.UpdatedAt.Equal(wantTime) || block.Version != 2 ||
		!block.ResolvedAt.Valid || !block.ResolvedAt.V.Equal(wantTime) ||
		!block.SlippedAt.Valid || !block.SlippedAt.V.Equal(wantTime) ||
		!block.ResolvedSeq.Valid || block.ResolvedSeq.V != op.Seq ||
		!block.SlippedSeq.Valid || block.SlippedSeq.V != op.Seq ||
		!task.Reasons[0].Since.Equal(wantTime) || task.Reasons[0].Seq != op.Seq {
		t.Fatalf("resolution metadata: op=%+v block=%+v task=%+v", op, block, task)
	}
	requireEventKeys(t, s, op.ID, "block:update 1", "triage:add 1 slipped")
	var image map[string]any
	if err := json.Unmarshal(s.Events(op.ID)[0].After, &image); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(image, map[string]any{
		"status": "missed", "outcome_source": "auto", "slipped": float64(1),
		"slipped_at": float64(wantTime.UnixMilli()), "resolved_at": float64(wantTime.UnixMilli()),
		"slipped_seq": float64(op.Seq), "resolved_seq": float64(op.Seq),
	}) {
		t.Fatalf("changed columns = %v", image)
	}
	s.Reconcile()
	if s.OpCount() != count+1 || !reflect.DeepEqual(s.Task(id), task) || s.BlockView(b) != block {
		t.Fatal("repeat reconcile changed resolved state")
	}
}

func TestBLK19(t *testing.T) {
	s := testutil.NewScenario(t).At("11-01 09:00")
	id := s.Add("A")
	b := s.Block(id, "2026-11-01 13:00-15:00")
	count := s.OpCount()
	s.At("2026-11-02T06:59Z").Reconcile()
	requirePlanned(t, s, b)
	if s.OpCount() != count {
		t.Fatal("fall-back day resolved before local midnight")
	}
}

func TestBLK20(t *testing.T) {
	s := testutil.NewScenario(t).At("11-01 09:00")
	id := s.Add("A")
	b := s.Block(id, "2026-11-01 13:00-15:00")
	s.At("2026-11-02T07:00Z").Reconcile()
	requireBlockState(t, s, b, "{missed, auto, 1, –}")
	requireDueReasons(t, s, id, domain.ReasonSlipped)
	requireSlips(t, s, id, 1, 1)
}

func TestBLK21(t *testing.T) {
	s := testutil.NewScenario(t).At("2027-03-14 09:00")
	id := s.Add("A")
	b := s.Block(id, "2027-03-14 13:00-15:00")
	count := s.OpCount()
	s.At("2027-03-15T05:59Z").Reconcile()
	requirePlanned(t, s, b)
	if s.OpCount() != count {
		t.Fatal("spring-forward day resolved before local midnight")
	}
	s.At("2027-03-15T06:00Z").Reconcile()
	requireBlockState(t, s, b, "{missed, auto, 1, –}")
	requireDueReasons(t, s, id, domain.ReasonSlipped)
}

func TestBLK23(t *testing.T) {
	s := testutil.NewScenario(t).At("10-27 09:00")
	id := s.Add("A")
	b := s.Block(id, "10-27 13:00-15:00")
	s.Log(id, "x", "10-27")
	s.At("10-28 00:00").Reconcile()
	requireBlockState(t, s, b, "{worked, auto, 0, –}")
	requireDueReasons(t, s, id, domain.ReasonContinue)
	requireEventKeys(t, s, latestOp(t, s).ID, "block:update 1", "triage:add 1 continue")
	requireSlips(t, s, id, 0, 0)
}

func TestBLK25(t *testing.T) {
	s := testutil.NewScenario(t).At("10-27 08:00")
	id := s.Add("A")
	b1 := s.Block(id, "10-27 09:00-10:00")
	b2 := s.Block(id, "10-27 14:00-15:00")
	s.Log(id, "x", "10-27")
	version := s.Task(id).Version
	s.At("10-28 00:00").Reconcile()
	for _, b := range []domain.BlockID{b1, b2} {
		requireBlockState(t, s, b, "{worked, auto, 0, –}")
	}
	requireDueReasons(t, s, id, domain.ReasonContinue)
	requireEventKeys(t, s, latestOp(t, s).ID, "block:update 1", "triage:add 1 continue", "block:update 2")
	if s.Task(id).Version != version+1 {
		t.Fatal("two resolutions must bump task version only once")
	}
}

func TestBLK26(t *testing.T) {
	s := testutil.NewScenario(t).At("10-28 09:00")
	id := s.Add("A")
	b := s.Block(id, "10-28 13:00-15:00")
	s.At("10-29 00:00").Reconcile()
	requireBlockState(t, s, b, "{missed, auto, 1, –}")
	requireDueReasons(t, s, id, domain.ReasonSlipped)
	requireSlips(t, s, id, 1, 1)
}

func missedMonday(t *testing.T) (*testutil.Scenario, domain.TaskID, domain.BlockID) {
	t.Helper()
	s := testutil.NewScenario(t).At("10-26 09:00")
	id := s.Add("A")
	b := s.Block(id, "10-26 13:00-15:00")
	s.At("10-27 00:00").Reconcile()
	requireBlockState(t, s, b, "{missed, auto, 1, –}")
	requireDueReasons(t, s, id, domain.ReasonSlipped)
	return s.At("10-28 10:00"), id, b
}

func TestSLP01(t *testing.T) {
	s, id, b := missedMonday(t)
	before := s.BlockView(b)
	s.Log(id, "Did it", "10-26")
	requireBlockState(t, s, b, "{worked, auto, 0, –}")
	requireDueReasons(t, s, id, domain.ReasonContinue)
	requireSlips(t, s, id, 0, 0)
	after := s.BlockView(b)
	if after.SlippedAt != before.SlippedAt || after.SlippedSeq != before.SlippedSeq ||
		!after.ResolvedAt.V.Equal(s.Clock.Now()) || after.ResolvedSeq.V != s.Op(s.Last.OpID).Seq ||
		after.Version != before.Version+1 {
		t.Fatalf("recomputed block = %+v", after)
	}
	requireEventKeys(t, s, s.Last.OpID, "entry:insert 1", "block:update 1 [derived]",
		"triage:remove 1 slipped [derived]", "triage:add 1 continue [derived]")
}

func twoSlips(t *testing.T, acknowledge bool) (*testutil.Scenario, domain.TaskID, domain.BlockID, domain.BlockID) {
	t.Helper()
	s := testutil.NewScenario(t).At("10-26 09:00")
	id := s.Add("A")
	b1 := s.Block(id, "10-26 13:00-15:00")
	b2 := s.Block(id, "10-27 13:00-15:00")
	s.At("10-27 00:00").Reconcile()
	if acknowledge {
		s.At("10-27 09:00").Place(id, domain.PlaceKeep)
	}
	s.At("10-28 00:00").Reconcile()
	requireDueReasons(t, s, id, domain.ReasonSlipped)
	return s.At("10-28 10:00"), id, b1, b2
}

func TestSLP02(t *testing.T) {
	s, id, b1, b2 := twoSlips(t, false)
	before := s.Task(id).Reasons[0]
	s.Log(id, "x", "10-26")
	requireBlockState(t, s, b1, "{worked, auto, 0, –}")
	requireBlockState(t, s, b2, "{missed, auto, 1, –}")
	requireDueReasons(t, s, id, domain.ReasonSlipped, domain.ReasonContinue)
	requireSlips(t, s, id, 1, 1)
	if s.Task(id).Reasons[0] != before || !before.Since.Equal(testutil.Local(t, "10-27 00:00")) {
		t.Fatal("remaining unacknowledged slip must preserve the original reason")
	}
}

func TestSLP03(t *testing.T) {
	s, id, b1, b2 := twoSlips(t, true)
	s.Log(id, "x", "10-27")
	requireBlockState(t, s, b1, "{missed, auto, 1, –}")
	requireBlockState(t, s, b2, "{worked, auto, 0, –}")
	requireDueReasons(t, s, id, domain.ReasonContinue)
	requireSlips(t, s, id, 1, 0)
	if s.BlockView(b1).SlippedSeq.V >= s.Task(id).LastPlacedSeq.V {
		t.Fatal("Monday slip must be acknowledged by Tuesday placement")
	}
}

func TestSLP06(t *testing.T) {
	s := testutil.NewScenario(t).At("10-26 09:00")
	id := s.Add("A")
	b1 := s.Block(id, "10-26 13:00-15:00")
	b2 := s.Block(id, "10-27 09:00-10:00")
	s.Log(id, "Monday", "10-26")
	s.At("10-27 00:00").Reconcile()
	requireBlockState(t, s, b1, "{worked, auto, 0, –}")
	requirePlanned(t, s, b2)
	requireDueReasons(t, s, id)
	requireEventKeys(t, s, latestOp(t, s).ID, "block:update 1")
	s.At("10-27 10:00").Log(id, "Tuesday", "10-27")
	s.At("10-28 00:00").Reconcile()
	requireBlockState(t, s, b2, "{worked, auto, 0, –}")
	requireDueReasons(t, s, id, domain.ReasonContinue)
}

func TestSLP12(t *testing.T) {
	s, id, b1, b2 := twoSlips(t, true)
	requireBlockState(t, s, b1, "{missed, auto, 1, –}")
	requireBlockState(t, s, b2, "{missed, auto, 1, –}")
	requireDueReasons(t, s, id, domain.ReasonSlipped)
	requireSlips(t, s, id, 2, 1)
}

func TestSLP14(t *testing.T) {
	s := testutil.NewScenario(t).At("10-26 09:00")
	id := s.Add("A")
	b1 := s.Block(id, "10-26 13:00-15:00")
	b2 := s.Block(id, "10-27 13:00-15:00")
	s.At("10-28 09:00").Reconcile()
	op := latestOp(t, s)
	for _, b := range []domain.BlockID{b1, b2} {
		requireBlockState(t, s, b, "{missed, auto, 1, –}")
		if got := s.BlockView(b); !got.SlippedAt.V.Equal(s.Clock.Now()) || got.SlippedSeq.V != op.Seq {
			t.Fatalf("downtime slip = %+v", got)
		}
	}
	requireDueReasons(t, s, id, domain.ReasonSlipped)
	if row := s.Task(id).Reasons[0]; !row.Since.Equal(s.Clock.Now()) || row.Seq != op.Seq {
		t.Fatalf("downtime reason = %+v", row)
	}
	requireEventKeys(t, s, op.ID, "block:update 1", "triage:add 1 slipped", "block:update 2")
	requireSlips(t, s, id, 2, 2)
}

func TestSLP15(t *testing.T) {
	s := testutil.NewScenario(t).At("10-26 09:00")
	id := s.Add("A")
	s.Block(id, "10-26 13:00-15:00")
	s.Log(id, "Monday", "10-26")
	s.At("10-27 00:00").Reconcile()
	requireDueReasons(t, s, id, domain.ReasonContinue)
	original := s.Task(id).Reasons[0]
	b2 := s.At("10-27 09:00").Block(id, "10-27 13:00-15:00")
	s.Log(id, "Tuesday", "10-27")
	// AIDEV-NOTE: F14 fixture shortcut: Block is a placement and clears reasons.
	s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), []domain.EntityRef{{Entity: domain.EntTask, ID: int64(id)}}, func(q store.Q) error {
		_, err := store.AddReason(s.Ctx(), q, original)
		return err
	}))
	before := s.Task(id)
	s.At("10-28 00:00").Reconcile()
	requireBlockState(t, s, b2, "{worked, auto, 0, –}")
	requireDueReasons(t, s, id, domain.ReasonContinue)
	after := s.Task(id)
	if after.Reasons[0] != original || !original.Since.Equal(testutil.Local(t, "10-27 00:00")) || after.Version != before.Version {
		t.Fatalf("existing continue row changed: %+v", after)
	}
	requireEventKeys(t, s, latestOp(t, s).ID, "block:update 2")
}

func TestREC07(t *testing.T) {
	s := testutil.NewScenario(t).At("10-28 09:00")
	id := s.Add("A")
	b := s.Block(id, "10-28 13:00-15:00")
	count, version := s.OpCount(), s.Task(id).Version
	s.At("10-29 00:30").Log(id, "x", "10-28")
	requireBlockState(t, s, b, "{worked, auto, 0, –}")
	requireDueReasons(t, s, id, domain.ReasonContinue)
	user := s.Op(s.Last.OpID)
	var systemID domain.OpID
	if err := s.Store.Reader.QueryRowContext(s.Ctx(), `SELECT op_id FROM ops WHERE seq = ?`, user.Seq-1).Scan(&systemID); err != nil {
		t.Fatal(err)
	}
	system := s.Op(systemID)
	if s.OpCount() != count+2 || system.Kind != domain.OpSystemReconcile || system.Actor != "system" ||
		user.Kind != domain.OpEntryAdd || user.Actor != "owner" || systemID == user.ID ||
		s.Task(id).Version != version+2 || s.BlockView(b).Version != 3 {
		t.Fatalf("reconcile/user operations = %+v / %+v", system, user)
	}
	requireEventKeys(t, s, systemID, "block:update 1", "triage:add 1 slipped")
	requireEventKeys(t, s, user.ID, "entry:insert 1", "block:update 1 [derived]",
		"triage:remove 1 slipped [derived]", "triage:add 1 continue [derived]")
	requireHistoryOp(t, s, id, systemID)
	requireHistoryOp(t, s, id, user.ID)
}

func reconcileDSTDays(t *testing.T, start, first, second, beforeMidnight, midnight string) {
	t.Helper()
	s := testutil.NewScenario(t).At(start)
	id := s.Add("A")
	b1 := s.Block(id, first+" 13:00-15:00")
	b2 := s.Block(id, second+" 13:00-15:00")
	s.At(beforeMidnight).Reconcile()
	requireBlockState(t, s, b1, "{missed, auto, 1, –}")
	requirePlanned(t, s, b2)
	requireEventKeys(t, s, latestOp(t, s).ID, "block:update 1", "triage:add 1 slipped")
	s.At(midnight).Reconcile()
	requireBlockState(t, s, b2, "{missed, auto, 1, –}")
	requireEventKeys(t, s, latestOp(t, s).ID, "block:update 2")
	requireSlips(t, s, id, 2, 2)
}

func TestREC08(t *testing.T) {
	reconcileDSTDays(t, "10-31 12:00", "10-31", "2026-11-01", "2026-11-02T06:30Z", "2026-11-02T07:00Z")
}

func TestREC09(t *testing.T) {
	reconcileDSTDays(t, "2027-03-13 09:00", "2027-03-13", "2027-03-14", "2027-03-15T05:59Z", "2027-03-15T06:00Z")
}

func TestRCP06(t *testing.T) {
	s := testutil.NewScenario(t).At("10-28 09:00")
	id := s.Add("A")
	b := s.Block(id, "10-28 13:00-15:00")
	s.At("10-28 16:00")
	if !s.BlockView(b).NeedsReblock {
		t.Fatal("ended block without work must need reblock")
	}
	s.Log(id, "x", "10-28")
	requirePlanned(t, s, b)
	if s.BlockView(b).NeedsReblock {
		t.Fatal("work must clear the reblock marker")
	}
	requireEventKeys(t, s, s.Last.OpID, "entry:insert 1")
	s.At("10-29 00:00").Reconcile()
	requireBlockState(t, s, b, "{worked, auto, 0, –}")
	requireDueReasons(t, s, id, domain.ReasonContinue)
}

func TestDUE13(t *testing.T) {
	s := testutil.NewScenario(t).At("10-27 09:00")
	id := s.Add("A", testutil.Due("10-28"))
	b := s.Block(id, "10-28 13:00-15:00")
	count := s.OpCount()
	for _, at := range []string{"10-28 00:00", "10-28 16:00"} {
		s.At(at).Reconcile()
		requirePlanned(t, s, b)
		requireDueReasons(t, s, id)
	}
	if s.OpCount() != count {
		t.Fatal("planned block must suppress due through its entire day")
	}
	s.At("10-29 00:00").Reconcile()
	requireBlockState(t, s, b, "{missed, auto, 1, –}")
	requireDueReasons(t, s, id, domain.ReasonSlipped, domain.ReasonOverdue, domain.ReasonDue)
	requireEventKeys(t, s, latestOp(t, s).ID, "triage:add 1 due", "triage:add 1 overdue",
		"block:update 1", "triage:add 1 slipped")
}

func TestRecomputeEventsAreDerived(t *testing.T) {
	s, id, _ := missedMonday(t)
	s.Log(id, "Did it", "10-26")
	requireEventKeys(t, s, s.Last.OpID, "entry:insert 1", "block:update 1 [derived]",
		"triage:remove 1 slipped [derived]", "triage:add 1 continue [derived]")
	for _, ev := range s.Events(s.Last.OpID) {
		if ev.Derived != (ev.Entity != domain.EntEntry) || !ev.TaskID.Valid || ev.TaskID.V != id {
			t.Fatalf("derived flag or task link = %+v", ev)
		}
	}
}

func TestClockMovesBackward(t *testing.T) {
	s := testutil.NewScenario(t).At("10-28 09:00")
	id := s.Add("A")
	b := s.Block(id, "10-28 13:00-15:00")
	s.At("10-29 00:00").Reconcile() // B1 missed, #1{slipped}
	ops := s.OpCount()
	s.At("10-28 23:59") // NTP correction backwards
	s.Reconcile()
	if s.OpCount() != ops || s.BlockState(b) != "{missed, auto, 1, –}" {
		t.Fatalf("re-resolved after the clock moved back: %s", s.BlockState(b))
	}
	s.Place(id, domain.PlaceKeep)
	if l, st := s.Slips(id); l != 1 || st != 0 || len(s.Reasons(id)) != 0 {
		t.Fatalf("L/S = %d/%d reasons %v", l, st, s.Reasons(id))
	}
}

func TestSetBlockPlacementSuppressesReasons(t *testing.T) {
	for _, status := range []domain.BlockStatus{domain.BlockMissed, domain.BlockWorked} {
		t.Run(string(status), func(t *testing.T) {
			s := testutil.NewScenario(t).At("10-28 09:00")
			id := s.Add("A")
			b := s.Block(id, "10-28 13:00-15:00")
			s.At("10-28 16:00")
			slipped := status == domain.BlockMissed
			s.Must(s.Svc.TestSetBlock(s.Ctx(), s.Meta(), b, service.BlockChangeForTest{
				Status: status, Source: domain.OutcomeAuto, Slipped: &slipped,
			}, true))
			requireDueReasons(t, s, id)
			block := s.BlockView(b)
			if block.Status != status || block.Slipped != slipped || block.ResolvedSeq.V != s.Task(id).LastPlacedSeq.V {
				t.Fatalf("placed resolution = %+v", block)
			}
			if slipped {
				requireSlips(t, s, id, 1, 0)
			}
		})
	}
}

func TestSetBlockUnwork(t *testing.T) {
	for _, acknowledged := range []bool{false, true} {
		name := "unacknowledged sibling"
		if acknowledged {
			name = "acknowledged sibling"
		}
		t.Run(name, func(t *testing.T) {
			s := testutil.NewScenario(t).At("10-28 08:00")
			id := s.Add("A")
			b1 := s.Block(id, "10-28 09:00-10:00")
			b2 := s.Block(id, "10-28 13:00-15:00")
			worked := service.BlockChangeForTest{Status: domain.BlockWorked, Source: domain.OutcomeUser}
			s.At("10-28 10:00")
			s.Must(s.Svc.TestSetBlock(s.Ctx(), s.Meta(), b1, worked, false))
			if acknowledged {
				s.At("10-28 11:00").Place(id, domain.PlaceKeep)
			}
			s.At("10-28 16:00")
			s.Must(s.Svc.TestSetBlock(s.Ctx(), s.Meta(), b2, worked, false))
			requireDueReasons(t, s, id, domain.ReasonContinue)
			slipped := true
			s.At("10-28 17:00")
			s.Must(s.Svc.TestSetBlock(s.Ctx(), s.Meta(), b2, service.BlockChangeForTest{
				Status: domain.BlockMissed, Source: domain.OutcomeUser, Slipped: &slipped,
			}, false))
			requireBlockState(t, s, b2, "{missed, user, 1, –}")
			if acknowledged {
				requireDueReasons(t, s, id, domain.ReasonSlipped)
				requireEventKeys(t, s, s.Last.OpID, "block:update 2", "triage:add 1 slipped", "triage:remove 1 continue")
			} else {
				requireDueReasons(t, s, id, domain.ReasonSlipped, domain.ReasonContinue)
				requireEventKeys(t, s, s.Last.OpID, "block:update 2", "triage:add 1 slipped")
			}
		})
	}
}

func TestSetBlockBackToPlanned(t *testing.T) {
	s := testutil.NewScenario(t).At("10-28 09:00")
	id := s.Add("A")
	b := s.Block(id, "10-28 13:00-15:00")
	s.At("10-28 16:00")
	slipped := true
	s.Must(s.Svc.TestSetBlock(s.Ctx(), s.Meta(), b, service.BlockChangeForTest{
		Status: domain.BlockMissed, Source: domain.OutcomeUser, Slipped: &slipped,
	}, false))
	before := s.BlockView(b)
	s.At("10-28 17:00")
	slipped = false
	s.Must(s.Svc.TestSetBlock(s.Ctx(), s.Meta(), b, service.BlockChangeForTest{
		Status: domain.BlockPlanned, Slipped: &slipped,
	}, false))
	requireBlockState(t, s, b, "{planned, –, 0, –}")
	requireDueReasons(t, s, id)
	after := s.BlockView(b)
	if after.ResolvedAt.Valid || after.ResolvedSeq.Valid || after.SlippedAt != before.SlippedAt || after.SlippedSeq != before.SlippedSeq {
		t.Fatalf("reset resolution = %+v", after)
	}
	requireEventKeys(t, s, s.Last.OpID, "block:update 1", "triage:remove 1 slipped")
}

func restoreColumns(t *testing.T, image json.RawMessage) map[string]any {
	t.Helper()
	var cols map[string]any
	d := json.NewDecoder(bytes.NewReader(image))
	d.UseNumber()
	if err := d.Decode(&cols); err != nil {
		t.Fatal(err)
	}
	return cols
}

func TestSetBlockRestoreSkipsHooks(t *testing.T) {
	s := testutil.NewScenario(t).At("10-28 09:00")
	id := s.Add("A")
	b := s.Block(id, "10-28 13:00-15:00")
	s.At("10-28 16:00")
	slipped := true
	s.Must(s.Svc.TestSetBlock(s.Ctx(), s.Meta(), b, service.BlockChangeForTest{
		Status: domain.BlockMissed, Source: domain.OutcomeAuto, Slipped: &slipped,
	}, false))
	change, original, task := s.Events(s.Last.OpID)[0], s.BlockView(b), s.Task(id)
	s.At("10-28 17:00")
	s.Must(s.Svc.TestSetBlock(s.Ctx(), s.Meta(), b, service.BlockChangeForTest{
		Restore: true, Image: restoreColumns(t, change.Before),
	}, false))
	requireBlockState(t, s, b, "{planned, –, 0, –}")
	requireDueReasons(t, s, id, domain.ReasonSlipped) // Restore must skip the unslip hook.
	if got := s.BlockView(b); got.ResolvedSeq.Valid || got.ResolvedAt.Valid || got.SlippedSeq.Valid || got.SlippedAt.Valid {
		t.Fatalf("restore did not write nulls verbatim: %+v", got)
	}
	if got := s.Task(id); got.Task != task.Task || !reflect.DeepEqual(got.Reasons, task.Reasons) {
		t.Fatal("restore changed task reasons or version")
	}
	requireEventKeys(t, s, s.Last.OpID, "block:update 1")
	s.Place(id, domain.PlaceKeep)
	s.At("10-28 18:00")
	s.Must(s.Svc.TestSetBlock(s.Ctx(), s.Meta(), b, service.BlockChangeForTest{
		Restore: true, Image: restoreColumns(t, change.After),
	}, false))
	requireBlockState(t, s, b, "{missed, auto, 1, –}")
	requireDueReasons(t, s, id) // Restore must skip the slip hook and preserve the old seq.
	after := s.BlockView(b)
	if after.ResolvedAt != original.ResolvedAt || after.ResolvedSeq != original.ResolvedSeq ||
		after.SlippedAt != original.SlippedAt || after.SlippedSeq != original.SlippedSeq {
		t.Fatalf("restore refreshed metadata: %+v", after)
	}
	requireSlips(t, s, id, 1, 0)
	requireEventKeys(t, s, s.Last.OpID, "block:update 1")
}

func TestRecomputeUnchangedOutcome(t *testing.T) {
	s, id, b := missedMonday(t)
	s.Log(id, "First", "10-26")
	block, task := s.BlockView(b), s.Task(id)
	s.At("10-28 11:00").Log(id, "Second", "10-26")
	if s.BlockView(b) != block || !reflect.DeepEqual(s.Task(id), task) {
		t.Fatal("unchanged outcome refreshed metadata, reasons, or versions")
	}
	requireEventKeys(t, s, s.Last.OpID, "entry:insert 2")
}

func TestRecomputeExcludesUserCancelledDeleted(t *testing.T) {
	for _, state := range []string{"user", "cancelled", "deleted"} {
		t.Run(state, func(t *testing.T) {
			s := testutil.NewScenario(t).At("10-27 09:00")
			id := s.Add("A")
			b := s.Block(id, "10-27 13:00-15:00")
			s.At("10-27 16:00")
			slipped := true
			ch := service.BlockChangeForTest{Status: domain.BlockMissed, Source: domain.OutcomeAuto, Slipped: &slipped}
			switch state {
			case "user":
				ch.Source = domain.OutcomeUser
			case "cancelled":
				ch.Status, ch.Source, ch.Cancel, ch.Slipped = domain.BlockCancelled, "", domain.CancelUser, nil
			}
			s.Must(s.Svc.TestSetBlock(s.Ctx(), s.Meta(), b, ch, false))
			if state == "deleted" {
				s.Must(s.Svc.TestSetBlock(s.Ctx(), s.Meta(), b, service.BlockChangeForTest{
					Restore: true, Image: map[string]any{"deleted_at": s.Clock.Now().UnixMilli()},
				}, false))
			}
			before := s.BlockView(b)
			s.At("10-28 10:00").Log(id, "x", "10-27")
			if s.BlockView(b) != before {
				t.Fatalf("recompute touched %s block", state)
			}
			requireEventKeys(t, s, s.Last.OpID, "entry:insert 1")
		})
	}
}

func TestSetBlockSourceOnlyAndNoChange(t *testing.T) {
	s, id, b := missedMonday(t)
	before, task := s.BlockView(b), s.Task(id)
	ch := service.BlockChangeForTest{Status: domain.BlockMissed, Source: domain.OutcomeUser}
	s.Must(s.Svc.TestSetBlock(s.Ctx(), s.Meta(), b, ch, false))
	after := s.BlockView(b)
	requireBlockState(t, s, b, "{missed, user, 1, –}")
	if after.ResolvedAt != before.ResolvedAt || after.ResolvedSeq != before.ResolvedSeq ||
		after.SlippedAt != before.SlippedAt || after.SlippedSeq != before.SlippedSeq ||
		after.Version != before.Version+1 || !reflect.DeepEqual(s.Task(id), task) {
		t.Fatalf("source-only change refreshed metadata: %+v", after)
	}
	requireEventKeys(t, s, s.Last.OpID, "block:update 1")
	cols := restoreColumns(t, s.Events(s.Last.OpID)[0].After)
	if !reflect.DeepEqual(cols, map[string]any{"outcome_source": "user"}) {
		t.Fatalf("source-only columns = %v", cols)
	}
	count := s.OpCount()
	s.Must(s.Svc.TestSetBlock(s.Ctx(), s.Meta(), b, ch, false))
	if !s.Last.NoChange || s.OpCount() != count || s.BlockView(b) != after {
		t.Fatal("unchanged block wrote an op or bumped version")
	}
}

func TestSetBlockClosedTasks(t *testing.T) {
	for _, state := range []string{"dropped", "deleted"} {
		for _, status := range []domain.BlockStatus{domain.BlockMissed, domain.BlockWorked} {
			t.Run(state+"/"+string(status), func(t *testing.T) {
				s := testutil.NewScenario(t).At("10-28 09:00")
				id := s.Add("A")
				b := s.Block(id, "10-28 13:00-15:00")
				s.At("10-28 16:00")
				initial := domain.BlockWorked
				if status == domain.BlockWorked {
					initial = domain.BlockMissed
				}
				slipped := initial == domain.BlockMissed
				s.Must(s.Svc.TestSetBlock(s.Ctx(), s.Meta(), b, service.BlockChangeForTest{
					Status: initial, Source: domain.OutcomeAuto, Slipped: &slipped,
				}, false))
				s.Place(id, domain.PlaceKeep)
				// AIDEV-NOTE: F14 fixture shortcut: lifecycle ops arrive in Task 28.
				s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), []domain.EntityRef{{Entity: domain.EntTask, ID: int64(id)}}, func(q store.Q) error {
					task, err := store.GetTask(s.Ctx(), q, id)
					if err != nil {
						return err
					}
					if state == "dropped" {
						task.Status = domain.StatusDropped
					} else {
						task.DeletedAt = sql.Null[time.Time]{V: s.Clock.Now(), Valid: true}
					}
					return store.UpdateTask(s.Ctx(), q, task)
				}))
				s.At("10-28 17:00")
				slipped = status == domain.BlockMissed
				s.Must(s.Svc.TestSetBlock(s.Ctx(), s.Meta(), b, service.BlockChangeForTest{
					Status: status, Source: domain.OutcomeAuto, Slipped: &slipped,
				}, false))
				requireDueReasons(t, s, id)
				if block := s.BlockView(b); block.Status != status || block.Slipped != slipped {
					t.Fatalf("closed task block = %+v", block)
				}
				requireEventKeys(t, s, s.Last.OpID, "block:update 1")
			})
		}
	}
}

func TestRecomputeAllBlocksOnDate(t *testing.T) {
	s := testutil.NewScenario(t).At("10-26 08:00")
	id := s.Add("A")
	b1 := s.Block(id, "10-26 09:00-10:00")
	b2 := s.Block(id, "10-26 13:00-15:00")
	s.At("10-27 00:00").Reconcile()
	s.At("10-28 10:00").Log(id, "Did both", "10-26")
	for _, b := range []domain.BlockID{b1, b2} {
		requireBlockState(t, s, b, "{worked, auto, 0, –}")
	}
	requireDueReasons(t, s, id, domain.ReasonContinue)
	requireSlips(t, s, id, 0, 0)
	requireEventKeys(t, s, s.Last.OpID, "entry:insert 1", "block:update 1 [derived]",
		"triage:add 1 continue [derived]", "block:update 2 [derived]", "triage:remove 1 slipped [derived]")
}

func TestReconcileSkipsDeletedBlock(t *testing.T) {
	s := testutil.NewScenario(t).At("10-28 09:00")
	id := s.Add("A")
	b := s.Block(id, "10-28 13:00-15:00")
	s.Must(s.Svc.TestSetBlock(s.Ctx(), s.Meta(), b, service.BlockChangeForTest{
		Restore: true, Image: map[string]any{"deleted_at": s.Clock.Now().UnixMilli()},
	}, false))
	count, before := s.OpCount(), s.BlockView(b).TimeBlock
	s.At("10-29 00:00").Reconcile()
	if s.OpCount() != count || s.BlockView(b).TimeBlock != before {
		t.Fatal("reconcile resolved a tombstoned planned block")
	}
	requireDueReasons(t, s, id)
}
