package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"taskronin/internal/auth"
	"taskronin/internal/civil"
	"taskronin/internal/clock"
	"taskronin/internal/domain"
	"taskronin/internal/store"
)

func TestOpInvariantScopeAndRanks(t *testing.T) {
	s, m := runnerFixture(t)
	touched := fixtureTask(t, s, 1)
	untouched := fixtureTask(t, s, 1)
	if _, err := s.store.Writer.ExecContext(t.Context(), `UPDATE tasks SET
		defer_date = '2026-10-30', due_date = '2026-10-29' WHERE id = ?`, untouched.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := s.store.Writer.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	o := s.newOp(t.Context(), tx, m.Actor, opSpec{}, s.clock.Now())
	o.markTaskDirty(touched.ID)
	if err := o.checkInvariants(); err != nil {
		t.Fatalf("checked an untouched task or an unmoved rank: %v", err)
	}
	// Rank checks also apply when the moved task has no dirty version ref.
	o.dirty = make(map[domain.EntityRef]bool)
	o.rankMoved[touched.ID] = true
	err = o.checkInvariants()
	requireRunnerCode(t, err, domain.CodeConflict)
	if de, _ := domain.AsError(err); !strings.Contains(de.Detail, "invariant I7 violated:") {
		t.Fatal(err)
	}
	o.rankMoved[touched.ID] = false
	o.markTaskDirty(untouched.ID)
	err = o.checkInvariants()
	requireRunnerCode(t, err, domain.CodeConflict)
	if de, _ := domain.AsError(err); !strings.Contains(de.Detail, "invariant I3 violated:") || strings.Contains(de.Detail, "invariant I7") {
		t.Fatal(err)
	}
}

func TestOpInvariantStructuralOnlyAndFocus(t *testing.T) {
	s, m := runnerFixture(t)
	tk := fixtureTask(t, s, 1)
	if _, err := s.store.Writer.ExecContext(t.Context(), `UPDATE tasks SET defer_date = '2026-10-29' WHERE id = ?`, tk.ID); err != nil {
		t.Fatal(err)
	}
	b := domain.TimeBlock{TaskID: tk.ID, Span: domain.BlockSpan{Date: s.zone.DateOf(s.clock.Now()), Start: 660, End: 720}, Status: domain.BlockPlanned, CreatedAt: s.clock.Now()}
	if err := store.InsertBlock(t.Context(), s.store.Writer, &b); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.Writer.ExecContext(t.Context(), `UPDATE focus SET task_id = ?, date = '2026-10-27' WHERE id = 1`, tk.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := s.store.Writer.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	o := s.newOp(t.Context(), tx, m.Actor, opSpec{structuralOnly: true}, s.clock.Now())
	o.markTaskDirty(tk.ID)
	if err := o.checkInvariants(); err != nil {
		t.Fatalf("structural-only op checked temporal state: %v", err)
	}
	o.spec.structuralOnly = false
	err = o.checkInvariants()
	requireRunnerCode(t, err, domain.CodeConflict)
	de, _ := domain.AsError(err)
	if !strings.Contains(de.Detail, "invariant I1b violated:") || !strings.Contains(de.Detail, "invariant I6 violated:") {
		t.Fatal(err)
	}
	o.dirty = make(map[domain.EntityRef]bool)
	err = o.checkInvariants()
	requireRunnerCode(t, err, domain.CodeConflict)
	if de, _ := domain.AsError(err); !strings.Contains(de.Detail, "invariant I6 violated:") || strings.Contains(de.Detail, "invariant I1b") {
		t.Fatalf("focus must be checked even without dirty refs: %v", err)
	}
	o.spec.structuralOnly = true
	o.markTaskDirty(tk.ID)
	if _, err := tx.ExecContext(t.Context(), `UPDATE tasks SET due_date = '2026-10-28' WHERE id = ?`, tk.ID); err != nil {
		t.Fatal(err)
	}
	err = o.checkInvariants()
	requireRunnerCode(t, err, domain.CodeConflict)
	if de, _ := domain.AsError(err); !strings.Contains(de.Detail, "invariant I3 violated:") {
		t.Fatal(err)
	}
}

func TestOpInvariantTouchedTaskBlocks(t *testing.T) {
	s, m := runnerFixture(t)
	tk := fixtureTask(t, s, 1)
	other := fixtureTask(t, s, 2)
	if _, err := s.store.Writer.ExecContext(t.Context(), `DROP TRIGGER time_blocks_no_overlap_ins`); err != nil {
		t.Fatal(err)
	}
	for _, task := range []domain.Task{tk, other} {
		b := domain.TimeBlock{TaskID: task.ID, Span: domain.BlockSpan{Date: s.zone.DateOf(s.clock.Now()), Start: 660, End: 720}, Status: domain.BlockPlanned, CreatedAt: s.clock.Now()}
		if err := store.InsertBlock(t.Context(), s.store.Writer, &b); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := s.store.Writer.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	o := s.newOp(t.Context(), tx, m.Actor, opSpec{}, s.clock.Now())
	o.markTaskDirty(tk.ID)
	err = o.checkInvariants()
	requireRunnerCode(t, err, domain.CodeConflict)
	de, _ := domain.AsError(err)
	if strings.Count(de.Detail, "invariant I2 violated:") != 1 {
		t.Fatalf("planned blocks of a touched task must check overlaps with untouched blocks: %v", err)
	}
}

func TestOpInvariantDSTGap(t *testing.T) {
	for _, tc := range []struct {
		name, zone     string
		structuralOnly bool
	}{
		{"Denver all", "America/Denver", false},
		{"Denver structural", "America/Denver", true},
		{"UTC all", "UTC", false},
		{"UTC structural", "UTC", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, m := runnerFixture(t)
			z, err := civil.LoadZone(tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			s.zone = z
			storedZone := "UTC"
			if tc.zone == "UTC" {
				storedZone = "America/Denver"
			}
			if err := store.SetSetting(t.Context(), s.store.Writer, "tz_last_seen", storedZone); err != nil {
				t.Fatal(err)
			}
			tk := fixtureTask(t, s, 1)
			_, err = s.run(t.Context(), m, opSpec{class: classForward, kind: "test.invariant", scopes: domain.ScopeWrite, structuralOnly: tc.structuralOnly}, func(o *op) error {
				after := tk
				after.DeferDate = sql.Null[civil.Date]{V: civil.MustDate("2026-03-08"), Valid: true}
				after.DueDate = after.DeferDate
				after.DeferMin = sql.Null[civil.Minute]{V: 130, Valid: true}
				after.DueMin = sql.Null[civil.Minute]{V: 150, Valid: true}
				if err := store.UpdateTask(o.ctx, o.tx, after); err != nil {
					return err
				}
				return o.recordTaskUpdate(tk, after)
			})
			if tc.zone == "America/Denver" {
				requireRunnerCode(t, err, domain.CodeConflict)
				if de, _ := domain.AsError(err); !strings.Contains(de.Detail, "invariant I3 violated:") {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			got, err := store.GetTask(t.Context(), s.store.Reader, tk.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.zone == "America/Denver" {
				if got.DeferDate.Valid || got.DueDate.Valid || got.Version != 1 {
					t.Fatalf("invalid DST moments survived rollback: %+v", got)
				}
			} else if !got.DeferDate.Valid || !got.DueDate.Valid || got.Version != 2 {
				t.Fatalf("valid UTC moments were not committed: %+v", got)
			}
		})
	}
}

// Internal runner tests avoid importing testutil, which imports service itself.
func runnerFixture(t *testing.T) (*Service, Meta) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runner.db")
	staging, err := store.OpenStaging(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := staging.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := staging.Migrate(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := staging.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.FTSIntegrityCheck(context.Background(), st.Writer); err != nil {
			t.Error(err)
		}
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	z, err := civil.LoadZone("America/Denver")
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{store: st, clock: clock.NewFake(time.Date(2026, 10, 28, 16, 0, 0, 123456789, time.UTC)), zone: z,
		gen: domain.NewGeneration(), undoDepth: 2}
	return s, Meta{Actor: domain.Actor{Kind: domain.ActorOwner, Scopes: domain.AllScopes, Channel: domain.ChanCLI}, Gen: s.gen}
}

func fixtureTask(t *testing.T, s *Service, rank int64) domain.Task {
	t.Helper()
	tk := domain.Task{Title: "task", Status: domain.StatusOpen, DueAck: domain.DueNone, Rank: rank,
		Origin: "owner", CreatedAt: s.clock.Now(), UpdatedAt: s.clock.Now()}
	if err := store.InsertTask(t.Context(), s.store.Writer, &tk); err != nil {
		t.Fatal(err)
	}
	return tk
}

func requireRunnerCode(t *testing.T, err error, want domain.Code) {
	t.Helper()
	de, ok := domain.AsError(err)
	if !ok || de.Code != want {
		t.Fatalf("error = %v, want %s", err, want)
	}
}

func TestRunNoEventsDiscardsWritesAndVersions(t *testing.T) {
	s, m := runnerFixture(t)
	tk := fixtureTask(t, s, 1)
	r, err := s.run(t.Context(), m, opSpec{class: classForward, kind: "test.empty", scopes: domain.ScopeWrite, revertible: true}, func(o *op) error {
		after := tk
		after.Title = "discarded"
		if err := store.UpdateTask(o.ctx, o.tx, after); err != nil {
			return err
		}
		o.markTaskDirty(tk.ID)
		o.ref(domain.EntityRef{Entity: domain.EntTask, ID: int64(tk.ID)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !r.NoChange || !r.OpID.IsZero() || r.Created || len(r.Effects) != 0 || r.Task.Title != tk.Title || r.Task.Version != 1 {
		t.Fatalf("result = %+v", r)
	}
	var n int
	if err := s.store.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM ops`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("no-op retained its ops row")
	}
	stack, err := store.StackOps(t.Context(), s.store.Reader, m.Actor.Key(), store.StackUndo)
	if err != nil || len(stack) != 0 {
		t.Fatalf("stack = %v, %v", stack, err)
	}
}

func TestInsertedVersionsAndResultRefs(t *testing.T) {
	s, m := runnerFixture(t)
	var tasks []domain.Task
	var entries []domain.LogEntry
	var blocks []domain.TimeBlock
	r, err := s.run(t.Context(), m, opSpec{class: classForward, kind: "test.insert", scopes: domain.ScopeCapture, revertible: true}, func(o *op) error {
		for i := range 2 {
			tk := domain.Task{Title: "created", Status: domain.StatusOpen, DueAck: domain.DueNone, Rank: int64(i + 1), Origin: "owner", CreatedAt: o.now, UpdatedAt: o.now}
			if err := store.InsertTask(o.ctx, o.tx, &tk); err != nil {
				return err
			}
			tasks = append(tasks, tk)
			tr := domain.EntityRef{Entity: domain.EntTask, ID: int64(tk.ID)}
			o.markInserted(tr)
			o.markTaskDirty(tk.ID)
			o.markTaskDirty(tk.ID)
			payload, err := json.Marshal(taskCols(tk))
			if err != nil {
				return err
			}
			if err := o.record(domain.Event{Entity: domain.EntTask, EntityID: tr.ID, Action: domain.ActInsert, After: payload}); err != nil {
				return err
			}
			e := domain.LogEntry{TaskID: sql.Null[domain.TaskID]{V: tk.ID, Valid: true}, Kind: domain.EntryWork, Title: "work", WorkDate: o.today, CreatedAt: o.now, UpdatedAt: o.now}
			if err := store.InsertEntry(o.ctx, o.tx, &e); err != nil {
				return err
			}
			entries = append(entries, e)
			er := domain.EntityRef{Entity: domain.EntEntry, ID: int64(e.ID)}
			o.markInserted(er)
			o.markDirty(er)
			o.ref(er)
			payload, err = json.Marshal(entryCols(e))
			if err != nil {
				return err
			}
			if err := o.record(domain.Event{Entity: domain.EntEntry, EntityID: er.ID, Action: domain.ActInsert, After: payload}); err != nil {
				return err
			}
			b := domain.TimeBlock{TaskID: tk.ID, Span: domain.BlockSpan{Date: o.today, Start: civil.Minute(600 + i*60), End: civil.EndMinute(660 + i*60)}, Status: domain.BlockPlanned, CreatedAt: o.now}
			if err := store.InsertBlock(o.ctx, o.tx, &b); err != nil {
				return err
			}
			blocks = append(blocks, b)
			br := domain.EntityRef{Entity: domain.EntBlock, ID: int64(b.ID)}
			o.markInserted(br)
			o.markDirty(br)
			o.ref(br)
			payload, err = json.Marshal(blockCols(b))
			if err != nil {
				return err
			}
			if err := o.record(domain.Event{Entity: domain.EntBlock, EntityID: br.ID, Action: domain.ActInsert, After: payload}); err != nil {
				return err
			}
		}
		for _, tk := range []domain.Task{tasks[1], tasks[0], tasks[1]} {
			o.ref(domain.EntityRef{Entity: domain.EntTask, ID: int64(tk.ID)})
		}
		o.addPair(sql.Null[domain.TaskID]{}, o.today)
		o.addPair(sql.Null[domain.TaskID]{V: tasks[0].ID, Valid: true}, o.today)
		o.addPair(sql.Null[domain.TaskID]{V: tasks[0].ID, Valid: true}, o.today)
		if len(o.pairs) != 1 || o.target != nil {
			t.Fatal("op collections were not initialized correctly")
		}
		o.created = true
		o.warn("fixture warning")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Created || r.NoChange || len(r.Refs) != 6 || r.Task != nil || len(r.Tasks) != 2 || r.Tasks[0].ID != tasks[1].ID || r.Tasks[1].ID != tasks[0].ID || r.Entry.ID != entries[1].ID || r.Block.ID != blocks[1].ID || !slices.Equal(r.Warnings, []string{"fixture warning"}) {
		t.Fatalf("result = %+v", r)
	}
	for _, ref := range r.Refs {
		switch ref.Entity {
		case domain.EntTask:
			v, err := store.GetTask(t.Context(), s.store.Reader, domain.TaskID(ref.ID))
			if err != nil || v.Version != 1 {
				t.Fatalf("task: %+v, %v", v, err)
			}
		case domain.EntEntry:
			v, err := store.GetEntry(t.Context(), s.store.Reader, domain.EntryID(ref.ID))
			if err != nil || v.Version != 1 {
				t.Fatalf("entry: %+v, %v", v, err)
			}
		case domain.EntBlock:
			v, err := store.GetBlock(t.Context(), s.store.Reader, domain.BlockID(ref.ID))
			if err != nil || v.Version != 1 {
				t.Fatalf("block: %+v, %v", v, err)
			}
		default:
			t.Fatalf("unexpected ref: %+v", ref)
		}
	}
	op, err := store.GetOp(t.Context(), s.store.Reader, r.OpID)
	if err != nil {
		t.Fatal(err)
	}
	if op.Scopes != domain.ScopeCapture || !op.Revertible || op.State != domain.OpActive || op.At.Nanosecond()%1000000 != 0 {
		t.Fatalf("op = %+v", op)
	}
	for _, e := range mustEvents(t, s, r.OpID) {
		if !e.TaskID.Valid || e.OpID != r.OpID || e.Derived {
			t.Fatalf("event = %+v", e)
		}
	}
}

func mustEvents(t *testing.T, s *Service, id domain.OpID) []domain.Event {
	t.Helper()
	evs, err := store.EventsOfOp(t.Context(), s.store.Reader, id)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestRunUndoStackPolicy(t *testing.T) {
	s, m := runnerFixture(t)
	var ids []domain.OpID
	for i := range 3 {
		r, err := s.run(t.Context(), m, opSpec{class: classForward, kind: "test.stack", scopes: domain.ScopeWrite, revertible: true}, func(o *op) error {
			return o.record(domain.Event{Entity: domain.EntSetting, Action: domain.ActUpdate})
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.OpID)
		if i == 0 {
			if err := store.PushStack(t.Context(), s.store.Writer, m.Actor.Key(), store.StackRedo, r.OpID); err != nil {
				t.Fatal(err)
			}
			if err := store.PushStack(t.Context(), s.store.Writer, "token:other", store.StackRedo, r.OpID); err != nil {
				t.Fatal(err)
			}
		}
	}
	undo, err := store.StackOps(t.Context(), s.store.Reader, m.Actor.Key(), store.StackUndo)
	if err != nil || !slices.Equal(undo, ids[1:]) {
		t.Fatalf("undo = %v, %v", undo, err)
	}
	redo, err := store.StackOps(t.Context(), s.store.Reader, m.Actor.Key(), store.StackRedo)
	if err != nil || len(redo) != 0 {
		t.Fatalf("redo = %v, %v", redo, err)
	}
	other, err := store.StackOps(t.Context(), s.store.Reader, "token:other", store.StackRedo)
	if err != nil || !slices.Equal(other, ids[:1]) {
		t.Fatalf("other = %v, %v", other, err)
	}
}

func TestReconcileSavepointBoundaries(t *testing.T) {
	for _, outcome := range []string{"success", "no change", "user error", "finalize error", "reconcile error", "reconcile no events"} {
		t.Run(outcome, func(t *testing.T) {
			s, m := runnerFixture(t)
			tk := fixtureTask(t, s, 1)
			var rec domain.OpRecord
			var order []string
			old := reconcileSteps
			t.Cleanup(func() { reconcileSteps = old })
			reconcileSteps = []reconcileStep{
				{name: "surface", need: func(context.Context, store.Q, time.Time, civil.Date) (bool, error) {
					order = append(order, "need surface")
					return true, nil
				}, run: func(o *op) error {
					order = append(order, "run surface")
					rec = o.rec
					if o.today != civil.MustDate("2026-10-28") {
						t.Fatal("incorrect local date")
					}
					after := tk
					after.Notes = "reconciled"
					if err := store.UpdateTask(o.ctx, o.tx, after); err != nil {
						return err
					}
					if outcome == "reconcile no events" {
						return nil
					}
					return o.recordTaskUpdate(tk, after)
				}},
				{name: "focus", need: func(context.Context, store.Q, time.Time, civil.Date) (bool, error) {
					order = append(order, "need focus")
					return false, nil
				}, run: func(_ *op) error {
					order = append(order, "run focus")
					if outcome == "reconcile error" {
						return domain.Errorf(domain.CodeConflict, "reconcile failed")
					}
					return nil
				}},
			}
			r, err := s.run(t.Context(), m, opSpec{class: classForward, kind: "test.reconcile", scopes: domain.ScopeWrite}, func(o *op) error {
				current, err := store.GetTask(o.ctx, o.tx, tk.ID)
				if err != nil {
					return err
				}
				want := domain.Version(2)
				if outcome == "reconcile no events" {
					want = 1
				}
				if current.Version != want {
					t.Fatalf("user sees version %d, want %d", current.Version, want)
				}
				if !o.now.Equal(rec.At) {
					t.Fatal("reconcile and user clocks differ")
				}
				after := current
				after.Title = "user edit"
				if err := store.UpdateTask(o.ctx, o.tx, after); err != nil {
					return err
				}
				if outcome == "no change" {
					return nil
				}
				if err := o.recordTaskUpdate(current, after); err != nil {
					return err
				}
				if outcome == "user error" {
					return domain.Errorf(domain.CodeValidation, "user failed")
				}
				if outcome == "finalize error" {
					o.markTaskDirty(9999)
				}
				return nil
			})
			switch outcome {
			case "user error":
				requireRunnerCode(t, err, domain.CodeValidation)
			case "finalize error":
				requireRunnerCode(t, err, domain.CodeNotFound)
			case "reconcile error":
				requireRunnerCode(t, err, domain.CodeConflict)
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
			if !slices.Equal(order, []string{"need surface", "need focus", "run surface", "run focus"}) {
				t.Fatalf("step order: %v", order)
			}
			current, err := store.GetTask(t.Context(), s.store.Reader, tk.ID)
			if err != nil {
				t.Fatal(err)
			}
			var count int
			if err := s.store.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM ops`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			switch outcome {
			case "success":
				if count != 2 || current.Version != 3 || current.Title != "user edit" || current.Notes != "reconciled" {
					t.Fatalf("state = %+v; ops = %d", current, count)
				}
				user, err := store.GetOp(t.Context(), s.store.Reader, r.OpID)
				if err != nil {
					t.Fatal(err)
				}
				if user.Seq <= rec.Seq {
					t.Fatal("sequence order")
				}
			case "no change", "user error", "finalize error":
				if count != 1 || current.Version != 2 || current.Title != tk.Title || current.Notes != "reconciled" {
					t.Fatalf("state = %+v; ops = %d", current, count)
				}
				if outcome == "no change" && !r.NoChange {
					t.Fatal("expected no change")
				}
			case "reconcile error":
				if count != 0 || current.Version != 1 || current.Notes != "" {
					t.Fatalf("state = %+v; ops = %d", current, count)
				}
			case "reconcile no events":
				if count != 1 || current.Version != 2 || current.Notes != "" {
					t.Fatalf("state = %+v; ops = %d", current, count)
				}
			}
			if outcome != "reconcile error" && outcome != "reconcile no events" {
				persisted, err := store.GetOp(t.Context(), s.store.Reader, rec.ID)
				if err != nil {
					t.Fatal(err)
				}
				if persisted.Kind != domain.OpSystemReconcile || persisted.Actor != "system" || persisted.Channel != domain.ChanSystem || persisted.Scopes != 0 || persisted.Revertible {
					t.Fatalf("reconcile = %+v", persisted)
				}
				if evs := mustEvents(t, s, rec.ID); len(evs) != 1 {
					t.Fatalf("reconcile events: %+v", evs)
				}
			}
		})
	}
}

func TestReadWriteAndAuthorization(t *testing.T) {
	s, m := runnerFixture(t)
	for _, spec := range []opSpec{
		{class: classForward, scopes: domain.ScopeAdmin},
		{class: classRevert, ownerOnly: true},
		{class: classUndo, notWeb: true},
		{class: classRedo, ownerOnly: true},
	} {
		a := m
		a.Actor.Kind, a.Actor.Channel, a.Actor.Scopes = domain.ActorToken, domain.ChanWeb, domain.ScopeRead
		_, err := s.run(t.Context(), a, spec, func(*op) error { t.Fatal("unauthorized callback"); return nil })
		requireRunnerCode(t, err, domain.CodeForbidden)
	}
	err := s.read(t.Context(), domain.SystemActor(), domain.ScopeRead, false, func(store.Q, time.Time, civil.Date) error { t.Fatal("unauthorized read"); return nil })
	requireRunnerCode(t, err, domain.CodeForbidden)
	err = s.read(t.Context(), m.Actor, domain.ScopeRead, true, func(q store.Q, now time.Time, today civil.Date) error {
		if now.Nanosecond() != 123000000 || today != civil.MustDate("2026-10-28") {
			t.Fatal("read clock")
		}
		return store.SetSetting(t.Context(), q, "read_must_not_write", "x")
	})
	if err == nil {
		t.Fatal("reader allowed a write")
	}
	cause := errors.New("abort maintenance")
	err = s.writeTx(t.Context(), func(tx *sql.Tx, now time.Time) error {
		if now.Nanosecond() != 123000000 {
			t.Fatal("writer clock")
		}
		if err := store.SetSetting(t.Context(), tx, "maintenance", "x"); err != nil {
			return err
		}
		return cause
	})
	if !errors.Is(err, cause) {
		t.Fatalf("cause lost: %v", err)
	}
	if _, err := store.GetSetting(t.Context(), s.store.Reader, "maintenance"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("maintenance did not roll back")
	}
	if err := s.writeTx(t.Context(), func(tx *sql.Tx, _ time.Time) error {
		return store.SetSetting(t.Context(), tx, "maintenance", "committed")
	}); err != nil {
		t.Fatal(err)
	}
}

func TestColumnImagesAndUpdateEvents(t *testing.T) {
	s, m := runnerFixture(t)
	tk := fixtureTask(t, s, 1)
	var entry domain.LogEntry
	r, err := s.run(t.Context(), m, opSpec{kind: "test.columns", class: classForward, idem: true, structuralOnly: true}, func(o *op) error {
		// Rank and bookkeeping alone never become a task update event.
		after := tk
		after.Rank++
		after.Version++
		after.UpdatedAt = o.now.Add(time.Hour)
		if err := o.recordTaskUpdate(tk, after); err != nil {
			return err
		}
		if len(o.events) != 0 || len(o.dirty) != 0 {
			t.Fatal("equal fields emitted an event")
		}
		after = tk
		after.DueDate = sql.Null[civil.Date]{V: o.today, Valid: true}
		after.DueMin = sql.Null[civil.Minute]{V: 720, Valid: true}
		if err := store.UpdateTask(o.ctx, o.tx, after); err != nil {
			return err
		}
		o.derived = true
		if err := o.recordTaskUpdate(tk, after); err != nil {
			return err
		}
		entry = domain.LogEntry{Kind: domain.EntryWork, Title: "log", WorkDate: o.today, CreatedAt: o.now, UpdatedAt: o.now}
		if err := store.InsertEntry(o.ctx, o.tx, &entry); err != nil {
			return err
		}
		o.markInserted(domain.EntityRef{Entity: domain.EntEntry, ID: int64(entry.ID)})
		if err := o.recordEntryUpdate(entry, entry); err != nil {
			return err
		}
		changed := entry
		changed.TaskID = sql.Null[domain.TaskID]{V: tk.ID, Valid: true}
		changed.OccurredMin = sql.Null[civil.Minute]{V: 600, Valid: true}
		if err := store.UpdateEntry(o.ctx, o.tx, changed); err != nil {
			return err
		}
		return o.recordEntryUpdate(entry, changed)
	})
	if err != nil {
		t.Fatal(err)
	}
	evs := mustEvents(t, s, r.OpID)
	if len(evs) != 2 {
		t.Fatalf("events = %+v", evs)
	}
	for _, e := range evs {
		if !e.Derived || !e.TaskID.Valid || e.TaskID.V != tk.ID {
			t.Fatalf("event = %+v", e)
		}
	}
	if string(evs[0].After) != `{"due_date":"2026-10-28","due_min":720}` || string(evs[1].After) != `{"occurred_min":600,"task_id":1}` {
		t.Fatalf("payloads: %s; %s", evs[0].After, evs[1].After)
	}
	now := s.clock.Now().Truncate(time.Millisecond)
	tk.LastPlacedAt = sql.Null[time.Time]{V: now, Valid: true}
	tk.DueDate = sql.Null[civil.Date]{V: civil.MustDate("2026-10-30"), Valid: true}
	tk.DueMin = sql.Null[civil.Minute]{V: 540, Valid: true}
	cols := taskCols(tk)
	if cols["last_placed_at"] != now.UnixMilli() || cols["due_date"] != "2026-10-30" || cols["due_min"] != int64(540) || cols["project"] != nil || cols["priority"] != nil {
		t.Fatalf("columns = %+v", cols)
	}
	for _, image := range []map[string]any{cols, entryCols(entry), blockCols(domain.TimeBlock{Slipped: true})} {
		for _, key := range []string{"id", "version", "created_at", "updated_at", "purged_at"} {
			if _, found := image[key]; found {
				t.Fatalf("included %s", key)
			}
		}
	}
	if blockCols(domain.TimeBlock{Slipped: true})["slipped"] != int64(1) || entryCols(entry)["reopened"] != int64(0) {
		t.Fatal("boolean storage representation")
	}
	de := validation("due", errors.New("invalid moment"))
	if de.Code != domain.CodeValidation || !reflect.DeepEqual(de.Fields, []domain.FieldError{{Field: "due", Message: "invalid moment"}}) {
		t.Fatalf("validation = %+v", de)
	}
}

func TestNewDefaultsAndCachedIdentity(t *testing.T) {
	s, _ := runnerFixture(t)
	gen := domain.NewGeneration()
	if err := store.SetSetting(t.Context(), s.store.Writer, "db_generation", string(gen)); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword(domain.NewOpID().String())
	if err != nil {
		t.Fatal(err)
	}
	key, err := auth.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutAuthState(t.Context(), s.store.Writer, store.AuthState{PasswordHash: hash, PasswordChangedAt: s.clock.Now(), IdemHMACKey: key}); err != nil {
		t.Fatal(err)
	}
	newSvc, err := New(Options{Store: s.store, Clock: s.clock, Zone: s.zone, DueSoonDays: 0})
	if err != nil {
		t.Fatal(err)
	}
	if newSvc.slipGrace != 15*time.Minute || newSvc.undoDepth != 1000 || newSvc.dueSoonDays != 0 || newSvc.logger == nil || newSvc.idemHMACKey != key {
		t.Fatal("incorrect defaults or loaded HMAC key")
	}
	if err := store.SetSetting(t.Context(), s.store.Writer, "db_generation", string(domain.NewGeneration())); err != nil {
		t.Fatal(err)
	}
	if newSvc.Generation() != gen || newSvc.Zone().Name() != "America/Denver" {
		t.Fatal("identity was not cached")
	}
}

func TestUpdateEventCompanions(t *testing.T) {
	date := civil.MustDate("2026-10-30")
	task := domain.Task{DueDate: sql.Null[civil.Date]{V: date, Valid: true}, DueMin: sql.Null[civil.Minute]{V: 540, Valid: true},
		DeferDate: sql.Null[civil.Date]{V: date, Valid: true}, DeferMin: sql.Null[civil.Minute]{V: 600, Valid: true}}
	dueAfter := task
	dueAfter.DueMin.V = 600
	deferAfter := task
	deferAfter.DeferDate.V = date.AddDays(1)
	resolvedAt := time.Date(2026, 10, 30, 16, 0, 0, 0, time.UTC)
	userBlock := domain.TimeBlock{Status: domain.BlockWorked, OutcomeSource: domain.OutcomeUser,
		ResolvedAt: sql.Null[time.Time]{V: resolvedAt, Valid: true}}
	userAfter := userBlock
	userAfter.Status = domain.BlockMissed
	autoBlock, autoAfter := userBlock, userAfter
	autoBlock.OutcomeSource, autoAfter.OutcomeSource = domain.OutcomeAuto, domain.OutcomeAuto
	for _, tt := range []struct {
		name                  string
		entity                domain.EntityKind
		before, after         map[string]any
		wantBefore, wantAfter map[string]any
		want                  Effect
	}{
		{"due minute only", domain.EntTask, taskCols(task), taskCols(dueAfter),
			map[string]any{"due_date": "2026-10-30", "due_min": int64(540)},
			map[string]any{"due_date": "2026-10-30", "due_min": int64(600)},
			Effect{Kind: EffDueSet, TaskID: 7, To: "2026-10-30 10:00"}},
		{"defer date only", domain.EntTask, taskCols(task), taskCols(deferAfter),
			map[string]any{"defer_date": "2026-10-30", "defer_min": int64(600)},
			map[string]any{"defer_date": "2026-10-31", "defer_min": int64(600)},
			Effect{Kind: EffDeferred, TaskID: 7, To: "2026-10-31 10:00"}},
		{"user outcome unchanged", domain.EntBlock, blockCols(userBlock), blockCols(userAfter),
			map[string]any{"status": "worked", "outcome_source": "user", "resolved_at": resolvedAt.UnixMilli()},
			map[string]any{"status": "missed", "outcome_source": "user", "resolved_at": resolvedAt.UnixMilli()},
			Effect{Kind: EffOutcomeSet, TaskID: 7, BlockID: 9, To: "missed"}},
		{"auto outcome unchanged", domain.EntBlock, blockCols(autoBlock), blockCols(autoAfter),
			map[string]any{"status": "worked", "outcome_source": "auto", "resolved_at": resolvedAt.UnixMilli()},
			map[string]any{"status": "missed", "outcome_source": "auto", "resolved_at": resolvedAt.UnixMilli()},
			Effect{Kind: EffBlockResolved, TaskID: 7, BlockID: 9, To: "missed"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b, a := diffCols(tt.before, tt.after)
			if !reflect.DeepEqual(b, tt.wantBefore) || !reflect.DeepEqual(a, tt.wantAfter) {
				t.Errorf("images = %#v -> %#v, want %#v -> %#v", b, a, tt.wantBefore, tt.wantAfter)
			}
			bj, err := json.Marshal(b)
			if err != nil {
				t.Fatal(err)
			}
			aj, err := json.Marshal(a)
			if err != nil {
				t.Fatal(err)
			}
			e := domain.Event{Entity: tt.entity, EntityID: 9, TaskID: sql.Null[domain.TaskID]{V: 7, Valid: true}, Action: domain.ActUpdate, Before: bj, After: aj}
			if got := effectsFromEvents(domain.OpRecord{}, []domain.Event{e}); !reflect.DeepEqual(got, []Effect{tt.want}) {
				t.Errorf("effects = %+v, want %+v", got, tt.want)
			}
		})
	}
	t.Run("null companions and no extra columns", func(t *testing.T) {
		before := taskCols(domain.Task{Status: domain.StatusOpen})
		after := taskCols(domain.Task{Status: domain.StatusDone, DueDate: sql.Null[civil.Date]{V: date, Valid: true}})
		b, a := diffCols(before, after)
		if !reflect.DeepEqual(b, map[string]any{"status": "open", "due_date": nil, "due_min": nil}) ||
			!reflect.DeepEqual(a, map[string]any{"status": "done", "due_date": "2026-10-30", "due_min": nil}) {
			t.Fatalf("task status images: %#v -> %#v", b, a)
		}
		b, a = diffCols(after, after)
		if len(b) != 0 || len(a) != 0 {
			t.Fatalf("unchanged row emitted companions: %#v -> %#v", b, a)
		}
	})
	t.Run("missing outcome source is not automatic", func(t *testing.T) {
		e := domain.Event{Entity: domain.EntBlock, Action: domain.ActUpdate,
			Before: json.RawMessage(`{"status":"worked"}`), After: json.RawMessage(`{"status":"missed"}`)}
		if got := effectsFromEvents(domain.OpRecord{}, []domain.Event{e}); len(got) != 0 {
			t.Fatalf("inferred missing source: %+v", got)
		}
	})
}

func TestEventTaskAttribution(t *testing.T) {
	s, m := runnerFixture(t)
	task := fixtureTask(t, s, 1)
	other := fixtureTask(t, s, 2)
	taskID := sql.Null[domain.TaskID]{V: task.ID, Valid: true}
	var entry domain.LogEntry
	var block domain.TimeBlock
	var interruption domain.Interruption
	r, err := s.run(t.Context(), m, opSpec{class: classForward, kind: "test.events"}, func(o *op) error {
		entry = domain.LogEntry{TaskID: taskID, Title: "entry", Kind: domain.EntryWork, WorkDate: o.today, CreatedAt: o.now, UpdatedAt: o.now}
		if err := store.InsertEntry(o.ctx, o.tx, &entry); err != nil {
			return err
		}
		block = domain.TimeBlock{TaskID: task.ID, Span: domain.BlockSpan{Date: o.today, Start: 600, End: 660}, Status: domain.BlockPlanned, CreatedAt: o.now}
		if err := store.InsertBlock(o.ctx, o.tx, &block); err != nil {
			return err
		}
		interruption = domain.Interruption{At: o.now, Date: o.today, FromTaskID: taskID, ToTaskID: other.ID, OpID: o.rec.ID}
		if err := store.InsertInterruption(o.ctx, o.tx, &interruption); err != nil {
			return err
		}
		for _, e := range []domain.Event{
			{Entity: domain.EntTask, EntityID: int64(task.ID), Action: domain.ActUpdate},
			{Entity: domain.EntTriage, EntityID: int64(task.ID), Action: domain.ActAdd},
			{Entity: domain.EntTaskTags, EntityID: int64(task.ID), Action: domain.ActSet},
			{Entity: domain.EntEntry, EntityID: int64(entry.ID), Action: domain.ActInsert},
			{Entity: domain.EntBlock, EntityID: int64(block.ID), Action: domain.ActInsert},
			{Entity: domain.EntInterruption, EntityID: int64(interruption.ID), Action: domain.ActInsert},
			{Entity: domain.EntFocus, EntityID: 1, Action: domain.ActSet, Before: json.RawMessage(`{"task_id":1}`), After: json.RawMessage(`{"task_id":2}`)},
			{Entity: domain.EntFocus, EntityID: 1, Action: domain.ActSet, Before: json.RawMessage(`{"task_id":2}`), After: json.RawMessage(`{"task_id":null}`)},
			{Entity: domain.EntSetting, Action: domain.ActUpdate, TaskID: taskID},
			{Entity: domain.EntToken, Action: domain.ActInsert, TaskID: taskID},
			{Entity: domain.EntAuth, Action: domain.ActUpdate, TaskID: taskID},
			{Entity: domain.EntTask, Action: domain.ActPurge, After: json.RawMessage(`{"ids":[1,2]}`)},
			{Entity: domain.EntEntry, Action: domain.ActPurge, After: json.RawMessage(`{"ids":[1]}`)},
		} {
			if err := o.record(e); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	evs := mustEvents(t, s, r.OpID)
	want := []domain.TaskID{task.ID, task.ID, task.ID, task.ID, task.ID, other.ID, other.ID, other.ID, 0, 0, 0, 0, 0}
	if len(evs) != len(want) {
		t.Fatalf("events = %+v", evs)
	}
	for i, ev := range evs {
		if ev.TaskID.Valid != (want[i] != 0) || ev.TaskID.V != want[i] {
			t.Errorf("event %d task = %+v, want %d", i, ev.TaskID, want[i])
		}
	}
}

func TestReconcileReaderCheckAndWriterRecheck(t *testing.T) {
	for _, mode := range []string{"work", "stale reader", "reader error", "writer error"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := runnerFixture(t)
			old := reconcileSteps
			t.Cleanup(func() { reconcileSteps = old })
			checks, runs := 0, 0
			cause := errors.New("need check failed")
			reconcileSteps = []reconcileStep{{name: "surface", need: func(_ context.Context, _ store.Q, now time.Time, today civil.Date) (bool, error) {
				checks++
				if now.Nanosecond() != 123000000 || today != civil.MustDate("2026-10-28") {
					t.Fatal("need check clock")
				}
				if (mode == "reader error" && checks == 1) || (mode == "writer error" && checks == 2) {
					return false, cause
				}
				return mode != "stale reader" || checks != 2, nil
			}, run: func(o *op) error {
				runs++
				if err := store.SetSetting(o.ctx, o.tx, "reconcile.test", "applied"); err != nil {
					return err
				}
				return o.record(domain.Event{Entity: domain.EntSetting, Action: domain.ActUpdate})
			}}}
			err := s.Reconcile(t.Context())
			if mode == "reader error" || mode == "writer error" {
				requireRunnerCode(t, err, domain.CodeInternal)
				if !errors.Is(err, cause) {
					t.Fatalf("cause lost: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			wantChecks := 2
			if mode == "reader error" {
				wantChecks = 1
			}
			wantRuns := 0
			if mode == "work" {
				wantRuns = 1
			}
			var n int
			if err := s.store.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM ops`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if checks != wantChecks || runs != wantRuns || n != wantRuns {
				t.Fatalf("checks/runs/ops = %d/%d/%d, want %d/%d/%d", checks, runs, n, wantChecks, wantRuns, wantRuns)
			}
		})
	}
}
