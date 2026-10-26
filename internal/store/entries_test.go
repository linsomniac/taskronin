package store

import (
	"database/sql"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
)

func entryFixture() domain.LogEntry {
	return domain.LogEntry{
		Kind: domain.EntryWork, Title: "work", Notes: "notes\nsecond line",
		WorkDate:  civil.MustDate("2026-10-28"),
		CreatedAt: time.UnixMilli(1793203200123).UTC(), UpdatedAt: time.UnixMilli(1793203200456).UTC(),
	}
}

func mustInsertEntry(t *testing.T, q Q, e domain.LogEntry) domain.LogEntry {
	t.Helper()
	if err := InsertEntry(t.Context(), q, &e); err != nil {
		t.Fatal(err)
	}
	return e
}

func requireEntry(t *testing.T, q Q, want domain.LogEntry) {
	t.Helper()
	got, err := GetEntry(t.Context(), q, want.ID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("GetEntry = %+v, %v; want %+v", got, err, want)
	}
}

func requireEntryIDs(t *testing.T, got []domain.LogEntry, err error, want ...domain.EntryID) {
	t.Helper()
	var ids []domain.EntryID
	for _, e := range got {
		ids = append(ids, e.ID)
	}
	if err != nil || !slices.Equal(ids, want) {
		t.Fatalf("entry IDs = %v, %v; want %v", ids, err, want)
	}
}

func TestEntryRoundTrip(t *testing.T) {
	st := newTestStore(t)
	task := mustInsertTask(t, st.Writer, taskFixture())
	full := entryFixture()
	full.TaskID, full.Kind, full.Reopened = present(task.ID), domain.EntryComplete, true
	full.OccurredMin = present(civil.Minute(0))
	full.DeletedAt, full.PurgedAt = present(full.UpdatedAt), present(full.UpdatedAt.Add(time.Millisecond))
	for _, e := range []domain.LogEntry{entryFixture(), full} {
		e.ID, e.Version = 999, 42
		e = mustInsertEntry(t, st.Writer, e)
		if e.ID <= 0 || e.ID == 999 || e.Version != 1 {
			t.Fatalf("inserted ID/version = %d/%d", e.ID, e.Version)
		}
		requireEntry(t, st.Reader, e)
	}
	if _, err := GetEntry(t.Context(), st.Reader, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetEntry missing = %v", err)
	}
	e := entryFixture()
	e.CreatedAt = e.CreatedAt.In(time.FixedZone("test", -6*3600)).Add(999 * time.Microsecond)
	e = mustInsertEntry(t, st.Writer, e)
	e.CreatedAt = e.CreatedAt.UTC().Truncate(time.Millisecond)
	requireEntry(t, st.Reader, e)
	bad := entryFixture()
	bad.TaskID = present(domain.TaskID(999))
	before := bad
	var de *domain.Error
	if err := InsertEntry(t.Context(), st.Writer, &bad); !errors.As(err, &de) || de.Code != domain.CodeConflict || bad != before {
		t.Fatalf("invalid entry insert = %+v, %v", bad, err)
	}
}

func TestUpdateEntry(t *testing.T) {
	st := newTestStore(t)
	task := mustInsertTask(t, st.Writer, taskFixture())
	original := mustInsertEntry(t, st.Writer, entryFixture())
	want := original
	want.TaskID, want.Title, want.Notes = present(task.ID), "changed", "changed notes"
	want.WorkDate, want.OccurredMin = civil.MustDate("2026-10-29"), present(civil.Minute(1439))
	want.DeletedAt = present(want.UpdatedAt)
	want.Kind, want.Version = domain.EntryComplete, 99
	want.CreatedAt, want.UpdatedAt = want.CreatedAt.Add(time.Hour), want.UpdatedAt.Add(time.Hour)
	want.PurgedAt = present(want.UpdatedAt)
	if err := UpdateEntry(t.Context(), st.Writer, want); err != nil {
		t.Fatal(err)
	}
	want.Kind, want.Version = original.Kind, original.Version
	want.CreatedAt, want.UpdatedAt, want.PurgedAt = original.CreatedAt, original.UpdatedAt, original.PurgedAt
	requireEntry(t, st.Reader, want)
	if err := UpdateEntry(t.Context(), st.Writer, original); err != nil {
		t.Fatal(err)
	}
	requireEntry(t, st.Reader, original)
	complete := entryFixture()
	complete.TaskID, complete.Kind = present(task.ID), domain.EntryComplete
	complete = mustInsertEntry(t, st.Writer, complete)
	complete.Reopened = true
	if err := UpdateEntry(t.Context(), st.Writer, complete); err != nil {
		t.Fatal(err)
	}
	requireEntry(t, st.Reader, complete)
	original.ID = 999
	if err := UpdateEntry(t.Context(), st.Writer, original); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateEntry missing = %v", err)
	}
}

// The fixture independently exercises each sort key, midnight versus untimed,
// standalone rows, project boundaries, and live entries of deleted tasks.
func entryListFixture(t *testing.T, st *Store) ([]domain.LogEntry, []domain.Task) {
	t.Helper()
	var tasks []domain.Task
	for _, project := range []string{"infra", "infra.keycloak", "infrastructure"} {
		task := taskFixture()
		task.Project = project
		tasks = append(tasks, mustInsertTask(t, st.Writer, task))
	}
	if err := SetTaskTags(t.Context(), st.Writer, tasks[0].ID, []string{"Infra"}, tasks[0].CreatedAt); err != nil {
		t.Fatal(err)
	}
	mustExec(t, st.Writer, "UPDATE tasks SET deleted_at = ? WHERE id = ?", 123, tasks[0].ID)
	var entries []domain.LogEntry
	for _, row := range []struct {
		date    string
		minute  int
		created int64
		task    int
		deleted bool
	}{
		{"2026-10-29", -1, 100, 0, false},
		{"2026-10-28", -1, 500, -1, false},
		{"2026-10-28", 0, 400, 0, false},
		{"2026-10-28", 600, 100, 1, false},
		{"2026-10-28", 600, 200, 0, false},
		{"2026-10-28", 600, 200, 0, false},
		{"2026-10-27", 1439, 900, 2, false},
		{"2026-10-28", 1200, 300, 0, true},
		{"2026-10-28", -1, 600, -1, false},
	} {
		e := entryFixture()
		e.WorkDate, e.CreatedAt = civil.MustDate(row.date), time.UnixMilli(row.created).UTC()
		if row.minute >= 0 {
			e.OccurredMin = present(civil.Minute(row.minute))
		}
		if row.task >= 0 {
			e.TaskID = present(tasks[row.task].ID)
		}
		if row.deleted {
			e.DeletedAt = present(e.UpdatedAt)
		}
		entries = append(entries, mustInsertEntry(t, st.Writer, e))
	}
	return entries, tasks
}

func TestListEntriesOrderAndFilters(t *testing.T) {
	st := newTestStore(t)
	e, tasks := entryListFixture(t, st)
	day := civil.MustDate("2026-10-28")
	standalone, task := sql.Null[domain.TaskID]{}, present(tasks[0].ID)
	all := []domain.EntryID{e[0].ID, e[5].ID, e[4].ID, e[3].ID, e[2].ID, e[8].ID, e[1].ID, e[6].ID}
	for _, tc := range []struct {
		name  string
		query EntryQuery
		want  []domain.EntryID
	}{
		{"all", EntryQuery{}, all},
		{"standalone", EntryQuery{Task: &standalone}, []domain.EntryID{e[8].ID, e[1].ID}},
		{"task", EntryQuery{Task: &task}, []domain.EntryID{e[0].ID, e[5].ID, e[4].ID, e[2].ID}},
		{"deleted", EntryQuery{IncludeDeleted: true}, []domain.EntryID{e[0].ID, e[7].ID, e[5].ID, e[4].ID, e[3].ID, e[2].ID, e[8].ID, e[1].ID, e[6].ID}},
		{"range inclusive", EntryQuery{From: day, To: day}, []domain.EntryID{e[5].ID, e[4].ID, e[3].ID, e[2].ID, e[8].ID, e[1].ID}},
		{"from only", EntryQuery{From: day.AddDays(1)}, []domain.EntryID{e[0].ID}},
		{"to only", EntryQuery{To: day.AddDays(-1)}, []domain.EntryID{e[6].ID}},
		{"tag", EntryQuery{Tag: "iNfRa"}, []domain.EntryID{e[0].ID, e[5].ID, e[4].ID, e[2].ID}},
		{"project", EntryQuery{Project: "infra"}, []domain.EntryID{e[0].ID, e[5].ID, e[4].ID, e[3].ID, e[2].ID}},
		{"both", EntryQuery{Tag: "infra", Project: "infra.keycloak"}, nil},
		{"standalone tag", EntryQuery{Task: &standalone, Tag: "infra"}, nil},
		{"standalone project", EntryQuery{Task: &standalone, Project: "infra"}, nil},
		{"literal filter", EntryQuery{Tag: "' OR 1=1 --", Project: "%"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.query.Limit = 100
			got, err := ListEntries(t.Context(), st.Reader, tc.query)
			requireEntryIDs(t, got, err, tc.want...)
		})
	}
	for _, limit := range []int{1, 2, 3} {
		query := EntryQuery{Limit: limit}
		var got []domain.LogEntry
		for page := 0; page <= len(all); page++ {
			rows, err := ListEntries(t.Context(), st.Reader, query)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) == 0 {
				break
			}
			if len(rows) > limit {
				t.Fatalf("page size = %d; limit %d", len(rows), limit)
			}
			got = append(got, rows...)
			last := rows[len(rows)-1]
			untimed := 1
			if last.OccurredMin.Valid {
				untimed = 0
			}
			query.AfterKeys = []any{last.WorkDate.String(), untimed, int(last.OccurredMin.V), last.CreatedAt.UnixMilli()}
			query.AfterID = int64(last.ID)
		}
		requireEntryIDs(t, got, nil, all...)
	}
	for _, keys := range [][]any{{}, {"2026-10-28"}, {"2026-10-28", 0, 0, 1, 2}} {
		var de *domain.Error
		if _, err := ListEntries(t.Context(), st.Reader, EntryQuery{AfterKeys: keys, Limit: 10}); !errors.As(err, &de) || de.Code != domain.CodeValidation {
			t.Fatalf("malformed cursor keys = %v", err)
		}
	}
	got, err := EntriesOfTask(t.Context(), st.Reader, tasks[0].ID)
	requireEntryIDs(t, got, err, e[0].ID, e[5].ID, e[4].ID, e[2].ID)
}

func TestReportEntries(t *testing.T) {
	st := newTestStore(t)
	e, _ := entryListFixture(t, st)
	from, to := civil.MustDate("2026-10-27"), civil.MustDate("2026-10-29")
	for _, tc := range []struct {
		tag, project string
		want         []domain.EntryID
	}{
		{"", "", []domain.EntryID{e[6].ID, e[2].ID, e[3].ID, e[4].ID, e[5].ID, e[1].ID, e[8].ID, e[0].ID}},
		{"INFRA", "", []domain.EntryID{e[2].ID, e[4].ID, e[5].ID, e[0].ID}},
		{"", "infra", []domain.EntryID{e[2].ID, e[3].ID, e[4].ID, e[5].ID, e[0].ID}},
		{"infra", "infra.keycloak", nil},
	} {
		got, err := ReportEntries(t.Context(), st.Reader, from, to, tc.tag, tc.project)
		requireEntryIDs(t, got, err, tc.want...)
	}
	got, err := ReportEntries(t.Context(), st.Reader, to, to, "", "")
	requireEntryIDs(t, got, err, e[0].ID)
}

func TestCountEntriesOn(t *testing.T) {
	st := newTestStore(t)
	task := mustInsertTask(t, st.Writer, taskFixture())
	other := mustInsertTask(t, st.Writer, taskFixture())
	day := civil.MustDate("2026-10-28")
	for _, row := range []struct {
		task    domain.TaskID
		date    civil.Date
		kind    domain.EntryKind
		deleted bool
	}{
		{task.ID, day, domain.EntryWork, false}, {task.ID, day, domain.EntryWork, false},
		{task.ID, day, domain.EntryComplete, false}, {task.ID, day, domain.EntryComplete, true},
		{task.ID, day, domain.EntryWork, true}, {other.ID, day, domain.EntryWork, false},
		{task.ID, day.AddDays(1), domain.EntryComplete, false},
	} {
		e := entryFixture()
		e.TaskID, e.WorkDate, e.Kind = present(row.task), row.date, row.kind
		e.Reopened = row.kind == domain.EntryComplete
		if row.deleted {
			e.DeletedAt = present(e.UpdatedAt)
		}
		mustInsertEntry(t, st.Writer, e)
	}
	mustInsertEntry(t, st.Writer, entryFixture())
	work, complete, err := CountEntriesOn(t.Context(), st.Reader, task.ID, day)
	if err != nil || work != 2 || complete != 1 {
		t.Fatalf("counts = %d, %d, %v", work, complete, err)
	}
	work, complete, err = CountEntriesOn(t.Context(), st.Reader, task.ID, day.AddDays(-1))
	if err != nil || work != 0 || complete != 0 {
		t.Fatalf("empty counts = %d, %d, %v", work, complete, err)
	}
}

func TestCompleteEntries(t *testing.T) {
	st := newTestStore(t)
	task := mustInsertTask(t, st.Writer, taskFixture())
	other := mustInsertTask(t, st.Writer, taskFixture())
	var want []domain.EntryID
	for i := 0; i < 3; i++ {
		e := entryFixture()
		e.TaskID, e.Kind, e.Reopened = present(task.ID), domain.EntryComplete, i != 0
		e.WorkDate = e.WorkDate.AddDays(-i)
		if i >= 1 {
			e.DeletedAt = present(e.UpdatedAt)
		}
		if i == 2 {
			e.PurgedAt = present(e.UpdatedAt)
		}
		want = append(want, mustInsertEntry(t, st.Writer, e).ID)
	}
	e := entryFixture()
	e.TaskID = present(task.ID)
	mustInsertEntry(t, st.Writer, e)
	e.TaskID, e.Kind = present(other.ID), domain.EntryComplete
	mustInsertEntry(t, st.Writer, e)
	got, err := CompleteEntries(t.Context(), st.Reader, task.ID)
	requireEntryIDs(t, got, err, want...)
}

func TestInterruptions(t *testing.T) {
	st := newTestStore(t)
	a := mustInsertTask(t, st.Writer, taskFixture())
	b := mustInsertTask(t, st.Writer, taskFixture())
	c := mustInsertTask(t, st.Writer, taskFixture())
	block := insertBlock(t, st, a.ID, "2026-10-28", 780, 900, domain.BlockPlanned)
	op := domain.NewOpID()
	mustExec(t, st.Writer, "INSERT INTO ops (op_id, kind, actor, channel, at, scopes, revertible) VALUES (?, ?, ?, ?, ?, ?, ?)", op, "interrupt", "owner", "cli", 100, "read,write", 1)
	day := civil.MustDate("2026-10-28")
	var saved []domain.Interruption
	for _, in := range []domain.Interruption{
		{At: time.UnixMilli(300).UTC(), Date: day, FromTaskID: present(a.ID), FromBlockID: present(block), ToTaskID: b.ID, OpID: op},
		{At: time.UnixMilli(100).UTC(), Date: day, FromTaskID: present(b.ID), ToTaskID: a.ID, OpID: op},
		{At: time.UnixMilli(200).UTC(), Date: day, FromTaskID: present(c.ID), ToTaskID: b.ID, OpID: op},
		{At: time.UnixMilli(100).UTC(), Date: day, FromTaskID: present(a.ID), ToTaskID: b.ID, OpID: op, DeletedAt: present(time.UnixMilli(500).UTC())},
		{At: time.UnixMilli(400).UTC(), Date: day.AddDays(1), FromTaskID: present(a.ID), ToTaskID: b.ID, OpID: op},
		{At: time.UnixMilli(500).UTC(), Date: day.AddDays(1), FromBlockID: present(block), ToTaskID: a.ID, OpID: op},
	} {
		in.ID = 999
		if err := InsertInterruption(t.Context(), st.Writer, &in); err != nil {
			t.Fatal(err)
		}
		if in.ID <= 0 || in.ID == 999 {
			t.Fatalf("interruption ID = %d", in.ID)
		}
		got, err := GetInterruption(t.Context(), st.Reader, in.ID)
		if err != nil || got != in {
			t.Fatalf("interruption = %+v, %v; want %+v", got, err, in)
		}
		saved = append(saved, in)
	}
	if _, err := GetInterruption(t.Context(), st.Reader, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing interruption = %v", err)
	}
	got, err := InterruptionsOn(t.Context(), st.Reader, day)
	if err != nil || !reflect.DeepEqual(got, []domain.Interruption{saved[1], saved[2], saved[0]}) {
		t.Fatalf("day interruptions = %+v, %v", got, err)
	}
	got, err = InterruptionsOfTask(t.Context(), st.Reader, a.ID)
	if err != nil || !reflect.DeepEqual(got, []domain.Interruption{saved[1], saved[0], saved[4], saved[5]}) {
		t.Fatalf("task interruptions = %+v, %v", got, err)
	}
}
