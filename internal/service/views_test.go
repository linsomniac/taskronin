package service_test

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"taskronin/internal/backup"
	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/store"
	"taskronin/internal/testutil"
)

func viewTaskIDs(vs []domain.TaskView) []domain.TaskID {
	ids := make([]domain.TaskID, len(vs))
	for i, v := range vs {
		ids[i] = v.ID
	}
	return ids
}

func requireViewTasks(t *testing.T, vs []domain.TaskView, ids ...domain.TaskID) {
	t.Helper()
	if got := viewTaskIDs(vs); !slices.Equal(got, ids) {
		t.Fatalf("task IDs = %v, want %v", got, ids)
	}
}

func readToday(t *testing.T, s *testutil.Scenario) service.TodayView {
	t.Helper()
	v, err := s.Svc.Today(s.Ctx(), s.Actor)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTRI20(t *testing.T) {
	s := testutil.NewScenario(t)
	for i := 1; i <= 10; i++ {
		s.Add(fmt.Sprint(i), testutil.Bottom())
	}
	// Fixture shortcut authorized by TRI-20/F5: select reason sequences independently
	// of task creation, and reverse display timestamps to prove they do not sort.
	reasons := []domain.TriageReason{domain.ReasonNew, domain.ReasonManual, domain.ReasonReopened,
		domain.ReasonSurfaced, domain.ReasonContinue, domain.ReasonDue, domain.ReasonOverdue,
		domain.ReasonSlipped, domain.ReasonSlipped, domain.ReasonOverdue}
	s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), nil, func(q store.Q) error {
		for i, reason := range reasons {
			seq := i + 1
			if i == 8 {
				seq = 1
			}
			if _, err := q.ExecContext(s.Ctx(), `INSERT INTO task_triage(task_id, reason, since, seq) VALUES (?, ?, ?, ?)`,
				i+1, reason, s.Clock.Now().Add(-time.Duration(i)*time.Hour).UnixMilli(), seq); err != nil {
				return err
			}
		}
		_, err := q.ExecContext(s.Ctx(), `INSERT INTO task_triage(task_id, reason, since, seq) VALUES (?, ?, ?, ?)`, 10, domain.ReasonNew, s.Clock.Now().UnixMilli(), 1)
		return err
	}))
	p, err := s.Svc.ListTasks(s.Ctx(), s.Actor, service.ListTasksIn{View: domain.ViewTriage})
	if err != nil {
		t.Fatal(err)
	}
	requireViewTasks(t, p.Items, 9, 8, 7, 10, 6, 5, 4, 3, 2, 1)
	if len(p.Items[3].Reasons) != 2 || p.Items[3].Reasons[0].Reason != domain.ReasonOverdue {
		t.Fatal("multi-reason task lost its highest reason")
	}
	today := readToday(t, s)
	if today.TriageCount != 10 {
		t.Fatalf("triage count = %d", today.TriageCount)
	}
	requireViewTasks(t, today.TriageTop, 9, 8, 7, 10, 6)
}

func TestDUE18(t *testing.T) {
	s := testutil.NewScenario(t)
	first := s.Add("near", testutil.Due("10-31"))
	s.Add("later", testutil.Due("11-01"))
	s.Add("no due")
	v, err := s.Svc.Upcoming(s.Ctx(), s.Actor)
	if err != nil {
		t.Fatal(err)
	}
	requireViewTasks(t, v.DueSoon, first)
	if len(v.Deferred) != 0 {
		t.Fatal(v)
	}
	s.SetDue(first, "")
	v, err = s.Svc.Upcoming(s.Ctx(), s.Actor)
	if err != nil || len(v.DueSoon)+len(v.Deferred) != 0 {
		t.Fatalf("empty upcoming = %+v, %v", v, err)
	}
}

func TestDUE19(t *testing.T) {
	s := testutil.NewScenario(t)
	first := s.Add("last", testutil.DeferTo("10-30"))
	second := s.Add("timed", testutil.DeferTo("10-29 09:00"))
	third := s.Add("date only", testutil.DeferTo("10-29"))
	v, err := s.Svc.Upcoming(s.Ctx(), s.Actor)
	if err != nil {
		t.Fatal(err)
	}
	requireViewTasks(t, v.Deferred, third, second, first)
}

func TestREC06(t *testing.T) {
	s := testutil.NewScenario(t)
	s.Add("ready", testutil.Bottom())
	before := s.OpCount()
	first, second := readToday(t, s), readToday(t, s)
	if !reflect.DeepEqual(first, second) || s.OpCount() != before {
		t.Fatalf("read changed state: first=%+v second=%+v ops=%d/%d", first, second, s.OpCount(), before)
	}
}

func TestREC12(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("surface", testutil.DeferTo("10-29"))
	before, version := s.OpCount(), s.Task(id).Version
	s.At("10-29 00:00")
	p, err := s.Svc.ListTasks(s.Ctx(), testutil.TokenActor("reader", domain.ScopeRead), service.ListTasksIn{View: domain.ViewTriage})
	if err != nil {
		t.Fatal(err)
	}
	requireViewTasks(t, p.Items, id)
	op := latestOp(t, s)
	if s.OpCount() != before+1 || op.Actor != "system" || op.Kind != domain.OpSystemReconcile || p.Items[0].Version != version+1 || p.Items[0].Deferred {
		t.Fatalf("reconcile op=%+v tasks=%+v", op, p.Items)
	}
	requireDueReasons(t, s, id, domain.ReasonSurfaced)
	requireEventKeys(t, s, op.ID, "task:update 1", "triage:add 1 surfaced")
}

func TestDAT14(t *testing.T) {
	s := testutil.NewScenario(t)
	actor := testutil.TokenActor("capture", domain.ScopeCapture)
	v, err := s.Svc.Clock(s.Ctx(), actor)
	if err != nil || !v.Now.Equal(time.Date(2026, 10, 28, 16, 0, 0, 0, time.UTC)) || v.Today != testutil.D("10-28") ||
		v.Zone != "America/Denver" || v.WeekStart != time.Monday || v.DueSoonDays != 3 || v.Generation != s.Svc.Generation() {
		t.Fatalf("clock=%+v, %v", v, err)
	}
	who, err := s.Svc.WhoAmI(s.Ctx(), actor)
	if err != nil || who.Actor != actor || who.Generation != v.Generation {
		t.Fatalf("who=%+v, %v", who, err)
	}
	s.At("11-02 10:00")
	v, err = s.Svc.Clock(s.Ctx(), actor)
	_, offset := v.Now.Zone()
	if err != nil || offset != -7*60*60 {
		t.Fatalf("fall clock=%+v, %v", v, err)
	}
}

func TestINT08(t *testing.T) {
	s := testutil.NewScenario(t).At("10-28 08:00")
	from := s.Add("original", testutil.Bottom())
	block := s.Block(from, "09:00-11:00")
	s.SetFocus(from, false)
	s.At("10-28 10:20")
	to := s.Add("Restart stuck pg replica", testutil.Now())
	op := s.Last.OpID
	day, err := s.Svc.Day(s.Ctx(), s.Actor, testutil.D("10-28"))
	today := readToday(t, s)
	if err != nil || len(day.Interruptions) != 1 || !reflect.DeepEqual(day.Interruptions, today.Interruptions) {
		t.Fatalf("interruptions day=%+v today=%+v err=%v", day, today, err)
	}
	row := day.Interruptions[0]
	if row.ToTaskID != to || !row.FromTaskID.Valid || row.FromTaskID.V != from || !row.FromBlockID.Valid || row.FromBlockID.V != block ||
		row.Date != testutil.D("10-28") || !row.At.Equal(s.Clock.Now()) || row.OpID != op {
		t.Fatalf("interruption = %+v", row)
	}
	s.Undo()
	day, err = s.Svc.Day(s.Ctx(), s.Actor, testutil.D("10-28"))
	if err != nil || len(day.Interruptions) != 0 || len(readToday(t, s).Interruptions) != 0 {
		t.Fatalf("tombstone visible: %+v, %v", day, err)
	}
}

func TestTodayView(t *testing.T) {
	s := testutil.NewScenario(t).At("10-27 08:00")
	overdue := s.Add("late", testutil.Due("10-27"))
	s.At("10-28 08:00")
	focus := s.Add("focused", testutil.Bottom())
	ended := s.Block(focus, "08:30-09:00")
	current := s.Block(focus, "09:00-11:00")
	s.SetFocus(focus, false)
	due := s.Add("due", testutil.Due("10-28"))
	soon := s.Add("soon", testutil.Due("10-31"))
	s.Add("far", testutil.Due("11-01"))
	s.Add("deferred", testutil.DeferTo("10-29"), testutil.Due("10-30"))
	closed := s.Add("closed", testutil.Due("10-28"))
	s.Reconcile()
	s.Drop(closed)
	ai := s.As(testutil.AI()).Add("AI triage")
	s.At("10-28 10:00")
	log := s.LogAt(0, "standalone", "", "09:45")
	deleted := s.Log(0, "deleted entry", "")
	s.DeleteEntry(deleted)
	v := readToday(t, s)
	if v.Focus == nil || v.Focus.ID != focus || v.CurrentBlock == nil || v.CurrentBlock.ID != current || !v.CurrentBlock.Current ||
		len(v.Blocks) != 2 || v.Blocks[0].ID != ended || !v.Blocks[0].NeedsReblock || v.TriageCount != 5 || len(v.TriageTop) != 5 ||
		v.AIChanges != 1 || v.Backup.Healthy || len(v.Entries) != 1 || v.Entries[0].ID != log {
		t.Fatalf("today = %+v", v)
	}
	requireViewTasks(t, v.Overdue, overdue)
	requireViewTasks(t, v.DueSoon, due, soon)
	if !slices.Contains(viewTaskIDs(v.TriageTop), ai) {
		t.Fatal("missing AI task")
	}
	upcoming, err := s.Svc.Upcoming(s.Ctx(), s.Actor)
	if err != nil {
		t.Fatal(err)
	}
	requireViewTasks(t, upcoming.DueSoon, due, soon)
}

func TestStatusView(t *testing.T) {
	s := testutil.NewScenario(t).At("10-27 08:00")
	s.Add("overdue", testutil.Due("10-27"))
	s.At("10-28 08:00")
	id := s.Add("focus", testutil.Bottom())
	b := s.Block(id, "09:00-11:00")
	s.SetFocus(id, false)
	s.Add("due", testutil.Due("10-28"))
	s.Add("deferred today", testutil.DeferTo("10-28 12:00"), testutil.Due("10-28"))
	s.Add("soon", testutil.Due("10-29"))
	dropped := s.Add("dropped", testutil.Due("10-28"))
	s.Reconcile()
	s.Drop(dropped)
	deleted := s.Add("deleted", testutil.Due("10-28"))
	s.Reconcile()
	s.Delete(deleted)
	done := s.Add("done", testutil.Due("10-28"))
	s.Reconcile()
	s.Done(done)
	s.As(testutil.AI()).Add("AI")
	s.At("10-28 10:00")
	v, err := s.Svc.Status(s.Ctx(), s.Actor)
	if err != nil || v.Focus == nil || *v.Focus != (service.StatusTask{ID: id, Title: "focus"}) ||
		v.CurrentBlock == nil || *v.CurrentBlock != (service.StatusBlock{ID: b, TaskID: id, Title: "focus", End: 660}) ||
		v.Triage != 4 || v.Due != 3 || v.AI != 1 || !v.BackupWarn || v.PurgeWarning {
		t.Fatalf("status=%+v, %v", v, err)
	}
	s.SetFocus(0, false)
	s.At("10-28 11:00")
	v, err = s.Svc.Status(s.Ctx(), s.Actor)
	if err != nil || v.Focus != nil || v.CurrentBlock != nil {
		t.Fatalf("ended status=%+v, %v", v, err)
	}
}

func setViewSettings(t *testing.T, s *testutil.Scenario, settings map[string]string) {
	t.Helper()
	// Fixture shortcut: settings administration/backup/review ops arrive in Tasks 33, 35 and 36.
	s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), nil, func(q store.Q) error {
		for k, v := range settings {
			if err := store.SetSetting(s.Ctx(), q, k, v); err != nil {
				return err
			}
		}
		return nil
	}))
}

func bannerKinds(v service.TodayView) []string {
	kinds := make([]string, len(v.Banners))
	for i, b := range v.Banners {
		kinds[i] = b.Kind
	}
	return kinds
}

func TestBanners(t *testing.T) {
	for _, tc := range []struct {
		name, lastError string
		age             time.Duration
		never           bool
		want            string
	}{
		{name: "never", never: true, want: "backup_stale"},
		{name: "healthy", age: time.Hour},
		{name: "exact48h", age: 48 * time.Hour},
		{name: "stale", age: 48*time.Hour + time.Millisecond, want: "backup_stale"},
		{name: "failed", age: time.Hour, lastError: "disk full", want: "backup_failed"},
		{name: "old_failed", age: 72 * time.Hour, lastError: "disk full", want: "backup_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testutil.NewScenario(t)
			value := ""
			if !tc.never {
				value = strconv.FormatInt(s.Clock.Now().Add(-tc.age).UnixMilli(), 10)
			}
			setViewSettings(t, s, map[string]string{"last_backup_at": value, "last_backup_error": tc.lastError})
			v := readToday(t, s)
			if v.Backup.Healthy != (tc.want == "") || v.Backup.LastSuccessAt.Valid == tc.never || v.Backup.LastError != tc.lastError {
				t.Fatalf("health=%+v", v.Backup)
			}
			want := []string{}
			if tc.want != "" {
				want = append(want, tc.want)
			}
			if !slices.Equal(bannerKinds(v), want) {
				t.Fatalf("banners=%+v", v.Banners)
			}
			for _, b := range v.Banners {
				if b.Message == "" {
					t.Fatal("empty banner message")
				}
			}
			status, err := s.Svc.Status(s.Ctx(), s.Actor)
			if err != nil || status.BackupWarn != !v.Backup.Healthy {
				t.Fatalf("status=%+v, %v", status, err)
			}
		})
	}
	t.Run("AI sequence acknowledgement", func(t *testing.T) {
		s := testutil.NewScenario(t)
		s.As(testutil.AI()).Add("first")
		through := latestOp(t, s).Seq
		s.At("10-28 09:00")
		s.As(testutil.AI()).Add("second")
		setViewSettings(t, s, map[string]string{"ai_reviewed_through": strconv.FormatInt(int64(through), 10)})
		v := readToday(t, s)
		if v.AIChanges != 1 || !slices.Contains(bannerKinds(v), "ai_changes") {
			t.Fatalf("AI=%+v", v)
		}
		setViewSettings(t, s, map[string]string{"ai_reviewed_through": strconv.FormatInt(int64(latestOp(t, s).Seq), 10)})
		if v = readToday(t, s); v.AIChanges != 0 || slices.Contains(bannerKinds(v), "ai_changes") {
			t.Fatal(v)
		}
	})
	for _, damaged := range []bool{false, true} {
		t.Run(fmt.Sprintf("purge damaged=%v", damaged), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ledger")
			if damaged {
				if err := os.WriteFile(path, []byte("broken complete line\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ledger, _, err := backup.OpenLedger(path)
			if err != nil {
				t.Fatal(err)
			}
			if !damaged {
				if err := ledger.Append(backup.LedgerRecord{V: 1, Rec: domain.NewOpID().String(), State: backup.LedgerPending, Tasks: []domain.TaskID{1}}); err != nil {
					t.Fatal(err)
				}
			}
			s := testutil.NewScenario(t, func(o *service.Options) { o.Ledger = ledger })
			kind := "purge_incomplete"
			if damaged {
				kind = "purge_ledger_damaged"
			}
			v := readToday(t, s)
			status, err := s.Svc.Status(s.Ctx(), s.Actor)
			if !slices.Contains(bannerKinds(v), kind) || err != nil || !status.PurgeWarning {
				t.Fatalf("banners=%+v status=%+v err=%v", v.Banners, status, err)
			}
		})
	}
}

func TestDayView(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("task", testutil.Bottom())
	b := s.Block(id, "11:00-12:00")
	linked := s.LogAt(id, "work", "", "09:00")
	unlinked := s.Log(0, "standalone", "")
	s.Log(0, "yesterday", "10-27")
	v, err := s.Svc.Day(s.Ctx(), s.Actor, testutil.D("10-28"))
	if err != nil || v.Date != testutil.D("10-28") || len(v.Blocks) != 1 || v.Blocks[0].ID != b || len(v.Entries) != 2 || v.Entries[0].ID != linked || v.Entries[1].ID != unlinked || len(v.Unlinked) != 1 || v.Unlinked[0].ID != unlinked {
		t.Fatalf("day=%+v, %v", v, err)
	}
	_, err = s.Svc.Day(s.Ctx(), s.Actor, civil.Date{})
	testutil.RequireCode(t, err, domain.CodeValidation)
}

func TestLIF09_Views(t *testing.T) {
	s, id, _, _ := dropDeleteDay(t, true)
	s.Delete(id)
	for _, view := range []domain.View{domain.ViewTriage, domain.ViewBacklog, domain.ViewUpcoming, domain.ViewAll} {
		p, err := s.Svc.ListTasks(s.Ctx(), s.Actor, service.ListTasksIn{View: view})
		if err != nil || slices.Contains(viewTaskIDs(p.Items), id) {
			t.Fatalf("%s=%+v, %v", view, p, err)
		}
	}
	v := readToday(t, s)
	if v.Focus != nil || v.CurrentBlock != nil || len(v.Blocks) != 0 || len(v.TriageTop)+len(v.Overdue)+len(v.DueSoon) != 0 {
		t.Fatalf("deleted task in today: %+v", v)
	}
	u, err := s.Svc.Upcoming(s.Ctx(), s.Actor)
	if err != nil || len(u.Deferred)+len(u.DueSoon) != 0 {
		t.Fatalf("upcoming=%+v, %v", u, err)
	}
}

func TestListTasksFilters(t *testing.T) {
	s := testutil.NewScenario(t)
	add := func(title, project, tag string) domain.TaskID {
		return s.Add(title, testutil.Bottom(), func(in *service.AddTaskIn) { in.Project = project; in.Tags = []string{tag} })
	}
	root := add("root", "infra", "Infra")
	child := add("child", "infra.keycloak", "infra")
	add("boundary", "infrastructure", "Infra")
	add("tag boundary", "infra.db", "Infrastructure")
	for _, tc := range []struct {
		tag, project string
		want         []domain.TaskID
	}{
		{"INFRA", "infra", []domain.TaskID{root, child}},
		{"Infra", "infra.keycloak", []domain.TaskID{child}},
		{"absent", "infra", nil},
	} {
		p, err := s.Svc.ListTasks(s.Ctx(), s.Actor, service.ListTasksIn{View: domain.ViewBacklog, Tag: tc.tag, Project: tc.project})
		if err != nil {
			t.Fatal(err)
		}
		requireViewTasks(t, p.Items, tc.want...)
	}
	for _, in := range []service.ListTasksIn{{View: domain.ViewAll, Tag: "a b"}, {View: domain.ViewAll, Project: "a b"}, {View: "bad"}} {
		_, err := s.Svc.ListTasks(s.Ctx(), s.Actor, in)
		testutil.RequireCode(t, err, domain.CodeValidation)
	}
}

func collectTaskPages(t *testing.T, s *testutil.Scenario, view domain.View, limit int) []domain.TaskView {
	t.Helper()
	var result []domain.TaskView
	cursor := ""
	for range 30 {
		p, err := s.Svc.ListTasks(s.Ctx(), s.Actor, service.ListTasksIn{View: view, Limit: limit, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Items) > limit {
			t.Fatal("oversized page")
		}
		result = append(result, p.Items...)
		if p.NextCursor == "" {
			return result
		}
		if len(p.Items) != limit || p.NextCursor == cursor {
			t.Fatalf("invalid continuation=%+v", p)
		}
		cursor = p.NextCursor
	}
	t.Fatal("paging did not terminate")
	return nil
}

func TestTriagePaging(t *testing.T) {
	s := testutil.NewScenario(t)
	one := s.Add("new")
	two := s.Add("manual1")
	s.MarkManual(two)
	three := s.Add("manual2")
	s.MarkManual(three)
	four := s.Add("reopened1")
	s.Done(four)
	s.Reopen(four, false)
	five := s.Add("reopened2")
	s.Done(five)
	s.Reopen(five, false)
	requireViewTasks(t, collectTaskPages(t, s, domain.ViewTriage, 2), four, five, two, three, one)
}

func TestBacklogPagingNegativeRanks(t *testing.T) {
	s := testutil.NewScenario(t)
	old := s.Add("old", testutil.Bottom())
	older := s.Add("older", testutil.Bottom())
	a, b, c := s.Add("a", testutil.Top()), s.Add("b", testutil.Top()), s.Add("c", testutil.Top())
	if s.Task(a).Rank != 0 || s.Task(b).Rank != -65536 || s.Task(c).Rank != -131072 {
		t.Fatal("unexpected top ranks")
	}
	requireViewTasks(t, collectTaskPages(t, s, domain.ViewBacklog, 1), c, b, a, old, older)
}

func TestListTasksOtherPaging(t *testing.T) {
	for _, view := range []domain.View{domain.ViewUpcoming, domain.ViewDone, domain.ViewDropped, domain.ViewDeleted, domain.ViewAll} {
		t.Run(string(view), func(t *testing.T) {
			s := testutil.NewScenario(t)
			for i := 1; i <= 3; i++ {
				id := s.Add(fmt.Sprint(i), testutil.Bottom())
				switch view {
				case domain.ViewUpcoming:
					s.Defer(id, "10-29")
				case domain.ViewDone:
					s.Done(id)
				case domain.ViewDropped:
					s.Drop(id)
				case domain.ViewDeleted:
					s.Delete(id)
				default:
				}
			}
			want := []domain.TaskID{1, 2, 3}
			if view == domain.ViewDone || view == domain.ViewDropped || view == domain.ViewDeleted {
				slices.Reverse(want)
			}
			requireViewTasks(t, collectTaskPages(t, s, view, 1), want...)
		})
	}
}

func TestLastPageHasNoCursor(t *testing.T) {
	s := testutil.NewScenario(t)
	s.Add("a", testutil.Bottom())
	s.Add("b", testutil.Bottom())
	s.Log(0, "a", "")
	s.Log(0, "b", "")
	tasks, err := s.Svc.ListTasks(s.Ctx(), s.Actor, service.ListTasksIn{View: domain.ViewBacklog, Limit: 2})
	if err != nil || len(tasks.Items) != 2 || tasks.NextCursor != "" {
		t.Fatalf("tasks=%+v, %v", tasks, err)
	}
	entries, err := s.Svc.ListEntries(s.Ctx(), s.Actor, service.ListEntriesIn{From: testutil.D("10-28"), To: testutil.D("10-28"), Limit: 2})
	if err != nil || len(entries.Items) != 2 || entries.NextCursor != "" {
		t.Fatalf("entries=%+v, %v", entries, err)
	}
}

func TestListEntriesPaging(t *testing.T) {
	s := testutil.NewScenario(t)
	task := s.Add("task")
	old := s.Log(task, "old", "10-27")
	untimed := s.Log(0, "untimed", "")
	midnight := s.LogAt(task, "midnight", "", "00:00")
	first := s.LogAt(task, "first", "", "09:00")
	second := s.LogAt(task, "second", "", "09:00")
	deleted := s.LogAt(task, "deleted", "", "10:00")
	s.DeleteEntry(deleted)
	in := service.ListEntriesIn{From: testutil.D("10-27"), To: testutil.D("10-28"), Limit: 2}
	var ids []domain.EntryID
	for range 4 {
		p, err := s.Svc.ListEntries(s.Ctx(), s.Actor, in)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range p.Items {
			ids = append(ids, v.ID)
			if v.TaskID.Valid && v.TaskTitle != "task" {
				t.Fatal(v)
			}
		}
		in.Cursor = p.NextCursor
		if in.Cursor == "" {
			break
		}
	}
	if !slices.Equal(ids, []domain.EntryID{second, first, midnight, untimed, old}) || in.Cursor != "" {
		t.Fatalf("entry order=%v", ids)
	}
	only := sql.Null[domain.TaskID]{}
	p, err := s.Svc.ListEntries(s.Ctx(), s.Actor, service.ListEntriesIn{From: testutil.D("10-28"), To: testutil.D("10-28"), TaskID: &only})
	if err != nil || len(p.Items) != 1 || p.Items[0].ID != untimed {
		t.Fatalf("standalone=%+v, %v", p, err)
	}
	only = sql.Null[domain.TaskID]{V: task, Valid: true}
	p, err = s.Svc.ListEntries(s.Ctx(), s.Actor, service.ListEntriesIn{TaskID: &only, IncludeDeleted: true})
	if err != nil || len(p.Items) != 5 || p.Items[0].ID != deleted || !p.Items[0].DeletedAt.Valid {
		t.Fatalf("task entries=%+v, %v", p, err)
	}
}

func TestPagingInputs(t *testing.T) {
	s := testutil.NewScenario(t)
	for i := 0; i < 3; i++ {
		s.Add(fmt.Sprint(i), testutil.Bottom())
	}
	list := func(in service.ListTasksIn) (service.Page[domain.TaskView], error) {
		return s.Svc.ListTasks(s.Ctx(), s.Actor, in)
	}
	p, err := list(service.ListTasksIn{View: domain.ViewBacklog, Limit: 2})
	if err != nil || len(p.Items) != 2 || p.NextCursor == "" {
		t.Fatalf("page1=%+v, %v", p, err)
	}
	p2, err := list(service.ListTasksIn{View: domain.ViewBacklog, Limit: 2, Cursor: p.NextCursor})
	if err != nil || len(p2.Items) != 1 || p2.Items[0].ID != 3 || p2.NextCursor != "" {
		t.Fatalf("page2=%+v, %v", p2, err)
	}
	for _, limit := range []int{-1, 501} {
		_, err = list(service.ListTasksIn{View: domain.ViewBacklog, Limit: limit})
		testutil.RequireCode(t, err, domain.CodeValidation)
		_, err = s.Svc.ListEntries(s.Ctx(), s.Actor, service.ListEntriesIn{From: testutil.D("10-28"), To: testutil.D("10-28"), Limit: limit})
		testutil.RequireCode(t, err, domain.CodeValidation)
	}
	for _, limit := range []int{0, 500} {
		p, err := list(service.ListTasksIn{View: domain.ViewBacklog, Limit: limit})
		if err != nil || len(p.Items) != 3 {
			t.Fatalf("limit %d: %+v, %v", limit, p, err)
		}
	}
	_, err = list(service.ListTasksIn{View: domain.ViewTriage, Cursor: p.NextCursor})
	testutil.RequireCode(t, err, domain.CodeMalformed)
	for _, tc := range []struct {
		view domain.View
		keys []any
	}{
		{domain.ViewBacklog, []any{map[string]any{}}}, {domain.ViewBacklog, []any{1.5}},
		{domain.ViewBacklog, []any{"x"}}, {domain.ViewBacklog, []any{json.Number("9223372036854775808")}},
		{domain.ViewTriage, []any{9, 1}}, {domain.ViewTriage, []any{0, 1}}, {domain.ViewTriage, []any{1, 0}},
		{domain.ViewUpcoming, []any{"2026-02-30", 0, 1}}, {domain.ViewUpcoming, []any{"2026-10-29", 1440, 1}},
		{domain.ViewUpcoming, []any{"2026-10-29", -1, 1}}, {domain.ViewUpcoming, []any{"2026-10-29", 0, "1"}},
		{domain.ViewDone, []any{-1}}, {domain.ViewDropped, []any{-1}}, {domain.ViewDeleted, []any{-1}},
		{domain.ViewAll, []any{1}},
	} {
		_, err = list(service.ListTasksIn{View: tc.view, Cursor: domain.EncodeCursor(domain.Cursor{Keys: tc.keys, ID: 1})})
		testutil.RequireCode(t, err, domain.CodeMalformed)
	}
	for _, cursor := range []string{"garbage", domain.EncodeCursor(domain.Cursor{Offset: 0}), domain.EncodeCursor(domain.Cursor{Keys: []any{1}, ID: 0}), domain.EncodeCursor(domain.Cursor{Keys: []any{1}, ID: -1}), domain.EncodeCursor(domain.Cursor{ID: 1})} {
		_, err = list(service.ListTasksIn{View: domain.ViewBacklog, Cursor: cursor})
		testutil.RequireCode(t, err, domain.CodeMalformed)
	}
	for _, keys := range [][]any{{"2026-02-30", 0, 0, 1}, {"2026-10-28", 2, 0, 1}, {"2026-10-28", 0, 1440, 1}, {"2026-10-28", 0, 0, -1}, {"2026-10-28", 0, 0, 1.5}, {"2026-10-28", 0, 0, "1"}, {1}} {
		_, err = s.Svc.ListEntries(s.Ctx(), s.Actor, service.ListEntriesIn{From: testutil.D("10-28"), To: testutil.D("10-28"), Cursor: domain.EncodeCursor(domain.Cursor{Keys: keys, ID: 1})})
		testutil.RequireCode(t, err, domain.CodeMalformed)
	}
	from := testutil.D("10-28")
	for _, in := range []service.ListEntriesIn{{}, {From: from}, {To: from}, {From: from, To: from.AddDays(-1)}, {From: from, To: from.AddDays(367)}} {
		_, err = s.Svc.ListEntries(s.Ctx(), s.Actor, in)
		testutil.RequireCode(t, err, domain.CodeValidation)
		_, err = s.Svc.ListBlocks(s.Ctx(), s.Actor, service.ListBlocksIn{From: in.From, To: in.To})
		testutil.RequireCode(t, err, domain.CodeValidation)
	}
	_, err = s.Svc.ListEntries(s.Ctx(), s.Actor, service.ListEntriesIn{From: from, To: from.AddDays(366)})
	if err != nil {
		t.Fatal(err)
	}
	// Invalid cursors must not trigger midnight reconciliation before validation.
	s.Add("pending", testutil.DeferTo("10-29"))
	before := s.OpCount()
	s.At("10-29 00:00")
	_, err = list(service.ListTasksIn{View: domain.ViewTriage, Cursor: "garbage"})
	testutil.RequireCode(t, err, domain.CodeMalformed)
	if s.OpCount() != before {
		t.Fatal("invalid cursor caused reconciliation")
	}
}

func TestListBlocks(t *testing.T) {
	s := testutil.NewScenario(t)
	a, b := s.Add("a"), s.Add("b")
	later := s.Block(a, "10-29 11:00-12:00")
	first := s.Block(b, "10-28 11:00-12:00")
	middle := s.Block(a, "10-28 13:00-14:00")
	v, err := s.Svc.ListBlocks(s.Ctx(), s.Actor, service.ListBlocksIn{From: testutil.D("10-28"), To: testutil.D("10-29")})
	if err != nil || len(v) != 3 || v[0].ID != first || v[1].ID != middle || v[2].ID != later || v[0].TaskTitle != "b" {
		t.Fatalf("blocks=%+v, %v", v, err)
	}
	v, err = s.Svc.ListBlocks(s.Ctx(), s.Actor, service.ListBlocksIn{TaskID: sql.Null[domain.TaskID]{V: a, Valid: true}})
	if err != nil || len(v) != 2 || v[0].ID != middle || v[1].ID != later {
		t.Fatalf("task blocks=%+v, %v", v, err)
	}
}

func TestListTags(t *testing.T) {
	s := testutil.NewScenario(t)
	tags := func(in *service.AddTaskIn) { in.Tags = []string{"Infra", "zebra"} }
	a := s.Add("a", tags)
	s.Add("b", func(in *service.AddTaskIn) { in.Tags = []string{"infra", "alpha"} })
	closed := s.Add("closed", tags)
	s.Done(closed)
	deleted := s.Add("deleted", tags)
	s.Delete(deleted)
	dropped := s.Add("dropped", tags)
	s.Drop(dropped)
	s.Defer(a, "10-29")
	got, err := s.Svc.ListTags(s.Ctx(), s.Actor)
	want := []service.TagCount{{Name: "alpha", Count: 1}, {Name: "Infra", Count: 2}, {Name: "zebra", Count: 1}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("tags=%+v, %v", got, err)
	}
}

func TestSettingsView(t *testing.T) {
	s := testutil.NewScenario(t)
	v, err := s.Svc.Settings(s.Ctx(), s.Actor)
	if err != nil || v.KeepDaily != 7 || v.KeepWeekly != 4 {
		t.Fatalf("settings=%+v, %v", v, err)
	}
	setViewSettings(t, s, map[string]string{"backup_keep_daily": "10", "backup_keep_weekly": "6"})
	v, err = s.Svc.Settings(s.Ctx(), s.Actor)
	if err != nil || v.KeepDaily != 10 || v.KeepWeekly != 6 {
		t.Fatalf("settings=%+v, %v", v, err)
	}
}

func TestSyncTimezone(t *testing.T) {
	s := testutil.NewScenario(t)
	before := s.OpCount()
	previous, changed, err := s.Svc.SyncTimezone(s.Ctx(), "America/Denver")
	if err != nil || previous != "America/Denver" || changed {
		t.Fatalf("same=%s %v %v", previous, changed, err)
	}
	if slices.Contains(bannerKinds(readToday(t, s)), "tz_changed") {
		t.Fatal("unchanged zone banner")
	}
	previous, changed, err = s.Svc.SyncTimezone(s.Ctx(), "UTC")
	if err != nil || previous != "America/Denver" || !changed {
		t.Fatalf("changed=%s %v %v", previous, changed, err)
	}
	value, err := store.GetSetting(s.Ctx(), s.Store.Reader, "tz_last_seen")
	if err != nil || value != "UTC" || s.OpCount() != before {
		t.Fatalf("persisted=%q ops=%d err=%v", value, s.OpCount(), err)
	}
	previous, changed, err = s.Svc.SyncTimezone(s.Ctx(), "UTC")
	if err != nil || previous != "UTC" || changed || !slices.Contains(bannerKinds(readToday(t, s)), "tz_changed") {
		t.Fatalf("sticky=%s %v %v", previous, changed, err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 3 {
				if _, _, err := s.Svc.SyncTimezone(s.Ctx(), "UTC"); err != nil {
					t.Error(err)
				}
				if _, err := s.Svc.Today(s.Ctx(), s.Actor); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
}

func TestViewsScopes(t *testing.T) {
	s := testutil.NewScenario(t)
	a := testutil.TokenActor("capture", domain.ScopeCapture)
	d := testutil.D("10-28")
	for name, call := range map[string]func() error{
		"today":    func() error { _, e := s.Svc.Today(s.Ctx(), a); return e },
		"upcoming": func() error { _, e := s.Svc.Upcoming(s.Ctx(), a); return e },
		"status":   func() error { _, e := s.Svc.Status(s.Ctx(), a); return e },
		"day":      func() error { _, e := s.Svc.Day(s.Ctx(), a, d); return e },
		"tasks":    func() error { _, e := s.Svc.ListTasks(s.Ctx(), a, service.ListTasksIn{View: domain.ViewAll}); return e },
		"entries":  func() error { _, e := s.Svc.ListEntries(s.Ctx(), a, service.ListEntriesIn{From: d, To: d}); return e },
		"blocks":   func() error { _, e := s.Svc.ListBlocks(s.Ctx(), a, service.ListBlocksIn{From: d, To: d}); return e },
		"tags":     func() error { _, e := s.Svc.ListTags(s.Ctx(), a); return e },
		"settings": func() error { _, e := s.Svc.Settings(s.Ctx(), a); return e },
	} {
		t.Run(name, func(t *testing.T) { testutil.RequireCode(t, call(), domain.CodeForbidden) })
	}
}

func TestPagingOffsetInputs(t *testing.T) {
	for _, offset := range []int{0, 1, 500} {
		c, err := service.DecodeOffsetForTest(domain.EncodeCursor(domain.Cursor{Offset: offset}))
		if err != nil || c.Offset != offset {
			t.Fatalf("offset=%+v, %v", c, err)
		}
	}
	if c, err := service.DecodeOffsetForTest(""); err != nil || c.Offset != 0 {
		t.Fatalf("initial offset=%+v, %v", c, err)
	}
	for _, cursor := range []string{
		"garbage", domain.EncodeCursor(domain.Cursor{Offset: -1}),
		domain.EncodeCursor(domain.Cursor{Keys: []any{}, ID: 1}),
		domain.EncodeCursor(domain.Cursor{Keys: []any{1}, ID: 1}),
		base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"k":null,"id":0}`)),
		base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"k":[],"id":0,"o":0}`)),
		base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"o":1.5}`)),
		base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"o":9223372036854775808}`)),
	} {
		_, err := service.DecodeOffsetForTest(cursor)
		testutil.RequireCode(t, err, domain.CodeMalformed)
	}
}
