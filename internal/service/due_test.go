package service_test

import (
	"database/sql"
	"encoding/json"
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

func requireDueReasons(t *testing.T, s *testutil.Scenario, id domain.TaskID, want ...domain.TriageReason) {
	t.Helper()
	if got := s.Reasons(id); !slices.Equal(got, want) {
		t.Fatalf("reasons = %v, want %v", got, want)
	}
}

func TestDUE01(t *testing.T) {
	s := testutil.NewScenario(t).At("10-27 09:00")
	id := s.Add("A", testutil.Due("10-28"), testutil.Bottom())
	s.At("10-28 00:00").Reconcile()
	requireDueReasons(t, s, id, domain.ReasonDue)
	due := s.Task(id)
	s.At("10-28 23:59").Reconcile()
	requireDueReasons(t, s, id, domain.ReasonDue)
	if got := s.Task(id); got.Version != due.Version || !reflect.DeepEqual(got.Reasons, due.Reasons) {
		t.Fatal("due reason changed before the end of the day")
	}
	s.At("2026-10-29T06:00Z").Reconcile()
	requireDueReasons(t, s, id, domain.ReasonOverdue, domain.ReasonDue)
}

func TestDUE02(t *testing.T) {
	s := testutil.NewScenario(t).At("10-27 09:00")
	id := s.Add("A", testutil.Due("10-28 17:00"), testutil.Bottom())
	s.At("10-28 00:00").Reconcile()
	requireDueReasons(t, s, id, domain.ReasonDue)
	due := s.Task(id)
	s.At("10-28 16:59").Reconcile()
	requireDueReasons(t, s, id, domain.ReasonDue)
	if got := s.Task(id); got.Version != due.Version || !reflect.DeepEqual(got.Reasons, due.Reasons) {
		t.Fatal("due reason changed before the due minute")
	}
	s.At("10-28 17:00").Reconcile()
	requireDueReasons(t, s, id, domain.ReasonOverdue, domain.ReasonDue)
}

func TestDUE03(t *testing.T) {
	s := testutil.NewScenario(t).At("10-31 09:00")
	id := s.Add("A", testutil.Due("2026-11-01 01:30"), testutil.Bottom())
	s.At("2026-11-01T07:29Z").Reconcile()
	requireDueReasons(t, s, id, domain.ReasonDue)
	s.At("2026-11-01T07:30Z").Reconcile()
	requireDueReasons(t, s, id, domain.ReasonOverdue, domain.ReasonDue)
	overdue := s.Task(id)
	s.At("2026-11-01T08:30Z").Reconcile()
	if got := s.Task(id); got.Version != overdue.Version || !reflect.DeepEqual(got.Reasons, overdue.Reasons) {
		t.Fatal("later DST occurrence changed the overdue reason")
	}
}

func TestDUE10(t *testing.T) {
	for _, clear := range []bool{false, true} {
		name := "reschedule"
		if clear {
			name = "clear"
		}
		t.Run(name, func(t *testing.T) {
			s := testutil.NewScenario(t).At("10-25 09:00")
			id := s.Add("A", testutil.Due("10-26"))
			seq := s.Op(s.Last.OpID).Seq
			s.At(testutil.DefaultNow)
			// AIDEV-NOTE: Controller-authorized DUE-10 fixture shortcut. Real
			// placements clear reasons, so this Given needs raw acknowledgement/reason rows.
			s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), []domain.EntityRef{{Entity: domain.EntTask, ID: int64(id)}}, func(q store.Q) error {
				task, err := store.GetTask(s.Ctx(), q, id)
				if err != nil {
					return err
				}
				task.DueAck = domain.DueOverdue
				task.DueAckDate = sql.Null[civil.Date]{V: testutil.D("10-28"), Valid: true}
				if err := store.UpdateTask(s.Ctx(), q, task); err != nil {
					return err
				}
				for _, reason := range []domain.TriageReason{domain.ReasonNew, domain.ReasonDue, domain.ReasonOverdue} {
					if _, err := store.AddReason(s.Ctx(), q, domain.TriageRow{TaskID: id, Reason: reason, Since: s.Clock.Now(), Seq: seq}); err != nil {
						return err
					}
				}
				return nil
			}))
			requireDueReasons(t, s, id, domain.ReasonOverdue, domain.ReasonDue, domain.ReasonNew)
			before := s.Task(id)
			moment := "11-05"
			if clear {
				moment = ""
			}
			s.SetDue(id, moment)
			after := s.Task(id)
			requireDueReasons(t, s, id, domain.ReasonNew)
			if after.DueAck != domain.DueNone || after.DueAckDate.Valid || after.DueMin.Valid || after.Version != before.Version+1 ||
				after.Rank != before.Rank || after.LastPlacedSeq != before.LastPlacedSeq ||
				after.DueDate.Valid == clear || (!clear && after.DueDate.V != testutil.D("11-05")) {
				t.Fatalf("SetDue task = %+v", after)
			}
			if !reflect.DeepEqual(after.Reasons[0], before.Reasons[2]) {
				t.Fatal("SetDue changed the new reason")
			}
			if got := s.EventKeys(s.Last.OpID); !slices.Equal(got, []string{"task:update 1", "triage:remove 1 due", "triage:remove 1 overdue"}) {
				t.Fatalf("SetDue events = %v", got)
			}
			var image map[string]any
			if err := json.Unmarshal(s.Events(s.Last.OpID)[0].After, &image); err != nil {
				t.Fatal(err)
			}
			var wantDate any = "2026-11-05"
			if clear {
				wantDate = nil
			}
			if !reflect.DeepEqual(image, map[string]any{"due_date": wantDate, "due_min": nil, "due_ack": "none", "due_ack_date": nil}) {
				t.Fatalf("SetDue image = %#v", image)
			}
			count := s.OpCount()
			s.Reconcile()
			s.At("11-04 23:59").Reconcile()
			requireDueReasons(t, s, id, domain.ReasonNew)
			if s.OpCount() != count || s.Task(id).Version != after.Version {
				t.Fatal("reconcile changed task before the new due date")
			}
			s.At("11-05 00:00").Reconcile()
			if clear {
				requireDueReasons(t, s, id, domain.ReasonNew)
			} else {
				requireDueReasons(t, s, id, domain.ReasonDue, domain.ReasonNew)
			}
		})
	}
}

func TestDUE11(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	yesterday := testutil.Moment("10-27")
	_, err := s.Svc.SetDue(s.Ctx(), s.Meta(), service.SetDueIn{ID: id, Expect: s.Expect(id), Due: &yesterday})
	testutil.RequireCode(t, err, domain.CodeValidation)
	s.SetDue(id, "10-28")
	if task := s.Task(id); !task.DueDate.Valid || task.DueDate.V != testutil.D("10-28") || task.DueMin.Valid {
		t.Fatalf("today date-only = %+v", task)
	}
}

func TestDUE14(t *testing.T) {
	s := testutil.NewScenario(t).At("10-25 09:00")
	id := s.Add("A", testutil.Due("10-26"), testutil.Bottom())
	s.At("10-27 00:00").Reconcile()
	requireDueReasons(t, s, id, domain.ReasonOverdue, domain.ReasonDue)
	s.At("10-27 09:00").Place(id, domain.PlaceKeep)
	requireDueReasons(t, s, id)
	if task := s.Task(id); task.DueAck != domain.DueOverdue || !task.DueAckDate.Valid || task.DueAckDate.V != testutil.D("10-27") {
		t.Fatalf("acknowledgement = %+v", task)
	}
	s.At("10-27 23:59").Reconcile()
	requireDueReasons(t, s, id)
	s.At("10-28 00:00").Reconcile()
	requireDueReasons(t, s, id, domain.ReasonOverdue)
	overdue := s.Task(id)
	if !overdue.Reasons[0].Since.Equal(testutil.Local(t, "10-28 00:00")) {
		t.Fatalf("overdue since = %v", overdue.Reasons[0].Since)
	}
	s.At("10-29 00:00").Reconcile()
	requireDueReasons(t, s, id, domain.ReasonOverdue)
	if got := s.Task(id); got.Version != overdue.Version || !reflect.DeepEqual(got.Reasons, overdue.Reasons) {
		t.Fatal("unacknowledged overdue reason was refreshed")
	}
	s.Place(id, domain.PlaceKeep)
	requireDueReasons(t, s, id)
	s.At("10-30 00:00").Reconcile()
	requireDueReasons(t, s, id, domain.ReasonOverdue)
}

func TestDUE15(t *testing.T) {
	s := testutil.NewScenario(t).At("10-27 09:00")
	id := s.Add("A", testutil.Due("10-28 17:00"), testutil.Bottom())
	s.At(testutil.DefaultNow).Reconcile()
	requireDueReasons(t, s, id, domain.ReasonDue)
	version := s.Task(id).Version
	s.Place(id, domain.PlaceKeep)
	requirePlaced(t, s, id, version+1)
	if task := s.Task(id); task.DueAck != domain.DueDue || task.DueAckDate.V != testutil.D("10-28") {
		t.Fatalf("acknowledgement = %+v", task)
	}
	s.At("10-28 17:00").Reconcile()
	requireDueReasons(t, s, id, domain.ReasonOverdue)
}

func TestDUE16(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A", testutil.Due("11-05"))
	requireDueReasons(t, s, id, domain.ReasonNew)
	s.Place(id, domain.PlaceKeep)
	requirePlaced(t, s, id, 2)
	if task := s.Task(id); task.DueAck != domain.DueNone || !task.DueAckDate.Valid || task.DueAckDate.V != testutil.D("10-28") {
		t.Fatalf("acknowledgement = %+v", task)
	}
}

func TestTRI24(t *testing.T) {
	s := testutil.NewScenario(t).At("10-27 09:00")
	id := s.Add("A", testutil.Due("10-28"), testutil.Bottom())
	requireDueReasons(t, s, id)
	if s.Task(id).DueAck != domain.DueNone {
		t.Fatal("future due was acknowledged")
	}
	s.At(testutil.DefaultNow).Reconcile()
	requireDueReasons(t, s, id, domain.ReasonDue)
	version := s.Task(id).Version
	s.Place(id, domain.PlaceKeep)
	requirePlaced(t, s, id, version+1)
	if task := s.Task(id); task.DueAck != domain.DueDue || task.DueAckDate.V != testutil.D("10-28") {
		t.Fatalf("acknowledgement = %+v", task)
	}
}

func TestSetDueValidation(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	for _, tc := range []struct {
		name string
		due  domain.Moment
	}{
		{"minute 1440", domain.Moment{Date: testutil.D("10-29"), Min: sql.Null[civil.Minute]{V: 1440, Valid: true}}},
		{"minute negative", domain.Moment{Date: testutil.D("10-29"), Min: sql.Null[civil.Minute]{V: -1, Valid: true}}},
		{"minute above civil range", domain.Moment{Date: testutil.D("10-29"), Min: sql.Null[civil.Minute]{V: 1441, Valid: true}}},
		{"invalid date", domain.Moment{}},
		{"past", testutil.Moment("10-28 09:59")},
		{"equal now", testutil.Moment("10-28 10:00")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, count := s.Task(id), s.OpCount()
			_, err := s.Svc.SetDue(s.Ctx(), s.Meta(), service.SetDueIn{ID: id, Expect: s.Expect(id), Due: &tc.due})
			testutil.RequireCode(t, err, domain.CodeValidation)
			if s.OpCount() != count || !reflect.DeepEqual(s.Task(id), before) {
				t.Fatal("invalid due changed state")
			}
		})
	}

	s.SetDue(id, "10-29")
	// Task 20 permits this tombstone fixture until Delete is implemented.
	s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), []domain.EntityRef{{Entity: domain.EntTask, ID: int64(id)}}, func(q store.Q) error {
		task, err := store.GetTask(s.Ctx(), q, id)
		if err != nil {
			return err
		}
		if _, err := store.RemoveReason(s.Ctx(), q, id, domain.ReasonNew); err != nil {
			return err
		}
		task.DeletedAt = sql.Null[time.Time]{V: s.Clock.Now(), Valid: true}
		return store.UpdateTask(s.Ctx(), q, task)
	}))
	count := s.OpCount()
	s.At("10-30 10:00").Reconcile()
	if s.OpCount() != count || len(s.Reasons(id)) != 0 {
		t.Fatal("reconcile added reasons to a deleted task")
	}
	due := testutil.Moment("11-05")
	_, err := s.Svc.SetDue(s.Ctx(), s.Meta(), service.SetDueIn{ID: id, Expect: s.Expect(id), Due: &due})
	testutil.RequireCode(t, err, domain.CodeDeleted)
}

func TestSetDueContract(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	due := testutil.Moment("10-29 00:00")
	in := service.SetDueIn{ID: id, Expect: s.Expect(id), Due: &due}
	reader := s.As(testutil.TokenActor("reader", domain.ScopeRead))
	_, err := reader.Svc.SetDue(reader.Ctx(), reader.Meta(), in)
	testutil.RequireCode(t, err, domain.CodeForbidden)
	_, err = s.Svc.SetDue(s.Ctx(), s.Meta(), service.SetDueIn{ID: id, Due: &due})
	testutil.RequireCode(t, err, domain.CodePreconditionReq)
	m := s.Meta()
	result := s.Must(s.Svc.SetDue(s.Ctx(), m, in))
	if result.Task == nil || result.Task.Version != in.Expect.Version+1 || !result.Task.DueMin.Valid || result.Task.DueMin.V != 0 {
		t.Fatalf("SetDue result = %+v", result)
	}
	op := s.Op(result.OpID)
	if op.Kind != domain.OpTaskDue || op.Scopes != domain.ScopeWrite || !op.Revertible ||
		!slices.Contains(s.Stack(s.Actor.Key(), store.StackUndo), result.OpID) {
		t.Fatalf("SetDue op = %+v", op)
	}
	count := s.OpCount()
	replay := s.Must(s.Svc.SetDue(s.Ctx(), m, in))
	if !replay.Replayed || replay.OpID != result.OpID || s.OpCount() != count {
		t.Fatal("SetDue replay changed state")
	}
	_, err = s.Svc.SetDue(s.Ctx(), s.Meta(), in)
	testutil.RequireCode(t, err, domain.CodePrecondition)
	s.SetDue(id, "10-29 00:00")
	if !s.Last.NoChange || s.OpCount() != count {
		t.Fatal("identical SetDue wrote an op")
	}
	s.SetDue(id, "10-29 23:59")
	if task := s.Task(id); !task.DueMin.Valid || task.DueMin.V != 1439 {
		t.Fatalf("last minute = %+v", task.DueMin)
	}
	s.SetDue(id, "")
	if task := s.Task(id); task.DueDate.Valid || task.DueMin.Valid || task.DueAckDate.Valid || task.DueAck != domain.DueNone {
		t.Fatalf("cleared due = %+v", task)
	}
}
