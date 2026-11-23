package service_test

import (
	"database/sql"
	"fmt"
	"reflect"
	"testing"

	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/service"
	"github.com/linsomniac/taskronin/internal/store"
	"github.com/linsomniac/taskronin/internal/testutil"
)

const reportRetitle = "Add svc-cluster weencrypt name to certs"

func reportFixture(t *testing.T) (*testutil.Scenario, []domain.EntryID) {
	t.Helper()
	s := testutil.NewScenario(t).At("10-23 09:00")
	// Allocate the catalog's exact task IDs through real capture operations.
	for id := 1; id <= 57; id++ {
		in := service.AddTaskIn{Title: fmt.Sprintf("Fixture task %d", id)}
		switch id {
		case 7:
			in.Title, in.Project = "Write report", "docs"
		case 42:
			in.Title, in.Project, in.Tags = "Set up keycloak postgres cluster", "infra.keycloak", []string{"infra"}
		case 57:
			in.Title, in.Tags = "Run ansible against dev qt cluster", []string{"infra"}
		}
		r := s.Must(s.Svc.AddTask(s.Ctx(), s.Meta(), in))
		if r.Task.ID != domain.TaskID(id) {
			t.Fatalf("task ID = %d, want %d", r.Task.ID, id)
		}
	}
	ids := []domain.EntryID{s.Log(42, "Added Ansible role for postgres cluster", "10-23")}
	s.At("10-23 15:00")
	ids = append(ids, s.Log(42, "Created pgk cluster in dev", "10-23"), s.LogAt(0, "Answered Bob's DNS question", "10-23", "11:00"))
	s.At("10-26 10:00")
	ids = append(ids, s.Log(7, "Drafted outline", "10-26"), s.Log(42, "Failover tested in dev", "10-26"))
	s.Done(42)
	ids = append(ids, s.Last.Entry.ID)
	title := reportRetitle
	s.Done(57, func(in *service.CompleteIn) { in.As = &title })
	ids = append(ids, s.Last.Entry.ID)
	s.At("10-27 10:00").Reopen(42, false)
	return s, ids
}

func readReport(t *testing.T, s *testutil.Scenario, in service.ReportIn) service.Report {
	t.Helper()
	r, err := s.Svc.Report(s.Ctx(), s.Actor, in)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func catalogReportIn() service.ReportIn {
	return service.ReportIn{From: testutil.D("10-23"), To: testutil.D("10-26")}
}

func reportLine(id domain.EntryID, kind domain.EntryKind, title string, task domain.TaskID, minute sql.Null[civil.Minute], reopened bool) service.ReportLine {
	return service.ReportLine{EntryID: id, Kind: kind, Title: title,
		TaskID: sql.Null[domain.TaskID]{V: task, Valid: task != 0}, Time: minute, Reopened: reopened}
}

func TestRPT01(t *testing.T) {
	s, ids := reportFixture(t)
	r := readReport(t, s, catalogReportIn())
	want := []service.ReportDay{
		{Date: testutil.D("10-23"), Lines: []service.ReportLine{
			reportLine(ids[2], domain.EntryWork, "Answered Bob's DNS question", 0, sql.Null[civil.Minute]{V: 660, Valid: true}, false),
			reportLine(ids[0], domain.EntryWork, "Added Ansible role for postgres cluster", 42, sql.Null[civil.Minute]{}, false),
			reportLine(ids[1], domain.EntryWork, "Created pgk cluster in dev", 42, sql.Null[civil.Minute]{}, false),
		}},
		{Date: testutil.D("10-26"), Lines: []service.ReportLine{
			reportLine(ids[3], domain.EntryWork, "Drafted outline", 7, sql.Null[civil.Minute]{}, false),
			reportLine(ids[4], domain.EntryWork, "Failover tested in dev", 42, sql.Null[civil.Minute]{}, false),
			reportLine(ids[5], domain.EntryComplete, "Set up keycloak postgres cluster", 42, sql.Null[civil.Minute]{}, true),
			reportLine(ids[6], domain.EntryComplete, reportRetitle, 57, sql.Null[civil.Minute]{}, false),
		}},
	}
	if !reflect.DeepEqual(r.Days, want) {
		t.Fatalf("days = %+v, want %+v", r.Days, want)
	}
}

func TestRPT02(t *testing.T) {
	s, ids := reportFixture(t)
	r := readReport(t, s, catalogReportIn())
	for _, day := range r.Days {
		for _, line := range day.Lines {
			complete := line.EntryID == ids[5] || line.EntryID == ids[6]
			if (line.Kind == domain.EntryComplete) != complete || line.Reopened != (line.EntryID == ids[5]) {
				t.Fatalf("completion/reopened markers = %+v", line)
			}
		}
	}
}

func TestRPT05(t *testing.T) {
	s := testutil.NewScenario(t)
	first := s.Add("backlog first", testutil.Bottom())
	second := s.Add("backlog second", testutil.Bottom())
	newTask := s.Add("new")
	manual := s.Add("manual", testutil.Bottom())
	s.MarkManual(manual)
	deferred := s.Add("later", testutil.DeferTo("10-30"), testutil.Due("10-30"))
	earlier := s.Add("earlier", testutil.DeferTo("10-29"))
	soon := s.Add("due soon", testutil.Bottom(), testutil.Due("10-31"))
	s.Add("distant", testutil.Bottom(), testutil.Due("11-01"))
	for _, flags := range []struct{ todo, upcoming bool }{{true, false}, {false, true}, {true, true}} {
		in := catalogReportIn()
		in.Todo, in.Upcoming = flags.todo, flags.upcoming
		r := readReport(t, s, in)
		if (r.Todo != nil) != flags.todo || (r.Upcoming != nil) != flags.upcoming {
			t.Fatalf("optional sections = %+v", r)
		}
		if flags.todo {
			requireViewTasks(t, r.Todo.Triage, manual, newTask)
			for _, view := range []domain.View{domain.ViewTriage, domain.ViewBacklog} {
				page, err := s.Svc.ListTasks(s.Ctx(), s.Actor, service.ListTasksIn{View: view})
				if err != nil {
					t.Fatal(err)
				}
				got := r.Todo.Triage
				if view == domain.ViewBacklog {
					got = r.Todo.Backlog
					if got[0].ID != first || got[1].ID != second {
						t.Fatalf("backlog rank order = %+v", got)
					}
				}
				if !reflect.DeepEqual(got, page.Items) {
					t.Fatalf("report %s differs from ListTasks", view)
				}
			}
		}
		if flags.upcoming {
			requireViewTasks(t, r.Upcoming, earlier, deferred, soon)
			upcoming, err := s.Svc.Upcoming(s.Ctx(), s.Actor)
			if err != nil || !reflect.DeepEqual(r.Upcoming, append(upcoming.Deferred, upcoming.DueSoon...)) {
				t.Fatalf("report Upcoming differs: %+v, %v", upcoming, err)
			}
		}
	}
}

func TestRPT06(t *testing.T) {
	s, ids := reportFixture(t)
	for _, tt := range []struct {
		tag, project string
		want         []domain.EntryID
	}{
		{"infra", "", []domain.EntryID{ids[0], ids[1], ids[4], ids[5], ids[6]}},
		{"Infra", "", []domain.EntryID{ids[0], ids[1], ids[4], ids[5], ids[6]}},
		{"", "docs", []domain.EntryID{ids[3]}},
		{"", "infra", []domain.EntryID{ids[0], ids[1], ids[4], ids[5]}},
		{"", "inf", []domain.EntryID{}},
		{"", "infra.key", []domain.EntryID{}},
		{"infra", "docs", []domain.EntryID{}},
	} {
		in := catalogReportIn()
		in.Tag, in.Project = tt.tag, tt.project
		r := readReport(t, s, in)
		got := make([]domain.EntryID, 0)
		for _, day := range r.Days {
			for _, line := range day.Lines {
				got = append(got, line.EntryID)
			}
		}
		if !reflect.DeepEqual(got, tt.want) || r.Tag != tt.tag || r.Project != tt.project {
			t.Fatalf("filters %q/%q = %v, want %v; report = %+v", tt.tag, tt.project, got, tt.want, r)
		}
	}
}

func TestRPT08(t *testing.T) {
	s := testutil.NewScenario(t).At("10-23 09:00")
	task := s.Add("Run ansible against dev qt cluster")
	id := s.Log(task, "Earlier work", "10-23")
	s.At("10-26 10:00")
	title := reportRetitle
	s.Done(task, func(in *service.CompleteIn) { in.As = &title })
	// Earlier work retains its own title; the task annotation uses the current title.
	r := readReport(t, s, catalogReportIn())
	if r.Tasks[task].Title != reportRetitle {
		t.Fatalf("current task title = %+v", r.Tasks[task])
	}
	line := r.Days[0].Lines[0]
	if line.EntryID != id || line.Title != "Earlier work" || line.TaskID.V != task {
		t.Fatalf("earlier entry = %+v", line)
	}
}

func TestRPT09(t *testing.T) {
	s, _ := reportFixture(t)
	in := catalogReportIn()
	r := readReport(t, s, in)
	want := map[domain.TaskID]service.ReportTask{
		7:  {Title: "Write report", Status: domain.StatusOpen},
		42: {Title: "Set up keycloak postgres cluster", Status: domain.StatusOpen},
		57: {Title: reportRetitle, Status: domain.StatusDone},
	}
	if r.From != in.From || r.To != in.To || r.Tag != "" || r.Project != "" || r.Todo != nil || r.Upcoming != nil ||
		len(r.Days) != 2 || r.Days[0].Date != in.From || r.Days[1].Date != in.To || !reflect.DeepEqual(r.Tasks, want) {
		t.Fatalf("report shape = %+v", r)
	}
}

func TestRPT10(t *testing.T) {
	s, _ := reportFixture(t)
	r := readReport(t, s, service.ReportIn{From: testutil.D("10-24"), To: testutil.D("10-25")})
	if r.Days == nil || len(r.Days) != 0 || r.Tasks == nil || len(r.Tasks) != 0 {
		t.Fatalf("empty report = %+v", r)
	}
}

func TestReportBounds(t *testing.T) {
	s := testutil.NewScenario(t)
	from := testutil.D("10-28")
	for _, tt := range []struct {
		name string
		in   service.ReportIn
		bad  bool
	}{
		{"zero", service.ReportIn{}, true},
		{"missing from", service.ReportIn{To: from}, true},
		{"missing to", service.ReportIn{From: from}, true},
		{"reversed", service.ReportIn{From: from, To: from.AddDays(-1)}, true},
		{"367 days", service.ReportIn{From: from, To: from.AddDays(367)}, true},
		{"366 days", service.ReportIn{From: from, To: from.AddDays(366)}, false},
		{"one day", service.ReportIn{From: from, To: from}, false},
		{"invalid date", service.ReportIn{From: testutil.D("1000-01-01").AddDays(-1), To: from}, true},
		{"bad tag", service.ReportIn{From: from, To: from, Tag: "a b"}, true},
		{"bad project", service.ReportIn{From: from, To: from, Project: "a b"}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.Svc.Report(s.Ctx(), s.Actor, tt.in)
			if tt.bad {
				testutil.RequireCode(t, err, domain.CodeValidation)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReportReconcileAndScope(t *testing.T) {
	for _, flags := range []struct{ todo, upcoming bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		s := testutil.NewScenario(t)
		id := s.Add("surface", testutil.DeferTo("10-29"))
		before, version := s.OpCount(), s.Task(id).Version
		s.At("10-29 00:00")
		in := service.ReportIn{From: testutil.D("10-28"), To: testutil.D("10-29"), Todo: flags.todo, Upcoming: flags.upcoming}
		_, err := s.Svc.Report(s.Ctx(), testutil.TokenActor("capture", domain.ScopeCapture), in)
		testutil.RequireCode(t, err, domain.CodeForbidden)
		if s.OpCount() != before {
			t.Fatal("forbidden read reconciled")
		}
		r, err := s.Svc.Report(s.Ctx(), testutil.TokenActor("reader", domain.ScopeRead), in)
		if err != nil {
			t.Fatal(err)
		}
		if flags.todo || flags.upcoming {
			if s.OpCount() != before+1 || s.Task(id).Version != version+1 || latestOp(t, s).Actor != "system" {
				t.Fatal("report did not reconcile once")
			}
			if flags.todo {
				requireViewTasks(t, r.Todo.Triage, id)
				if len(r.Todo.Triage[0].Reasons) != 1 || r.Todo.Triage[0].Reasons[0].Reason != domain.ReasonSurfaced {
					t.Fatal("missing surfaced reason")
				}
			}
			if flags.upcoming && (r.Upcoming == nil || len(r.Upcoming) != 0) {
				t.Fatal("surfaced task remained upcoming")
			}
		} else if s.OpCount() != before || s.Task(id).Version != version {
			t.Fatal("historical report reconciled")
		}
	}
}

func TestReportSectionFilters(t *testing.T) {
	s := testutil.NewScenario(t)
	match := func(in *service.AddTaskIn) { in.Tags, in.Project = []string{"Infra"}, "infra.keycloak" }
	triage := s.Add("triage", match)
	backlog := s.Add("backlog", match, testutil.Bottom())
	deferred := s.Add("deferred", match, testutil.DeferTo("10-30"), testutil.Due("10-30"))
	soon := s.Add("soon", match, testutil.Bottom(), testutil.Due("10-31"))
	s.Add("other triage")
	s.Add("other backlog", testutil.Bottom())
	s.Add("other deferred", testutil.DeferTo("10-29"))
	s.Add("other soon", testutil.Bottom(), testutil.Due("10-29"))
	for _, filters := range [][2]string{{"infra", ""}, {"", "infra"}, {"infra", "infra.keycloak"}} {
		in := catalogReportIn()
		in.Todo, in.Upcoming, in.Tag, in.Project = true, true, filters[0], filters[1]
		r := readReport(t, s, in)
		requireViewTasks(t, r.Todo.Triage, triage)
		requireViewTasks(t, r.Todo.Backlog, backlog, soon)
		requireViewTasks(t, r.Upcoming, deferred, soon)
		if len(r.Tasks) != 0 {
			t.Fatal("tasks map includes tasks without report lines")
		}
	}
}

func TestReportEntryOrderAndNotes(t *testing.T) {
	s := testutil.NewScenario(t).At("10-28 16:00")
	untimed := s.Log(0, "untimed", "")
	late := s.LogAt(0, "late", "", "15:00")
	early := s.LogAt(0, "early", "", "09:00")
	tie := s.LogAt(0, "same time", "", "09:00")
	s.At("10-28 17:00")
	laterCreation := s.LogAt(0, "later creation", "", "09:00")
	notes := "details\nsecond line"
	s.EditEntry(early, func(in *service.UpdateEntryIn) { in.Notes = &notes })
	s.DeleteEntry(s.Log(0, "deleted", ""))
	r := readReport(t, s, service.ReportIn{From: testutil.D("10-28"), To: testutil.D("10-28")})
	want := []domain.EntryID{early, tie, laterCreation, late, untimed}
	if len(r.Days) != 1 || len(r.Days[0].Lines) != len(want) {
		t.Fatalf("days = %+v", r.Days)
	}
	for i, id := range want {
		if r.Days[0].Lines[i].EntryID != id {
			t.Fatalf("line order = %+v", r.Days[0].Lines)
		}
	}
	if r.Days[0].Lines[0].Notes != notes {
		t.Fatal("report lost notes")
	}
}

func TestLIF02_Report(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("Run ansible against dev qt cluster")
	title := reportRetitle
	s.Done(id, func(in *service.CompleteIn) { in.As = &title })
	r := readReport(t, s, service.ReportIn{From: testutil.D("10-28"), To: testutil.D("10-28")})
	want := reportLine(s.Last.Entry.ID, domain.EntryComplete, title, id, sql.Null[civil.Minute]{}, false)
	if len(r.Days) != 1 || !reflect.DeepEqual(r.Days[0].Lines, []service.ReportLine{want}) || r.Tasks[id].Title != title {
		t.Fatalf("retitled completion report = %+v", r)
	}
}

func TestLIF09_Report(t *testing.T) {
	s, id, _, _ := dropDeleteDay(t, true)
	s.Delete(id)
	r := readReport(t, s, service.ReportIn{From: testutil.D("10-27"), To: testutil.D("10-28")})
	if len(r.Days) != 1 || len(r.Days[0].Lines) != 1 || r.Days[0].Lines[0].Title != "yesterday" ||
		r.Days[0].Lines[0].TaskID.V != id || r.Tasks[id] != (service.ReportTask{Title: "A", Status: domain.StatusOpen, Deleted: true}) {
		t.Fatalf("deleted task report = %+v", r)
	}
}

func TestLIF17_Report(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("Set up keycloak")
	s.Done(id)
	first := s.Last.Entry.ID
	s.Reopen(id, false)
	s.At("10-29 10:00").Done(id)
	want := []service.ReportDay{
		{Date: testutil.D("10-28"), Lines: []service.ReportLine{reportLine(first, domain.EntryComplete, "Set up keycloak", id, sql.Null[civil.Minute]{}, true)}},
		{Date: testutil.D("10-29"), Lines: []service.ReportLine{reportLine(s.Last.Entry.ID, domain.EntryComplete, "Set up keycloak", id, sql.Null[civil.Minute]{}, false)}},
	}
	r := readReport(t, s, service.ReportIn{From: testutil.D("10-28"), To: testutil.D("10-29")})
	if !reflect.DeepEqual(r.Days, want) || r.Tasks[id].Status != domain.StatusDone {
		t.Fatalf("recompleted report = %+v", r)
	}
}

func TestReportPurgedTask(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("private task")
	work := s.Log(id, "Retained work", "")
	s.Done(id)
	complete := s.Last.Entry.ID
	s.Delete(id)
	// AIDEV-NOTE: F14 fixture shortcut: the purge service arrives in Task 37.
	// Scrub the task and its completion using the store's real purge primitive.
	s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), nil, func(q store.Q) error {
		_, err := store.ScrubEntities(s.Ctx(), q, []domain.TaskID{id}, []domain.EntryID{complete}, s.Clock.Now())
		return err
	}))
	r := readReport(t, s, service.ReportIn{From: testutil.D("10-28"), To: testutil.D("10-28")})
	if len(r.Days) != 1 || len(r.Days[0].Lines) != 1 || r.Days[0].Lines[0].EntryID != work ||
		r.Days[0].Lines[0].Title != "Retained work" || r.Tasks[id] != (service.ReportTask{Title: "[purged]", Status: domain.StatusDone, Deleted: true}) {
		t.Fatalf("purged task report = %+v", r)
	}
}
