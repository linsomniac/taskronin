package service_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/service"
	"github.com/linsomniac/taskronin/internal/store"
	"github.com/linsomniac/taskronin/internal/testutil"
)

func requireDeferredPlacement(t *testing.T, s *testutil.Scenario, id domain.TaskID, moment string, version domain.Version) {
	t.Helper()
	v, want := s.Task(id), testutil.Moment(moment)
	if len(s.Reasons(id)) != 0 || !v.DeferDate.Valid || v.DeferDate.V != want.Date || v.DeferMin != want.Min ||
		!v.Deferred || v.View != domain.ViewUpcoming || v.Version != version ||
		!v.LastPlacedAt.Valid || !v.LastPlacedAt.V.Equal(s.Clock.Now()) ||
		!v.LastPlacedSeq.Valid || v.LastPlacedSeq.V != s.Op(s.Last.OpID).Seq ||
		v.DueAck != v.DuePhase || !v.DueAckDate.Valid || v.DueAckDate.V != testutil.Zone.DateOf(s.Clock.Now()) {
		t.Fatalf("deferred placement = %+v, want %s, version %d", v, moment, version)
	}
	if slices.Contains(s.Backlog(), id) {
		t.Fatal("deferred task appears in backlog")
	}
}

func addNewReasonFixture(t *testing.T, s *testutil.Scenario, id domain.TaskID) {
	t.Helper()
	// AIDEV-NOTE: F14 fixture shortcut: creating a block places its task and
	// clears new. These catalog Givens require new alongside an existing block.
	seq := s.Op(s.Last.OpID).Seq
	s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), []domain.EntityRef{{Entity: domain.EntTask, ID: int64(id)}}, func(q store.Q) error {
		_, err := store.AddReason(s.Ctx(), q, domain.TriageRow{TaskID: id, Reason: domain.ReasonNew, Since: s.Clock.Now(), Seq: seq})
		return err
	}))
}

func TestTRI05(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("B", testutil.DeferTo("10-29"))
	requireDeferredPlacement(t, s, id, "10-29", 1)
	requireEventKeys(t, s, s.Last.OpID, "task:insert 1", "task:update 1", "task:update 1")
	if !s.Last.Created || s.Last.Task == nil || s.Last.Task.ID != id {
		t.Fatalf("add result = %+v", s.Last)
	}
	other := s.Add("C", testutil.DeferTo("10-30 15:00"))
	if s.Task(other).Rank <= s.Task(id).Rank {
		t.Fatal("deferred additions did not retain bottom rank order")
	}
}

func TestTRI13(t *testing.T) {
	s, id, _ := resolvedYesterday(t, false)
	addNewReasonFixture(t, s, id)
	requireDueReasons(t, s, id, domain.ReasonSlipped, domain.ReasonNew)
	before := s.Task(id)
	s.Defer(id, "10-30")
	requireDeferredPlacement(t, s, id, "10-30", before.Version+1)
	if s.Task(id).Rank != before.Rank {
		t.Fatal("defer changed rank")
	}
	requireSlips(t, s, id, 1, 0)
	requireEventKeys(t, s, s.Last.OpID, "triage:remove 1 slipped", "triage:remove 1 new", "task:update 1", "task:update 1")
}

func TestTRI26(t *testing.T) {
	for _, moment := range []string{"11-02", "11-02 15:00"} {
		t.Run(moment, func(t *testing.T) {
			for _, how := range []domain.PlaceHow{domain.PlaceKeep, domain.PlaceTop} {
				t.Run(string(how), func(t *testing.T) {
					s := testutil.NewScenario(t)
					s.Add("first", testutil.Bottom())
					id := s.Add("A", testutil.DeferTo(moment))
					before := s.Task(id)
					s.Place(id, how)
					requirePlaced(t, s, id, before.Version+1)
					if s.Task(id).View != domain.ViewBacklog || (how == domain.PlaceKeep && s.Task(id).Rank != before.Rank) {
						t.Fatalf("placed task = %+v", s.Task(id))
					}
					want := []string{"task:update 2"}
					if how == domain.PlaceTop {
						want = []string{"task:move 2", "task:update 2"}
					}
					requireEventKeys(t, s, s.Last.OpID, want...)
				})
			}
		})
	}
}

func TestTRI27(t *testing.T) {
	t.Run("clear", func(t *testing.T) {
		s := testutil.NewScenario(t)
		id := s.Add("A", testutil.DeferTo("11-02"))
		before := s.Task(id)
		s.Defer(id, "")
		requirePlaced(t, s, id, before.Version+1)
		if s.Task(id).View != domain.ViewBacklog || s.Task(id).Rank != before.Rank || s.Last.OpID.IsZero() || s.Op(s.Last.OpID).Kind != domain.OpTaskDefer {
			t.Fatalf("defer none = %+v", s.Last)
		}
		requireEventKeys(t, s, s.Last.OpID, "task:update 1")
	})
	t.Run("non_deferred", func(t *testing.T) {
		s := testutil.NewScenario(t)
		s.Add("first", testutil.Bottom())
		id := s.Add("A")
		b := s.Block(id, "10-29 09:00-10:00")
		addNewReasonFixture(t, s, id)
		s.SetFocus(id, false)
		before, block, focus := s.Task(id), s.BlockView(b).TimeBlock, s.Focus()
		s.Defer(id, "")
		requirePlaced(t, s, id, before.Version+1)
		if s.Task(id).Rank != before.Rank || !reflect.DeepEqual(s.BlockView(b).TimeBlock, block) || !reflect.DeepEqual(s.Focus(), focus) {
			t.Fatal("defer none changed rank, block or focus")
		}
		requireEventKeys(t, s, s.Last.OpID, "triage:remove 2 new", "task:update 2")
	})
}

func TestTRI28(t *testing.T) {
	for _, moment := range []string{"11-02", "11-02 15:00"} {
		t.Run(moment, func(t *testing.T) {
			s := testutil.NewScenario(t)
			id := s.Add("A", testutil.DeferTo(moment))
			before := s.Task(id)
			s.At("10-28 11:00").MarkManual(id)
			requireDueReasons(t, s, id, domain.ReasonManual)
			requireNotPlaced(t, s, before)
			v := s.Task(id)
			if v.DeferDate.Valid || v.DeferMin.Valid || v.Deferred || v.View != domain.ViewTriage || v.Version != before.Version+1 {
				t.Fatalf("manual task = %+v", v)
			}
			requireEventKeys(t, s, s.Last.OpID, "task:update 1", "triage:add 1 manual")
		})
	}
}

func TestBLK09(t *testing.T) {
	for _, moment := range []string{"11-02", "11-02 15:00"} {
		t.Run(moment, func(t *testing.T) {
			s := testutil.NewScenario(t)
			id := s.Add("A", testutil.DeferTo(moment))
			before := s.Task(id)
			b := s.Block(id, "10-29 09:00-10:00")
			requirePlanned(t, s, b)
			requirePlaced(t, s, id, before.Version+1)
			if s.Task(id).View != domain.ViewBacklog || s.Task(id).Rank != before.Rank {
				t.Fatalf("blocked task = %+v", s.Task(id))
			}
		})
	}
}

func TestBLK29(t *testing.T) {
	s, id, b := scheduledToday(t, "10-28 09:00")
	before := s.Task(id)
	s.At("10-28 10:00").Defer(id, "10-30")
	requireBlockState(t, s, b, "{cancelled, –, 1, deferred}")
	requireDeferredPlacement(t, s, id, "10-30", before.Version+1)
	requireSlips(t, s, id, 1, 0)
	block := s.BlockView(b)
	if !block.SlippedAt.Valid || !block.SlippedAt.V.Equal(s.Clock.Now()) || block.SlippedSeq.V != s.Op(s.Last.OpID).Seq || s.Task(id).Rank != before.Rank {
		t.Fatalf("slipped defer = %+v", block)
	}
	requireEventKeys(t, s, s.Last.OpID, "block:update 1", "task:update 1", "task:update 1")
}

func TestBLK30Defer(t *testing.T) {
	s, id, b := scheduledToday(t, "10-28 09:50")
	before := s.Task(id)
	s.At("10-28 10:00").Defer(id, "10-30")
	requireBlockState(t, s, b, "{cancelled, –, 0, deferred}")
	requireDeferredPlacement(t, s, id, "10-30", before.Version+1)
	requireSlips(t, s, id, 0, 0)
	if block := s.BlockView(b); block.SlippedAt.Valid || block.SlippedSeq.Valid {
		t.Fatalf("fresh block slipped: %+v", block)
	}
	for _, key := range s.EventKeys(s.Last.OpID) {
		if strings.Contains(key, "slipped") {
			t.Fatalf("fresh cancellation emitted slip event: %s", key)
		}
	}
}

func TestSLP07(t *testing.T) {
	s, id, b := scheduledToday(t, "10-28 09:00")
	s.At("10-28 14:00").Log(id, "work", "10-28")
	before := s.Task(id)
	s.Defer(id, "10-30")
	requireBlockState(t, s, b, "{worked, auto, 0, –}")
	requireDeferredPlacement(t, s, id, "10-30", before.Version+1)
	requireSlips(t, s, id, 0, 0)
	requireEventKeys(t, s, s.Last.OpID, "block:update 1", "task:update 1", "task:update 1")
}

func TestDUE04(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("gap", testutil.DeferTo("2027-03-14 02:30"))
	before, count := s.Task(id), s.OpCount()
	s.At("2027-03-14T08:59Z").Reconcile()
	if !reflect.DeepEqual(s.Task(id).Task, before.Task) || s.OpCount() != count || !s.Task(id).Deferred {
		t.Fatal("gap defer surfaced before the first valid instant")
	}
	s.At("2027-03-14T09:00Z").Reconcile()
	requireDueReasons(t, s, id, domain.ReasonSurfaced)
	if v := s.Task(id); v.DeferDate.Valid || v.DeferMin.Valid || v.Version != before.Version+1 {
		t.Fatalf("surfaced gap task = %+v", v)
	}
}

func TestDUE05(t *testing.T) {
	for _, moment := range []string{"10-29", "10-29 00:00"} {
		t.Run(moment, func(t *testing.T) {
			s := testutil.NewScenario(t)
			id := s.Add("A", testutil.DeferTo(moment))
			before, count := s.Task(id), s.OpCount()
			s.At("10-28 23:59").Reconcile()
			if s.Task(id).View != domain.ViewUpcoming || s.OpCount() != count {
				t.Fatal("date-only defer surfaced early")
			}
			s.At("10-29 00:00").Reconcile()
			requireDueReasons(t, s, id, domain.ReasonSurfaced)
			v := s.Task(id)
			if v.DeferDate.Valid || v.DeferMin.Valid || v.Version != before.Version+1 || v.View != domain.ViewTriage || v.Rank != before.Rank {
				t.Fatalf("surfaced task = %+v", v)
			}
			requireNotPlaced(t, s, before)
			op := latestOp(t, s)
			requireEventKeys(t, s, op.ID, "task:update 1", "triage:add 1 surfaced")
			if op.Kind != domain.OpSystemReconcile || op.Actor != "system" || op.Revertible || !v.Reasons[0].Since.Equal(s.Clock.Now()) || v.Reasons[0].Seq != op.Seq {
				t.Fatalf("surface op/reason = %+v / %+v", op, v.Reasons)
			}
			var image map[string]any
			if err := json.Unmarshal(s.Events(op.ID)[0].After, &image); err != nil {
				t.Fatal(err)
			}
			if val, ok := image["defer_date"]; !ok || val != nil {
				t.Fatalf("surface event = %v", image)
			}
			if moment != "10-29" {
				if val, ok := image["defer_min"]; !ok || val != nil {
					t.Fatalf("surface event = %v", image)
				}
			}
			s.Reconcile()
			if s.OpCount() != count+1 || !reflect.DeepEqual(s.Task(id), v) {
				t.Fatal("repeated surface reconcile changed state")
			}
		})
	}
}

func checkDeferDueCases(t *testing.T, due string, cases []struct {
	moment string
	valid  bool
}) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.moment, func(t *testing.T) {
			for _, add := range []bool{false, true} {
				t.Run(map[bool]string{false: "defer", true: "add"}[add], func(t *testing.T) {
					s := testutil.NewScenario(t)
					until, dueM := testutil.Moment(tc.moment), testutil.Moment(due)
					var r *service.OpResult
					var err error
					var before domain.TaskView
					if add {
						r, err = s.Svc.AddTask(s.Ctx(), s.Meta(), service.AddTaskIn{Title: "A", Due: &dueM, Place: &service.AddPlacement{How: "defer", Defer: until}})
					} else {
						id := s.Add("A", testutil.Due(due))
						before = s.Task(id)
						r, err = s.Svc.Defer(s.Ctx(), s.Meta(), service.DeferIn{ID: id, Expect: s.Expect(id), Until: &until})
					}
					if !tc.valid {
						testutil.RequireCode(t, err, domain.CodeValidation)
						if (add && s.OpCount() != 0) || (!add && !reflect.DeepEqual(s.Task(before.ID), before)) {
							t.Fatal("invalid defer persisted changes")
						}
						return
					}
					s.Must(r, err)
					requireDeferredPlacement(t, s, r.Task.ID, tc.moment, before.Version+1)
				})
			}
		})
	}
}

func TestDUE06(t *testing.T) {
	checkDeferDueCases(t, "10-30", []struct {
		moment string
		valid  bool
	}{{"10-30", true}, {"10-30 23:59", true}, {"10-31", false}})
}

func TestDUE07(t *testing.T) {
	checkDeferDueCases(t, "10-30 10:00", []struct {
		moment string
		valid  bool
	}{{"10-30 10:00", false}, {"10-30 09:59", true}, {"10-30", true}})
}

func TestDUE08(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A", testutil.DeferTo("11-02"))
	for _, tc := range []struct {
		moment string
		valid  bool
	}{{"10-30", false}, {"11-02", true}, {"11-02 00:00", false}, {"11-02 00:01", true}} {
		before := s.Task(id)
		due := testutil.Moment(tc.moment)
		r, err := s.Svc.SetDue(s.Ctx(), s.Meta(), service.SetDueIn{ID: id, Expect: s.Expect(id), Due: &due})
		if !tc.valid {
			testutil.RequireCode(t, err, domain.CodeValidation)
			if !reflect.DeepEqual(s.Task(id), before) {
				t.Fatal("invalid due changed deferred task")
			}
		} else {
			s.Must(r, err)
			if v := s.Task(id); v.DeferDate != before.DeferDate || v.DeferMin != before.DeferMin || v.DueDate.V != due.Date || v.DueMin != due.Min {
				t.Fatalf("due edit = %+v", v)
			}
		}
	}
}

func TestDUE09(t *testing.T) {
	s := testutil.NewScenario(t).At("10-28 08:00")
	id := s.Add("A", testutil.Due("10-28 09:00"), testutil.Bottom())
	s.At("10-28 10:00").Reconcile()
	requireDueReasons(t, s, id, domain.ReasonOverdue)
	for _, moment := range []string{"10-28 15:00", "10-28 08:00"} {
		until := testutil.Moment(moment)
		_, err := s.Svc.Defer(s.Ctx(), s.Meta(), service.DeferIn{ID: id, Expect: s.Expect(id), Until: &until})
		testutil.RequireCode(t, err, domain.CodeValidation)
		de, _ := domain.AsError(err)
		if !strings.Contains(de.Detail, "change the due date first") {
			t.Fatalf("overdue rejection lost priority: %+v", de)
		}
	}
	// Clearing a defer is a placement even for an overdue task.
	before := s.Task(id)
	s.Defer(id, "")
	requirePlaced(t, s, id, before.Version+1)
}

func TestDUE12(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A", testutil.DeferTo("10-30"), testutil.Due("10-30"))
	before, count := s.Task(id), s.OpCount()
	s.At("10-30 00:00").Reconcile()
	requireDueReasons(t, s, id, domain.ReasonDue, domain.ReasonSurfaced)
	op := latestOp(t, s)
	if s.OpCount() != count+1 || s.Task(id).Version != before.Version+1 || s.Task(id).DeferDate.Valid {
		t.Fatal("surfacing and due did not share one reconcile/version bump")
	}
	for _, reason := range s.Task(id).Reasons {
		if !reason.Since.Equal(s.Clock.Now()) || reason.Seq != op.Seq {
			t.Fatalf("reason = %+v", reason)
		}
	}
	requireEventKeys(t, s, op.ID, "task:update 1", "triage:add 1 surfaced", "triage:add 1 due")
}

func TestDUE17(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A", testutil.DeferTo("10-30"), testutil.Due("11-02"))
	if v := s.Task(id); v.View != domain.ViewUpcoming || v.DueSoon {
		t.Fatalf("initial upcoming = %+v", v)
	}
	s.At("10-30 00:00").Reconcile()
	requireDueReasons(t, s, id, domain.ReasonSurfaced)
	s.At("10-30 09:00").Place(id, domain.PlaceKeep)
	requireDueReasons(t, s, id)
	if s.Task(id).DueAck != domain.DueNone {
		t.Fatal("placement acknowledged future due")
	}
	s.At("11-02 00:00").Reconcile()
	requireDueReasons(t, s, id, domain.ReasonDue)
	s.At("11-03 00:00").Reconcile()
	requireDueReasons(t, s, id, domain.ReasonOverdue, domain.ReasonDue)
	s.Place(id, domain.PlaceKeep)
	if v := s.Task(id); v.DueAck != domain.DueOverdue || v.DueAckDate.V != testutil.D("11-03") {
		t.Fatalf("overdue acknowledgement = %+v", v)
	}
	s.At("11-04 00:00").Reconcile()
	requireDueReasons(t, s, id, domain.ReasonOverdue)
}

func TestDUE21(t *testing.T) {
	s := testutil.NewScenario(t)
	deferred := s.Add("deferred", testutil.DeferTo("10-30"))
	due := testutil.Moment("10-29")
	_, err := s.Svc.SetDue(s.Ctx(), s.Meta(), service.SetDueIn{ID: deferred, Expect: s.Expect(deferred), Due: &due})
	testutil.RequireCode(t, err, domain.CodeValidation)
	id := s.Add("due", testutil.Due("10-29"))
	until := testutil.Moment("10-30")
	_, err = s.Svc.Defer(s.Ctx(), s.Meta(), service.DeferIn{ID: id, Expect: s.Expect(id), Until: &until})
	testutil.RequireCode(t, err, domain.CodeValidation)
	if s.Task(deferred).DueDate.Valid || s.Task(id).DeferDate.Valid {
		t.Fatal("invalid due/defer ordering was persisted")
	}
}

func TestDAT05(t *testing.T) {
	for _, tc := range []struct{ name, moment string }{
		{"today", "10-28"}, {"yesterday", "10-27"}, {"-1d", "10-27"}, {"today 09:00", "10-28 09:00"}, {"exactly now", "10-28 10:00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testutil.NewScenario(t)
			id := s.Add("A")
			until := testutil.Moment(tc.moment)
			_, err := s.Svc.Defer(s.Ctx(), s.Meta(), service.DeferIn{ID: id, Expect: s.Expect(id), Until: &until})
			testutil.RequireCode(t, err, domain.CodeValidation)
			if de, _ := domain.AsError(err); de.Detail != "defer must be in the future" {
				t.Fatalf("API problem detail = %q", de.Detail)
			}
			_, err = s.Svc.AddTask(s.Ctx(), s.Meta(), service.AddTaskIn{Title: "B", Place: &service.AddPlacement{How: "defer", Defer: until}})
			testutil.RequireCode(t, err, domain.CodeValidation)
			if s.OpCount() != 1 || s.Task(id).DeferDate.Valid {
				t.Fatal("past defer changed state")
			}
		})
	}
	s := testutil.NewScenario(t)
	id := s.Add("A")
	s.Defer(id, "10-28 15:00")
	requireDeferredPlacement(t, s, id, "10-28 15:00", 2)
}

func TestREC02(t *testing.T) {
	s := testutil.NewScenario(t).At("10-25 09:00")
	one := s.Add("blocks", testutil.Bottom())
	b1 := s.Block(one, "10-26 13:00-15:00")
	b2 := s.Block(one, "10-27 13:00-15:00")
	two := s.Add("deferred", testutil.DeferTo("10-27"))
	three := s.Add("due", testutil.Due("10-26"), testutil.Bottom())
	s.SetFocus(one, false)
	s.At("10-25 12:00")
	count := s.OpCount()
	versions := map[domain.TaskID]domain.Version{one: s.Task(one).Version, two: s.Task(two).Version, three: s.Task(three).Version}
	s.At("10-28 09:00").Reconcile()
	op := latestOp(t, s)
	if s.OpCount() != count+1 || op.Kind != domain.OpSystemReconcile || op.Actor != "system" || op.Revertible {
		t.Fatalf("downtime reconcile = %+v", op)
	}
	for _, b := range []domain.BlockID{b1, b2} {
		requireBlockState(t, s, b, "{missed, auto, 1, –}")
		v := s.BlockView(b)
		if !v.SlippedAt.Valid || !v.SlippedAt.V.Equal(s.Clock.Now()) || v.SlippedSeq.V != op.Seq {
			t.Fatalf("downtime slip = %+v", v)
		}
	}
	requireDueReasons(t, s, one, domain.ReasonSlipped)
	requireDueReasons(t, s, two, domain.ReasonSurfaced)
	requireDueReasons(t, s, three, domain.ReasonOverdue, domain.ReasonDue)
	requireFocus(t, s, 0)
	if v := s.Task(two); v.DeferDate.Valid || v.DeferMin.Valid {
		t.Fatal("downtime defer not cleared")
	}
	for id, version := range versions {
		v := s.Task(id)
		if v.Version != version+1 {
			t.Fatalf("task %d version = %d, want %d", id, v.Version, version+1)
		}
		for _, reason := range v.Reasons {
			if !reason.Since.Equal(s.Clock.Now()) || reason.Seq != op.Seq {
				t.Fatalf("downtime reason = %+v", reason)
			}
		}
	}
	requireEventKeys(t, s, op.ID, "task:update 2", "triage:add 2 surfaced", "triage:add 3 due", "triage:add 3 overdue",
		"block:update 1", "triage:add 1 slipped", "block:update 2", "focus:set")
	s.Reconcile()
	if s.OpCount() != count+1 {
		t.Fatal("downtime reconcile not idempotent")
	}
}

func TestLIF11(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	s.SetFocus(id, false)
	before := s.Task(id)
	s.Defer(id, "10-30")
	requireDeferredPlacement(t, s, id, "10-30", before.Version+1)
	requireFocus(t, s, 0)
	requireEventKeys(t, s, s.Last.OpID, "triage:remove 1 new", "task:update 1", "task:update 1", "focus:set")
	s.Reconcile()
	requireFocus(t, s, 0)
}

func TestDeferTypedMoments(t *testing.T) {
	for _, until := range []domain.Moment{
		{},
		{Date: testutil.D("10-30"), Min: sql.Null[civil.Minute]{V: -1, Valid: true}},
		{Date: testutil.D("10-30"), Min: sql.Null[civil.Minute]{V: 1440, Valid: true}},
		{Date: testutil.D("10-30"), Min: sql.Null[civil.Minute]{V: 32767, Valid: true}},
	} {
		t.Run(until.String(), func(t *testing.T) {
			s := testutil.NewScenario(t)
			id := s.Add("A")
			_, err := s.Svc.Defer(s.Ctx(), s.Meta(), service.DeferIn{ID: id, Expect: s.Expect(id), Until: &until})
			testutil.RequireCode(t, err, domain.CodeValidation)
			_, err = s.Svc.AddTask(s.Ctx(), s.Meta(), service.AddTaskIn{Title: "B", Place: &service.AddPlacement{How: "defer", Defer: until}})
			testutil.RequireCode(t, err, domain.CodeValidation)
			if s.OpCount() != 1 {
				t.Fatal("invalid typed moment wrote an op")
			}
		})
	}
	s := testutil.NewScenario(t)
	id := s.Add("A")
	for _, moment := range []string{"10-30 00:00", "10-30 23:59"} {
		version, rank := s.Task(id).Version, s.Task(id).Rank
		s.Defer(id, moment)
		requireDeferredPlacement(t, s, id, moment, version+1)
		if s.Task(id).Rank != rank {
			t.Fatal("changing defer changed rank")
		}
	}
}

func TestDeferClosesOnlyPlannedBlocks(t *testing.T) {
	s, id, old := resolvedYesterday(t, false)
	oldBefore := s.BlockView(old).TimeBlock
	current := s.Block(id, "10-28 09:00-11:00")
	future := s.Block(id, "10-29 13:00-15:00")
	s.At("10-28 10:30").Defer(id, "10-30")
	requireBlockState(t, s, current, "{missed, auto, 1, –}")
	requireBlockState(t, s, future, "{cancelled, –, 0, deferred}")
	requireSlips(t, s, id, 2, 0)
	if !reflect.DeepEqual(s.BlockView(old).TimeBlock, oldBefore) {
		t.Fatal("defer modified a resolved block")
	}
	for _, key := range s.EventKeys(s.Last.OpID) {
		if strings.HasPrefix(key, "triage:add") {
			t.Fatalf("placement emitted transient reason: %s", key)
		}
	}
}

func TestDeferValidationBeforeClosing(t *testing.T) {
	s, id, b := scheduledToday(t, "10-28 09:00")
	s.SetDue(id, "10-29")
	s.At("10-28 10:00").SetFocus(id, false)
	before, block, focus, count := s.Task(id), s.BlockView(b).TimeBlock, s.Focus(), s.OpCount()
	until := testutil.Moment("10-30")
	_, err := s.Svc.Defer(s.Ctx(), s.Meta(), service.DeferIn{ID: id, Expect: s.Expect(id), Until: &until})
	testutil.RequireCode(t, err, domain.CodeValidation)
	if !reflect.DeepEqual(s.Task(id), before) || !reflect.DeepEqual(s.BlockView(b).TimeBlock, block) || !reflect.DeepEqual(s.Focus(), focus) || s.OpCount() != count {
		t.Fatal("invalid defer changed task, block or focus")
	}
}

func TestSurfaceNeedChecksInstant(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A", testutil.DeferTo("10-28 15:00"))
	count := s.OpCount()
	// A same-day future defer must pass the read-only need check without a writer.
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
	if err != nil || s.OpCount() != count || !s.Task(id).Deferred {
		t.Fatalf("future defer acquired writer or surfaced: %v", err)
	}
	s.At("10-28 15:00").Reconcile()
	requireDueReasons(t, s, id, domain.ReasonSurfaced)
}

func TestDeferContract(t *testing.T) {
	for _, moment := range []string{"", "10-30"} {
		t.Run(moment, func(t *testing.T) {
			s := testutil.NewScenario(t)
			id := s.Add("A")
			in := service.DeferIn{ID: id, Expect: s.Expect(id)}
			if moment != "" {
				until := testutil.Moment(moment)
				in.Until = &until
			}
			for _, tc := range []struct {
				name   string
				expect domain.Expect
				code   domain.Code
			}{
				{"missing", domain.Expect{}, domain.CodePreconditionReq},
				{"stale", domain.Expect{Gen: s.Svc.Generation(), Version: in.Expect.Version + 1}, domain.CodePrecondition},
			} {
				t.Run(tc.name, func(t *testing.T) {
					bad := in
					bad.Expect = tc.expect
					_, err := s.Svc.Defer(s.Ctx(), s.Meta(), bad)
					testutil.RequireCode(t, err, tc.code)
				})
			}
			m := s.Meta()
			m.Actor.Scopes = domain.ScopeRead | domain.ScopeCapture
			_, err := s.Svc.Defer(s.Ctx(), m, in)
			testutil.RequireCode(t, err, domain.CodeForbidden)
			m = s.Meta()
			m.Idem.Key = ""
			_, err = s.Svc.Defer(s.Ctx(), m, in)
			testutil.RequireCode(t, err, domain.CodeIdemRequired)
			if s.OpCount() != 1 {
				t.Fatal("rejected defer wrote an op")
			}
			m = s.Meta()
			first := s.Must(s.Svc.Defer(s.Ctx(), m, in))
			before, count := s.Task(id), s.OpCount()
			replay := s.Must(s.Svc.Defer(s.Ctx(), m, in))
			if !replay.Replayed || replay.OpID != first.OpID || !reflect.DeepEqual(replay.Effects, first.Effects) || s.OpCount() != count || !reflect.DeepEqual(s.Task(id), before) ||
				!s.Op(first.OpID).Revertible || !slices.Contains(s.Stack(s.Actor.Key(), store.StackUndo), first.OpID) {
				t.Fatalf("replay/undo contract = %+v / %+v", first, replay)
			}
		})
	}
}

func TestDeferEligibility(t *testing.T) {
	for _, state := range []string{"missing", "deleted", "dropped", "done"} {
		t.Run(state, func(t *testing.T) {
			s := testutil.NewScenario(t)
			id, expect, code := domain.TaskID(99), domain.Expect{Gen: s.Svc.Generation(), Version: 1}, domain.CodeNotFound
			if state != "missing" {
				// F14 fixture shortcut: lifecycle ops arrive in Tasks 28-29.
				id = focusIneligibleFixture(t, s, state)
				expect = s.Expect(id)
				code = domain.CodeConflict
				if state == "deleted" {
					code = domain.CodeDeleted
				}
			}
			until, count := testutil.Moment("10-30"), s.OpCount()
			_, err := s.Svc.Defer(s.Ctx(), s.Meta(), service.DeferIn{ID: id, Expect: expect, Until: &until})
			testutil.RequireCode(t, err, code)
			if s.OpCount() != count {
				t.Fatal("ineligible defer wrote an op")
			}
		})
	}
}

func TestSurfaceExcludesClosedAndDeleted(t *testing.T) {
	for _, state := range []string{"dropped", "deleted"} {
		t.Run(state, func(t *testing.T) {
			s := testutil.NewScenario(t)
			id := s.Add("A", testutil.DeferTo("10-29"))
			// AIDEV-NOTE: F14 fixture shortcut: Drop/Delete are not available
			// until Tasks 28-29; seed the closed/tombstoned Given only.
			s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), []domain.EntityRef{{Entity: domain.EntTask, ID: int64(id)}}, func(q store.Q) error {
				row, err := store.GetTask(s.Ctx(), q, id)
				if err != nil {
					return err
				}
				if state == "dropped" {
					row.Status = domain.StatusDropped
				} else {
					row.DeletedAt = sql.Null[time.Time]{V: s.Clock.Now(), Valid: true}
				}
				return store.UpdateTask(s.Ctx(), q, row)
			}))
			before, count := s.Task(id).Task, s.OpCount()
			s.At("10-30 10:00").Reconcile()
			if !reflect.DeepEqual(s.Task(id).Task, before) || s.OpCount() != count || len(s.Reasons(id)) != 0 {
				t.Fatal("ineligible task surfaced")
			}
		})
	}
}

func TestSurfaceFoldAndStalePrecondition(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("fold", testutil.DeferTo("11-01 01:30"))
	expect := s.Expect(id)
	s.At("2026-11-01T07:29Z").Reconcile()
	if !s.Task(id).Deferred {
		t.Fatal("fold defer surfaced early")
	}
	s.At("2026-11-01T07:30Z")
	_, err := s.Svc.Defer(s.Ctx(), s.Meta(), service.DeferIn{ID: id, Expect: expect})
	testutil.RequireCode(t, err, domain.CodePrecondition)
	requireDueReasons(t, s, id, domain.ReasonSurfaced)
	before, count := s.Task(id), s.OpCount()
	if before.DeferDate.Valid || before.Version != expect.Version+1 {
		t.Fatalf("precondition was checked before surfacing: %+v", before)
	}
	s.At("2026-11-01T08:30Z").Reconcile()
	if !reflect.DeepEqual(s.Task(id), before) || s.OpCount() != count {
		t.Fatal("second fold occurrence surfaced again")
	}
}

func TestAddDeferredValidation(t *testing.T) {
	s := testutil.NewScenario(t)
	until := testutil.Moment("10-30")
	for _, due := range []domain.Moment{
		{Date: testutil.D("10-31"), Min: sql.Null[civil.Minute]{V: -1, Valid: true}},
		{Date: testutil.D("10-31"), Min: sql.Null[civil.Minute]{V: 1440, Valid: true}},
	} {
		_, err := s.Svc.AddTask(s.Ctx(), s.Meta(), service.AddTaskIn{Title: "A", Due: &due, Place: &service.AddPlacement{How: "defer", Defer: until}})
		testutil.RequireCode(t, err, domain.CodeValidation)
	}
	due := testutil.Moment("10-28 09:00")
	_, err := s.Svc.AddTask(s.Ctx(), s.Meta(), service.AddTaskIn{Title: "A", Due: &due, Place: &service.AddPlacement{How: "defer", Defer: until}})
	testutil.RequireCode(t, err, domain.CodeValidation)
	de, _ := domain.AsError(err)
	if !strings.Contains(de.Detail, "change the due date first") {
		t.Fatalf("AddTask defer did not share overdue validation: %+v", de)
	}
	if s.OpCount() != 0 {
		t.Fatal("invalid deferred addition wrote an op")
	}
	m := s.Meta()
	m.Actor = testutil.AI()
	_, err = s.Svc.AddTask(s.Ctx(), m, service.AddTaskIn{Title: "A", Place: &service.AddPlacement{How: "defer", Defer: until}})
	testutil.RequireCode(t, err, domain.CodeForbidden)
}
