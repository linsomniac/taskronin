package store

import (
	"crypto/rand"
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

func opFixture(kind domain.OpKind) domain.OpRecord {
	return domain.OpRecord{ID: domain.NewOpID(), Kind: kind, Actor: "owner", Channel: domain.ChanCLI,
		At: time.UnixMilli(1793203200123).UTC(), Scopes: domain.ScopeWrite, Revertible: true, State: domain.OpActive}
}

func mustInsertOp(t *testing.T, q Q, op domain.OpRecord) domain.OpRecord {
	t.Helper()
	if err := InsertOp(t.Context(), q, &op); err != nil {
		t.Fatal(err)
	}
	return op
}

func insertOp(t *testing.T, st *Store, kind domain.OpKind) domain.OpRecord {
	t.Helper()
	return mustInsertOp(t, st.Writer, opFixture(kind))
}

func mustInsertEvent(t *testing.T, q Q, e domain.Event) domain.Event {
	t.Helper()
	if err := InsertEvent(t.Context(), q, &e); err != nil {
		t.Fatal(err)
	}
	return e
}

func insertEvent(t *testing.T, st *Store, op domain.OpRecord, ent domain.EntityKind, id int64, action domain.Action, payload string) domain.Event {
	t.Helper()
	e := domain.Event{OpID: op.ID, Entity: ent, EntityID: id, Action: action}
	if action == domain.ActRemove {
		e.Before = json.RawMessage(payload)
	} else {
		e.After = json.RawMessage(payload)
	}
	return mustInsertEvent(t, st.Writer, e)
}

func TestOpRoundTrip(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	if seq, err := LatestSeq(ctx, st.Reader); err != nil || seq != 0 {
		t.Fatalf("empty LatestSeq = %d, %v", seq, err)
	}
	a := insertOp(t, st, domain.OpTaskAdd)
	b := opFixture(domain.OpUndo)
	b.Seq, b.At, b.Via, b.RevertsOpID = 999, a.At.Add(-time.Hour), "undo", present(a.ID)
	b = mustInsertOp(t, st.Writer, b)
	if a.Seq <= 0 || b.Seq <= a.Seq || b.Seq == 999 {
		t.Fatalf("sequences = %d, %d", a.Seq, b.Seq)
	}
	for _, want := range []domain.OpRecord{a, b} {
		got, err := GetOp(ctx, st.Reader, want.ID)
		if err != nil || got != want {
			t.Fatalf("GetOp = %+v, %v; want %+v", got, err, want)
		}
	}
	if seq, err := LatestSeq(ctx, st.Reader); err != nil || seq != b.Seq {
		t.Fatalf("LatestSeq = %d, %v", seq, err)
	}
	if got, err := ActiveReversal(ctx, st.Reader, a.ID); err != nil || !got.Valid || got.V != b {
		t.Fatalf("ActiveReversal = %+v, %v", got, err)
	}
	if err := SetOpState(ctx, st.Writer, b.ID, domain.OpReverted); err != nil {
		t.Fatal(err)
	}
	if got, err := ActiveReversal(ctx, st.Reader, a.ID); err != nil || got.Valid {
		t.Fatalf("inactive reversal = %+v, %v", got, err)
	}
	b.State = domain.OpReverted
	if got, err := GetOp(ctx, st.Reader, b.ID); err != nil || got != b {
		t.Fatalf("changed op = %+v, %v", got, err)
	}
	missing := domain.NewOpID()
	if _, err := GetOp(ctx, st.Reader, missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing op = %v", err)
	}
	if err := SetOpState(ctx, st.Writer, missing, domain.OpActive); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing state update = %v", err)
	}
	bad := opFixture(domain.OpRevert)
	bad.Via = "revert"
	if err := InsertOp(ctx, st.Writer, &bad); err == nil || bad.Seq != 0 {
		t.Fatalf("unpaired reversal insert = %v, seq %d", err, bad.Seq)
	}
}

func TestCountOpsSince(t *testing.T) {
	st := newTestStore(t)
	since := time.UnixMilli(1793203200123).UTC()
	for _, tc := range []struct {
		actor  string
		offset time.Duration
	}{
		{"token:ai", -time.Millisecond}, {"token:ai", 0}, {"token:ai", 2 * time.Millisecond},
		{"owner", time.Millisecond}, {"token:ai", time.Millisecond},
	} {
		op := opFixture(domain.OpTaskAdd)
		op.Actor, op.At = tc.actor, since.Add(tc.offset)
		mustInsertOp(t, st.Writer, op)
	}
	n, oldest, err := CountOpsSince(t.Context(), st.Reader, "token:ai", since)
	if err != nil || n != 2 || !oldest.Valid || !oldest.V.Equal(since.Add(time.Millisecond)) {
		t.Fatalf("CountOpsSince = %d, %v, %v", n, oldest, err)
	}
	n, oldest, err = CountOpsSince(t.Context(), st.Reader, "absent", since)
	if err != nil || n != 0 || oldest.Valid {
		t.Fatalf("empty CountOpsSince = %d, %v, %v", n, oldest, err)
	}
}

func TestListOpsAIOnly(t *testing.T) {
	st := newTestStore(t)
	var ops []domain.OpRecord
	for i, tc := range []struct {
		actor string
		kind  domain.OpKind
	}{
		{"owner", domain.OpTaskAdd}, {"token:ai", domain.OpTaskAdd},
		{"owner", domain.OpSettingsRetentionLowered}, {"system", domain.OpSystemReconcile},
		{"token:other", domain.OpTaskPlace}, {"owner", domain.OpSettingsUpdate},
	} {
		op := opFixture(tc.kind)
		op.Actor, op.At = tc.actor, op.At.Add(-time.Duration(i)*time.Hour)
		ops = append(ops, mustInsertOp(t, st.Writer, op))
	}
	for _, tc := range []struct {
		name  string
		query OpQuery
		want  []domain.OpRecord
	}{
		{"AI", OpQuery{AIOnly: true, Limit: 20}, []domain.OpRecord{ops[4], ops[2], ops[1]}},
		{"bounds", OpQuery{AIOnly: true, SinceSeq: ops[1].Seq, BeforeSeq: ops[4].Seq, Limit: 20}, []domain.OpRecord{ops[2]}},
		{"actor", OpQuery{Actor: "owner", Limit: 20}, []domain.OpRecord{ops[5], ops[2], ops[0]}},
		{"actor and AI", OpQuery{Actor: "owner", AIOnly: true, Limit: 20}, []domain.OpRecord{ops[2]}},
		{"limit", OpQuery{Limit: 2}, []domain.OpRecord{ops[5], ops[4]}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ListOps(t.Context(), st.Reader, tc.query)
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("ListOps = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		after domain.OpSeq
		want  int
	}{{0, 3}, {ops[1].Seq, 2}, {ops[4].Seq, 0}} {
		if n, err := CountAIOps(t.Context(), st.Reader, tc.after); err != nil || n != tc.want {
			t.Fatalf("CountAIOps(%d) = %d, %v", tc.after, n, err)
		}
	}
}

func TestEventsAfterTriageKey(t *testing.T) {
	st := newTestStore(t)
	a := insertOp(t, st, domain.OpTaskAdd)
	b := insertOp(t, st, domain.OpTaskPlace)
	e1 := insertEvent(t, st, a, domain.EntTriage, 1, domain.ActAdd, `{"reason":"new","since":1,"seq":1}`)
	e2 := insertEvent(t, st, b, domain.EntTriage, 1, domain.ActRemove, `{"reason":"new","since":1,"seq":1}`)
	insertEvent(t, st, b, domain.EntTriage, 1, domain.ActAdd, `{"reason":"manual","since":2,"seq":2}`)
	insertEvent(t, st, b, domain.EntTriage, 2, domain.ActAdd, `{"reason":"new","since":2,"seq":2}`)
	insertEvent(t, st, b, domain.EntTask, 1, domain.ActUpdate, `{"title":"new"}`)
	if err := SetOpState(t.Context(), st.Writer, b.ID, domain.OpReverted); err != nil {
		t.Fatal(err)
	}
	b.State = domain.OpReverted
	got, err := EventsAfter(t.Context(), st.Reader, EventKey{Entity: domain.EntTriage, ID: 1, Reason: domain.ReasonNew}, e1.ID)
	if err != nil || !reflect.DeepEqual(got, []EventOp{{Event: e2, Op: b}}) {
		t.Fatalf("EventsAfter = %+v, %v", got, err)
	}
	e3 := insertEvent(t, st, b, domain.EntTriage, 1, domain.ActAdd, `{"reason":"new","since":3,"seq":2}`)
	got, err = EventsAfter(t.Context(), st.Reader, EventKey{Entity: domain.EntTriage, ID: 1, Reason: domain.ReasonNew}, e1.ID)
	if err != nil || !reflect.DeepEqual(got, []EventOp{{Event: e2, Op: b}, {Event: e3, Op: b}}) {
		t.Fatalf("before/after triage matches = %+v, %v", got, err)
	}
}

func TestTaskEvents(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	task := mustInsertTask(t, st.Writer, taskFixture())
	a := insertOp(t, st, domain.OpTaskAdd)
	b := insertOp(t, st, domain.OpTaskUpdate)
	e1 := mustInsertEvent(t, st.Writer, domain.Event{OpID: a.ID, Entity: domain.EntTask, EntityID: int64(task.ID),
		TaskID: present(task.ID), Action: domain.ActInsert, After: json.RawMessage(`{"title":"task"}`)})
	e2 := mustInsertEvent(t, st.Writer, domain.Event{OpID: b.ID, Entity: domain.EntTask, EntityID: int64(task.ID),
		TaskID: present(task.ID), Action: domain.ActUpdate, Before: json.RawMessage(`{"title":"task"}`), After: json.RawMessage(`{"title":"changed"}`), Derived: true})
	e3 := mustInsertEvent(t, st.Writer, domain.Event{OpID: b.ID, Entity: domain.EntEntry, EntityID: 42,
		TaskID: present(task.ID), Action: domain.ActRemove, Before: json.RawMessage(`{"title":"work"}`)})
	insertEvent(t, st, b, domain.EntTask, int64(task.ID), domain.ActUpdate, `{"notes":"no task_id"}`)
	got, err := TaskEvents(ctx, st.Reader, task.ID, 0, 2)
	if err != nil || !reflect.DeepEqual(got, []EventOp{{Event: e3, Op: b}, {Event: e2, Op: b}}) {
		t.Fatalf("TaskEvents = %+v, %v", got, err)
	}
	got, err = TaskEvents(ctx, st.Reader, task.ID, e2.ID, 2)
	if err != nil || !reflect.DeepEqual(got, []EventOp{{Event: e1, Op: a}}) {
		t.Fatalf("TaskEvents page = %+v, %v", got, err)
	}
	got, err = EventsAfter(ctx, st.Reader, EventKey{Entity: domain.EntTask, ID: int64(task.ID), Reason: domain.ReasonManual}, 0)
	if err != nil || len(got) != 3 || got[0].ID != e1.ID || got[1].ID != e2.ID || !got[1].Derived {
		t.Fatalf("non-triage EventsAfter = %+v, %v", got, err)
	}
	events, err := EventsOfOp(ctx, st.Reader, a.ID)
	if err != nil || !reflect.DeepEqual(events, []domain.Event{e1}) {
		t.Fatalf("EventsOfOp = %+v, %v", events, err)
	}
	if events, err = EventsOfOp(ctx, st.Reader, b.ID); err != nil || len(events) != 3 || !reflect.DeepEqual(events[:2], []domain.Event{e2, e3}) {
		t.Fatalf("EventsOfOp order = %+v, %v", events, err)
	}
	if ids, err := OpTaskIDs(ctx, st.Reader, b.ID); err != nil || !slices.Equal(ids, []domain.TaskID{task.ID}) {
		t.Fatalf("OpTaskIDs = %v, %v", ids, err)
	}
	var before, after sql.Null[string]
	if err := st.Reader.QueryRowContext(ctx, `SELECT before_json, after_json FROM events WHERE id = ?`, e1.ID).Scan(&before, &after); err != nil || before.Valid || !after.Valid {
		t.Fatalf("insert JSON nulls = %v/%v, %v", before, after, err)
	}
	if err := st.Reader.QueryRowContext(ctx, `SELECT before_json, after_json FROM events WHERE id = ?`, e3.ID).Scan(&before, &after); err != nil || !before.Valid || after.Valid {
		t.Fatalf("remove JSON nulls = %v/%v, %v", before, after, err)
	}
	bad := domain.Event{OpID: a.ID, Entity: domain.EntTask, EntityID: 1, Action: domain.ActUpdate, After: json.RawMessage(`invalid`)}
	if err := InsertEvent(ctx, st.Writer, &bad); err == nil || bad.ID != 0 {
		t.Fatalf("invalid event = %v, ID %d", err, bad.ID)
	}
}

func TestApplyColumns(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	task := mustInsertTask(t, st.Writer, taskFixture())
	want := fullTaskFixture()
	want.ID, want.Version, want.PurgedAt = task.ID, task.Version, task.PurgedAt
	want.Title, want.Notes, want.Rank = "restored", "restored notes", 9007199254740993
	cols := map[string]any{"title": want.Title, "notes": want.Notes, "status": "done", "rank": json.Number("9007199254740993"),
		"last_placed_at": want.LastPlacedAt.V.UnixMilli(), "last_placed_seq": 7,
		"defer_date": "2026-10-27", "defer_min": 0, "due_date": "2026-10-28", "due_min": 1439,
		"due_ack": "due", "due_ack_date": "2026-10-28", "project": "infra.keycloak", "priority": "H",
		"completed_at": want.CompletedAt.V.UnixMilli(), "completed_date": "2026-10-28", "deleted_at": want.DeletedAt.V.UnixMilli()}
	if err := ApplyColumns(ctx, st.Writer, domain.EntTask, int64(task.ID), cols); err != nil {
		t.Fatal(err)
	}
	requireTask(t, st.Reader, want)
	for _, bad := range []map[string]any{
		{"title": "must not write", "version": 4}, {"title": "must not write", "title = ? --": "unsafe"},
		{"title": "must not write", "rank": json.Number("1.5")}, {"rank": json.Number("9223372036854775808")},
		{"title": "must not write", "status": "open"}, // The completion CHECK rejects the entire UPDATE.
	} {
		if err := ApplyColumns(ctx, st.Writer, domain.EntTask, int64(task.ID), bad); err == nil {
			t.Fatal("invalid columns succeeded")
		}
		requireTask(t, st.Reader, want)
	}
	if err := ApplyColumns(ctx, st.Writer, domain.EntTask, int64(task.ID), map[string]any{"project": nil, "priority": nil}); err != nil {
		t.Fatal(err)
	}
	want.Project, want.Priority = "", ""
	requireTask(t, st.Reader, want)
	if err := ApplyColumns(ctx, st.Writer, domain.EntTask, int64(task.ID), nil); err != nil {
		t.Fatal(err)
	}
	if err := ApplyColumns(ctx, st.Writer, domain.EntTask, 999, map[string]any{"title": "absent"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing row = %v", err)
	}
	for _, ent := range []domain.EntityKind{domain.EntTriage, domain.EntTaskTags, domain.EntToken, domain.EntSetting, domain.EntAuth, "tasks; DROP TABLE tasks"} {
		if err := ApplyColumns(ctx, st.Writer, ent, 1, map[string]any{"deleted_at": nil}); err == nil {
			t.Fatalf("unknown entity %q succeeded", ent)
		}
	}
	entry := entryFixture()
	entry.Kind, entry.TaskID = domain.EntryComplete, present(task.ID)
	entry = mustInsertEntry(t, st.Writer, entry)
	if err := ApplyColumns(ctx, st.Writer, domain.EntEntry, int64(entry.ID), map[string]any{
		"task_id": int64(task.ID), "title": "entry restored", "notes": "restored", "work_date": "2026-10-27", "occurred_min": json.Number("0"), "reopened": true, "deleted_at": 123,
	}); err != nil {
		t.Fatal(err)
	}
	entry.Title, entry.Notes, entry.WorkDate, entry.OccurredMin, entry.Reopened, entry.DeletedAt = "entry restored", "restored", civil.MustDate("2026-10-27"), present(civil.Minute(0)), true, present(time.UnixMilli(123).UTC())
	requireEntry(t, st.Reader, entry)
	block := mustInsertBlock(t, st.Writer, blockFixture(task.ID, "2026-10-28", 600, 660, domain.BlockPlanned))
	if err := ApplyColumns(ctx, st.Writer, domain.EntBlock, int64(block.ID), map[string]any{
		"date": "2026-10-27", "start_min": 0, "end_min": 1440, "status": "missed", "outcome_source": "auto", "cancel_reason": nil,
		"slipped": true, "slipped_at": 123, "slipped_seq": json.Number("42"), "resolved_at": 456, "resolved_seq": 43, "deleted_at": 789,
	}); err != nil {
		t.Fatal(err)
	}
	block.Span = domain.BlockSpan{Date: civil.MustDate("2026-10-27"), Start: 0, End: 1440}
	block.Status, block.OutcomeSource, block.Slipped = domain.BlockMissed, domain.OutcomeAuto, true
	block.SlippedAt, block.SlippedSeq = present(time.UnixMilli(123).UTC()), present(domain.OpSeq(42))
	block.ResolvedAt, block.ResolvedSeq, block.DeletedAt = present(time.UnixMilli(456).UTC()), present(domain.OpSeq(43)), present(time.UnixMilli(789).UTC())
	requireBlock(t, st.Reader, block)
	if err := ApplyColumns(ctx, st.Writer, domain.EntBlock, int64(block.ID), map[string]any{"slipped": false}); err != nil {
		t.Fatal(err)
	}
	block.Slipped = false
	requireBlock(t, st.Reader, block)
	if err := ApplyColumns(ctx, st.Writer, domain.EntFocus, 1, map[string]any{"task_id": int64(task.ID), "date": "2026-10-28", "set_at": 123}); err != nil {
		t.Fatal(err)
	}
	focus, err := GetFocus(ctx, st.Reader)
	if err != nil || focus.TaskID != present(task.ID) || focus.Date != present(civil.MustDate("2026-10-28")) || focus.SetAt != present(time.UnixMilli(123).UTC()) || focus.Version != 1 {
		t.Fatalf("focus = %+v, %v", focus, err)
	}
	op := insertOp(t, st, domain.OpFocusSet)
	in := domain.Interruption{At: task.CreatedAt, Date: civil.MustDate("2026-10-28"), FromTaskID: present(task.ID), ToTaskID: task.ID, OpID: op.ID}
	if err := InsertInterruption(ctx, st.Writer, &in); err != nil {
		t.Fatal(err)
	}
	if err := ApplyColumns(ctx, st.Writer, domain.EntInterruption, int64(in.ID), map[string]any{"deleted_at": 123}); err != nil {
		t.Fatal(err)
	}
	in.DeletedAt = present(time.UnixMilli(123).UTC())
	if got, err := GetInterruption(ctx, st.Reader, in.ID); err != nil || got != in {
		t.Fatalf("interruption = %+v, %v", got, err)
	}
	for _, tc := range []struct {
		entity domain.EntityKind
		id     int64
		cols   map[string]any
	}{
		{domain.EntEntry, int64(entry.ID), map[string]any{"kind": "work"}},
		{domain.EntBlock, int64(block.ID), map[string]any{"task_id": int64(task.ID)}},
		{domain.EntFocus, 1, map[string]any{"version": 2}},
		{domain.EntInterruption, int64(in.ID), map[string]any{"at": 456}},
	} {
		if err := ApplyColumns(ctx, st.Writer, tc.entity, tc.id, tc.cols); err == nil {
			t.Fatalf("forbidden %s columns succeeded", tc.entity)
		}
	}
}

func TestIdem(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	op := insertOp(t, st, domain.OpTaskAdd)
	r := IdemRow{Actor: "owner", Key: rand.Text(), OpID: present(op.ID), Status: 201,
		Refs: []domain.EntityRef{{Entity: domain.EntTask, ID: 42}}, CreatedAt: op.At}
	if _, err := rand.Read(r.Hash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := GetIdem(ctx, st.Reader, r.Actor, r.Key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing idem = %v", err)
	}
	if err := PutIdem(ctx, st.Writer, r); err != nil {
		t.Fatal(err)
	}
	if got, err := GetIdem(ctx, st.Reader, r.Actor, r.Key); err != nil || !reflect.DeepEqual(got, r) {
		t.Fatalf("idempotency round trip failed: %v", err)
	}
	var refs string
	if err := st.Reader.QueryRowContext(ctx, `SELECT entity_refs FROM idempotency WHERE actor = ? AND key = ?`, r.Actor, r.Key).Scan(&refs); err != nil || refs != `[{"entity":"task","id":42}]` {
		t.Fatalf("stored refs = %q, %v", refs, err)
	}
	r.Status, r.OpID, r.ErrorJSON = 409, sql.Null[domain.OpID]{}, []byte(`{"code":"revert_conflict","detail":"changed","conflicts":[{"id":42}]}`)
	if err := PutIdem(ctx, st.Writer, r); err != nil {
		t.Fatal(err)
	}
	if got, err := GetIdem(ctx, st.Reader, r.Actor, r.Key); err != nil || !reflect.DeepEqual(got, r) {
		t.Fatalf("idempotency replace failed: %v", err)
	}
	other := r
	other.Actor, other.Status, other.ErrorJSON, other.Refs = "token:ai", 200, nil, []domain.EntityRef{}
	other.CreatedAt = r.CreatedAt.Add(time.Millisecond)
	if err := PutIdem(ctx, st.Writer, other); err != nil {
		t.Fatal(err)
	}
	if got, err := GetIdem(ctx, st.Reader, other.Actor, other.Key); err != nil || !reflect.DeepEqual(got, other) {
		t.Fatalf("stored no-op/actor isolation failed: %v", err)
	}
	if n, err := DeleteIdemBefore(ctx, st.Writer, r.CreatedAt); err != nil || n != 0 {
		t.Fatalf("boundary delete = %d, %v", n, err)
	}
	if n, err := DeleteIdemBefore(ctx, st.Writer, other.CreatedAt); err != nil || n != 1 {
		t.Fatalf("expiry delete = %d, %v", n, err)
	}
	if _, err := GetIdem(ctx, st.Reader, r.Actor, r.Key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired idem = %v", err)
	}
	if err := ClearIdempotency(ctx, st.Writer); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM idempotency`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("cleared idempotency = %d, %v", n, err)
	}
}

func TestUndoStack(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	ops := []domain.OpRecord{insertOp(t, st, domain.OpTaskAdd), insertOp(t, st, domain.OpTaskPlace), insertOp(t, st, domain.OpTaskUpdate)}
	if seq, op, ok, err := TopOfStack(ctx, st.Reader, "owner", StackUndo); err != nil || ok || seq != 0 || !op.IsZero() {
		t.Fatalf("empty top = %d, %s, %v, %v", seq, op, ok, err)
	}
	for _, op := range ops {
		if err := PushStack(ctx, st.Writer, "owner", StackUndo, op.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		actor string
		stack Stack
	}{{"token:ai", StackUndo}, {"owner", StackRedo}} {
		if err := PushStack(ctx, st.Writer, tc.actor, tc.stack, ops[0].ID); err != nil {
			t.Fatal(err)
		}
	}
	if seq, op, ok, err := TopOfStack(ctx, st.Reader, "owner", StackUndo); err != nil || !ok || seq != 3 || op != ops[2].ID {
		t.Fatalf("top = %d, %s, %v, %v", seq, op, ok, err)
	}
	if err := DeleteStackRow(ctx, st.Writer, "owner", StackUndo, 2); err != nil {
		t.Fatal(err)
	}
	if err := PushStack(ctx, st.Writer, "owner", StackUndo, ops[1].ID); err != nil {
		t.Fatal(err)
	}
	if seq, op, ok, err := TopOfStack(ctx, st.Reader, "owner", StackUndo); err != nil || !ok || seq != 4 || op != ops[1].ID {
		t.Fatalf("max+1 top = %d, %s, %v, %v", seq, op, ok, err)
	}
	if err := TrimStack(ctx, st.Writer, "owner", StackUndo, 2); err != nil {
		t.Fatal(err)
	}
	if got, err := StackOps(ctx, st.Reader, "owner", StackUndo); err != nil || !slices.Equal(got, []domain.OpID{ops[2].ID, ops[1].ID}) {
		t.Fatalf("trimmed stack = %v, %v", got, err)
	}
	if err := ClearStack(ctx, st.Writer, "owner", StackUndo); err != nil {
		t.Fatal(err)
	}
	if got, err := StackOps(ctx, st.Reader, "owner", StackUndo); err != nil || len(got) != 0 {
		t.Fatalf("cleared stack = %v, %v", got, err)
	}
	for _, tc := range []struct {
		actor string
		stack Stack
	}{{"token:ai", StackUndo}, {"owner", StackRedo}} {
		if got, err := StackOps(ctx, st.Reader, tc.actor, tc.stack); err != nil || !slices.Equal(got, []domain.OpID{ops[0].ID}) {
			t.Fatalf("isolated stack = %v, %v", got, err)
		}
	}
	if err := TrimStack(ctx, st.Writer, "owner", StackRedo, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := TopOfStack(ctx, st.Reader, "owner", StackRedo); err != nil || ok {
		t.Fatalf("trim zero = %v, %v", ok, err)
	}
}
