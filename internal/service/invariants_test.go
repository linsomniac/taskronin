package service_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/store"
	"taskronin/internal/testutil"
)

func invariantSQL(t *testing.T, q store.Q, query string, args ...any) {
	t.Helper()
	if _, err := q.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func invariantTask(t *testing.T, q store.Q, id int64) {
	t.Helper()
	invariantSQL(t, q, `INSERT INTO tasks (id, title, rank, origin, created_at, updated_at)
		VALUES (?, 'task', ?, 'owner', 0, 0)`, id, id*65536)
}

func invariantBlock(t *testing.T, q store.Q, task int64, start, end int) {
	t.Helper()
	invariantSQL(t, q, `INSERT INTO time_blocks (task_id, date, start_min, end_min, created_at)
		VALUES (?, '2026-10-28', ?, ?, 0)`, task, start, end)
}

func requireInvariant(t *testing.T, err error, ids ...string) {
	t.Helper()
	testutil.RequireCode(t, err, domain.CodeConflict)
	de, _ := domain.AsError(err)
	for _, id := range ids {
		if !strings.Contains(de.Detail, "invariant "+id+" violated:") {
			t.Fatalf("detail = %q, missing invariant %s", de.Detail, id)
		}
	}
}

func checkAll(t *testing.T, q store.Q) error {
	t.Helper()
	return service.CheckAll(t.Context(), q, testutil.Local(t, testutil.DefaultNow), testutil.Zone)
}

func TestI1aPlannedOnDoneTask(t *testing.T) {
	st := testutil.StoreNoCheck(t)
	invariantTask(t, st.Writer, 7)
	invariantBlock(t, st.Writer, 7, 660, 720)
	invariantSQL(t, st.Writer, `UPDATE tasks SET status = 'done', completed_at = 0,
		completed_date = '2026-10-28' WHERE id = 7`)
	requireInvariant(t, checkAll(t, st.Reader), "I1a")
}

func TestI1bPlannedOnDeferredTask(t *testing.T) {
	for _, tc := range []struct {
		name, date, now string
		minute          any
		fail            bool
	}{
		{"date only future", "2026-10-29", testutil.DefaultNow, nil, true},
		{"timed future", "2026-10-28", testutil.DefaultNow, 601, true},
		{"exact moment", "2026-10-28", testutil.DefaultNow, 600, false},
		{"past", "2026-10-28", testutil.DefaultNow, nil, false},
		{"gap before", "2026-03-08", "2026-03-08 01:59", 150, true},
		{"gap at", "2026-03-08", "2026-03-08 03:00", 150, false},
		{"fold before", "2026-11-01", "2026-11-01T07:29:00Z", 90, true},
		{"fold second hour", "2026-11-01", "2026-11-01T08:15:00Z", 90, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := testutil.StoreNoCheck(t)
			invariantTask(t, st.Writer, 7)
			invariantBlock(t, st.Writer, 7, 660, 720)
			invariantSQL(t, st.Writer, `UPDATE tasks SET defer_date = ?, defer_min = ? WHERE id = 7`, tc.date, tc.minute)
			if err := service.CheckStructural(t.Context(), st.Reader); err != nil {
				t.Fatalf("structural: %v", err)
			}
			now := testutil.Local(t, tc.now)
			for _, err := range []error{service.CheckTimeDependent(t.Context(), st.Reader, now, testutil.Zone), service.CheckAll(t.Context(), st.Reader, now, testutil.Zone)} {
				if tc.fail {
					requireInvariant(t, err, "I1b")
				} else if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestI2Overlap(t *testing.T) {
	st := testutil.StoreNoCheck(t)
	invariantSQL(t, st.Writer, `DROP TRIGGER time_blocks_no_overlap_ins`)
	invariantTask(t, st.Writer, 1)
	invariantTask(t, st.Writer, 2)
	invariantBlock(t, st.Writer, 1, 660, 720)
	invariantBlock(t, st.Writer, 2, 719, 780)
	requireInvariant(t, checkAll(t, st.Reader), "I2")
}

func TestI3DeferAfterDue(t *testing.T) {
	for _, tc := range []struct {
		name, deferDate, dueDate string
		deferMin, dueMin         any
		fail                     bool
	}{
		{"later date", "2026-10-30", "2026-10-29", nil, nil, true},
		{"same timed moment", "2026-10-29", "2026-10-29", 600, 600, true},
		{"later minute", "2026-10-29", "2026-10-29", 601, 600, true},
		{"date defer at timed due", "2026-10-29", "2026-10-29", nil, 0, true},
		{"both date only", "2026-10-29", "2026-10-29", nil, nil, false},
		{"timed defer date due", "2026-10-29", "2026-10-29", 1439, nil, false},
		{"earlier minute", "2026-10-29", "2026-10-29", 599, 600, false},
		{"earlier date", "2026-10-28", "2026-10-29", 1439, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := testutil.StoreNoCheck(t)
			invariantTask(t, st.Writer, 7)
			invariantSQL(t, st.Writer, `UPDATE tasks SET defer_date = ?, defer_min = ?, due_date = ?, due_min = ? WHERE id = 7`, tc.deferDate, tc.deferMin, tc.dueDate, tc.dueMin)
			err := checkAll(t, st.Reader)
			if tc.fail {
				requireInvariant(t, err, "I3")
				de, _ := domain.AsError(err)
				if !strings.Contains(de.Detail, "task #7") {
					t.Fatalf("detail = %q", de.Detail)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestI3DSTGapInstants(t *testing.T) {
	for _, tc := range []struct {
		name             string
		deferMin, dueMin int
		fail             bool
	}{
		{"within gap", 130, 150, true},
		{"gap to boundary", 130, 180, true},
		{"before gap", 119, 150, false},
		{"after gap", 130, 181, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := testutil.StoreNoCheck(t)
			invariantTask(t, st.Writer, 7)
			invariantSQL(t, st.Writer, `UPDATE tasks SET defer_date = '2026-03-08', defer_min = ?,
				due_date = '2026-03-08', due_min = ? WHERE id = 7`, tc.deferMin, tc.dueMin)
			for _, check := range []struct {
				name string
				run  func() error
			}{
				{"structural", func() error { return service.CheckStructural(t.Context(), st.Reader) }},
				{"all", func() error { return checkAll(t, st.Reader) }},
			} {
				t.Run(check.name, func(t *testing.T) {
					err := check.run()
					if tc.fail {
						requireInvariant(t, err, "I3")
					} else if err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}

func TestI3ConfiguredZone(t *testing.T) {
	for _, tc := range []struct {
		stored, supplied string
	}{
		{"UTC", "America/Denver"},
		{"America/Denver", "UTC"},
	} {
		t.Run(tc.supplied, func(t *testing.T) {
			st := testutil.StoreNoCheck(t)
			invariantTask(t, st.Writer, 7)
			invariantSQL(t, st.Writer, `UPDATE tasks SET defer_date = '2026-03-08', defer_min = 130,
				due_date = '2026-03-08', due_min = 150 WHERE id = 7`)
			if err := store.SetSetting(t.Context(), st.Writer, "tz_last_seen", tc.stored); err != nil {
				t.Fatal(err)
			}
			z, err := civil.LoadZone(tc.supplied)
			if err != nil {
				t.Fatal(err)
			}
			t.Run("structural uses stored zone", func(t *testing.T) {
				err := service.CheckStructural(t.Context(), st.Reader)
				if tc.stored == "America/Denver" {
					requireInvariant(t, err, "I3")
				} else if err != nil {
					t.Fatal(err)
				}
			})
			t.Run("all uses supplied zone", func(t *testing.T) {
				err := service.CheckAll(t.Context(), st.Reader, testutil.Local(t, testutil.DefaultNow), z)
				if tc.supplied == "America/Denver" {
					requireInvariant(t, err, "I3")
				} else if err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestCheckStructuralInvalidZone(t *testing.T) {
	st := testutil.StoreNoCheck(t)
	if err := store.SetSetting(t.Context(), st.Writer, "tz_last_seen", ""); err != nil {
		t.Fatal(err)
	}
	err := service.CheckStructural(t.Context(), st.Reader)
	testutil.RequireCode(t, err, domain.CodeInternal)
	if de, _ := domain.AsError(err); de.Detail != "" || de.Err == nil {
		t.Fatalf("invalid zone must retain a private cause: %+v", de)
	}
}

func TestI4DoneWithoutCompletion(t *testing.T) {
	st := testutil.StoreNoCheck(t)
	invariantTask(t, st.Writer, 7)
	invariantSQL(t, st.Writer, `UPDATE tasks SET status = 'done', completed_at = 0,
		completed_date = '2026-10-28' WHERE id = 7`)
	requireInvariant(t, checkAll(t, st.Reader), "I4")
}

func TestI4PurgedCompletionAccepted(t *testing.T) {
	for _, n := range []int{1, 2} {
		t.Run(strings.Repeat("purged", n), func(t *testing.T) {
			st := testutil.StoreNoCheck(t)
			invariantTask(t, st.Writer, 7)
			invariantSQL(t, st.Writer, `UPDATE tasks SET status = 'done', completed_at = 0,
				completed_date = '2026-10-28' WHERE id = 7`)
			for range n {
				invariantSQL(t, st.Writer, `INSERT INTO log_entries (task_id, kind, title, work_date, created_at, updated_at, deleted_at, purged_at, reopened)
					VALUES (7, 'complete', '[purged]', '2026-10-28', 0, 0, 1, 1, 1)`)
			}
			if err := checkAll(t, st.Reader); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestI4CompletionStates(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		live         int
		reopened     bool
		deleted      any
		fail         bool
	}{
		{"one completion", "done", 1, false, nil, false},
		{"two completions", "done", 2, false, nil, true},
		{"reopened completion", "done", 1, true, nil, true},
		{"deleted completion", "done", 1, false, 1, true},
		{"open completion", "open", 1, false, nil, true},
		{"dropped completion", "dropped", 1, false, nil, true},
		{"open reopened", "open", 1, true, nil, false},
		{"open deleted", "open", 1, false, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := testutil.StoreNoCheck(t)
			invariantTask(t, st.Writer, 7)
			invariantSQL(t, st.Writer, `UPDATE tasks SET status = ?, completed_at = CASE WHEN ? = 'done' THEN 0 END,
				completed_date = CASE WHEN ? = 'done' THEN '2026-10-28' END WHERE id = 7`, tc.status, tc.status, tc.status)
			for range tc.live {
				invariantSQL(t, st.Writer, `INSERT INTO log_entries (task_id, kind, title, work_date, created_at, updated_at, deleted_at, reopened)
					VALUES (7, 'complete', 'completed', '2026-10-28', 0, 0, ?, ?)`, tc.deleted, tc.reopened)
			}
			err := checkAll(t, st.Reader)
			if tc.fail {
				requireInvariant(t, err, "I4")
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestI5TriageOnDoneTask(t *testing.T) {
	st := testutil.StoreNoCheck(t)
	invariantTask(t, st.Writer, 7)
	invariantSQL(t, st.Writer, `UPDATE tasks SET status = 'done', completed_at = 0,
		completed_date = '2026-10-28' WHERE id = 7`)
	invariantSQL(t, st.Writer, `INSERT INTO task_triage VALUES (7, 'new', 0, 1)`)
	requireInvariant(t, checkAll(t, st.Reader), "I5")
}

func TestI6FocusYesterday(t *testing.T) {
	st := testutil.StoreNoCheck(t)
	invariantTask(t, st.Writer, 7)
	invariantSQL(t, st.Writer, `UPDATE focus SET task_id = 7, date = '2026-10-27' WHERE id = 1`)
	if err := service.CheckStructural(t.Context(), st.Reader); err != nil {
		t.Fatal(err)
	}
	requireInvariant(t, service.CheckTimeDependent(t.Context(), st.Reader, testutil.Local(t, testutil.DefaultNow), testutil.Zone), "I6")
	requireInvariant(t, checkAll(t, st.Reader), "I6")
}

func TestI6FocusTaskState(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		deleted      any
		deferDate    any
		deferMin     any
		fail         bool
	}{
		{"open", "open", nil, nil, nil, false},
		{"dropped", "dropped", nil, nil, nil, true},
		{"deleted", "open", 1, nil, nil, true},
		{"deferred", "open", nil, "2026-10-29", nil, true},
		{"exact defer moment", "open", nil, "2026-10-28", 600, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := testutil.StoreNoCheck(t)
			invariantTask(t, st.Writer, 7)
			invariantSQL(t, st.Writer, `UPDATE tasks SET status = ?, deleted_at = ?, defer_date = ?, defer_min = ? WHERE id = 7`, tc.status, tc.deleted, tc.deferDate, tc.deferMin)
			invariantSQL(t, st.Writer, `UPDATE focus SET task_id = 7, date = '2026-10-28' WHERE id = 1`)
			err := checkAll(t, st.Reader)
			if tc.fail {
				requireInvariant(t, err, "I6")
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestI7DuplicateRank(t *testing.T) {
	st := testutil.StoreNoCheck(t)
	invariantTask(t, st.Writer, 1)
	invariantTask(t, st.Writer, 2)
	invariantSQL(t, st.Writer, `UPDATE tasks SET rank = 65536, deleted_at = 1 WHERE id = 2`)
	requireInvariant(t, checkAll(t, st.Reader), "I7")
}

func TestI8CompletionNeedsTask(t *testing.T) {
	st := testutil.StoreNoCheck(t)
	invariantSQL(t, st.Writer, `PRAGMA ignore_check_constraints = ON`)
	invariantSQL(t, st.Writer, `INSERT INTO log_entries (kind, title, work_date, created_at, updated_at)
		VALUES ('complete', 'orphan', '2026-10-28', 0, 0)`)
	invariantSQL(t, st.Writer, `PRAGMA ignore_check_constraints = OFF`)
	requireInvariant(t, checkAll(t, st.Reader), "I8")
}

func TestI8EntryKindImmutable(t *testing.T) {
	st := testutil.Store(t)
	invariantTask(t, st.Writer, 7)
	invariantSQL(t, st.Writer, `INSERT INTO log_entries (task_id, kind, title, work_date, created_at, updated_at)
		VALUES (7, 'work', 'work', '2026-10-28', 0, 0)`)
	_, err := st.Writer.ExecContext(t.Context(), `UPDATE log_entries SET kind = 'complete'`)
	testutil.RequireCode(t, store.MapErr(err), domain.CodeConflict)
}

func TestCheckAllCleanStore(t *testing.T) {
	st := testutil.Store(t)
	if err := checkAll(t, st.Writer); err != nil {
		t.Fatal(err)
	}
	invariantTask(t, st.Writer, 1)
	invariantTask(t, st.Writer, 2)
	invariantBlock(t, st.Writer, 1, 660, 720)
	invariantBlock(t, st.Writer, 2, 720, 780)
	// Tombstoned planned blocks neither overlap nor bind closed tasks.
	invariantSQL(t, st.Writer, `INSERT INTO time_blocks (task_id, date, start_min, end_min, created_at, deleted_at)
		VALUES (2, '2026-10-28', 660, 780, 0, 1)`)
	if err := checkAll(t, st.Writer); err != nil {
		t.Fatal(err)
	}
}

func TestCheckAllJoinsViolations(t *testing.T) {
	st := testutil.StoreNoCheck(t)
	invariantTask(t, st.Writer, 7)
	invariantSQL(t, st.Writer, `UPDATE tasks SET defer_date = '2026-10-30', due_date = '2026-10-29' WHERE id = 7`)
	invariantSQL(t, st.Writer, `UPDATE focus SET task_id = 7, date = '2026-10-27' WHERE id = 1`)
	err := checkAll(t, st.Reader)
	requireInvariant(t, err, "I3", "I6")
	de, _ := domain.AsError(err)
	if !strings.Contains(de.Detail, "; ") {
		t.Fatalf("violations not joined: %q", de.Detail)
	}
}

func TestCheckAllDatabaseErrors(t *testing.T) {
	st := testutil.Store(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	testutil.RequireCode(t, service.CheckAll(ctx, st.Reader, testutil.Local(t, testutil.DefaultNow), testutil.Zone), domain.CodeInternal)
}

func TestOpInvariantRollsBack(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	_, err := s.Svc.TestOpRaw(s.Ctx(), s.Meta(), []domain.EntityRef{{Entity: domain.EntTask, ID: int64(id)}},
		func(q store.Q) error {
			_, err := q.ExecContext(s.Ctx(), `UPDATE tasks SET due_date = '2026-10-29',
			defer_date = '2026-10-30' WHERE id = ?`, id)
			return err
		})
	testutil.RequireCode(t, err, domain.CodeConflict) // I3
	if tv := s.Task(id); tv.DueDate.Valid || tv.Version != 1 {
		t.Fatalf("op not rolled back: %+v", tv)
	}
}

func TestOpInvariantRelatedTasks(t *testing.T) {
	for _, entity := range []domain.EntityKind{domain.EntBlock, domain.EntEntry} {
		t.Run(string(entity), func(t *testing.T) {
			s := testutil.NewScenario(t)
			id := s.Add("A")
			var ref domain.EntityRef
			if entity == domain.EntBlock {
				b := domain.TimeBlock{TaskID: id, Span: domain.BlockSpan{Date: testutil.D("10-28"), Start: 660, End: 720}, Status: domain.BlockPlanned, CreatedAt: s.Clock.Now()}
				if err := store.InsertBlock(s.Ctx(), s.Store.Writer, &b); err != nil {
					t.Fatal(err)
				}
				ref = domain.EntityRef{Entity: entity, ID: int64(b.ID)}
			} else {
				e := domain.LogEntry{TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}, Kind: domain.EntryWork, Title: "work", WorkDate: testutil.D("10-28"), CreatedAt: s.Clock.Now(), UpdatedAt: s.Clock.Now()}
				if err := store.InsertEntry(s.Ctx(), s.Store.Writer, &e); err != nil {
					t.Fatal(err)
				}
				ref = domain.EntityRef{Entity: entity, ID: int64(e.ID)}
			}
			_, err := s.Svc.TestOpRaw(s.Ctx(), s.Meta(), []domain.EntityRef{ref}, func(q store.Q) error {
				_, err := q.ExecContext(s.Ctx(), `UPDATE tasks SET due_date = '2026-10-29', defer_date = '2026-10-30' WHERE id = ?`, id)
				return err
			})
			requireInvariant(t, err, "I3")
			if s.Task(id).DueDate.Valid {
				t.Fatal("related task mutation was not rolled back")
			}
		})
	}
}
