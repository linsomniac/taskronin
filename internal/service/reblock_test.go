package service_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/store"
	"taskronin/internal/testutil"
)

func scheduledToday(t *testing.T, created string) (*testutil.Scenario, domain.TaskID, domain.BlockID) {
	t.Helper()
	s := testutil.NewScenario(t).At(created)
	id := s.Add("A", testutil.Bottom())
	b := s.Block(id, "10-28 13:00-15:00")
	return s, id, b
}

func resolvedYesterday(t *testing.T, worked bool) (*testutil.Scenario, domain.TaskID, domain.BlockID) {
	t.Helper()
	s := testutil.NewScenario(t).At("10-27 09:00")
	id := s.Add("A", testutil.Bottom())
	b := s.Block(id, "10-27 13:00-15:00")
	if worked {
		s.At("10-27 14:00").Log(id, "work", "10-27")
	}
	s.At("10-28 00:00").Reconcile()
	s.At("10-28 10:00")
	return s, id, b
}

func reblockInput(s *testutil.Scenario, b domain.BlockID, span string) service.ReblockIn {
	v := s.BlockView(b)
	in := blockInput(s, v.TaskID, span)
	return service.ReblockIn{BlockID: b, Expect: s.BlockExpect(b), TaskExpect: in.Expect, Span: in.Span}
}

func requireNotPlaced(t *testing.T, s *testutil.Scenario, before domain.TaskView) {
	t.Helper()
	after := s.Task(before.ID)
	if after.LastPlacedAt != before.LastPlacedAt || after.LastPlacedSeq != before.LastPlacedSeq ||
		after.DueAck != before.DueAck || after.DueAckDate != before.DueAckDate || after.Rank != before.Rank {
		t.Fatal("non-placement changed placement fields")
	}
}

func requireReblocked(t *testing.T, s *testutil.Scenario, before domain.TaskView, b domain.BlockID, span string) {
	t.Helper()
	requirePlanned(t, s, b)
	requirePlaced(t, s, before.ID, before.Version+1)
	r := s.Last
	if r.Block == nil || r.Block.ID != b || r.Block.Span != blockInput(s, before.ID, span).Span ||
		r.Task == nil || r.Task.ID != before.ID || len(r.Refs) != 2 ||
		!slices.Contains(r.Refs, domain.EntityRef{Entity: domain.EntBlock, ID: int64(b)}) ||
		!slices.Contains(r.Refs, domain.EntityRef{Entity: domain.EntTask, ID: int64(before.ID)}) {
		t.Fatalf("Reblock result = %+v", r)
	}
}

func TestBLK04(t *testing.T) {
	s, _, b := scheduledToday(t, "10-28 09:00")
	other := s.Add("B", testutil.Bottom())
	s.At("10-28 10:00").Unblock(b)
	requireBlockState(t, s, b, "{cancelled, –, 1, user}")
	next := s.Block(other, "10-28 13:00-15:00")
	requirePlanned(t, s, next)
}

func TestBLK10(t *testing.T) {
	s := testutil.NewScenario(t).At("10-28 09:00")
	id := s.Add("A", testutil.Bottom())
	b := s.Block(id, "10-29 09:00-10:00")
	s.At("10-29 09:30")
	before := s.Task(id)
	next := s.Reblock(b, "10-29 09:30-10:30")
	requireBlockState(t, s, b, "{missed, auto, 1, –}")
	if next == b || !s.BlockView(b).ResolvedAt.V.Equal(s.Clock.Now()) {
		t.Fatal("started block was not resolved at now and replaced")
	}
	requireReblocked(t, s, before, next, "10-29 09:30-10:30")
	requireSlips(t, s, id, 1, 0)
}

func TestBLK27(t *testing.T) {
	s, id, b := scheduledToday(t, "10-28 09:00")
	before := s.Task(id)
	s.At("10-28 10:00").Unblock(b)
	requireBlockState(t, s, b, "{cancelled, –, 1, user}")
	v := s.BlockView(b)
	if !v.SlippedAt.Valid || !v.SlippedAt.V.Equal(testutil.Local(t, "10-28 10:00")) ||
		v.SlippedSeq.V != s.Op(s.Last.OpID).Seq || s.Task(id).View != domain.ViewTriage {
		t.Fatalf("cancelled block = %+v, task = %+v", v, s.Task(id))
	}
	requireDueReasons(t, s, id, domain.ReasonSlipped)
	requireSlips(t, s, id, 1, 1)
	requireNotPlaced(t, s, before)
	requireEventKeys(t, s, s.Last.OpID, "block:update 1", "triage:add 1 slipped")
}

func TestBLK28(t *testing.T) {
	s, id, b := scheduledToday(t, "10-28 09:00")
	before := s.Task(id)
	next := s.At("10-28 10:00").Reblock(b, "10-29 13:00-15:00")
	requireBlockState(t, s, b, "{cancelled, –, 1, rescheduled}")
	requireReblocked(t, s, before, next, "10-29 13:00-15:00")
	requireSlips(t, s, id, 1, 0)
}

func TestBLK30(t *testing.T) {
	for _, move := range []bool{false, true} {
		t.Run(map[bool]string{false: "unblock", true: "reblock"}[move], func(t *testing.T) {
			s, id, b := scheduledToday(t, "10-28 09:50")
			before := s.Task(id)
			s.At("10-28 10:00")
			if move {
				next := s.Reblock(b, "10-29 13:00-15:00")
				requireBlockState(t, s, b, "{cancelled, –, 0, rescheduled}")
				requireReblocked(t, s, before, next, "10-29 13:00-15:00")
			} else {
				s.Unblock(b)
				requireBlockState(t, s, b, "{cancelled, –, 0, user}")
				requireNotPlaced(t, s, before)
			}
			requireDueReasons(t, s, id)
			requireSlips(t, s, id, 0, 0)
			if v := s.BlockView(b); v.SlippedAt.Valid || v.SlippedSeq.Valid {
				t.Fatalf("fresh cancellation has slip metadata: %+v", v)
			}
			for _, key := range s.EventKeys(s.Last.OpID) {
				if strings.Contains(key, "slipped") {
					t.Fatalf("fresh cancellation emitted slip event: %s", key)
				}
			}
		})
	}
}

func TestBLK31(t *testing.T) {
	for _, tc := range []struct {
		at, state string
		slips     int
	}{
		{"10-28 10:00:00", "{cancelled, –, 1, user}", 1},
		{"10-28 09:59:59", "{cancelled, –, 0, user}", 0},
	} {
		t.Run(tc.at, func(t *testing.T) {
			s, id, b := scheduledToday(t, "10-28 09:45:00")
			s.At(tc.at).Unblock(b)
			requireBlockState(t, s, b, tc.state)
			requireSlips(t, s, id, tc.slips, tc.slips)
		})
	}
}

func TestBLK32(t *testing.T) {
	for _, date := range []string{"10-28", "10-29"} {
		t.Run(date, func(t *testing.T) {
			s := testutil.NewScenario(t).At("10-28 09:00")
			id := s.Add("A", testutil.Bottom())
			b := s.Block(id, date+" 13:00-15:00")
			before, block := s.Task(id), s.BlockView(b).TimeBlock
			next := s.At("10-28 10:00").Reblock(b, date+" 16:00-17:00")
			requireBlockState(t, s, b, "{planned, –, 0, –}")
			v := s.BlockView(b)
			if next != b || v.Span.Start != 960 || v.Span.End != 1020 || v.Version != block.Version+1 ||
				v.CreatedAt != block.CreatedAt || v.ResolvedAt.Valid || v.SlippedAt.Valid {
				t.Fatalf("in-place block = %+v", v)
			}
			detail, err := s.Svc.GetTask(s.Ctx(), s.Actor, id)
			if err != nil || len(detail.Blocks) != 1 || s.Last.Block.ID != b || s.Last.Task.ID != id {
				t.Fatalf("in-place result = %+v, detail = %+v, err = %v", s.Last, detail, err)
			}
			requirePlaced(t, s, id, before.Version+1)
			requireEventKeys(t, s, s.Last.OpID, "block:update 1", "task:update 1")
		})
	}
}

func TestBLK33(t *testing.T) {
	for _, move := range []bool{false, true} {
		t.Run(map[bool]string{false: "unblock", true: "reblock"}[move], func(t *testing.T) {
			s := testutil.NewScenario(t).At("10-28 09:00")
			id := s.Add("A", testutil.Bottom())
			b := s.Block(id, "10-29 09:00-10:00")
			before := s.Task(id)
			s.At("10-28 10:00")
			if move {
				next := s.Reblock(b, "10-30 09:00-10:00")
				requireBlockState(t, s, b, "{cancelled, –, 0, rescheduled}")
				requireReblocked(t, s, before, next, "10-30 09:00-10:00")
			} else {
				s.Unblock(b)
				requireBlockState(t, s, b, "{cancelled, –, 0, user}")
				requireNotPlaced(t, s, before)
			}
			requireDueReasons(t, s, id)
			requireSlips(t, s, id, 0, 0)
		})
	}
}

func TestBLK35(t *testing.T) {
	s, id, b := scheduledToday(t, "10-28 09:00")
	before := s.Task(id)
	s.At("10-28 14:00").Unblock(b)
	requireBlockState(t, s, b, "{missed, auto, 1, –}")
	v := s.BlockView(b)
	if !v.ResolvedAt.Valid || !v.ResolvedAt.V.Equal(s.Clock.Now()) || v.SlippedAt != v.ResolvedAt {
		t.Fatalf("early resolution = %+v", v)
	}
	requireDueReasons(t, s, id, domain.ReasonSlipped)
	requireSlips(t, s, id, 1, 1)
	requireNotPlaced(t, s, before)
}

func TestBLK36(t *testing.T) {
	s, id, b := scheduledToday(t, "10-28 09:00")
	s.At("10-28 14:00").Log(id, "work", "10-28")
	before := s.Task(id)
	s.Unblock(b)
	requireBlockState(t, s, b, "{worked, auto, 0, –}")
	requireDueReasons(t, s, id, domain.ReasonContinue)
	requireSlips(t, s, id, 0, 0)
	requireNotPlaced(t, s, before)
}

func TestBLK37(t *testing.T) {
	s, id, b := scheduledToday(t, "10-28 09:00")
	later := s.Block(id, "10-29 09:00-10:00")
	s.At("10-28 14:00").Log(id, "work", "10-28")
	s.Unblock(b)
	requireBlockState(t, s, b, "{worked, auto, 0, –}")
	requirePlanned(t, s, later)
	requireDueReasons(t, s, id)
	requireSlips(t, s, id, 0, 0)
}

func TestBLK38(t *testing.T) {
	for _, span := range []string{"10-29 09:00-10:00", "10-28 15:00-16:00"} {
		t.Run(span, func(t *testing.T) {
			s, id, b := scheduledToday(t, "10-28 09:00")
			before := s.Task(id)
			next := s.At("10-28 14:00").Reblock(b, span)
			if next == b {
				t.Fatal("started block updated in place")
			}
			requireBlockState(t, s, b, "{missed, auto, 1, –}")
			requireReblocked(t, s, before, next, span)
			requireSlips(t, s, id, 1, 0)
		})
	}
}

func TestBLK39(t *testing.T) {
	s := testutil.NewScenario(t).At("10-28 09:55")
	id := s.Add("A", testutil.Bottom())
	b := s.Block(id, "10-28 09:55-11:00")
	s.At("10-28 10:00").Unblock(b)
	requireBlockState(t, s, b, "{missed, auto, 1, –}")
	requireDueReasons(t, s, id, domain.ReasonSlipped)
	requireSlips(t, s, id, 1, 1)
}

func TestBLK40(t *testing.T) {
	s, id, b := resolvedYesterday(t, false)
	before, block := s.Task(id), s.BlockView(b).TimeBlock
	next := s.Reblock(b, "10-28 13:00-15:00")
	if s.BlockView(b).TimeBlock != block || block.SlippedSeq.V >= s.Task(id).LastPlacedSeq.V {
		t.Fatal("resolved block changed or its slip was not acknowledged")
	}
	requireBlockState(t, s, b, "{missed, auto, 1, –}")
	requireReblocked(t, s, before, next, "10-28 13:00-15:00")
	requireSlips(t, s, id, 1, 0)
	requireEventKeys(t, s, s.Last.OpID, "block:insert 2", "triage:remove 1 slipped", "task:update 1")
}

func TestBLK41(t *testing.T) {
	for _, worked := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelled", true: "worked"}[worked], func(t *testing.T) {
			s, id, b := scheduledToday(t, "10-28 09:00")
			if worked {
				s.At("10-28 14:00").Log(id, "work", "10-28")
			} else {
				s.At("10-28 10:00")
			}
			s.Unblock(b)
			before, block := s.Task(id), s.BlockView(b).TimeBlock
			next := s.Reblock(b, "10-29 09:00-10:00")
			if next == b || s.BlockView(b).TimeBlock != block {
				t.Fatal("reblock changed the old row")
			}
			requireReblocked(t, s, before, next, "10-29 09:00-10:00")
		})
	}
}

func TestBLK42(t *testing.T) {
	s, id, b := scheduledToday(t, "10-28 09:00")
	other := s.Add("B", testutil.Bottom())
	s.Block(other, "10-29 09:00-10:00")
	s.At("10-28 10:00")
	before, block, count := s.Task(id), s.BlockView(b).TimeBlock, s.OpCount()
	_, err := s.Svc.Reblock(s.Ctx(), s.Meta(), reblockInput(s, b, "10-29 09:30-10:30"))
	testutil.RequireCode(t, err, domain.CodeConflict)
	if !reflect.DeepEqual(s.Task(id), before) || s.BlockView(b).TimeBlock != block || s.OpCount() != count {
		t.Fatal("failed Reblock was not atomic")
	}
	requireBlockState(t, s, b, "{planned, –, 0, –}")
}

func TestBLK43(t *testing.T) {
	for _, state := range []string{"cancelled", "missed", "missing"} {
		t.Run(state, func(t *testing.T) {
			s, _, b := scheduledToday(t, "10-28 09:00")
			code := domain.CodeConflict
			switch state {
			case "cancelled":
				s.At("10-28 10:00").Unblock(b)
			case "missed":
				s.At("10-29 00:00").Reconcile()
			case "missing":
				b, code = 999, domain.CodeNotFound
			}
			in := service.BlockRef{BlockID: b, Expect: domain.Expect{Gen: s.Svc.Generation(), Version: 1}}
			if state != "missing" {
				in.Expect = s.BlockExpect(b)
			}
			count := s.OpCount()
			_, err := s.Svc.Unblock(s.Ctx(), s.Meta(), in)
			testutil.RequireCode(t, err, code)
			if s.OpCount() != count {
				t.Fatal("rejected Unblock wrote an op")
			}
		})
	}
}

func TestBLK44(t *testing.T) {
	s, id, b := resolvedYesterday(t, false)
	before := s.Task(id)
	s.Outcome(b, domain.SetWorked)
	requireBlockState(t, s, b, "{worked, user, 0, –}")
	requireDueReasons(t, s, id, domain.ReasonContinue)
	requireSlips(t, s, id, 0, 0)
	requireNotPlaced(t, s, before)
	requireEventKeys(t, s, s.Last.OpID, "block:update 1", "triage:remove 1 slipped", "triage:add 1 continue")
}

func TestBLK45(t *testing.T) {
	s, id, b := resolvedYesterday(t, true)
	before := s.Task(id)
	s.Outcome(b, domain.SetMissed)
	requireBlockState(t, s, b, "{missed, user, 1, –}")
	if !s.BlockView(b).SlippedAt.V.Equal(s.Clock.Now()) {
		t.Fatal("outcome did not set slipped_at to now")
	}
	requireDueReasons(t, s, id, domain.ReasonSlipped)
	requireSlips(t, s, id, 1, 1)
	requireNotPlaced(t, s, before)
	requireEventKeys(t, s, s.Last.OpID, "block:update 1", "triage:add 1 slipped", "triage:remove 1 continue")
}

func TestBLK46(t *testing.T) {
	s, id, b := resolvedYesterday(t, true)
	s.Outcome(b, domain.SetMissed)
	before, block := s.Task(id), s.BlockView(b).TimeBlock
	e := s.Log(id, "x", "10-27")
	entry, err := s.Svc.GetEntry(s.Ctx(), s.Actor, e)
	if err != nil || entry.Title != "x" || entry.WorkDate != testutil.D("10-27") ||
		!entry.TaskID.Valid || entry.TaskID.V != id || s.BlockView(b).TimeBlock != block || !reflect.DeepEqual(s.Task(id), before) {
		t.Fatalf("entry = %+v, err = %v; user outcome or reasons changed", entry, err)
	}
	requireBlockState(t, s, b, "{missed, user, 1, –}")
	requireDueReasons(t, s, id, domain.ReasonSlipped)
	requireEventKeys(t, s, s.Last.OpID, "entry:insert 2")
}

func TestBLK47(t *testing.T) {
	for _, at := range []string{"16:00", "14:00", "13:00", "12:00", "cancelled"} {
		t.Run(at, func(t *testing.T) {
			s, id, b := scheduledToday(t, "10-28 09:00")
			if at == "cancelled" {
				s.At("10-28 10:00").Unblock(b)
			} else {
				s.At("10-28 " + at)
			}
			before, count := s.BlockView(b).TimeBlock, s.OpCount()
			r, err := s.Svc.SetOutcome(s.Ctx(), s.Meta(), service.SetOutcomeIn{BlockID: b, Expect: s.BlockExpect(b), Outcome: domain.SetMissed})
			if at == "12:00" || at == "cancelled" {
				testutil.RequireCode(t, err, domain.CodeConflict)
				de, _ := domain.AsError(err)
				if !strings.Contains(de.Detail, "use unblock") || s.BlockView(b).TimeBlock != before || s.OpCount() != count {
					t.Fatalf("outcome rejection = %v; state changed", err)
				}
				return
			}
			s.Must(r, err)
			requireBlockState(t, s, b, "{missed, user, 1, –}")
			v := s.BlockView(b)
			if !v.ResolvedAt.Valid || !v.ResolvedAt.V.Equal(s.Clock.Now()) || !v.ResolvedSeq.Valid ||
				v.ResolvedSeq.V != s.Op(r.OpID).Seq || v.SlippedSeq != v.ResolvedSeq {
				t.Fatalf("outcome timestamps = %+v", v)
			}
			requireDueReasons(t, s, id, domain.ReasonSlipped)
		})
	}
}

func TestBLK49(t *testing.T) {
	s, id, b := resolvedYesterday(t, true)
	s.Outcome(b, domain.SetMissed)
	s.Outcome(b, domain.SetAuto)
	requireBlockState(t, s, b, "{worked, auto, 0, –}")
	requireDueReasons(t, s, id, domain.ReasonContinue)
	requireSlips(t, s, id, 0, 0)
}

func TestBLK50(t *testing.T) {
	s, id, b := scheduledToday(t, "10-28 09:00")
	s.At("10-28 16:00").Outcome(b, domain.SetMissed)
	slip := s.BlockView(b)
	s.At("10-28 17:00").Outcome(b, domain.SetAuto)
	requireBlockState(t, s, b, "{planned, –, 0, –}")
	var restored []domain.BlockID
	for _, effect := range s.Last.Effects {
		if effect.Kind == service.EffBlockRestored {
			restored = append(restored, effect.BlockID)
		}
	}
	if !slices.Equal(restored, []domain.BlockID{b}) {
		t.Errorf("restored block IDs = %v, want [%d]", restored, b)
	}
	v := s.BlockView(b)
	if v.ResolvedAt.Valid || v.ResolvedSeq.Valid || v.SlippedAt != slip.SlippedAt || v.SlippedSeq != slip.SlippedSeq {
		t.Fatalf("auto reset metadata = %+v", v)
	}
	requireDueReasons(t, s, id)
	requireSlips(t, s, id, 0, 0)
	s.At("10-29 00:00").Reconcile()
	requireBlockState(t, s, b, "{missed, auto, 1, –}")
	requireDueReasons(t, s, id, domain.ReasonSlipped)
	requireSlips(t, s, id, 1, 1)
}

func TestRCP07(t *testing.T) {
	s, id, b := resolvedYesterday(t, false)
	auto := s.BlockView(b).TimeBlock
	s.Outcome(b, domain.SetMissed)
	before, block := s.Task(id), s.BlockView(b).TimeBlock
	if !block.ResolvedAt.Valid || !block.ResolvedAt.V.Equal(s.Clock.Now()) ||
		!block.ResolvedSeq.Valid || block.ResolvedSeq.V != s.Op(s.Last.OpID).Seq ||
		block.SlippedAt != auto.SlippedAt || block.SlippedSeq != auto.SlippedSeq {
		t.Fatalf("user confirmation metadata = %+v; automatic metadata = %+v", block, auto)
	}
	s.Log(id, "recap work", "10-27")
	if s.BlockView(b).TimeBlock != block || !reflect.DeepEqual(s.Task(id), before) {
		t.Fatal("entry recompute changed a user outcome or reasons")
	}
	requireBlockState(t, s, b, "{missed, user, 1, –}")
	requireDueReasons(t, s, id, domain.ReasonSlipped)
}

func TestOutcomeConfirmationMetadata(t *testing.T) {
	for _, outcome := range []domain.UserOutcome{domain.SetMissed, domain.SetWorked} {
		t.Run(string(outcome), func(t *testing.T) {
			s, id, b := resolvedYesterday(t, outcome == domain.SetWorked)
			// Confirm the automatic outcome, then confirm the same user outcome.
			// Each fresh request resolves at now; an idempotent retry does not.
			for _, at := range []string{"10-28 10:00", "10-28 11:00"} {
				s.At(at)
				before, task := s.BlockView(b).TimeBlock, s.Task(id)
				m := s.Meta()
				in := service.SetOutcomeIn{BlockID: b, Expect: s.BlockExpect(b), Outcome: outcome}
				r := s.Must(s.Svc.SetOutcome(s.Ctx(), m, in))
				after := s.BlockView(b).TimeBlock
				if after.Status != before.Status || after.OutcomeSource != domain.OutcomeUser ||
					!after.ResolvedAt.Valid || !after.ResolvedAt.V.Equal(s.Clock.Now()) ||
					!after.ResolvedSeq.Valid || after.ResolvedSeq.V != s.Op(r.OpID).Seq ||
					after.Slipped != before.Slipped || after.SlippedAt != before.SlippedAt || after.SlippedSeq != before.SlippedSeq ||
					after.Version != before.Version+1 || !reflect.DeepEqual(s.Task(id), task) {
					t.Fatalf("confirmation = %+v, before = %+v", after, before)
				}
				requireEventKeys(t, s, r.OpID, "block:update 1")
				event := s.Events(r.OpID)[0]
				var cols struct {
					ResolvedAt  int64        `json:"resolved_at"`
					ResolvedSeq domain.OpSeq `json:"resolved_seq"`
				}
				if err := json.Unmarshal(event.After, &cols); err != nil {
					t.Fatal(err)
				}
				if cols.ResolvedAt != s.Clock.Now().UnixMilli() || cols.ResolvedSeq != s.Op(r.OpID).Seq {
					t.Fatalf("resolution event columns = %v", cols)
				}
				count := s.OpCount()
				s.Clock.Advance(time.Minute)
				replay := s.Must(s.Svc.SetOutcome(s.Ctx(), m, in))
				if !replay.Replayed || replay.OpID != r.OpID || s.OpCount() != count || s.BlockView(b).TimeBlock != after {
					t.Fatal("idempotent confirmation refreshed resolution metadata")
				}
			}
		})
	}
}

func TestDUE20(t *testing.T) {
	s := testutil.NewScenario(t).At("10-27 09:00")
	id := s.Add("A", testutil.Bottom(), testutil.Due("10-28"))
	b := s.Block(id, "10-28 13:00-15:00")
	s.At("10-28 10:00").Reconcile()
	requireDueReasons(t, s, id)
	s.Unblock(b)
	requireBlockState(t, s, b, "{cancelled, –, 1, user}")
	s.Reconcile()
	requireDueReasons(t, s, id, domain.ReasonSlipped, domain.ReasonDue)
}

func TestReblockSpanValidation(t *testing.T) {
	for _, tc := range []struct {
		span string
		code domain.Code
	}{
		{"10-28 14:00-16:00", ""}, // Self-overlap is excluded for an in-place move.
		{"10-28 15:00-14:00", domain.CodeValidation},
		{"10-28 09:00-10:00", domain.CodeValidation},
		{"10-27 13:00-15:00", domain.CodeValidation},
		{"10-28 16:30-17:30", domain.CodeConflict},
	} {
		t.Run(tc.span, func(t *testing.T) {
			s, id, b := scheduledToday(t, "10-28 09:00")
			other := s.Add("B", testutil.Bottom())
			s.Block(other, "10-28 16:00-17:00")
			s.At("10-28 10:00")
			before, block, count := s.Task(id), s.BlockView(b).TimeBlock, s.OpCount()
			r, err := s.Svc.Reblock(s.Ctx(), s.Meta(), reblockInput(s, b, tc.span))
			if tc.code == "" {
				s.Must(r, err)
				if r.Block.ID != b {
					t.Fatal("self-overlap created another block")
				}
				return
			}
			testutil.RequireCode(t, err, tc.code)
			if s.OpCount() != count || s.BlockView(b).TimeBlock != block || !reflect.DeepEqual(s.Task(id), before) {
				t.Fatal("invalid move changed state")
			}
		})
	}
	t.Run("DST gap in place", func(t *testing.T) {
		s := testutil.NewScenario(t).At("2027-03-13 09:00")
		id := s.Add("A", testutil.Bottom())
		b := s.Block(id, "2027-03-14 04:00-05:00")
		for _, span := range []string{"2027-03-14 02:30-04:00", "2027-03-14 01:00-02:00"} {
			_, err := s.Svc.Reblock(s.Ctx(), s.Meta(), reblockInput(s, b, span))
			testutil.RequireCode(t, err, domain.CodeValidation)
		}
		requirePlanned(t, s, b)
	})
}

func TestReblockSuppressesTransientReasons(t *testing.T) {
	for _, tc := range []struct {
		name, at string
		worked   bool
	}{
		{"cancelled slip", "10-28 10:00", false},
		{"resolved slip", "10-28 14:00", false},
		{"continue", "10-28 14:00", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, id, b := scheduledToday(t, "10-28 09:00")
			s.At(tc.at)
			if tc.worked {
				s.Log(id, "work", "10-28")
			}
			s.Reblock(b, "10-29 09:00-10:00")
			requireDueReasons(t, s, id)
			// A§3.1: a placement suppresses reason additions within the op,
			// including before its final acknowledgement is persisted.
			requireEventKeys(t, s, s.Last.OpID, "block:update 1", "block:insert 2", "task:update 1")
		})
	}
}

func TestBlockMutationsContract(t *testing.T) {
	for _, kind := range []domain.OpKind{domain.OpBlockUnblock, domain.OpBlockReblock, domain.OpBlockOutcome} {
		t.Run(string(kind), func(t *testing.T) {
			s, id, b := scheduledToday(t, "10-28 09:00")
			s.At("10-28 14:00")
			blockExpect, taskExpect := s.BlockExpect(b), s.Expect(id)
			invoke := func(m service.Meta, exp, task domain.Expect) (*service.OpResult, error) {
				switch kind {
				case domain.OpBlockUnblock:
					return s.Svc.Unblock(s.Ctx(), m, service.BlockRef{BlockID: b, Expect: exp})
				case domain.OpBlockReblock:
					in := reblockInput(s, b, "10-29 09:00-10:00")
					in.Expect, in.TaskExpect = exp, task
					return s.Svc.Reblock(s.Ctx(), m, in)
				default:
					return s.Svc.SetOutcome(s.Ctx(), m, service.SetOutcomeIn{BlockID: b, Expect: exp, Outcome: domain.SetWorked})
				}
			}
			reader := s.Meta()
			reader.Actor = testutil.TokenActor("reader", domain.ScopeRead)
			_, err := invoke(reader, blockExpect, taskExpect)
			testutil.RequireCode(t, err, domain.CodeForbidden)
			_, err = invoke(s.Meta(), domain.Expect{}, taskExpect)
			testutil.RequireCode(t, err, domain.CodePreconditionReq)
			stale := blockExpect
			stale.Version++
			_, err = invoke(s.Meta(), stale, taskExpect)
			testutil.RequireCode(t, err, domain.CodePrecondition)
			if kind == domain.OpBlockReblock {
				_, err = invoke(s.Meta(), blockExpect, domain.Expect{})
				testutil.RequireCode(t, err, domain.CodePreconditionReq)
				staleTask := taskExpect
				staleTask.Version++
				_, err = invoke(s.Meta(), blockExpect, staleTask)
				testutil.RequireCode(t, err, domain.CodePrecondition)
			}
			m := s.Meta()
			r := s.Must(invoke(m, blockExpect, taskExpect))
			op := s.Op(r.OpID)
			if op.Kind != kind || op.Scopes != domain.ScopeWrite || !op.Revertible || r.Block == nil ||
				!slices.Contains(s.Stack(s.Actor.Key(), store.StackUndo), r.OpID) {
				t.Fatalf("op = %+v, result = %+v", op, r)
			}
			count := s.OpCount()
			replay := s.Must(invoke(m, blockExpect, taskExpect))
			if !replay.Replayed || replay.OpID != r.OpID || s.OpCount() != count || replay.Block.ID != r.Block.ID {
				t.Fatal("idempotent replay changed state or response refs")
			}
		})
	}
}

func TestOutcomeAutoNoChangeAndInvalid(t *testing.T) {
	s, _, b := scheduledToday(t, "10-28 09:00")
	before, count := s.BlockView(b).TimeBlock, s.OpCount()
	s.Outcome(b, domain.SetAuto)
	if !s.Last.NoChange || !s.Last.OpID.IsZero() || s.Last.Block == nil || s.Last.Block.ID != b ||
		s.BlockView(b).TimeBlock != before || s.OpCount() != count {
		t.Fatal("auto on a planned block was not a no-op")
	}
	_, err := s.Svc.SetOutcome(s.Ctx(), s.Meta(), service.SetOutcomeIn{BlockID: b, Expect: s.BlockExpect(b), Outcome: "done"})
	testutil.RequireCode(t, err, domain.CodeValidation)
}

func TestUnblockUsesInstantAndConfiguredGrace(t *testing.T) {
	t.Run("fold start already passed", func(t *testing.T) {
		s := testutil.NewScenario(t).At("10-31 09:00")
		b := s.Block(s.Add("A", testutil.Bottom()), "11-01 01:30-02:30")
		s.At("2026-11-01T08:15Z").Unblock(b) // 01:15 MST, after the earlier 01:30 MDT start.
		requireBlockState(t, s, b, "{missed, auto, 1, –}")
	})
	t.Run("exact start", func(t *testing.T) {
		s, _, b := scheduledToday(t, "10-28 12:55")
		s.At("10-28 13:00").Unblock(b)
		requireBlockState(t, s, b, "{missed, auto, 1, –}")
	})
	t.Run("configured grace", func(t *testing.T) {
		s := testutil.NewScenario(t, func(o *service.Options) { o.SlipGrace = 30 * time.Minute }).At("10-28 09:40")
		b := s.Block(s.Add("A", testutil.Bottom()), "10-28 13:00-15:00")
		s.At("10-28 10:00").Unblock(b)
		requireBlockState(t, s, b, "{cancelled, –, 0, user}")
	})
}
