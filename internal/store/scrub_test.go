package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
)

func TestScrubEntities(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	task := taskFixture()
	task.Title, task.Notes, task.Project = "secret keycloak", "secret task notes", "infra"
	task = mustInsertTask(t, st.Writer, task)
	other := mustInsertTask(t, st.Writer, taskFixture())
	now := task.CreatedAt.Add(time.Hour)
	if err := SetTaskTags(ctx, st.Writer, task.ID, []string{"db", "shared"}, now); err != nil {
		t.Fatal(err)
	}
	if err := SetTaskTags(ctx, st.Writer, other.ID, []string{"shared"}, now); err != nil {
		t.Fatal(err)
	}
	entry := entryFixture()
	entry.Title, entry.Notes, entry.TaskID = "secret entry", "secret entry notes", present(task.ID)
	entry = mustInsertEntry(t, st.Writer, entry)
	kept := entryFixture()
	kept.TaskID = present(task.ID)
	kept = mustInsertEntry(t, st.Writer, kept)
	a := insertOp(t, st, domain.OpTaskAdd)
	b := insertOp(t, st, domain.OpEntryAdd)
	c := insertOp(t, st, domain.OpTaskPlace)
	d := insertOp(t, st, domain.OpBlockOutcome)
	e := insertOp(t, st, domain.OpTaskUpdate)
	ea := insertEvent(t, st, a, domain.EntTask, int64(task.ID), domain.ActInsert, `{"title":"secret keycloak","project":"infra"}`)
	eb := insertEvent(t, st, b, domain.EntEntry, int64(entry.ID), domain.ActInsert, `{"title":"secret entry"}`)
	ec := insertEvent(t, st, c, domain.EntTriage, int64(task.ID), domain.ActAdd, `{"reason":"new","since":1,"seq":1}`)
	ed := mustInsertEvent(t, st.Writer, domain.Event{OpID: d.ID, Entity: domain.EntBlock, EntityID: 1,
		TaskID: present(task.ID), Action: domain.ActUpdate, After: json.RawMessage(`{"status":"planned"}`)})
	ee := mustInsertEvent(t, st.Writer, domain.Event{OpID: e.ID, Entity: domain.EntTaskTags, EntityID: int64(task.ID),
		Action: domain.ActSet, Before: json.RawMessage(`{"tags":["db"]}`), After: json.RawMessage(`{"tags":["db","shared"]}`)})
	// Duplicate and absent IDs must not inflate the number of changes.
	r, err := ScrubEntities(ctx, st.Writer, []domain.TaskID{task.ID, task.ID, 999}, []domain.EntryID{entry.ID, entry.ID, 999}, now)
	if err != nil || r != (ScrubResult{Rows: 2, Events: 3, Ops: 4}) || !r.Changed() {
		t.Fatalf("scrub = %+v, %v", r, err)
	}
	task.Title, task.Notes, task.Project = "[purged]", "", ""
	task.DeletedAt, task.PurgedAt = present(now), present(now)
	entry.Title, entry.Notes = "[purged]", ""
	entry.DeletedAt, entry.PurgedAt = present(now), present(now)
	requireTask(t, st.Reader, task)
	requireTask(t, st.Reader, other)
	requireEntry(t, st.Reader, entry)
	requireEntry(t, st.Reader, kept)
	var project sql.Null[string]
	if err := st.Reader.QueryRowContext(ctx, `SELECT project FROM tasks WHERE id = ?`, task.ID).Scan(&project); err != nil || project.Valid {
		t.Fatalf("purged project = %v, %v", project, err)
	}
	if tags, err := TaskTags(ctx, st.Reader, task.ID); err != nil || len(tags) != 0 {
		t.Fatalf("purged task tags = %v, %v", tags, err)
	}
	if tags, err := TaskTags(ctx, st.Reader, other.ID); err != nil || !slices.Equal(tags, []string{"shared"}) {
		t.Fatalf("other task tags = %v, %v", tags, err)
	}
	var n int
	if err := st.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM tags WHERE name = 'db'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("orphan tags = %d, %v", n, err)
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM tasks_fts WHERE tasks_fts MATCH 'secret'`,
		`SELECT COUNT(*) FROM log_entries_fts WHERE log_entries_fts MATCH 'secret'`,
	} {
		if err := st.Reader.QueryRowContext(ctx, query).Scan(&n); err != nil || n != 0 {
			t.Fatalf("secret FTS hits = %d, %v", n, err)
		}
	}
	if err := FTSIntegrityCheck(ctx, st.Writer); err != nil {
		t.Fatal(err)
	}
	ea.After = json.RawMessage(`{"title":"[purged]","project":null}`)
	eb.After = json.RawMessage(`{"title":"[purged]"}`)
	ee.Before, ee.After = json.RawMessage(`{"tags":[]}`), json.RawMessage(`{"tags":[]}`)
	for _, want := range []domain.Event{ea, eb, ec, ed, ee} {
		got, err := EventsOfOp(ctx, st.Reader, want.OpID)
		if err != nil || !reflect.DeepEqual(got, []domain.Event{want}) {
			t.Fatalf("events = %+v, %v; want %+v", got, err, want)
		}
	}
	for _, op := range []domain.OpRecord{a, b, c, d, e} {
		requirePurgedOp(t, st.Reader, op.ID, op.ID != d.ID)
	}
	if r, err := ScrubEntities(ctx, st.Writer, []domain.TaskID{task.ID}, []domain.EntryID{entry.ID}, now.Add(time.Hour)); err != nil || r.Changed() {
		t.Fatalf("second scrub = %+v, %v", r, err)
	}
	requireTask(t, st.Reader, task)
	requireEntry(t, st.Reader, entry)
	if r, err := ScrubEntities(ctx, st.Writer, nil, nil, now); err != nil || r.Changed() {
		t.Fatalf("empty scrub = %+v, %v", r, err)
	}
}

func requirePurgedOp(t *testing.T, q Q, id domain.OpID, want bool) {
	t.Helper()
	var purged, revertible bool
	if err := q.QueryRowContext(t.Context(), `SELECT purged, revertible FROM ops WHERE op_id = ?`, id).Scan(&purged, &revertible); err != nil || purged != want || revertible == want {
		t.Fatalf("op flags = %v/%v, %v; want purged %v", purged, revertible, err, want)
	}
}

func TestScrubEntitiesSequential(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	task := taskFixture()
	task.DeletedAt = present(task.CreatedAt)
	task = mustInsertTask(t, st.Writer, task)
	entry := entryFixture()
	entry.TaskID = present(task.ID)
	entry = mustInsertEntry(t, st.Writer, entry)
	op := insertOp(t, st, domain.OpTaskComplete)
	taskEvent := mustInsertEvent(t, st.Writer, domain.Event{OpID: op.ID, Entity: domain.EntTask, EntityID: int64(task.ID),
		Action: domain.ActUpdate, Before: json.RawMessage(`{"notes":"secret","rank":9007199254740993}`),
		After: json.RawMessage(`{"title":"secret retitle","notes":null,"project":null,"tags":[],"status":"done"}`)})
	entryEvent := insertEvent(t, st, op, domain.EntEntry, int64(entry.ID), domain.ActInsert, `{"title":"secret completion"}`)
	now := task.CreatedAt.Add(time.Hour)
	r, err := ScrubEntities(ctx, st.Writer, nil, []domain.EntryID{entry.ID}, now)
	if err != nil || r != (ScrubResult{Rows: 1, Events: 1, Ops: 1}) {
		t.Fatalf("entry scrub = %+v, %v", r, err)
	}
	entryEvent.After = json.RawMessage(`{"title":"[purged]"}`)
	if got, err := EventsOfOp(ctx, st.Reader, op.ID); err != nil || !reflect.DeepEqual(got, []domain.Event{taskEvent, entryEvent}) {
		t.Fatalf("entry scrub touched task event: %+v, %v", got, err)
	}
	requirePurgedOp(t, st.Reader, op.ID, true)
	r, err = ScrubEntities(ctx, st.Writer, []domain.TaskID{task.ID}, nil, now)
	if err != nil || r != (ScrubResult{Rows: 1, Events: 1}) {
		t.Fatalf("task scrub in already-purged op = %+v, %v", r, err)
	}
	taskEvent.Before = json.RawMessage(`{"notes":"","rank":9007199254740993}`)
	taskEvent.After = json.RawMessage(`{"title":"[purged]","notes":"","project":null,"tags":[],"status":"done"}`)
	if got, err := EventsOfOp(ctx, st.Reader, op.ID); err != nil || !reflect.DeepEqual(got, []domain.Event{taskEvent, entryEvent}) {
		t.Fatalf("sequential scrub events = %+v, %v", got, err)
	}
	task.Title, task.Notes, task.PurgedAt = "[purged]", "", present(now)
	requireTask(t, st.Reader, task) // The original tombstone time is preserved.
	if r, err := ScrubEntities(ctx, st.Writer, []domain.TaskID{task.ID}, []domain.EntryID{entry.ID}, now); err != nil || r.Changed() {
		t.Fatalf("sequential replay = %+v, %v", r, err)
	}
	if err := FTSIntegrityCheck(ctx, st.Writer); err != nil {
		t.Fatal(err)
	}
}

func TestScrubEventContent(t *testing.T) {
	st := newTestStore(t)
	task := mustInsertTask(t, st.Writer, taskFixture())
	op := insertOp(t, st, domain.OpTaskUpdate)
	var want []domain.Event
	changed := 0
	for _, tc := range []struct{ in, out string }{
		{`{"title":"secret"}`, `{"title":"[purged]"}`},
		{`{"notes":"secret"}`, `{"notes":""}`},
		{`{"project":"infra"}`, `{"project":null}`},
		{`{"tags":["db"]}`, `{"tags":[]}`},
		{`{"project":""}`, `{"project":null}`},
		{`{"title":null,"notes":null,"project":null,"tags":[]}`, ""},
		{`{"title":"[purged]","notes":"","project":null,"tags":[]}`, ""},
		{`{ "status": "done", "rank": 9007199254740993 }`, ""},
		{`{}`, ""},
		{"", ""},
	} {
		for _, before := range []bool{false, true} {
			action := domain.ActInsert
			if before {
				action = domain.ActRemove
			}
			e := insertEvent(t, st, op, domain.EntTask, int64(task.ID), action, tc.in)
			if tc.out != "" {
				changed++
				if before {
					e.Before = json.RawMessage(tc.out)
				} else {
					e.After = json.RawMessage(tc.out)
				}
			} else if tc.in == "" {
				e.Before, e.After = nil, nil
			}
			want = append(want, e)
		}
	}
	if r, err := ScrubEntities(t.Context(), st.Writer, []domain.TaskID{task.ID}, nil, task.CreatedAt); err != nil || r != (ScrubResult{Rows: 1, Events: changed, Ops: 1}) {
		t.Fatalf("content scrub = %+v, %v", r, err)
	}
	if got, err := EventsOfOp(t.Context(), st.Reader, op.ID); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("content scrub events = %+v, %v; want %+v", got, err, want)
	}
	if r, err := ScrubEntities(t.Context(), st.Writer, []domain.TaskID{task.ID}, nil, task.CreatedAt); err != nil || r.Changed() {
		t.Fatalf("content replay = %+v, %v", r, err)
	}
	// Replayed history may need scrubbing even when both the row and op already
	// carry purge markers. Content-free triage can also require only an op change.
	insertEvent(t, st, op, domain.EntTask, int64(task.ID), domain.ActUpdate, `{"notes":"secret restored history"}`)
	if r, err := ScrubEntities(t.Context(), st.Writer, []domain.TaskID{task.ID}, nil, task.CreatedAt); err != nil || r != (ScrubResult{Events: 1}) || !r.Changed() {
		t.Fatalf("event-only scrub = %+v, %v", r, err)
	}
	triageOp := insertOp(t, st, domain.OpTaskPlace)
	insertEvent(t, st, triageOp, domain.EntTriage, int64(task.ID), domain.ActAdd, `{"reason":"manual"}`)
	if r, err := ScrubEntities(t.Context(), st.Writer, []domain.TaskID{task.ID}, nil, task.CreatedAt); err != nil || r != (ScrubResult{Ops: 1}) || !r.Changed() {
		t.Fatalf("op-only scrub = %+v, %v", r, err)
	}
}

func TestScrubEntitiesRollback(t *testing.T) {
	st := newTestStore(t)
	task := mustInsertTask(t, st.Writer, taskFixture())
	op := insertOp(t, st, domain.OpTaskAdd)
	event := insertEvent(t, st, op, domain.EntTask, int64(task.ID), domain.ActInsert, `{"title":"task"}`)
	tx, err := st.Writer.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Error(err)
		}
	}()
	if r, err := ScrubEntities(t.Context(), tx, []domain.TaskID{task.ID}, nil, task.CreatedAt); err != nil || r != (ScrubResult{Rows: 1, Events: 1, Ops: 1}) {
		t.Fatalf("transactional scrub = %+v, %v", r, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	requireTask(t, st.Reader, task)
	requirePurgedOp(t, st.Reader, op.ID, false)
	if got, err := EventsOfOp(t.Context(), st.Reader, op.ID); err != nil || !reflect.DeepEqual(got, []domain.Event{event}) {
		t.Fatalf("rolled back events = %+v, %v", got, err)
	}
	if hits, err := Search(t.Context(), st.Reader, `"task"`, "task", 10, 0); err != nil || len(hits) != 1 {
		t.Fatalf("rolled back search = %+v, %v", hits, err)
	}
	if err := FTSIntegrityCheck(t.Context(), st.Writer); err != nil {
		t.Fatal(err)
	}
}

func TestSearch(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	for i := range 4 {
		task, entry := taskFixture(), entryFixture()
		task.Title, task.Notes = "needle", "plain"
		if i == 0 {
			task.Title, task.Notes = "plain", "needle"
		}
		entry.Title, entry.Notes = task.Title, task.Notes
		task = mustInsertTask(t, st.Writer, task)
		if i == 1 {
			entry.TaskID = present(task.ID)
		}
		entry = mustInsertEntry(t, st.Writer, entry)
		if i == 3 {
			task.DeletedAt, entry.DeletedAt = present(task.CreatedAt), present(entry.CreatedAt)
			if err := UpdateTask(ctx, st.Writer, task); err != nil {
				t.Fatal(err)
			}
			if err := UpdateEntry(ctx, st.Writer, entry); err != nil {
				t.Fatal(err)
			}
		}
	}
	all, err := Search(ctx, st.Reader, `"needle"`, "all", 20, 0)
	if err != nil || len(all) != 6 {
		t.Fatalf("search = %+v, %v", all, err)
	}
	for i, key := range []struct {
		kind string
		id   int64
	}{{"entry", 2}, {"entry", 3}, {"task", 2}, {"task", 3}, {"entry", 1}, {"task", 1}} {
		hit := all[i]
		if hit.Kind != key.kind || hit.ID != key.id || hit.Snippet != "needle" || hit.Score >= 0 {
			t.Fatalf("hit %d = %+v; want %+v with plain snippet", i, hit, key)
		}
		title := "needle"
		if hit.ID == 1 {
			title = "plain"
		}
		if hit.Title != title {
			t.Fatalf("hit title = %q; want %q", hit.Title, title)
		}
		if hit.Kind == "task" {
			if hit.Status != domain.StatusOpen || hit.WorkDate.Valid || hit.TaskID.Valid {
				t.Fatalf("task metadata = %+v", hit)
			}
		} else if hit.Status != "" || hit.WorkDate != present(civil.MustDate("2026-10-28")) || hit.TaskID.Valid != (hit.ID == 2) || (hit.TaskID.Valid && hit.TaskID.V != 2) {
			t.Fatalf("entry metadata = %+v", hit)
		}
	}
	if all[0].Score != all[3].Score || all[4].Score != all[5].Score || all[3].Score >= all[4].Score {
		t.Fatalf("title/notes ranking = %+v", all)
	}
	for _, kind := range []string{"task", "entry"} {
		got, err := Search(ctx, st.Reader, `"needle"`, kind, 20, 0)
		var want []FTSHit
		for _, hit := range all {
			if hit.Kind == kind {
				want = append(want, hit)
			}
		}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("search %s = %+v, %v; want %+v", kind, got, err, want)
		}
	}
	for offset := 0; offset <= len(all); offset += 2 {
		got, err := Search(ctx, st.Reader, `"needle"`, "all", 2, offset)
		if err != nil || !slices.Equal(got, all[offset:min(offset+2, len(all))]) {
			t.Fatalf("page at %d = %+v, %v", offset, got, err)
		}
	}
	if got, err := Search(ctx, st.Reader, `"missing"`, "all", 20, 0); err != nil || len(got) != 0 {
		t.Fatalf("no hits = %+v, %v", got, err)
	}
	if _, err := Search(ctx, st.Reader, `"needle"`, "invalid", 20, 0); err == nil {
		t.Fatal("invalid search kind succeeded")
	}
}

func TestSequences(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	if got, err := Sequences(ctx, st.Reader); err != nil || len(got) != 0 {
		t.Fatalf("initial sequences = %v, %v", got, err)
	}
	task := mustInsertTask(t, st.Writer, taskFixture())
	op := insertOp(t, st, domain.OpTaskAdd)
	want := map[string]int64{"tasks": int64(task.ID), "ops": int64(op.Seq)}
	if got, err := Sequences(ctx, st.Reader); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("sequences after inserts = %v, %v", got, err)
	}
	for _, m := range []map[string]int64{{"tasks": 100, "log_entries": 200, "tags": 0}, {"tasks": 10, "log_entries": 20, "ops": 0}, nil} {
		if err := RaiseSequences(ctx, st.Writer, m); err != nil {
			t.Fatal(err)
		}
	}
	want = map[string]int64{"tasks": 100, "log_entries": 200, "tags": 0, "ops": int64(op.Seq)}
	if got, err := Sequences(ctx, st.Reader); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("raised sequences = %v, %v", got, err)
	}
	if task := mustInsertTask(t, st.Writer, taskFixture()); task.ID != 101 {
		t.Fatalf("next task ID = %d", task.ID)
	}
	if entry := mustInsertEntry(t, st.Writer, entryFixture()); entry.ID != 201 {
		t.Fatalf("next entry ID = %d", entry.ID)
	}
	var n int
	if err := st.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_sequence WHERE name = 'tasks'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("task sequence rows = %d, %v", n, err)
	}
}

func TestExportTable(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	for _, id := range []int64{20, 3} {
		mustExec(t, st.Writer, `INSERT INTO tasks (id, title, rank, origin, created_at, updated_at) VALUES (?, ?, ?, 'owner', ?, ?)`,
			id, "export task", int64(9007199254740993), int64(1793203200123), int64(1793203200456))
		mustExec(t, st.Writer, `INSERT INTO tags (id, name, created_at) VALUES (?, ?, 123)`, id, map[int64]string{20: "db", 3: "infra"}[id])
	}
	mustExec(t, st.Writer, `INSERT INTO task_triage (task_id, reason, since, seq) VALUES (20, 'new', 1, 1), (3, 'new', 1, 1), (3, 'manual', 1, 1)`)
	mustExec(t, st.Writer, `INSERT INTO task_tags (task_id, tag_id) VALUES (20, 20), (3, 20), (3, 3)`)
	a, b := insertOp(t, st, domain.OpTaskAdd), insertOp(t, st, domain.OpTaskUpdate)
	insertEvent(t, st, a, domain.EntTask, 3, domain.ActInsert, `{"title":"export task"}`)
	for _, row := range []struct {
		actor, stack string
		seq          int
	}{{"z", "undo", 1}, {"a", "undo", 2}, {"a", "undo", 1}, {"a", "redo", 1}} {
		mustExec(t, st.Writer, `INSERT INTO undo_stack (actor, stack, seq, op_id) VALUES (?, ?, ?, ?)`, row.actor, row.stack, row.seq, a.ID)
	}
	for _, tc := range []struct {
		table string
		keys  []string
		want  [][]any
	}{
		{"tasks", []string{"id", "rank", "project", "created_at"}, [][]any{{int64(3), int64(9007199254740993), nil, int64(1793203200123)}, {int64(20), int64(9007199254740993), nil, int64(1793203200123)}}},
		{"tags", []string{"id"}, [][]any{{int64(3)}, {int64(20)}}},
		{"task_triage", []string{"task_id", "reason"}, [][]any{{int64(3), "manual"}, {int64(3), "new"}, {int64(20), "new"}}},
		{"task_tags", []string{"task_id", "tag_id"}, [][]any{{int64(3), int64(3)}, {int64(3), int64(20)}, {int64(20), int64(20)}}},
		{"ops", []string{"seq", "op_id"}, [][]any{{int64(a.Seq), a.ID.String()}, {int64(b.Seq), b.ID.String()}}},
		{"undo_stack", []string{"actor", "stack", "seq"}, [][]any{{"a", "redo", int64(1)}, {"a", "undo", int64(1)}, {"a", "undo", int64(2)}, {"z", "undo", int64(1)}}},
	} {
		t.Run(tc.table, func(t *testing.T) {
			var got [][]any
			var retained []map[string]any
			err := ExportTable(ctx, st.Reader, tc.table, func(row map[string]any) error {
				retained = append(retained, row)
				var values []any
				for _, key := range tc.keys {
					value, ok := row[key]
					if !ok {
						t.Fatalf("missing column %s", key)
					}
					values = append(values, value)
				}
				got = append(got, values)
				return nil
			})
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("export = %v, %v; want %v", got, err, tc.want)
			}
			if len(retained) > 1 && retained[0][tc.keys[0]] != got[0][0] {
				t.Fatal("export reused a row map")
			}
		})
	}
	for _, table := range ExportTables {
		if err := ExportTable(ctx, st.Reader, table, func(row map[string]any) error {
			for key, value := range row {
				switch value.(type) {
				case int64, string, nil:
				default:
					t.Fatalf("%s.%s has type %T", table, key, value)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"unknown", "auth_state", "api_tokens", "web_sessions", "idempotency", "schema_migrations", "settings", "sqlite_sequence", "tasks; DROP TABLE tasks"} {
		if err := ExportTable(ctx, st.Reader, table, func(map[string]any) error {
			t.Fatalf("callback for forbidden table %q", table)
			return nil
		}); err == nil {
			t.Fatalf("export of %q succeeded", table)
		}
	}
	stop := errors.New("stop export")
	calls := 0
	if err := ExportTable(ctx, st.Reader, "tasks", func(map[string]any) error { calls++; return stop }); !errors.Is(err, stop) || calls != 1 {
		t.Fatalf("callback error = %v, calls %d", err, calls)
	}
}

func TestExportFile(t *testing.T) {
	if ExportFormat != "taskronin-export" || ExportVersion != 1 ||
		!slices.Equal(ExportTables, []string{"tasks", "task_triage", "tags", "task_tags", "log_entries", "time_blocks", "focus", "interruptions", "ops", "events", "undo_stack"}) ||
		!slices.Equal(ExportSettings, []string{"backup_keep_daily", "backup_keep_weekly", "ai_reviewed_through"}) {
		t.Fatal("export format constants differ from A§12.4")
	}
	data, err := json.Marshal(ExportFile{Format: ExportFormat, Version: ExportVersion, SchemaVersion: 1,
		ExportedAt: "2026-10-26T09:00:00.000-06:00", DBGeneration: "generation", Sequences: map[string]int64{},
		Settings: map[string]string{}, Tables: map[string][]map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	want := []string{"format", "version", "schema_version", "exported_at", "db_generation", "sequences", "settings", "tables"}
	if len(fields) != len(want) {
		t.Fatalf("export JSON = %s", data)
	}
	for _, key := range want {
		if _, ok := fields[key]; !ok {
			t.Fatalf("missing export field %s", key)
		}
	}
}
