package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"taskronin/internal/domain"
	"taskronin/internal/store"
)

func TestFinalKeyedBudgetBeforeCommit(t *testing.T) {
	for _, idem := range []bool{true, false} {
		t.Run(fmt.Sprint(idem), func(t *testing.T) {
			s, m := runnerFixture(t)
			// An injected short budget covers admission and the deliberately blocked body.
			s.opBudget = 2 * time.Second
			entered := false
			_, err := s.run(t.Context(), m, opSpec{class: classForward, kind: "test.budget", idem: idem}, func(o *op) error {
				entered = true
				<-o.ctx.Done()
				return o.ctx.Err()
			})
			if !entered {
				t.Fatal("did not reach the admitted operation")
			}
			want := domain.CodeInternal
			if idem {
				want = domain.CodeBusy
			}
			requireRunnerCode(t, err, want)
			de, _ := domain.AsError(err)
			if idem && de.RetryAfter <= 0 {
				t.Fatal("busy without retry delay", de)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("lost deadline cause", err)
			}
			var count int
			if err := s.store.Writer.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM idempotency`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("failed operation committed idempotency state", count)
			}
		})
	}
}

func TestFinalKeyedBudgetAfterCommitReplays(t *testing.T) {
	s, m := runnerFixture(t)
	s.opBudget = 2 * time.Second
	// Hold the reader, not the writer: AddTask can commit but cannot load its result.
	s.store.Reader.SetMaxOpenConns(1)
	conn, err := s.store.Reader.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = s.AddTask(t.Context(), m, AddTaskIn{Title: "exactly once"})
	requireRunnerCode(t, err, domain.CodeBusy)
	de, _ := domain.AsError(err)
	if de.RetryAfter <= 0 {
		t.Fatal("no retry delay", de)
	}
	var id int64
	if err := s.store.Writer.QueryRowContext(t.Context(), `SELECT id FROM tasks WHERE title = 'exactly once'`).Scan(&id); err != nil {
		t.Fatal("did not commit before budget expiry", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	s.opBudget = 0
	replay, err := s.AddTask(t.Context(), m, AddTaskIn{Title: "exactly once"})
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.Task == nil || int64(replay.Task.ID) != id {
		t.Fatalf("not a replay: %+v", replay)
	}
	var count int
	if err := s.store.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM tasks`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("duplicate task", count)
	}
}

func TestFinalBudgetCancellationAndOtherErrors(t *testing.T) {
	s, m := runnerFixture(t)
	for _, idem := range []bool{true, false} {
		_, err := s.run(t.Context(), m, opSpec{class: classForward, kind: "test.cancel", idem: idem}, func(*op) error { return context.Canceled })
		requireRunnerCode(t, err, domain.CodeInternal)
		if !errors.Is(err, context.Canceled) {
			t.Fatal("lost cancellation", err)
		}
	}
	// Ordinary operations still detach a canceled caller, as they did before S1′.
	caller, cancel := context.WithCancel(t.Context())
	cancel()
	r, err := s.AddTask(caller, m, AddTaskIn{Title: "caller detached"})
	if err != nil || r == nil || r.Task == nil {
		t.Fatal(r, err)
	}
	// A deadline-shaped error without expiry of this operation's budget is not busy.
	m.Idem.Key = domain.NewOpID().String()
	_, err = s.run(t.Context(), m, opSpec{class: classForward, kind: "test.deadline", idem: true}, func(*op) error { return context.DeadlineExceeded })
	requireRunnerCode(t, err, domain.CodeInternal)
}

func TestFinalBudgetSQLiteQuery(t *testing.T) {
	s, m := runnerFixture(t)
	s.opBudget = 2 * time.Second
	entered := false
	_, err := s.run(t.Context(), m, opSpec{class: classForward, kind: "test.sql-budget", idem: true}, func(o *op) error {
		entered = true
		_, err := o.tx.ExecContext(o.ctx, `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n) SELECT sum(x) FROM n`)
		return store.MapErr(err)
	})
	if !entered {
		t.Fatal("expired before admission")
	}
	requireRunnerCode(t, err, domain.CodeBusy)
}

func TestFinalExemptBudgetAfterCommitStaysInternal(t *testing.T) {
	s, m := runnerFixture(t)
	tk := fixtureTask(t, s, 1)
	s.opBudget = 2 * time.Second
	s.store.Reader.SetMaxOpenConns(1)
	conn, err := s.store.Reader.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = s.run(t.Context(), m, opSpec{class: classForward, kind: "test.exempt", idem: false}, func(o *op) error {
		if err := store.SetSetting(o.ctx, o.tx, "test.budget", "committed"); err != nil {
			return err
		}
		o.ref(domain.EntityRef{Entity: domain.EntTask, ID: int64(tk.ID)})
		return o.record(domain.Event{Entity: domain.EntSetting, Action: domain.ActUpdate,
			After: []byte(`{"key":"test.budget","value":"committed"}`)})
	})
	requireRunnerCode(t, err, domain.CodeInternal)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	value, err := store.GetSetting(t.Context(), s.store.Writer, "test.budget")
	if err != nil || value != "committed" {
		t.Fatalf("exempt write did not commit before expiry: value=%q, err=%v", value, err)
	}
}
