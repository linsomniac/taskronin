package store

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
)

const testRankGap int64 = 65536

func present[T any](v T) sql.Null[T] { return sql.Null[T]{V: v, Valid: true} }

func taskFixture() domain.Task {
	return domain.Task{
		Title: "task", Notes: "notes\nsecond line", Status: domain.StatusOpen,
		Rank: testRankGap, DueAck: domain.DueNone, Origin: "owner",
		CreatedAt: time.UnixMilli(1793203200123).UTC(), UpdatedAt: time.UnixMilli(1793203200456).UTC(),
	}
}

func fullTaskFixture() domain.Task {
	task := taskFixture()
	task.Status = domain.StatusDone
	task.LastPlacedAt = present(task.CreatedAt)
	task.LastPlacedSeq = present(domain.OpSeq(7))
	task.DeferDate = present(civil.MustDate("2026-10-27"))
	task.DeferMin = present(civil.Minute(0))
	task.DueDate = present(civil.MustDate("2026-10-28"))
	task.DueMin = present(civil.Minute(1439))
	task.DueAck = domain.DueDue
	task.DueAckDate = task.DueDate
	task.Project = "infra.keycloak"
	task.Priority = domain.PriorityH
	task.CompletedAt = present(task.UpdatedAt)
	task.CompletedDate = task.DueDate
	task.DeletedAt = present(task.UpdatedAt.Add(time.Hour))
	task.PurgedAt = present(task.UpdatedAt.Add(2 * time.Hour))
	return task
}

func mustInsertTask(t *testing.T, q Q, task domain.Task) domain.Task {
	t.Helper()
	if err := InsertTask(t.Context(), q, &task); err != nil {
		t.Fatal(err)
	}
	return task
}

func requireTask(t *testing.T, q Q, want domain.Task) {
	t.Helper()
	got, err := GetTask(t.Context(), q, want.ID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("GetTask = %+v, %v; want %+v", got, err, want)
	}
}

func TestTaskRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name string
		task domain.Task
	}{{"nulls", taskFixture()}, {"all fields", fullTaskFixture()}} {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			tc.task.ID, tc.task.Version = 999, 42
			got := mustInsertTask(t, st.Writer, tc.task)
			if got.ID <= 0 || got.ID == 999 || got.Version != 1 {
				t.Fatalf("inserted ID/version = %d/%d", got.ID, got.Version)
			}
			requireTask(t, st.Reader, got)
			var project, priority sql.Null[string]
			var created int64
			if err := st.Reader.QueryRowContext(t.Context(), "SELECT project, priority, created_at FROM tasks WHERE id = ?", got.ID).Scan(&project, &priority, &created); err != nil {
				t.Fatal(err)
			}
			if project.Valid != (got.Project != "") || priority.Valid != (got.Priority != "") || created != got.CreatedAt.UnixMilli() {
				t.Fatalf("stored optional strings/time = %v, %v, %d", project, priority, created)
			}
		})
	}
}

func TestGetTaskNotFound(t *testing.T) {
	st := newTestStore(t)
	if _, err := GetTask(t.Context(), st.Reader, 123); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTask missing = %v", err)
	}
}

func TestUpdateTask(t *testing.T) {
	st := newTestStore(t)
	original := mustInsertTask(t, st.Writer, taskFixture())
	want := fullTaskFixture()
	want.ID = original.ID
	want.Title, want.Notes, want.Origin = "changed", "new notes", "system"
	want.Rank, want.Version = -12, 99
	want.CreatedAt = want.CreatedAt.Add(time.Hour)
	want.UpdatedAt = want.UpdatedAt.Add(time.Hour)
	if err := UpdateTask(t.Context(), st.Writer, want); err != nil {
		t.Fatal(err)
	}
	want.Rank, want.Version = original.Rank, original.Version
	want.CreatedAt, want.UpdatedAt, want.PurgedAt = original.CreatedAt, original.UpdatedAt, original.PurgedAt
	requireTask(t, st.Reader, want)
	cleared := taskFixture()
	cleared.ID, cleared.Version = original.ID, original.Version
	if err := UpdateTask(t.Context(), st.Writer, cleared); err != nil {
		t.Fatal(err)
	}
	requireTask(t, st.Reader, cleared)
	cleared.ID = 999
	if err := UpdateTask(t.Context(), st.Writer, cleared); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateTask missing = %v", err)
	}
}

func TestBumpVersion(t *testing.T) {
	st := newTestStore(t)
	task := mustInsertTask(t, st.Writer, taskFixture())
	now := task.UpdatedAt.Add(time.Hour)
	mustExec(t, st.Writer, "INSERT INTO log_entries (id, kind, title, work_date, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)", 1, "work", "entry", "2026-10-28", 100, 200)
	mustExec(t, st.Writer, "INSERT INTO time_blocks (id, task_id, date, start_min, end_min, created_at) VALUES (?, ?, ?, ?, ?, ?)", 1, task.ID, "2026-10-28", 600, 660, 100)
	mustExec(t, st.Writer, "UPDATE focus SET task_id = ?, date = ?, set_at = ? WHERE id = 1", task.ID, "2026-10-28", task.CreatedAt.UnixMilli())
	for _, ref := range []domain.EntityRef{{Entity: domain.EntTask, ID: int64(task.ID)}, {Entity: domain.EntEntry, ID: 1}, {Entity: domain.EntBlock, ID: 1}, {Entity: domain.EntFocus, ID: 1}} {
		if err := BumpVersion(t.Context(), st.Writer, ref, now); err != nil {
			t.Fatal(err)
		}
		ref.ID = 999
		if err := BumpVersion(t.Context(), st.Writer, ref, now); !errors.Is(err, ErrNotFound) {
			t.Fatalf("BumpVersion missing %s = %v", ref.Entity, err)
		}
	}
	task.Version, task.UpdatedAt = 2, now
	requireTask(t, st.Reader, task)
	var version, created, updated int64
	if err := st.Reader.QueryRowContext(t.Context(), "SELECT version, created_at, updated_at FROM log_entries WHERE id = ?", 1).Scan(&version, &created, &updated); err != nil || version != 2 || created != 100 || updated != now.UnixMilli() {
		t.Fatalf("entry version/times = %d/%d/%d, %v", version, created, updated, err)
	}
	if err := st.Reader.QueryRowContext(t.Context(), "SELECT version, created_at FROM time_blocks WHERE id = ?", 1).Scan(&version, &created); err != nil || version != 2 || created != 100 {
		t.Fatalf("block version/time = %d/%d, %v", version, created, err)
	}
	focus, err := GetFocus(t.Context(), st.Reader)
	wantFocus := domain.Focus{TaskID: present(task.ID), Date: present(civil.MustDate("2026-10-28")), SetAt: present(task.CreatedAt), Version: 2}
	if err != nil || focus != wantFocus {
		t.Fatalf("focus = %+v, %v", focus, err)
	}
	if err := BumpVersion(t.Context(), st.Writer, domain.EntityRef{Entity: domain.EntSetting}, now); err == nil {
		t.Fatal("BumpVersion accepted an unversioned entity")
	}
}

func TestListTasksFiltersAndPaging(t *testing.T) {
	st := newTestStore(t)
	add := func(rank int64, project string) domain.Task {
		task := taskFixture()
		task.Rank, task.Project = rank, project
		return mustInsertTask(t, st.Writer, task)
	}
	a, b, c := add(3*testRankGap, ""), add(testRankGap, "infrastructure"), add(2*testRankGap, "infra.keycloak")
	if err := SetTaskTags(t.Context(), st.Writer, a.ID, []string{"Infra"}, a.CreatedAt); err != nil {
		t.Fatal(err)
	}
	triage := add(4*testRankGap, "")
	triage2 := add(5*testRankGap, "")
	triage3 := add(6*testRankGap, "")
	triage4 := add(7*testRankGap, "")
	for _, row := range []domain.TriageRow{
		{TaskID: triage.ID, Reason: domain.ReasonNew, Seq: 4, Since: a.CreatedAt},
		{TaskID: triage.ID, Reason: domain.ReasonSlipped, Seq: 9, Since: a.CreatedAt},
		{TaskID: triage2.ID, Reason: domain.ReasonSlipped, Seq: 9, Since: a.CreatedAt.Add(-time.Hour)},
		{TaskID: triage3.ID, Reason: domain.ReasonSlipped, Seq: 10, Since: a.CreatedAt.Add(-2 * time.Hour)},
		{TaskID: triage4.ID, Reason: domain.ReasonSlipped, Seq: 6, Since: a.CreatedAt.Add(time.Hour)},
		{TaskID: triage4.ID, Reason: domain.ReasonNew, Seq: 12, Since: a.CreatedAt.Add(2 * time.Hour)},
	} {
		if _, err := AddReason(t.Context(), st.Writer, row); err != nil {
			t.Fatal(err)
		}
	}
	deferred := taskFixture()
	deferred.DeferDate = present(civil.MustDate("2026-10-29"))
	deferred = mustInsertTask(t, st.Writer, deferred)
	// A deferred task may retain reasons, but belongs only to upcoming.
	if _, err := AddReason(t.Context(), st.Writer, domain.TriageRow{TaskID: deferred.ID, Reason: domain.ReasonDue, Since: a.CreatedAt, Seq: 1}); err != nil {
		t.Fatal(err)
	}
	done := taskFixture()
	done.Status, done.CompletedAt, done.CompletedDate = domain.StatusDone, present(done.UpdatedAt), present(civil.MustDate("2026-10-28"))
	done = mustInsertTask(t, st.Writer, done)
	dropped := taskFixture()
	dropped.Status = domain.StatusDropped
	dropped = mustInsertTask(t, st.Writer, dropped)
	deleted := taskFixture()
	deleted.DeletedAt = present(deleted.UpdatedAt)
	deleted = mustInsertTask(t, st.Writer, deleted)
	for _, tc := range []struct {
		view         domain.View
		tag, project string
		want         []domain.TaskID
	}{
		{domain.ViewBacklog, "", "", []domain.TaskID{b.ID, c.ID, a.ID}},
		{domain.ViewBacklog, "", "infra", []domain.TaskID{c.ID}},
		{domain.ViewBacklog, "", "infra.keycloak", []domain.TaskID{c.ID}},
		{domain.ViewBacklog, "infra", "", []domain.TaskID{a.ID}},
		{domain.ViewBacklog, "infra", "infra", nil},
		{domain.ViewBacklog, "inf", "", nil},
		{domain.ViewTriage, "", "", []domain.TaskID{triage4.ID, triage.ID, triage2.ID, triage3.ID}},
		{domain.ViewUpcoming, "", "", []domain.TaskID{deferred.ID}},
		{domain.ViewDone, "", "", []domain.TaskID{done.ID}},
		{domain.ViewDropped, "", "", []domain.TaskID{dropped.ID}},
		{domain.ViewDeleted, "", "", []domain.TaskID{deleted.ID}},
		{domain.ViewAll, "", "", []domain.TaskID{a.ID, b.ID, c.ID, triage.ID, triage2.ID, triage3.ID, triage4.ID, deferred.ID, done.ID, dropped.ID}},
		{domain.ViewAll, "' OR 1=1 --", "", nil},
		{domain.ViewAll, "", "%", nil},
	} {
		t.Run(string(tc.view)+"/"+tc.tag+"/"+tc.project, func(t *testing.T) {
			query := TaskQuery{View: tc.view, Tag: tc.tag, Project: tc.project, Limit: 100}
			assertTaskIDs(t, st.Reader, query, tc.want)
			assertTaskPages(t, st.Reader, query, tc.want)
		})
	}
}

func assertTaskIDs(t *testing.T, q Q, query TaskQuery, want []domain.TaskID) {
	t.Helper()
	rows, err := ListTasks(t.Context(), q, query)
	if err != nil {
		t.Fatal(err)
	}
	var got []domain.TaskID
	for _, row := range rows {
		got = append(got, row.ID)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ListTasks(%+v) = %v; want %v", query, got, want)
	}
}

func assertTaskPages(t *testing.T, q Q, query TaskQuery, want []domain.TaskID) {
	t.Helper()
	query.Limit = 1
	for _, id := range want {
		rows, err := ListTasks(t.Context(), q, query)
		if err != nil || len(rows) != 1 || rows[0].ID != id {
			t.Fatalf("page(%+v) = %+v, %v; want %d", query, rows, err, id)
		}
		row := rows[0]
		query.AfterID = int64(row.ID)
		switch query.View {
		case domain.ViewBacklog:
			query.AfterKeys = []any{row.Rank}
		case domain.ViewTriage:
			reasons, err := Reasons(t.Context(), q, row.ID)
			if err != nil || len(reasons) == 0 {
				t.Fatalf("Reasons = %v, %v", reasons, err)
			}
			query.AfterKeys = []any{int64(reasons[0].Reason.Rank()), int64(reasons[0].Seq)}
		case domain.ViewUpcoming:
			query.AfterKeys = []any{row.DeferDate.V.String(), int64(row.DeferMin.V), row.Rank}
		case domain.ViewDone:
			query.AfterKeys = []any{row.CompletedAt.V.UnixMilli()}
		case domain.ViewDropped:
			query.AfterKeys = []any{row.UpdatedAt.UnixMilli()}
		case domain.ViewDeleted:
			query.AfterKeys = []any{row.DeletedAt.V.UnixMilli()}
		case domain.ViewAll:
			query.AfterKeys = []any{}
		}
	}
	assertTaskIDs(t, q, query, nil)
}

func TestListTasksOrderTies(t *testing.T) {
	for _, view := range []domain.View{domain.ViewBacklog, domain.ViewUpcoming, domain.ViewDone, domain.ViewDropped, domain.ViewDeleted} {
		t.Run(string(view), func(t *testing.T) {
			st := newTestStore(t)
			var ids []domain.TaskID
			for i := range 5 {
				task := taskFixture()
				delta := time.Duration([]int{1, 1, 0, 2, 0}[i]) * time.Second
				switch view {
				case domain.ViewBacklog:
					task.Rank = int64(i / 2)
				case domain.ViewUpcoming:
					task.DeferDate = present(civil.MustDate("2026-10-29").AddDays(i / 4))
					if i > 0 {
						task.DeferMin = present(civil.Minute((i / 3) * 30))
					}
					task.Rank = int64((i / 2) % 2)
				case domain.ViewDone:
					task.Status, task.CompletedDate = domain.StatusDone, present(civil.MustDate("2026-10-28"))
					task.CompletedAt = present(task.UpdatedAt.Add(delta))
				case domain.ViewDropped:
					task.Status = domain.StatusDropped
					task.UpdatedAt = task.UpdatedAt.Add(delta)
				case domain.ViewDeleted:
					task.DeletedAt = present(task.UpdatedAt.Add(delta))
				default:
					t.Fatal("unexpected view")
				}
				task = mustInsertTask(t, st.Writer, task)
				ids = append(ids, task.ID)
			}
			if view == domain.ViewDone || view == domain.ViewDropped || view == domain.ViewDeleted {
				ids = []domain.TaskID{ids[3], ids[1], ids[0], ids[4], ids[2]}
			}
			query := TaskQuery{View: view, Limit: 100}
			assertTaskIDs(t, st.Reader, query, ids)
			assertTaskPages(t, st.Reader, query, ids)
		})
	}
}

func TestTriageRankOrder(t *testing.T) {
	st := newTestStore(t)
	var want []domain.TaskID
	for i, reason := range domain.TriageOrder {
		task := mustInsertTask(t, st.Writer, taskFixture())
		want = append(want, task.ID)
		if _, err := AddReason(t.Context(), st.Writer, domain.TriageRow{TaskID: task.ID, Reason: reason, Seq: domain.OpSeq(100 - i), Since: task.CreatedAt}); err != nil {
			t.Fatal(err)
		}
	}
	query := TaskQuery{View: domain.ViewTriage, Limit: 100}
	assertTaskIDs(t, st.Reader, query, want)
	assertTaskPages(t, st.Reader, query, want)
}

func TestRankQueries(t *testing.T) {
	st := newTestStore(t)
	lo, hi, err := RankBounds(t.Context(), st.Reader)
	if err != nil || lo.Valid || hi.Valid {
		t.Fatalf("empty bounds = %v/%v, %v", lo, hi, err)
	}
	var tasks []domain.Task
	for _, rank := range []int64{-10, -10, 30} {
		task := taskFixture()
		task.Rank = rank
		if rank == 30 {
			task.DeletedAt = present(task.UpdatedAt)
		}
		tasks = append(tasks, mustInsertTask(t, st.Writer, task))
	}
	lo, hi, err = RankBounds(t.Context(), st.Reader)
	if err != nil || lo != present(int64(-10)) || hi != present(int64(30)) {
		t.Fatalf("bounds = %v/%v, %v", lo, hi, err)
	}
	for i, task := range tasks {
		pos := RankPos{ID: task.ID, Rank: task.Rank}
		prev, err := RankPrev(t.Context(), st.Reader, pos)
		if err != nil {
			t.Fatal(err)
		}
		next, err := RankNext(t.Context(), st.Reader, pos)
		if err != nil {
			t.Fatal(err)
		}
		var wantPrev, wantNext sql.Null[RankPos]
		if i > 0 {
			wantPrev = present(RankPos{ID: tasks[i-1].ID, Rank: tasks[i-1].Rank})
		}
		if i+1 < len(tasks) {
			wantNext = present(RankPos{ID: tasks[i+1].ID, Rank: tasks[i+1].Rank})
		}
		if prev != wantPrev || next != wantNext {
			t.Fatalf("neighbors of %v = %v/%v; want %v/%v", pos, prev, next, wantPrev, wantNext)
		}
	}
	for _, tc := range []struct {
		id   domain.TaskID
		rank int64
		want bool
	}{
		{tasks[0].ID, -10, true}, {tasks[2].ID, 30, false}, {tasks[0].ID, 30, true}, {tasks[0].ID, 0, false},
	} {
		got, err := RankTaken(t.Context(), st.Reader, tc.id, tc.rank)
		if err != nil || got != tc.want {
			t.Fatalf("RankTaken = %v, %v; want %v", got, err, tc.want)
		}
	}
	if err := SetRank(t.Context(), st.Writer, tasks[0].ID, 100); err != nil {
		t.Fatal(err)
	}
	tasks[0].Rank = 100
	requireTask(t, st.Reader, tasks[0])
	if err := SetRank(t.Context(), st.Writer, 999, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetRank missing = %v", err)
	}
}

func TestRenormalizeRanks(t *testing.T) {
	st := newTestStore(t)
	if err := RenormalizeRanks(t.Context(), st.Writer); err != nil {
		t.Fatal(err)
	}
	var tasks []domain.Task
	for i, rank := range []int64{20, -10, -10, 0} {
		task := taskFixture()
		task.Rank = rank
		if i == 0 {
			task.DeletedAt = present(task.UpdatedAt)
		}
		if i == 3 {
			task.Status = domain.StatusDropped
		}
		task = mustInsertTask(t, st.Writer, task)
		if err := BumpVersion(t.Context(), st.Writer, domain.EntityRef{Entity: domain.EntTask, ID: int64(task.ID)}, task.UpdatedAt); err != nil {
			t.Fatal(err)
		}
		task.Version = 2
		tasks = append(tasks, task)
	}
	if err := RenormalizeRanks(t.Context(), st.Writer); err != nil {
		t.Fatal(err)
	}
	for i, rank := range []int64{4 * testRankGap, testRankGap, 2 * testRankGap, 3 * testRankGap} {
		tasks[i].Rank = rank
		requireTask(t, st.Reader, tasks[i])
	}
}

func TestReasons(t *testing.T) {
	st := newTestStore(t)
	task := mustInsertTask(t, st.Writer, taskFixture())
	var want []domain.TriageRow
	for i, reason := range []domain.TriageReason{domain.ReasonNew, domain.ReasonDue, domain.ReasonSlipped} {
		row := domain.TriageRow{TaskID: task.ID, Reason: reason, Seq: domain.OpSeq(1 + 4*i), Since: task.CreatedAt.Add(time.Duration(i) * time.Hour)}
		inserted, err := AddReason(t.Context(), st.Writer, row)
		if err != nil || !inserted {
			t.Fatalf("AddReason = %v, %v", inserted, err)
		}
		want = append(want, row)
		row.Seq, row.Since = 99, task.UpdatedAt
		inserted, err = AddReason(t.Context(), st.Writer, row)
		if err != nil || inserted {
			t.Fatalf("duplicate AddReason = %v, %v", inserted, err)
		}
	}
	slices.Reverse(want)
	got, err := Reasons(t.Context(), st.Reader, task.ID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Reasons = %v, %v; want %v", got, err, want)
	}
	removed, err := RemoveReason(t.Context(), st.Writer, task.ID, domain.ReasonDue)
	if err != nil || removed != present(want[1]) {
		t.Fatalf("RemoveReason = %v, %v", removed, err)
	}
	removed, err = RemoveReason(t.Context(), st.Writer, task.ID, domain.ReasonDue)
	if err != nil || removed.Valid {
		t.Fatalf("RemoveReason absent = %v, %v", removed, err)
	}
	got, err = Reasons(t.Context(), st.Reader, task.ID)
	if err != nil || !reflect.DeepEqual(got, []domain.TriageRow{want[0], want[2]}) {
		t.Fatalf("remaining reasons = %v, %v", got, err)
	}
}

func TestTags(t *testing.T) {
	st := newTestStore(t)
	a := mustInsertTask(t, st.Writer, taskFixture())
	b := mustInsertTask(t, st.Writer, taskFixture())
	if err := SetTaskTags(t.Context(), st.Writer, a.ID, []string{"Infra", "db"}, a.CreatedAt); err != nil {
		t.Fatal(err)
	}
	got, err := TaskTags(t.Context(), st.Reader, a.ID)
	if err != nil || !slices.Equal(got, []string{"db", "Infra"}) {
		t.Fatalf("tags = %v, %v", got, err)
	}
	if err := SetTaskTags(t.Context(), st.Writer, b.ID, []string{"infra", "INFRA"}, b.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	got, err = TaskTags(t.Context(), st.Reader, b.ID)
	if err != nil || !slices.Equal(got, []string{"Infra"}) {
		t.Fatalf("reused tags = %v, %v", got, err)
	}
	var count int
	var created int64
	var name string
	if err := st.Reader.QueryRowContext(t.Context(), "SELECT count(*), name, created_at FROM tags WHERE name = ? COLLATE NOCASE", "infra").Scan(&count, &name, &created); err != nil || count != 1 || name != "Infra" || created != a.CreatedAt.UnixMilli() {
		t.Fatalf("reused tag row = %d/%s/%d, %v", count, name, created, err)
	}
	if err := SetTaskTags(t.Context(), st.Writer, a.ID, []string{"db"}, a.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	got, err = TaskTags(t.Context(), st.Reader, a.ID)
	if err != nil || !slices.Equal(got, []string{"db"}) {
		t.Fatalf("replaced tags = %v, %v", got, err)
	}
	if err := SetTaskTags(t.Context(), st.Writer, a.ID, nil, a.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	got, err = TaskTags(t.Context(), st.Reader, a.ID)
	if err != nil || len(got) != 0 {
		t.Fatalf("cleared tags = %v, %v", got, err)
	}
	err = SetTaskTags(t.Context(), st.Writer, a.ID, []string{"a b"}, a.UpdatedAt)
	de, ok := domain.AsError(err)
	if !ok || de.Code != domain.CodeValidation {
		t.Fatalf("invalid tag = %v", err)
	}
}

func TestTagCounts(t *testing.T) {
	st := newTestStore(t)
	for i := range 5 {
		task := taskFixture()
		switch i {
		case 2:
			task.Status = domain.StatusDropped
		case 3:
			task.Status, task.CompletedAt, task.CompletedDate = domain.StatusDone, present(task.UpdatedAt), present(civil.MustDate("2026-10-28"))
		case 4:
			task.DeletedAt = present(task.UpdatedAt)
		}
		task = mustInsertTask(t, st.Writer, task)
		names := []string{"Infra", "db"}
		if i == 0 {
			names = []string{"Infra"}
		}
		if i > 1 {
			names = append(names, "zero")
		}
		if err := SetTaskTags(t.Context(), st.Writer, task.ID, names, task.CreatedAt); err != nil {
			t.Fatal(err)
		}
	}
	got, err := TagCounts(t.Context(), st.Reader)
	want := []TagCount{{Name: "db", Count: 1}, {Name: "Infra", Count: 2}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("TagCounts = %v, %v; want %v", got, err, want)
	}
}

func TestFocus(t *testing.T) {
	st := newTestStore(t)
	got, err := GetFocus(t.Context(), st.Reader)
	if err != nil || got != (domain.Focus{Version: 1}) {
		t.Fatalf("default focus = %+v, %v", got, err)
	}
	task := mustInsertTask(t, st.Writer, taskFixture())
	want := domain.Focus{TaskID: present(task.ID), Date: present(civil.MustDate("2026-10-28")), SetAt: present(task.UpdatedAt), Version: 42}
	if err := PutFocus(t.Context(), st.Writer, want); err != nil {
		t.Fatal(err)
	}
	want.Version = 1
	got, err = GetFocus(t.Context(), st.Reader)
	if err != nil || got != want {
		t.Fatalf("focus = %+v, %v; want %+v", got, err, want)
	}
	if err := PutFocus(t.Context(), st.Writer, domain.Focus{}); err != nil {
		t.Fatal(err)
	}
	got, err = GetFocus(t.Context(), st.Reader)
	if err != nil || got != (domain.Focus{Version: 1}) {
		t.Fatalf("cleared focus = %+v, %v", got, err)
	}
}

func TestSettings(t *testing.T) {
	st := newTestStore(t)
	if _, err := GetSetting(t.Context(), st.Reader, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing setting = %v", err)
	}
	for _, want := range []string{"first", "updated", ""} {
		if err := SetSetting(t.Context(), st.Writer, "test.key", want); err != nil {
			t.Fatal(err)
		}
		got, err := GetSetting(t.Context(), st.Reader, "test.key")
		if err != nil || got != want {
			t.Fatalf("GetSetting = %q, %v; want %q", got, err, want)
		}
	}
}

func TestAuthState(t *testing.T) {
	st := newTestStore(t)
	if _, err := GetAuthState(t.Context(), st.Reader); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing auth state = %v", err)
	}
	for _, disabled := range []bool{false, true} {
		want := AuthState{PasswordHash: "$argon2id$" + rand.Text(), PasswordChangedAt: time.UnixMilli(1793203200123).UTC(), WebLoginDisabled: disabled}
		if _, err := rand.Read(want.IdemHMACKey[:]); err != nil {
			t.Fatal(err)
		}
		if err := PutAuthState(t.Context(), st.Writer, want); err != nil {
			t.Fatal(err)
		}
		got, err := GetAuthState(t.Context(), st.Reader)
		if err != nil || got != want {
			t.Fatalf("auth state round trip failed: %v", err)
		}
	}
}

func TestTaskWritesMapErrors(t *testing.T) {
	st := newTestStore(t)
	task := mustInsertTask(t, st.Writer, taskFixture())
	for _, tc := range []struct {
		name  string
		write func() error
		code  domain.Code
	}{
		{"insert", func() error {
			invalid := taskFixture()
			invalid.Title = ""
			return InsertTask(t.Context(), st.Writer, &invalid)
		}, domain.CodeValidation},
		{"update", func() error {
			invalid := task
			invalid.Project = "a b"
			return UpdateTask(t.Context(), st.Writer, invalid)
		}, domain.CodeValidation},
		{"reason", func() error {
			_, err := AddReason(t.Context(), st.Writer, domain.TriageRow{TaskID: 999, Reason: domain.ReasonNew, Since: task.CreatedAt, Seq: 1})
			return err
		}, domain.CodeConflict},
		{"focus", func() error {
			return PutFocus(t.Context(), st.Writer, domain.Focus{TaskID: present(domain.TaskID(999)), Date: present(civil.MustDate("2026-10-28"))})
		}, domain.CodeConflict},
		{"auth", func() error {
			return PutAuthState(t.Context(), st.Writer, AuthState{PasswordHash: "", PasswordChangedAt: task.CreatedAt})
		}, domain.CodeValidation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.write()
			de, ok := domain.AsError(err)
			if !ok || de.Code != tc.code {
				t.Fatalf("write error = %v; want %s", err, tc.code)
			}
		})
	}
	requireTask(t, st.Reader, task)
	if err := SetTaskTags(t.Context(), st.Writer, 999, nil, task.CreatedAt); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing task tags write = %v", err)
	}
	mustExec(t, st.Writer, "DELETE FROM focus WHERE id = 1")
	if _, err := GetFocus(t.Context(), st.Reader); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing focus read = %v", err)
	}
	if err := PutFocus(t.Context(), st.Writer, domain.Focus{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing focus write = %v", err)
	}
}

func TestSetTaskTagsRollback(t *testing.T) {
	st := newTestStore(t)
	task := mustInsertTask(t, st.Writer, taskFixture())
	if err := SetTaskTags(t.Context(), st.Writer, task.ID, []string{"Infra"}, task.CreatedAt); err != nil {
		t.Fatal(err)
	}
	tx, err := st.Writer.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Error(err)
		}
	})
	err = SetTaskTags(t.Context(), tx, task.ID, []string{"temporary", "a b"}, task.UpdatedAt)
	de, ok := domain.AsError(err)
	if !ok || de.Code != domain.CodeValidation {
		t.Fatalf("invalid tag in transaction = %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	got, err := TaskTags(t.Context(), st.Reader, task.ID)
	if err != nil || !slices.Equal(got, []string{"Infra"}) {
		t.Fatalf("tags after rollback = %v, %v", got, err)
	}
	var count int
	if err := st.Reader.QueryRowContext(t.Context(), "SELECT count(*) FROM tags WHERE name = ?", "temporary").Scan(&count); err != nil || count != 0 {
		t.Fatalf("tag creation after rollback = %d, %v", count, err)
	}
}
