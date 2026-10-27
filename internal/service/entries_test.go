package service_test

import (
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/store"
	"taskronin/internal/testutil"
)

func TestDAT03(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	_, err := s.Svc.AddEntry(s.Ctx(), s.Meta(), service.AddEntryIn{
		TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}, Title: "x", WorkDate: testutil.D("10-29"),
	})
	requireEntryField(t, err, "work_date")
	if detail, err := s.Svc.GetTask(s.Ctx(), s.Actor, id); err != nil || len(detail.Entries) != 0 || s.OpCount() != 1 {
		t.Fatalf("future work wrote data: %+v, %v", detail, err)
	}
}

func TestDAT11(t *testing.T) {
	s := testutil.NewScenario(t)
	s.At("2027-03-15 10:00")
	id := s.Add("A")
	entry := s.LogAt(id, "x", "2027-03-14", "02:30")
	e, err := s.Svc.GetEntry(s.Ctx(), s.Actor, entry)
	if err != nil || e.WorkDate != testutil.D("2027-03-14") || !e.OccurredMin.Valid || e.OccurredMin.V != 150 {
		t.Fatalf("DST gap entry = %+v, %v", e, err)
	}
	var after map[string]any
	if err := json.Unmarshal(s.Events(s.Last.OpID)[0].After, &after); err != nil {
		t.Fatal(err)
	}
	if after["occurred_min"] != float64(150) || after["work_date"] != "2027-03-14" {
		t.Fatalf("event columns = %v", after)
	}
}

func TestDAT17(t *testing.T) {
	for _, tt := range []struct {
		name, date, at string
		valid          bool
	}{
		{"future today", "10-28", "10:30", false},
		{"earlier today", "10-28", "09:59", true},
		{"later yesterday", "10-27", "10:30", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := testutil.NewScenario(t)
			id := s.Add("A")
			moment := testutil.Moment(tt.date + " " + tt.at)
			if !tt.valid {
				_, err := s.Svc.AddEntry(s.Ctx(), s.Meta(), service.AddEntryIn{
					TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}, Title: "x", WorkDate: moment.Date, At: moment.Min,
				})
				requireEntryField(t, err, "time")
				if s.OpCount() != 1 {
					t.Fatal("future time wrote an op")
				}
				return
			}
			s.LogAt(id, "x", tt.date, tt.at)
			if e := s.Last.Entry; e.WorkDate != moment.Date || e.OccurredMin != moment.Min {
				t.Fatalf("entry = %+v", e)
			}
		})
	}
}

func TestAddEntryTargets(t *testing.T) {
	for _, linked := range []bool{false, true} {
		name := "standalone"
		if linked {
			name = "linked"
		}
		t.Run(name, func(t *testing.T) {
			s := testutil.NewScenario(t)
			var task domain.TaskID
			var before domain.Task
			if linked {
				task = s.Add("A")
				before = s.Task(task).Task
			}
			id := s.Log(task, "x", "")
			r, e := s.Last, s.Last.Entry
			if id != 1 || e.TaskID != (sql.Null[domain.TaskID]{V: task, Valid: linked}) ||
				e.Kind != domain.EntryWork || e.Title != "x" || e.Notes != "" || e.WorkDate != testutil.D("10-28") ||
				e.OccurredMin.Valid || e.Reopened || e.Version != 1 || e.DeletedAt.Valid || e.PurgedAt.Valid ||
				!e.CreatedAt.Equal(s.Clock.Now()) || !e.UpdatedAt.Equal(e.CreatedAt) {
				t.Fatalf("entry = %+v", e)
			}
			if !r.Created || r.Replayed || r.NoChange || len(r.Warnings) != 0 ||
				!slices.Equal(r.Refs, []domain.EntityRef{{Entity: domain.EntEntry, ID: int64(id)}}) ||
				!slices.Equal(r.Effects, []service.Effect{{Kind: service.EffEntryLogged, TaskID: task, EntryID: id}}) {
				t.Fatalf("result = %+v", r)
			}
			if got := s.EventKeys(r.OpID); !slices.Equal(got, []string{"entry:insert 1"}) {
				t.Fatalf("events = %v", got)
			}
			if ev := s.Events(r.OpID)[0]; ev.TaskID != e.TaskID || len(ev.Before) != 0 || ev.Derived {
				t.Fatalf("event = %+v", ev)
			}
			op := s.Op(r.OpID)
			stack := s.Stack("owner", store.StackUndo)
			if op.Kind != domain.OpEntryAdd || op.Scopes != domain.ScopeCapture || !op.Revertible || stack[len(stack)-1] != r.OpID {
				t.Fatalf("op = %+v, undo = %v", op, stack)
			}
			if got := s.EntryExpect(id); got != (domain.Expect{Gen: s.Svc.Generation(), Version: 1}) {
				t.Fatalf("entry expect = %+v", got)
			}
			if linked && s.Task(task).Task != before {
				t.Fatal("logging changed task fields")
			}
		})
	}
	t.Run("missing", func(t *testing.T) {
		s := testutil.NewScenario(t)
		_, err := s.Svc.AddEntry(s.Ctx(), s.Meta(), service.AddEntryIn{
			TaskID: sql.Null[domain.TaskID]{V: 999, Valid: true}, Title: "x",
		})
		testutil.RequireCode(t, err, domain.CodeNotFound)
		if s.OpCount() != 0 {
			t.Fatal("missing task wrote an op")
		}
	})
}

func TestAddEntryClosedTargets(t *testing.T) {
	for _, state := range []string{"done", "dropped", "deleted"} {
		t.Run(state, func(t *testing.T) {
			s := testutil.NewScenario(t)
			id := s.Add("A", testutil.Top())
			// Lifecycle operations arrive in Task 28; these non-catalog fixtures
			// preserve the completion and triage invariants checked at cleanup.
			switch state {
			case "done":
				s.Exec(`UPDATE tasks SET status = ?, completed_at = ?, completed_date = ? WHERE id = ?`,
					state, s.Clock.Now().UnixMilli(), "2026-10-28", id)
				s.Exec(`INSERT INTO log_entries (task_id, kind, title, work_date, created_at, updated_at)
					VALUES (?, ?, ?, ?, ?, ?)`, id, "complete", "A", "2026-10-28", s.Clock.Now().UnixMilli(), s.Clock.Now().UnixMilli())
			case "dropped":
				s.Exec(`UPDATE tasks SET status = ? WHERE id = ?`, state, id)
			case "deleted":
				s.Exec(`UPDATE tasks SET deleted_at = ? WHERE id = ?`, s.Clock.Now().UnixMilli(), id)
			}
			before, count := s.Task(id).Task, s.OpCount()
			in := service.AddEntryIn{TaskID: sql.Null[domain.TaskID]{V: id, Valid: true}, Title: "x"}
			m := s.Meta()
			r, err := s.Svc.AddEntry(s.Ctx(), m, in)
			if state == "deleted" {
				testutil.RequireCode(t, err, domain.CodeDeleted)
				if s.OpCount() != count {
					t.Fatal("deleted task wrote an op")
				}
				return
			}
			s.Must(r, err)
			if !slices.Equal(r.Warnings, []string{"logged against a " + state + " task"}) || s.Task(id).Task != before {
				t.Fatalf("closed task log = %+v, task = %+v", r, s.Task(id))
			}
			replayed := s.Must(s.Svc.AddEntry(s.Ctx(), m, in))
			if !replayed.Replayed || !replayed.Created || replayed.OpID != r.OpID || replayed.Entry.ID != r.Entry.ID ||
				len(replayed.Warnings) != 0 || !slices.Equal(replayed.Effects, r.Effects) || s.OpCount() != count+1 {
				t.Fatalf("replay = %+v", replayed)
			}
		})
	}
}

func TestAddEntryValidation(t *testing.T) {
	for _, tt := range []struct {
		name, field string
		modify      func(*service.AddEntryIn)
	}{
		{"empty title", "title", func(in *service.AddEntryIn) { in.Title = " \t " }},
		{"invalid UTF-8", "title", func(in *service.AddEntryIn) { in.Title = "\xff" }},
		{"bidi title", "title", func(in *service.AddEntryIn) { in.Title = "a\u202eb" }},
		{"long title", "title", func(in *service.AddEntryIn) { in.Title = strings.Repeat("é", 501) }},
		{"C1 notes", "notes", func(in *service.AddEntryIn) { in.Notes = "a\u0085b" }},
		{"long notes", "notes", func(in *service.AddEntryIn) { in.Notes = strings.Repeat("x", 65537) }},
		{"invalid date", "work_date", func(in *service.AddEntryIn) { in.WorkDate = testutil.D("1000-01-01").AddDays(-1) }},
		{"negative minute", "time", func(in *service.AddEntryIn) { in.At = sql.Null[civil.Minute]{V: -1, Valid: true} }},
		{"end of day minute", "time", func(in *service.AddEntryIn) { in.At = sql.Null[civil.Minute]{V: 1440, Valid: true} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := testutil.NewScenario(t)
			in := service.AddEntryIn{Title: "x"}
			tt.modify(&in)
			_, err := s.Svc.AddEntry(s.Ctx(), s.Meta(), in)
			requireEntryField(t, err, tt.field)
			var entries, events int
			if err := s.Store.Reader.QueryRowContext(s.Ctx(), `SELECT
				(SELECT COUNT(*) FROM log_entries), (SELECT COUNT(*) FROM events)`).Scan(&entries, &events); err != nil {
				t.Fatal(err)
			}
			if entries != 0 || events != 0 || s.OpCount() != 0 || len(s.Stack("owner", store.StackUndo)) != 0 {
				t.Fatal("invalid entry wrote data")
			}
		})
	}
}

func TestAddEntryNormalizationAndTimeBounds(t *testing.T) {
	s := testutil.NewScenario(t)
	r := s.Must(s.Svc.AddEntry(s.Ctx(), s.Meta(), service.AddEntryIn{Title: "\ufeff  x  ", Notes: "\ufeffa\r\nb\rc"}))
	if r.Entry.Title != "x" || r.Entry.Notes != "a\nb\nc" {
		t.Fatalf("normalized entry = %+v", r.Entry)
	}
	r = s.Must(s.Svc.AddEntry(s.Ctx(), s.Meta(), service.AddEntryIn{Title: strings.Repeat("é", 500), Notes: strings.Repeat("x", 65536)}))
	if r.Entry.Title != strings.Repeat("é", 500) || len(r.Entry.Notes) != 65536 {
		t.Fatal("text boundaries were not preserved")
	}
	for _, tt := range []struct{ date, at string }{{"", "00:00"}, {"", "10:00"}, {"10-27", "23:59"}} {
		s.LogAt(0, "x", tt.date, tt.at)
		if !s.Last.Entry.OccurredMin.Valid || s.Last.Entry.OccurredMin.V.String() != tt.at {
			t.Fatalf("time boundary = %+v", s.Last.Entry)
		}
	}
}

func TestAddEntryScopesAndIdempotency(t *testing.T) {
	s := testutil.NewScenario(t)
	capture := s.As(testutil.TokenActor("capture", domain.ScopeCapture))
	id := capture.Log(0, "x", "")
	_, err := s.Svc.GetEntry(s.Ctx(), capture.Actor, id)
	testutil.RequireCode(t, err, domain.CodeForbidden)
	ro := s.As(testutil.TokenActor("read", domain.ScopeRead))
	if _, err := s.Svc.GetEntry(s.Ctx(), ro.Actor, id); err != nil {
		t.Fatal(err)
	}
	_, err = ro.Svc.AddEntry(ro.Ctx(), ro.Meta(), service.AddEntryIn{Title: "x"})
	testutil.RequireCode(t, err, domain.CodeForbidden)
	m := s.Meta()
	m.Idem = service.Idem{}
	_, err = s.Svc.AddEntry(s.Ctx(), m, service.AddEntryIn{Title: "x"})
	testutil.RequireCode(t, err, domain.CodeIdemRequired)
	if s.OpCount() != 1 {
		t.Fatal("read or rejected operation wrote an op")
	}
}

func TestDerive(t *testing.T) {
	type row struct {
		kind    domain.EntryKind
		task    domain.TaskID
		date    string
		deleted bool
	}
	for _, tt := range []struct {
		name string
		rows []row
		want domain.BlockStatus
	}{
		{"empty", nil, domain.BlockMissed},
		{"complete", []row{{domain.EntryComplete, 1, "10-28", false}}, domain.BlockDone},
		{"work", []row{{domain.EntryWork, 1, "10-28", false}}, domain.BlockWorked},
		{"complete wins", []row{{domain.EntryWork, 1, "10-28", false}, {domain.EntryComplete, 1, "10-28", false}}, domain.BlockDone},
		{"deleted work", []row{{domain.EntryWork, 1, "10-28", true}}, domain.BlockMissed},
		{"deleted complete", []row{{domain.EntryComplete, 1, "10-28", true}, {domain.EntryWork, 1, "10-28", false}}, domain.BlockWorked},
		{"other day", []row{{domain.EntryWork, 1, "10-27", false}}, domain.BlockMissed},
		{"other task", []row{{domain.EntryWork, 2, "10-28", false}}, domain.BlockMissed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := testutil.StoreNoCheck(t)
			now := testutil.Local(t, testutil.DefaultNow)
			for _, rank := range []int64{service.Gap, 2 * service.Gap} {
				task := domain.Task{Title: "A", Status: domain.StatusOpen, DueAck: domain.DueNone,
					Rank: rank, Origin: "owner", CreatedAt: now, UpdatedAt: now}
				if err := store.InsertTask(t.Context(), st.Writer, &task); err != nil {
					t.Fatal(err)
				}
			}
			for _, r := range tt.rows {
				e := domain.LogEntry{TaskID: sql.Null[domain.TaskID]{V: r.task, Valid: true}, Kind: r.kind,
					Title: "x", WorkDate: testutil.D(r.date), CreatedAt: now, UpdatedAt: now}
				if r.deleted {
					e.DeletedAt.V, e.DeletedAt.Valid = now, true
				}
				if err := store.InsertEntry(t.Context(), st.Writer, &e); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := service.DeriveForTest(t.Context(), st.Reader, 1, testutil.D("10-28")); err != nil || got != tt.want {
				t.Fatalf("derive = %s, %v; want %s", got, err, tt.want)
			}
		})
	}
}

func TestGetEntry(t *testing.T) {
	s := testutil.NewScenario(t)
	task := s.Add("A")
	linked, standalone := s.Log(task, "linked", ""), s.Log(0, "standalone", "10-27")
	count := s.OpCount()
	for _, tt := range []struct {
		id        domain.EntryID
		taskTitle string
	}{{linked, "A"}, {standalone, ""}} {
		v, err := s.Svc.GetEntry(s.Ctx(), s.Actor, tt.id)
		if err != nil || v.ID != tt.id || v.TaskTitle != tt.taskTitle {
			t.Fatalf("entry = %+v, %v", v, err)
		}
	}
	_, err := s.Svc.GetEntry(s.Ctx(), s.Actor, 999)
	testutil.RequireCode(t, err, domain.CodeNotFound)
	if de, _ := domain.AsError(err); de.Detail != "entry #999 not found" {
		t.Fatalf("missing entry = %v", err)
	}
	// Entry deletion arrives in Task 25; GET must already retain tombstones.
	s.Exec(`UPDATE log_entries SET deleted_at = ? WHERE id = ?`, s.Clock.Now().UnixMilli(), linked)
	v, err := s.Svc.GetEntry(s.Ctx(), s.Actor, linked)
	if err != nil || !v.DeletedAt.Valid || v.TaskTitle != "A" || s.OpCount() != count {
		t.Fatalf("tombstoned entry = %+v, %v", v, err)
	}
}

func requireEntryField(t *testing.T, err error, field string) {
	t.Helper()
	testutil.RequireCode(t, err, domain.CodeValidation)
	de, _ := domain.AsError(err)
	if len(de.Fields) != 1 || de.Fields[0].Field != field {
		t.Fatalf("fields = %+v, want %s", de.Fields, field)
	}
}
