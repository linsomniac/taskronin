package service_test

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/store"
	"taskronin/internal/testutil"
)

func workedMonday(t *testing.T) (*testutil.Scenario, domain.TaskID, domain.BlockID, domain.EntryID) {
	t.Helper()
	s, task, block := missedMonday(t)
	entry := s.Log(task, "Did it", "10-26")
	requireBlockState(t, s, block, "{worked, auto, 0, –}")
	requireDueReasons(t, s, task, domain.ReasonContinue)
	return s, task, block, entry
}

func TestBLK24(t *testing.T) {
	s := testutil.NewScenario(t).At("10-27 09:00")
	t1, t2 := s.Add("A"), s.Add("B")
	b := s.Block(t1, "10-27 13:00-15:00")
	s.Log(t1, "x", "10-26")
	s.Log(t2, "y", "10-27")
	e := s.Log(t1, "z", "10-27")
	s.DeleteEntry(e)
	s.At("10-28 00:00").Reconcile()
	requireBlockState(t, s, b, "{missed, auto, 1, –}")
	requireDueReasons(t, s, t1, domain.ReasonSlipped)
	requireSlips(t, s, t1, 1, 1)
}

func TestSLP04(t *testing.T) { entryDeletionReslips(t) }

func TestRCP03(t *testing.T) { entryDeletionReslips(t) }

func entryDeletionReslips(t *testing.T) {
	t.Helper()
	s, task, block, entry := workedMonday(t)
	before, version := s.BlockView(block), s.EntryExpect(entry).Version
	s.At("10-28 11:00").DeleteEntry(entry)
	requireBlockState(t, s, block, "{missed, auto, 1, –}")
	requireDueReasons(t, s, task, domain.ReasonSlipped)
	requireSlips(t, s, task, 1, 1)
	b := s.BlockView(block)
	if b.SlippedAt.V != testutil.Local(t, "10-28 11:00").UTC() || b.SlippedSeq.V != s.Op(s.Last.OpID).Seq || b.Version != before.Version+1 {
		t.Fatalf("re-slip metadata = %+v", b)
	}
	if e := s.Last.Entry; !e.DeletedAt.Valid || !e.DeletedAt.V.Equal(s.Clock.Now()) || e.Version != version+1 {
		t.Fatalf("deleted entry = %+v", e)
	}
	requireEventKeys(t, s, s.Last.OpID, "entry:tombstone 1", "block:update 1 [derived]",
		"triage:add 1 slipped [derived]", "triage:remove 1 continue [derived]")
}

func TestSLP08(t *testing.T) {
	s, task, block, entry := workedMonday(t)
	s.EditEntry(entry, func(in *service.UpdateEntryIn) { in.WorkDate = new(testutil.D("10-27")) })
	requireBlockState(t, s, block, "{missed, auto, 1, –}")
	requireDueReasons(t, s, task, domain.ReasonSlipped)
	requireSlips(t, s, task, 1, 1)
	if s.Last.Entry.WorkDate != testutil.D("10-27") {
		t.Fatal("work date was not changed")
	}
}

func TestSLP13(t *testing.T) {
	s, task, b1, b2 := twoSlips(t, true)
	entry := s.Log(task, "x", "10-27")
	requireDueReasons(t, s, task, domain.ReasonContinue)
	requireSlips(t, s, task, 1, 0)
	first := s.BlockView(b1)
	s.At("10-28 11:00").DeleteEntry(entry)
	requireBlockState(t, s, b1, "{missed, auto, 1, –}")
	requireBlockState(t, s, b2, "{missed, auto, 1, –}")
	requireDueReasons(t, s, task, domain.ReasonSlipped)
	requireSlips(t, s, task, 2, 1)
	second, placed := s.BlockView(b2), s.Task(task)
	if s.BlockView(b1).TimeBlock != first.TimeBlock || !second.SlippedAt.V.Equal(testutil.Local(t, "10-28 11:00")) ||
		!second.SlippedAt.V.After(placed.LastPlacedAt.V) || second.SlippedSeq.V <= placed.LastPlacedSeq.V ||
		first.SlippedSeq.V >= placed.LastPlacedSeq.V {
		t.Fatalf("acknowledgement: B1=%+v B2=%+v task=%+v", first, second, placed)
	}
}

func TestRCP01(t *testing.T) {
	s := testutil.NewScenario(t).At("10-26 08:00")
	t1, t2 := s.Add("A"), s.Add("B")
	b1, b2 := s.Block(t1, "10-26 09:00-10:00"), s.Block(t2, "10-26 11:00-12:00")
	e := s.Log(t1, "did it", "")
	s.At("10-28 10:00").Reconcile()
	requireDueReasons(t, s, t1, domain.ReasonContinue)
	requireDueReasons(t, s, t2, domain.ReasonSlipped)
	v1, v2, count := s.Task(t1).Version, s.Task(t2).Version, s.OpCount()
	s.Relink(e, t2)
	requireBlockState(t, s, b1, "{missed, auto, 1, –}")
	requireBlockState(t, s, b2, "{worked, auto, 0, –}")
	requireDueReasons(t, s, t1, domain.ReasonSlipped)
	requireDueReasons(t, s, t2, domain.ReasonContinue)
	requireSlips(t, s, t1, 1, 1)
	requireSlips(t, s, t2, 0, 0)
	if s.OpCount() != count+1 || s.Task(t1).Version != v1+1 || s.Task(t2).Version != v2+1 ||
		!s.BlockView(b1).SlippedAt.V.Equal(s.Clock.Now()) || s.Last.Entry.TaskID.V != t2 {
		t.Fatal("relink must change both tasks in one op")
	}
	requireEventKeys(t, s, s.Last.OpID, "entry:update 1", "block:update 1 [derived]",
		"triage:add 1 slipped [derived]", "triage:remove 1 continue [derived]",
		"block:update 2 [derived]", "triage:remove 2 slipped [derived]", "triage:add 2 continue [derived]")
	for _, ev := range s.Events(s.Last.OpID) {
		if ev.OpID != s.Last.OpID {
			t.Fatal("relink events split across ops")
		}
	}
}

func TestRCP02(t *testing.T) {
	s, task, b1, b2 := twoSlips(t, false)
	e := s.Log(task, "x", "10-26")
	requireDueReasons(t, s, task, domain.ReasonSlipped, domain.ReasonContinue)
	s.EditEntry(e, func(in *service.UpdateEntryIn) { in.WorkDate = new(testutil.D("10-27")) })
	requireBlockState(t, s, b1, "{missed, auto, 1, –}")
	requireBlockState(t, s, b2, "{worked, auto, 0, –}")
	requireDueReasons(t, s, task, domain.ReasonSlipped, domain.ReasonContinue)
	requireSlips(t, s, task, 1, 1)
	if s.BlockView(b2).ResolvedSeq.V <= s.Task(task).LastPlacedSeq.V {
		t.Fatal("worked block must remain unacknowledged")
	}
}

func TestRCP04(t *testing.T) {
	s, task, block, entry := workedMonday(t)
	s.At("10-28 11:00").DeleteEntry(entry)
	requireDueReasons(t, s, task, domain.ReasonSlipped)
	before := s.BlockView(block)
	s.At("10-28 12:00").UndeleteEntry(entry)
	requireBlockState(t, s, block, "{worked, auto, 0, –}")
	requireDueReasons(t, s, task, domain.ReasonContinue)
	requireSlips(t, s, task, 0, 0)
	if s.Last.Entry.DeletedAt.Valid || s.BlockView(block).SlippedAt != before.SlippedAt {
		t.Fatal("undelete must restore work and preserve slip history")
	}
	requireEventKeys(t, s, s.Last.OpID, "entry:untombstone 1", "block:update 1 [derived]",
		"triage:remove 1 slipped [derived]", "triage:add 1 continue [derived]")
}

func TestRCP08(t *testing.T) {
	s, task, block, entry := workedMonday(t)
	b, taskBefore, entryVersion := s.BlockView(block), s.Task(task), s.EntryExpect(entry).Version
	s.EditEntry(entry, func(in *service.UpdateEntryIn) { in.Title, in.Notes = new("Better title"), new("Some notes") })
	if s.BlockView(block) != b || !reflect.DeepEqual(s.Task(task), taskBefore) || s.Last.Entry.Version != entryVersion+1 {
		t.Fatal("text edit changed block/task state or failed to bump entry")
	}
	requireEventKeys(t, s, s.Last.OpID, "entry:update 1")
	if s.Last.Entry.Title != "Better title" || s.Last.Entry.Notes != "Some notes" {
		t.Fatalf("edited entry = %+v", s.Last.Entry)
	}
}

func TestRCP09(t *testing.T) {
	s, task, block := missedMonday(t)
	e := s.Log(0, "x", "10-26")
	s.Relink(e, task)
	requireBlockState(t, s, block, "{worked, auto, 0, –}")
	requireDueReasons(t, s, task, domain.ReasonContinue)
	requireSlips(t, s, task, 0, 0)
	requireEventKeys(t, s, s.Last.OpID, "entry:update 1", "block:update 1 [derived]",
		"triage:remove 1 slipped [derived]", "triage:add 1 continue [derived]")
	// The opposite direction must recompute the old pair and ignore the null side.
	s.Relink(e, 0)
	requireBlockState(t, s, block, "{missed, auto, 1, –}")
	requireDueReasons(t, s, task, domain.ReasonSlipped)
	if s.Last.Entry.TaskID.Valid {
		t.Fatal("unlink did not clear task_id")
	}
}

func TestEntryEditValidation(t *testing.T) {
	for _, tt := range []struct {
		name, field string
		modify      func(*service.UpdateEntryIn)
	}{
		{"future date", "work_date", func(in *service.UpdateEntryIn) { in.WorkDate = new(testutil.D("10-29")) }},
		{"invalid date", "work_date", func(in *service.UpdateEntryIn) { in.WorkDate = new(testutil.D("1000-01-01").AddDays(-1)) }},
		{"future time today", "time", func(in *service.UpdateEntryIn) { in.At = &sql.Null[civil.Minute]{V: 601, Valid: true} }},
		{"negative minute", "time", func(in *service.UpdateEntryIn) { in.At = &sql.Null[civil.Minute]{V: -1, Valid: true} }},
		{"end of day", "time", func(in *service.UpdateEntryIn) { in.At = &sql.Null[civil.Minute]{V: 1440, Valid: true} }},
		{"empty title", "title", func(in *service.UpdateEntryIn) { in.Title = new(" \t ") }},
		{"invalid UTF-8", "title", func(in *service.UpdateEntryIn) { in.Title = new("\xff") }},
		{"bidi title", "title", func(in *service.UpdateEntryIn) { in.Title = new("a\u202eb") }},
		{"long title", "title", func(in *service.UpdateEntryIn) { in.Title = new(strings.Repeat("é", 501)) }},
		{"C1 notes", "notes", func(in *service.UpdateEntryIn) { in.Notes = new("a\u0085b") }},
		{"long notes", "notes", func(in *service.UpdateEntryIn) { in.Notes = new(strings.Repeat("x", 65537)) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := testutil.NewScenario(t)
			e := s.Log(0, "x", "")
			before, count := *s.Last.Entry, s.OpCount()
			in := service.UpdateEntryIn{ID: e, Expect: s.EntryExpect(e)}
			tt.modify(&in)
			_, err := s.Svc.UpdateEntry(s.Ctx(), s.Meta(), in)
			requireEntryField(t, err, tt.field)
			after, err := s.Svc.GetEntry(s.Ctx(), s.Actor, e)
			if err != nil || after != before || s.OpCount() != count {
				t.Fatalf("invalid edit changed state: %+v, %v", after, err)
			}
		})
	}
	t.Run("retained time when moving to today", func(t *testing.T) {
		s := testutil.NewScenario(t)
		e := s.LogAt(0, "x", "10-27", "23:59")
		_, err := s.Svc.UpdateEntry(s.Ctx(), s.Meta(), service.UpdateEntryIn{ID: e, Expect: s.EntryExpect(e), WorkDate: new(testutil.D("10-28"))})
		requireEntryField(t, err, "time")
	})
	t.Run("relink target", func(t *testing.T) {
		s := testutil.NewScenario(t)
		e := s.Log(0, "x", "")
		_, err := s.Svc.RelinkEntry(s.Ctx(), s.Meta(), service.RelinkEntryIn{ID: e, Expect: s.EntryExpect(e), TaskID: sql.Null[domain.TaskID]{V: 999, Valid: true}})
		testutil.RequireCode(t, err, domain.CodeNotFound)
		task := s.Add("deleted task", testutil.Top())
		// AIDEV-NOTE: F14 fixture shortcut: task deletion arrives in Task 29.
		s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), []domain.EntityRef{{Entity: domain.EntTask, ID: int64(task)}}, func(q store.Q) error {
			row, err := store.GetTask(s.Ctx(), q, task)
			if err != nil {
				return err
			}
			row.DeletedAt = sql.Null[time.Time]{V: s.Clock.Now(), Valid: true}
			return store.UpdateTask(s.Ctx(), q, row)
		}))
		count := s.OpCount()
		_, err = s.Svc.RelinkEntry(s.Ctx(), s.Meta(), service.RelinkEntryIn{ID: e, Expect: s.EntryExpect(e), TaskID: sql.Null[domain.TaskID]{V: task, Valid: true}})
		testutil.RequireCode(t, err, domain.CodeDeleted)
		if s.OpCount() != count {
			t.Fatal("rejected relink wrote an op")
		}
	})
}

func TestEntryEditFieldsAndNoChange(t *testing.T) {
	s := testutil.NewScenario(t).At("2027-03-15 10:00")
	e := s.LogAt(0, "x", "", "09:00")
	s.EditEntry(e, func(in *service.UpdateEntryIn) {
		in.Title, in.Notes = new("\ufeff  Better  "), new("\ufeffa\r\nb\rc")
		in.WorkDate = new(testutil.D("2027-03-14"))
		in.At = &sql.Null[civil.Minute]{V: 150, Valid: true} // Past DST gap is display data.
	})
	if r := s.Last.Entry; r.Title != "Better" || r.Notes != "a\nb\nc" || r.OccurredMin.V != 150 || r.WorkDate != testutil.D("2027-03-14") {
		t.Fatalf("normalized entry = %+v", r)
	}
	s.EditEntry(e, func(in *service.UpdateEntryIn) { in.At = &sql.Null[civil.Minute]{}; in.Notes = new("") })
	if s.Last.Entry.OccurredMin.Valid || s.Last.Entry.Notes != "" {
		t.Fatal("clear fields failed")
	}
	s.EditEntry(e, func(in *service.UpdateEntryIn) {
		in.Title = new(strings.Repeat("é", 500))
		in.Notes = new(strings.Repeat("x", 65536))
	})
	if len([]rune(s.Last.Entry.Title)) != 500 || len(s.Last.Entry.Notes) != 65536 {
		t.Fatal("text limits rejected")
	}
	before, count, stack := *s.Last.Entry, s.OpCount(), s.Stack("owner", store.StackUndo)
	for _, edit := range []func(){
		func() { s.EditEntry(e) },
		func() {
			s.EditEntry(e, func(in *service.UpdateEntryIn) {
				in.Title = new("  " + before.Title + "  ")
				in.Notes = &before.Notes
				in.WorkDate = &before.WorkDate
				in.At = &sql.Null[civil.Minute]{V: 999}
			})
		},
		func() { s.Relink(e, 0) },
		func() { s.UndeleteEntry(e) },
	} {
		edit()
		if !s.Last.NoChange || s.Last.OpID != (domain.OpID{}) || *s.Last.Entry != before || s.OpCount() != count || !slices.Equal(s.Stack("owner", store.StackUndo), stack) {
			t.Fatalf("no-op changed state: %+v", s.Last)
		}
	}
}

func TestEntryEditOperationContracts(t *testing.T) {
	for _, tt := range []struct {
		kind   domain.OpKind
		scope  domain.Scopes
		action domain.Action
		call   func(*testutil.Scenario, service.Meta, service.EntryRef) (*service.OpResult, error)
	}{
		{domain.OpEntryUpdate, domain.ScopeDestroy, domain.ActUpdate, func(s *testutil.Scenario, m service.Meta, r service.EntryRef) (*service.OpResult, error) {
			return s.Svc.UpdateEntry(s.Ctx(), m, service.UpdateEntryIn{ID: r.ID, Expect: r.Expect, Title: new("edited")})
		}},
		{domain.OpEntryRelink, domain.ScopeWrite, domain.ActUpdate, func(s *testutil.Scenario, m service.Meta, r service.EntryRef) (*service.OpResult, error) {
			return s.Svc.RelinkEntry(s.Ctx(), m, service.RelinkEntryIn{ID: r.ID, Expect: r.Expect})
		}},
		{domain.OpEntryDelete, domain.ScopeDestroy, domain.ActTombstone, func(s *testutil.Scenario, m service.Meta, r service.EntryRef) (*service.OpResult, error) {
			return s.Svc.DeleteEntry(s.Ctx(), m, r)
		}},
		{domain.OpEntryUndelete, domain.ScopeDestroy, domain.ActUntombstone, func(s *testutil.Scenario, m service.Meta, r service.EntryRef) (*service.OpResult, error) {
			return s.Svc.UndeleteEntry(s.Ctx(), m, r)
		}},
	} {
		t.Run(string(tt.kind), func(t *testing.T) {
			s := testutil.NewScenario(t)
			task := s.Add("A", testutil.Top())
			e := s.Log(task, "x", "")
			_, err := tt.call(s, s.Meta(), service.EntryRef{ID: 999, Expect: s.EntryExpect(e)})
			testutil.RequireCode(t, err, domain.CodeNotFound)
			if tt.kind == domain.OpEntryUndelete {
				s.DeleteEntry(e)
			}
			ref := service.EntryRef{ID: e, Expect: s.EntryExpect(e)}
			_, err = tt.call(s, s.Meta(), service.EntryRef{ID: e})
			testutil.RequireCode(t, err, domain.CodePreconditionReq)
			stale := ref
			stale.Expect.Version++
			_, err = tt.call(s, s.Meta(), stale)
			testutil.RequireCode(t, err, domain.CodePrecondition)
			m := s.Meta()
			m.Idem = service.Idem{}
			_, err = tt.call(s, m, ref)
			testutil.RequireCode(t, err, domain.CodeIdemRequired)
			denied := s.As(testutil.AI())
			if tt.scope == domain.ScopeWrite {
				denied = s.As(testutil.TokenActor("capture", domain.ScopeRead|domain.ScopeCapture))
			}
			_, err = tt.call(denied, denied.Meta(), ref)
			testutil.RequireCode(t, err, domain.CodeForbidden)
			caller := s
			if tt.kind == domain.OpEntryRelink {
				caller = s.As(testutil.AI())
			}
			before, err := s.Svc.GetEntry(s.Ctx(), s.Actor, e)
			if err != nil {
				t.Fatal(err)
			}
			count, taskBefore := s.OpCount(), s.Task(task)
			s.At("10-28 11:00")
			m = caller.Meta()
			r := s.Must(tt.call(caller, m, ref))
			op, evs := s.Op(r.OpID), s.Events(r.OpID)
			if op.Kind != tt.kind || op.Scopes != tt.scope || !op.Revertible || len(evs) != 1 || evs[0].Action != tt.action || evs[0].Derived ||
				r.Entry.Version != before.Version+1 || !r.Entry.UpdatedAt.Equal(s.Clock.Now()) || r.Entry.CreatedAt != before.CreatedAt ||
				!reflect.DeepEqual(s.Task(task), taskBefore) {
				t.Fatalf("op=%+v events=%+v result=%+v", op, evs, r)
			}
			stack := s.Stack(caller.Actor.Key(), store.StackUndo)
			if stack[len(stack)-1] != r.OpID {
				t.Fatal("entry edit missing from undo stack")
			}
			if tt.action != domain.ActUpdate {
				var b, a map[string]any
				if err := json.Unmarshal(evs[0].Before, &b); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(evs[0].After, &a); err != nil {
					t.Fatal(err)
				}
				wantBefore, wantAfter := any(nil), any(float64(s.Clock.Now().UnixMilli()))
				if tt.action == domain.ActUntombstone {
					wantBefore, wantAfter = float64(before.DeletedAt.V.UnixMilli()), nil
				}
				if !reflect.DeepEqual(b, map[string]any{"deleted_at": wantBefore}) || !reflect.DeepEqual(a, map[string]any{"deleted_at": wantAfter}) {
					t.Fatalf("tombstone images: before=%v after=%v", b, a)
				}
			}
			replay := s.Must(tt.call(caller, m, ref))
			if !replay.Replayed || replay.OpID != r.OpID || *replay.Entry != *r.Entry || !slices.Equal(replay.Effects, r.Effects) || s.OpCount() != count+1 {
				t.Fatalf("idempotency replay = %+v", replay)
			}
			if tt.kind != domain.OpEntryUndelete {
				if tt.kind != domain.OpEntryDelete {
					s.DeleteEntry(e)
				}
				_, err = tt.call(s, s.Meta(), service.EntryRef{ID: e, Expect: s.EntryExpect(e)})
				testutil.RequireCode(t, err, domain.CodeDeleted)
			}
		})
	}
}

func TestEntryEditComplete(t *testing.T) {
	for _, done := range []bool{true, false} {
		name := "reopened"
		if done {
			name = "done"
		}
		t.Run(name, func(t *testing.T) {
			s := testutil.NewScenario(t)
			task := s.Add("A", testutil.Top())
			var entry domain.LogEntry
			// AIDEV-NOTE: F14 fixture shortcut: Complete/Reopen arrive in Task 28.
			s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), []domain.EntityRef{{Entity: domain.EntTask, ID: int64(task)}}, func(q store.Q) error {
				row, err := store.GetTask(s.Ctx(), q, task)
				if err != nil {
					return err
				}
				if done {
					row.Status = domain.StatusDone
					row.CompletedAt = sql.Null[time.Time]{V: s.Clock.Now(), Valid: true}
					row.CompletedDate = sql.Null[civil.Date]{V: testutil.D("10-28"), Valid: true}
					if err := store.UpdateTask(s.Ctx(), q, row); err != nil {
						return err
					}
				}
				entry = domain.LogEntry{TaskID: sql.Null[domain.TaskID]{V: task, Valid: true}, Kind: domain.EntryComplete, Title: "A",
					WorkDate: testutil.D("10-28"), Reopened: !done, CreatedAt: s.Clock.Now(), UpdatedAt: s.Clock.Now()}
				return store.InsertEntry(s.Ctx(), q, &entry)
			}))
			for _, modify := range []func(*service.UpdateEntryIn){
				func(in *service.UpdateEntryIn) { in.Title = new("A") },
				func(in *service.UpdateEntryIn) { in.WorkDate = new(testutil.D("10-28")) },
				func(in *service.UpdateEntryIn) { in.At = &sql.Null[civil.Minute]{} },
			} {
				in := service.UpdateEntryIn{ID: entry.ID, Expect: s.EntryExpect(entry.ID), Notes: new("also rejected")}
				modify(&in)
				_, err := s.Svc.UpdateEntry(s.Ctx(), s.Meta(), in)
				testutil.RequireCode(t, err, domain.CodeValidation)
			}
			_, err := s.Svc.RelinkEntry(s.Ctx(), s.Meta(), service.RelinkEntryIn{ID: entry.ID, Expect: s.EntryExpect(entry.ID), TaskID: entry.TaskID})
			testutil.RequireCode(t, err, domain.CodeValidation)
			s.EditEntry(entry.ID, func(in *service.UpdateEntryIn) { in.Notes = new("completion notes") })
			if s.Last.Entry.Notes != "completion notes" || s.Last.Entry.Kind != domain.EntryComplete || s.Last.Entry.TaskID != entry.TaskID {
				t.Fatal("completion edit changed immutable fields")
			}
			if done {
				_, err = s.Svc.DeleteEntry(s.Ctx(), s.Meta(), service.EntryRef{ID: entry.ID, Expect: s.EntryExpect(entry.ID)})
				testutil.RequireCode(t, err, domain.CodeConflict)
				de, _ := domain.AsError(err)
				if !strings.Contains(de.Detail, "use reopen --remove-entry") {
					t.Fatalf("conflict detail = %q", de.Detail)
				}
			} else {
				s.DeleteEntry(entry.ID)
				s.UndeleteEntry(entry.ID)
				if s.Last.Entry.DeletedAt.Valid || !s.Last.Entry.Reopened {
					t.Fatal("reopened completion was not restored")
				}
			}
		})
	}
}
