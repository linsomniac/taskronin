package service_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/service"
	"github.com/linsomniac/taskronin/internal/testutil"
)

func TestTRI19(t *testing.T) {
	s := testutil.NewScenario(t).At("10-27 09:00")
	id := s.Add("A", testutil.Due("10-27 12:00"))
	stale := s.Expect(id)
	s.At("10-28 10:00") // The op's reconcile adds due and overdue before checking Expect.
	before := s.OpCount()
	_, err := s.Svc.Place(s.Ctx(), s.Meta(), service.PlaceIn{ID: id, Expect: stale, How: domain.PlaceKeep})
	testutil.RequireCode(t, err, domain.CodePrecondition)
	var de *domain.Error
	if !errors.As(err, &de) {
		t.Fatal(err)
	}
	if cur := de.Current.(domain.TaskView); cur.Version != stale.Version+1 ||
		!slices.Contains(s.Reasons(id), domain.ReasonOverdue) {
		t.Fatalf("current = %+v", de.Current)
	}
	requireDueReasons(t, s, id, domain.ReasonOverdue, domain.ReasonDue, domain.ReasonNew)
	if s.OpCount() != before+1 || s.Task(id).LastPlacedSeq.Valid {
		t.Fatal("failed placement changed the task or did not preserve the reconcile op")
	}
	s.Must(s.Svc.Place(s.Ctx(), s.Meta(), service.PlaceIn{ID: id, Expect: s.Expect(id), How: domain.PlaceKeep}))
	if len(s.Reasons(id)) != 0 {
		t.Fatal("placement did not clear the reasons")
	}
	before = s.OpCount()
	_, err = s.Svc.Place(s.Ctx(), s.Meta(), service.PlaceIn{ID: id, How: domain.PlaceKeep})
	testutil.RequireCode(t, err, domain.CodePreconditionReq)
	wrongGen := s.Expect(id)
	wrongGen.Gen = domain.NewGeneration()
	_, err = s.Svc.Place(s.Ctx(), s.Meta(), service.PlaceIn{ID: id, Expect: wrongGen, How: domain.PlaceKeep})
	testutil.RequireCode(t, err, domain.CodePrecondition)
	if s.OpCount() != before {
		t.Fatal("invalid preconditions wrote operations")
	}
}

func TestREC01(t *testing.T) {
	s := testutil.NewScenario(t).At("10-27 09:00")
	a := s.Add("A", testutil.Due("10-28 09:00"), testutil.Bottom())
	b := s.Add("B", testutil.Due("10-28"), testutil.Bottom())
	beforeA, beforeB, count := s.Task(a), s.Task(b), s.OpCount()
	s.At("10-28 10:00").Reconcile()
	requireDueReasons(t, s, a, domain.ReasonOverdue, domain.ReasonDue)
	requireDueReasons(t, s, b, domain.ReasonDue)
	afterA, afterB := s.Task(a), s.Task(b)
	if s.OpCount() != count+1 || afterA.Version != beforeA.Version+1 || afterB.Version != beforeB.Version+1 ||
		!afterA.UpdatedAt.Equal(s.Clock.Now()) || !afterB.UpdatedAt.Equal(s.Clock.Now()) {
		t.Fatal("reconcile must be one op, bumping each changed task once")
	}
	var opID domain.OpID
	if err := s.Store.Reader.QueryRowContext(s.Ctx(), `SELECT op_id FROM ops ORDER BY seq DESC LIMIT 1`).Scan(&opID); err != nil {
		t.Fatal(err)
	}
	op := s.Op(opID)
	if op.Kind != domain.OpSystemReconcile || op.Actor != "system" || op.Channel != domain.ChanSystem || op.Revertible || op.Scopes != 0 {
		t.Fatalf("reconcile op = %+v", op)
	}
	if got := s.EventKeys(opID); !slices.Equal(got, []string{"triage:add 1 due", "triage:add 2 due", "triage:add 1 overdue"}) {
		t.Fatalf("reconcile events = %v", got)
	}
	for _, task := range []domain.TaskView{afterA, afterB} {
		for _, row := range task.Reasons {
			if row.Seq != op.Seq || !row.Since.Equal(s.Clock.Now()) {
				t.Fatalf("reason = %+v", row)
			}
		}
	}

	// Holding the only writer connection proves a no-work reconcile stays on the reader.
	conn, err := s.Store.Writer.Conn(s.Ctx())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(s.Ctx(), slowRunnerWait)
	defer cancel()
	err = s.Svc.Reconcile(ctx)
	if closeErr := conn.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("second reconcile tried to acquire the writer: %v", err)
	}
	if s.OpCount() != count+1 || !reflect.DeepEqual(s.Task(a), afterA) || !reflect.DeepEqual(s.Task(b), afterB) || len(s.Events(opID)) != 3 {
		t.Fatal("second reconcile changed operations, events or task state")
	}
}
