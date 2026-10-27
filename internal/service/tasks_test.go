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

func TestTRI01(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	tv := s.Task(id)
	if tv.Status != domain.StatusOpen || tv.View != domain.ViewTriage || tv.Origin != "owner" || tv.Version != 1 ||
		tv.LastPlacedAt.Valid || tv.DueAck != domain.DueNone || tv.Rank != service.Gap {
		t.Fatalf("task = %+v", tv)
	}
	if got := s.Reasons(id); !slices.Equal(got, []domain.TriageReason{domain.ReasonNew}) ||
		!tv.Reasons[0].Since.Equal(testutil.Local(t, testutil.DefaultNow)) {
		t.Fatalf("reasons = %v", tv.Reasons)
	}
	if got := s.EventKeys(s.Last.OpID); !slices.Equal(got, []string{"task:insert 1", "triage:add 1 new"}) {
		t.Fatalf("events = %v", got)
	}
	if !s.Last.Created || s.Last.Task == nil || len(s.Last.Effects) != 2 || s.Last.Task.View != domain.ViewTriage {
		t.Fatalf("result = %+v", s.Last)
	}
	if len(s.Backlog()) != 0 || tv.Reasons[0].Seq != s.Op(s.Last.OpID).Seq {
		t.Fatal("new task is in backlog or reason has wrong sequence")
	}
}

func TestTRI08(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("B", testutil.Due("10-29"))
	tv := s.Task(id)
	if !tv.DueDate.Valid || tv.DueDate.V != testutil.D("10-29") || tv.DueMin.Valid || tv.DueAck != domain.DueNone ||
		!slices.Equal(s.Reasons(id), []domain.TriageReason{domain.ReasonNew}) || tv.LastPlacedAt.Valid || !tv.DueSoon {
		t.Fatalf("task = %+v", tv)
	}
	for _, e := range s.Last.Effects {
		if e.Kind == service.EffPlaced {
			t.Fatalf("due was treated as placement: %+v", s.Last.Effects)
		}
	}
}

func TestCreationKeepsVersion1(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A", testutil.Due("10-29"), func(in *service.AddTaskIn) { in.Tags = []string{"Infra", "work"} })
	if tv := s.Task(id); tv.Version != 1 || !slices.Equal(tv.Tags, []string{"Infra", "work"}) {
		t.Fatalf("task = %+v", tv)
	}
	if got := s.EventKeys(s.Last.OpID); !slices.Equal(got, []string{"task:insert 1", "triage:add 1 new", "task_tags:set 1"}) {
		t.Fatalf("events = %v", got)
	}
	e := s.Events(s.Last.OpID)[2]
	if string(e.Before) != `{"tags":[]}` || string(e.After) != `{"tags":["Infra","work"]}` {
		t.Fatalf("tag event = %+v", e)
	}
}

func TestAddTaskValidation(t *testing.T) {
	for _, tt := range []struct {
		name, field string
		modify      func(*service.AddTaskIn)
	}{
		{"empty title", "title", func(in *service.AddTaskIn) { in.Title = "" }},
		{"bidi title", "title", func(in *service.AddTaskIn) { in.Title = "a\u202eb" }},
		{"bad notes", "notes", func(in *service.AddTaskIn) { in.Notes = "a\u0085b" }},
		{"past due", "due", testutil.Due("10-27")},
		{"due now", "due", testutil.Due("10-28 10:00")},
		{"zero due date", "due", func(in *service.AddTaskIn) { in.Due = &domain.Moment{} }},
		{"negative due minute", "due", func(in *service.AddTaskIn) {
			in.Due = &domain.Moment{Date: testutil.D("10-29"), Min: sql.Null[civil.Minute]{V: -1, Valid: true}}
		}},
		{"end of day minute", "due", func(in *service.AddTaskIn) {
			in.Due = &domain.Moment{Date: testutil.D("10-29"), Min: sql.Null[civil.Minute]{V: 1440, Valid: true}}
		}},
		{"project", "project", func(in *service.AddTaskIn) { in.Project = "a b" }},
		{"priority", "priority", func(in *service.AddTaskIn) { in.Priority = "X" }},
		{"tag", "tags", func(in *service.AddTaskIn) { in.Tags = []string{"valid", "a b"} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := testutil.NewScenario(t)
			in := service.AddTaskIn{Title: "A"}
			tt.modify(&in)
			_, err := s.Svc.AddTask(s.Ctx(), s.Meta(), in)
			testutil.RequireCode(t, err, domain.CodeValidation)
			de, _ := domain.AsError(err)
			if len(de.Fields) != 1 || de.Fields[0].Field != tt.field {
				t.Fatalf("fields = %+v", de.Fields)
			}
			var n int
			if err := s.Store.Reader.QueryRowContext(s.Ctx(), `SELECT
				(SELECT COUNT(*) FROM tasks) + (SELECT COUNT(*) FROM events) +
				(SELECT COUNT(*) FROM task_triage) + (SELECT COUNT(*) FROM tags) +
				(SELECT COUNT(*) FROM task_tags)`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 || s.OpCount() != 0 || len(s.Stack("owner", store.StackUndo)) != 0 {
				t.Fatal("invalid input wrote data")
			}
		})
	}
}

func TestAddTaskNormalizationAndScopes(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("\ufeff  A  ", testutil.Due("10-28"), func(in *service.AddTaskIn) {
		in.Notes, in.Tags, in.Project, in.Priority = "\ufeffa\r\nb\rc", []string{"Infra", "infra", "Work"}, "Infra.Core", domain.PriorityH
	})
	if tv := s.Task(id); tv.Title != "A" || tv.Notes != "a\nb\nc" || tv.Project != "Infra.Core" || tv.Priority != domain.PriorityH ||
		!slices.Equal(tv.Tags, []string{"Infra", "Work"}) || tv.DuePhase != domain.DueDue || tv.Version != 1 {
		t.Fatalf("task = %+v", tv)
	}
	capture := s.As(testutil.TokenActor("capture", domain.ScopeCapture))
	r := capture.Must(capture.Svc.AddTask(capture.Ctx(), capture.Meta(), service.AddTaskIn{Title: "token", Tags: []string{"infra"}}))
	if r.Task.Origin != capture.Actor.Key() || r.Task.View != domain.ViewTriage || !slices.Equal(r.Task.Tags, []string{"Infra"}) {
		t.Fatalf("capture = %+v", r.Task)
	}
	_, err := capture.Svc.GetTask(capture.Ctx(), capture.Actor, id)
	testutil.RequireCode(t, err, domain.CodeForbidden)
	_, err = capture.Svc.MarkManual(capture.Ctx(), capture.Meta(), service.MarkManualIn{ID: id, Expect: s.Expect(id)})
	testutil.RequireCode(t, err, domain.CodeForbidden)
	ro := s.As(testutil.TokenActor("ro", domain.ScopeRead))
	_, err = ro.Svc.AddTask(ro.Ctx(), ro.Meta(), service.AddTaskIn{Title: "forbidden"})
	testutil.RequireCode(t, err, domain.CodeForbidden)
	if s.OpCount() != 2 {
		t.Fatal("forbidden operations wrote ops")
	}
}

func TestMarkManual(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	s.MarkManual(id)
	tv := s.Task(id)
	if !slices.Equal(s.Reasons(id), []domain.TriageReason{domain.ReasonManual, domain.ReasonNew}) || tv.Version != 2 {
		t.Fatalf("task = %+v", tv)
	}
	if got := s.EventKeys(s.Last.OpID); !slices.Equal(got, []string{"triage:add 1 manual"}) {
		t.Fatalf("events = %v", got)
	}
	before, count := tv.Reasons[0], s.OpCount()
	s.At("10-28 10:01").MarkManual(id)
	if !s.Last.NoChange || !s.Last.OpID.IsZero() || s.Last.Task == nil || s.Last.Task.Version != 2 ||
		s.Task(id).Version != 2 || s.OpCount() != count || s.Task(id).Reasons[0] != before {
		t.Fatalf("repeated manual changed state: %+v", s.Last)
	}
}

func TestExpectChecks(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	stale := s.Expect(id)
	s.MarkManual(id)
	for _, tt := range []struct {
		name string
		exp  domain.Expect
		code domain.Code
	}{
		{"missing", domain.Expect{}, domain.CodePreconditionReq},
		{"stale", stale, domain.CodePrecondition},
		{"other generation", domain.Expect{Gen: domain.NewGeneration(), Version: 2}, domain.CodePrecondition},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.Svc.MarkManual(s.Ctx(), s.Meta(), service.MarkManualIn{ID: id, Expect: tt.exp})
			testutil.RequireCode(t, err, tt.code)
			if tt.code == domain.CodePrecondition {
				de, _ := domain.AsError(err)
				current, ok := de.Current.(domain.TaskView)
				if !ok || current.Version != 2 || current.View != domain.ViewTriage || len(current.Reasons) != 2 {
					t.Fatalf("current = %+v", de.Current)
				}
			}
		})
	}
	if s.OpCount() != 2 {
		t.Fatal("failed preconditions wrote ops")
	}
}

func TestGetTask(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	detail, err := s.Svc.GetTask(s.Ctx(), s.Actor, id)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ID != id || detail.Title != "A" || detail.View != domain.ViewTriage ||
		len(detail.Entries) != 0 || len(detail.Blocks) != 0 || len(detail.Interruptions) != 0 {
		t.Fatalf("detail = %+v", detail)
	}
	_, err = s.Svc.GetTask(s.Ctx(), s.Actor, 999)
	testutil.RequireCode(t, err, domain.CodeNotFound)
	if de, _ := domain.AsError(err); de.Detail != "task #999 not found" {
		t.Fatalf("missing task = %v", err)
	}
	if s.OpCount() != 1 {
		t.Fatal("read wrote an op")
	}
}

func TestDuePhase(t *testing.T) {
	for _, tt := range []struct {
		due  string
		want domain.DuePhase
	}{
		{"10-28", domain.DueDue}, {"10-28 09:00", domain.DueOverdue},
		{"10-28 10:00", domain.DueOverdue}, {"10-28 10:01", domain.DueDue},
		{"10-29", domain.DueNone}, {"", domain.DueNone},
	} {
		t.Run(tt.due, func(t *testing.T) {
			task := domain.Task{}
			if tt.due != "" {
				m := testutil.Moment(tt.due)
				task.DueDate, task.DueMin = sql.Null[civil.Date]{V: m.Date, Valid: true}, m.Min
			}
			if got := service.DuePhaseForTest(task, testutil.Local(t, testutil.DefaultNow), testutil.D("10-28"), testutil.Zone); got != tt.want {
				t.Fatalf("phase = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestBlockViewFlags(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	s.Exec(`INSERT INTO time_blocks (task_id, date, start_min, end_min, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, id, "2026-10-28", 780, 900, "planned", s.Clock.Now().UnixMilli())
	for _, tt := range []struct {
		now                              string
		started, ended, current, reblock bool
	}{
		{"10-28 12:59", false, false, false, false},
		{"10-28 13:00", true, false, true, false},
		{"10-28 15:00", true, true, false, true},
	} {
		t.Run(tt.now, func(t *testing.T) {
			s.At(tt.now)
			detail, err := s.Svc.GetTask(s.Ctx(), s.Actor, id)
			if err != nil || len(detail.Blocks) != 1 {
				t.Fatalf("detail = %+v, %v", detail, err)
			}
			b := detail.Blocks[0]
			if b.Started != tt.started || b.Ended != tt.ended || b.Current != tt.current || b.NeedsReblock != tt.reblock ||
				b.TaskVersion != detail.Version || b.TaskTitle != "A" {
				t.Fatalf("block = %+v", b)
			}
		})
	}
	s.Exec(`INSERT INTO log_entries (task_id, kind, title, work_date, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, id, "work", "Worked", "2026-10-28", s.Clock.Now().UnixMilli(), s.Clock.Now().UnixMilli())
	detail, err := s.Svc.GetTask(s.Ctx(), s.Actor, id)
	if err != nil || detail.Blocks[0].NeedsReblock || len(detail.Entries) != 1 || detail.Entries[0].TaskTitle != "A" {
		t.Fatalf("detail after work = %+v, %v", detail, err)
	}
	s.Exec(`UPDATE log_entries SET deleted_at = ? WHERE task_id = ?`, s.Clock.Now().UnixMilli(), id)
	detail, err = s.Svc.GetTask(s.Ctx(), s.Actor, id)
	if err != nil || !detail.Blocks[0].NeedsReblock || len(detail.Entries) != 0 {
		t.Fatalf("detail after tombstone = %+v, %v", detail, err)
	}
}

func TestForwardOpsPushUndo(t *testing.T) {
	s := testutil.NewScenario(t, func(o *service.Options) { o.UndoDepth = 2 })
	id := s.Add("A")
	op1 := s.Last.OpID
	s.Add("B")
	op2 := s.Last.OpID
	if got := s.Stack("owner", store.StackUndo); !slices.Equal(got, []domain.OpID{op1, op2}) {
		t.Fatalf("undo = %v", got)
	}
	s.MarkManual(id)
	op3 := s.Last.OpID
	if got := s.Stack("owner", store.StackUndo); !slices.Equal(got, []domain.OpID{op2, op3}) {
		t.Fatalf("trimmed undo = %v", got)
	}
	s.MarkManual(id)
	if !s.Last.NoChange || !slices.Equal(s.Stack("owner", store.StackUndo), []domain.OpID{op2, op3}) {
		t.Fatal("no-change pushed undo")
	}
	for _, id := range []domain.OpID{op1, op2, op3} {
		if op := s.Op(id); !op.Revertible {
			t.Fatalf("op is not revertible: %+v", op)
		}
	}
}

func TestAddTaskEventImages(t *testing.T) {
	s := testutil.NewScenario(t)
	s.Add(strings.Repeat("é", 500), testutil.Due("10-29 09:30"))
	evs := s.Events(s.Last.OpID)
	var task, reason map[string]any
	if err := json.Unmarshal(evs[0].After, &task); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(evs[1].After, &reason); err != nil {
		t.Fatal(err)
	}
	if task["due_date"] != "2026-10-29" || task["due_min"] != float64(570) || task["rank"] != float64(65536) ||
		reason["reason"] != "new" || reason["since"] != float64(s.Clock.Now().UnixMilli()) || reason["seq"] != float64(s.Op(s.Last.OpID).Seq) {
		t.Fatalf("event columns: task=%v, reason=%v", task, reason)
	}
}
