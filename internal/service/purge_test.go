package service_test

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"taskronin/internal/backup"
	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/store"
	"taskronin/internal/testutil"
)

func purgeScenario(t *testing.T) (*testutil.Scenario, *backup.Ledger) {
	t.Helper()
	var ledger *backup.Ledger
	s := testutil.NewScenario(t, func(o *service.Options) { ledger = o.Ledger })
	return s, ledger
}

func purgeFixture(t *testing.T, s *testutil.Scenario, deleted bool) (add, log domain.OpID) {
	t.Helper()
	s.At("10-23 09:00")
	s.SeqFloor("tasks", 42)
	id := s.Add("Set up keycloak postgres cluster", func(in *service.AddTaskIn) {
		in.Notes, in.Project, in.Tags = "keycloak task notes", "infra.keycloak", []string{"keycloak"}
	})
	add = s.Last.OpID
	s.SeqFloor("log_entries", 100)
	w := s.Log(id, "keycloak work one", "10-23")
	log = s.Last.OpID
	s.At("10-26 10:00").Done(id, func(in *service.CompleteIn) { in.LogNotes = "keycloak completion notes" })
	c := s.Last.Entry.ID
	w2 := s.Log(id, "keycloak work two", "10-26")
	if id != 42 || w != 100 || c != 101 || w2 != 102 {
		t.Fatalf("fixture IDs = %d, %d, %d, %d", id, w, c, w2)
	}
	s.At(testutil.DefaultNow)
	if deleted {
		s.Delete(id)
	}
	return add, log
}

func purge(t *testing.T, s *testutil.Scenario, target string, id int64, withEntries bool) service.PurgeLiveResult {
	t.Helper()
	r, err := s.Svc.PurgeLive(s.Ctx(), s.Meta(), service.PurgeLiveIn{Target: target, ID: id, WithEntries: withEntries})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func purgeRecords(t *testing.T, ledger *backup.Ledger) []backup.LedgerRecord {
	t.Helper()
	recs, err := ledger.Records()
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func latestPurgeOp(t *testing.T, s *testutil.Scenario, kind domain.OpKind) domain.OpRecord {
	t.Helper()
	ops, err := store.ListOps(s.Ctx(), s.Store.Reader, store.OpQuery{Limit: 1})
	if err != nil || len(ops) != 1 || ops[0].Kind != kind {
		t.Fatalf("latest op = %+v, %v; want %s", ops, err, kind)
	}
	return ops[0]
}

func requirePurgeEvent(t *testing.T, s *testutil.Scenario, op domain.OpID, entity domain.EntityKind, id int64, payload string) {
	t.Helper()
	var purges []domain.Event
	for _, e := range s.Events(op) {
		if e.Action == domain.ActPurge {
			purges = append(purges, e)
		}
	}
	if len(purges) != 1 || purges[0].Entity != entity || purges[0].EntityID != id ||
		len(purges[0].Before) != 0 || string(purges[0].After) != payload || purges[0].Derived {
		t.Fatalf("purge events = %+v; want %s", purges, payload)
	}
}

func purgeEntry(t *testing.T, s *testutil.Scenario, id domain.EntryID) domain.LogEntry {
	t.Helper()
	e, err := store.GetEntry(s.Ctx(), s.Store.Reader, id)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func requireScrubbed(t *testing.T, s *testutil.Scenario, tasks []int64, entries []int64) {
	t.Helper()
	for _, id := range tasks {
		v := s.Task(domain.TaskID(id))
		if v.Title != "[purged]" || v.Notes != "" || v.Project != "" || !v.DeletedAt.Valid || !v.PurgedAt.Valid || v.View != domain.ViewDeleted || len(v.Tags) != 0 {
			t.Fatalf("task not scrubbed: %+v", v)
		}
	}
	for _, id := range entries {
		e := purgeEntry(t, s, domain.EntryID(id))
		if e.Title != "[purged]" || e.Notes != "" || !e.DeletedAt.Valid || !e.PurgedAt.Valid {
			t.Fatalf("entry not scrubbed: %+v", e)
		}
	}
}

func TestPRG04(t *testing.T) {
	s, ledger := purgeScenario(t)
	purgeFixture(t, s, false)
	count := s.OpCount()
	for _, in := range []service.PurgeLiveIn{{Target: "task", ID: 42}, {Target: "entry", ID: 100}} {
		_, err := s.Svc.PurgeLive(s.Ctx(), s.Meta(), in)
		testutil.RequireCode(t, err, domain.CodeConflict)
		de, _ := domain.AsError(err)
		if !strings.Contains(de.Detail, "delete it first") {
			t.Fatal(de)
		}
	}
	for _, target := range []string{"task", "entry"} {
		_, err := s.Svc.PurgeLive(s.Ctx(), s.Meta(), service.PurgeLiveIn{Target: target, ID: 999})
		testutil.RequireCode(t, err, domain.CodeNotFound)
	}
	if len(purgeRecords(t, ledger)) != 0 || s.OpCount() != count || s.Task(42).PurgedAt.Valid {
		t.Fatal("refused purge changed state")
	}
	s.Delete(42)
	_, err := s.Svc.PurgeLive(s.Ctx(), s.Meta(), service.PurgeLiveIn{Target: "entry", ID: 101})
	testutil.RequireCode(t, err, domain.CodeConflict)
	de, _ := domain.AsError(err)
	if !strings.Contains(de.Detail, "use tkr purge task 42 --with-entries") || len(purgeRecords(t, ledger)) != 0 {
		t.Fatalf("deleted-task hint = %v", err)
	}
}

func TestPRG05(t *testing.T) {
	s, ledger := purgeScenario(t)
	purgeFixture(t, s, true)
	count := s.OpCount()
	r := s.Must(s.Svc.ReplayLedger(s.Ctx(), nil, service.ReplayLive))
	if !r.NoChange || s.OpCount() != count {
		t.Fatal("empty replay recorded an operation")
	}
	m := s.Meta()
	m.Idem.Key = "ignored"
	got, err := s.Svc.PurgeLive(s.Ctx(), m, service.PurgeLiveIn{Target: "task", ID: 42})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Changed || !slices.Equal(got.TaskIDs, []int64{42}) || !slices.Equal(got.EntryIDs, []int64{101}) || !slices.Equal(got.LinkedNotIncluded, []int64{100, 102}) {
		t.Fatalf("result = %+v", got)
	}
	recs := purgeRecords(t, ledger)
	if len(recs) != 1 {
		t.Fatal(recs)
	}
	rec := recs[0]
	random, err := hex.DecodeString(rec.Rec)
	if err != nil || len(random) != 16 || rec.V != 1 || rec.At != s.Clock.Now().UnixMilli() || rec.State != backup.LedgerPending ||
		!slices.Equal(rec.Tasks, []domain.TaskID{42}) || !slices.Equal(rec.Entries, []domain.EntryID{101}) {
		t.Fatalf("record = %+v; %v", rec, err)
	}
	data, err := os.ReadFile(ledger.Path)
	if err != nil || strings.Count(string(data), "\n") != 1 || !strings.Contains(string(data), `"state":"pending"`) {
		t.Fatalf("ledger = %s; %v", data, err)
	}
	op := latestPurgeOp(t, s, domain.OpPurgeTask)
	if op.Actor != "owner" || op.Channel != domain.ChanCLI || op.Revertible || op.Scopes != domain.ScopeDestroy || s.OpCount() != count+1 {
		t.Fatal(op)
	}
	requirePurgeEvent(t, s, op.ID, domain.EntTask, 42, `{"ids":[42,101]}`)
	requireEventKeys(t, s, op.ID, "task:purge 42")
	requireNoIdem(t, s, m)
	requireScrubbed(t, s, got.TaskIDs, got.EntryIDs)
	for _, id := range []domain.EntryID{100, 102} {
		if e := purgeEntry(t, s, id); e.PurgedAt.Valid || e.DeletedAt.Valid || !strings.Contains(e.Title, "keycloak") {
			t.Fatal(e)
		}
	}
}

func TestPRG06(t *testing.T) {
	t.Run("with entries", func(t *testing.T) {
		s, _ := purgeScenario(t)
		purgeFixture(t, s, true)
		r := purge(t, s, "task", 42, true)
		if !r.Changed || !slices.Equal(r.TaskIDs, []int64{42}) || !slices.Equal(r.EntryIDs, []int64{100, 101, 102}) || len(r.LinkedNotIncluded) != 0 {
			t.Fatal(r)
		}
		requireScrubbed(t, s, r.TaskIDs, r.EntryIDs)
	})
	t.Run("entry only", func(t *testing.T) {
		s, _ := purgeScenario(t)
		purgeFixture(t, s, true)
		before := s.Task(42)
		s.DeleteEntry(100)
		r := purge(t, s, "entry", 100, false)
		if !r.Changed || len(r.TaskIDs) != 0 || !slices.Equal(r.EntryIDs, []int64{100}) || !reflect.DeepEqual(before, s.Task(42)) {
			t.Fatal(r)
		}
		requireScrubbed(t, s, nil, r.EntryIDs)
		op := latestPurgeOp(t, s, domain.OpPurgeEntry)
		requirePurgeEvent(t, s, op.ID, domain.EntEntry, 100, `{"ids":[100]}`)
	})
}

func TestPRG07(t *testing.T) {
	s, _ := purgeScenario(t)
	add, log := purgeFixture(t, s, true)
	before := append(s.Events(add), s.Events(log)...)
	purge(t, s, "task", 42, true)
	requireScrubbed(t, s, []int64{42}, []int64{100, 101, 102})
	for _, old := range before {
		for _, after := range s.Events(old.OpID) {
			if old.ID != after.ID {
				continue
			}
			for i, image := range []json.RawMessage{old.Before, old.After} {
				if len(image) == 0 {
					continue
				}
				var want, got map[string]any
				if err := json.Unmarshal(image, &want); err != nil {
					t.Fatal(err)
				}
				if old.Entity == domain.EntTask || old.Entity == domain.EntEntry || old.Entity == domain.EntTaskTags {
					for k, v := range map[string]any{"title": "[purged]", "notes": "", "project": nil, "tags": []any{}} {
						if _, ok := want[k]; ok {
							want[k] = v
						}
					}
				}
				if err := json.Unmarshal([]json.RawMessage{after.Before, after.After}[i], &got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("event payload = %v, want %v", got, want)
				}
			}
		}
	}
	var n int
	if err := s.Store.Reader.QueryRowContext(s.Ctx(), `SELECT COUNT(*) FROM events WHERE before_json LIKE '%keycloak%' OR after_json LIKE '%keycloak%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d events retain content", n)
	}
	if err := s.Store.Reader.QueryRowContext(s.Ctx(), `SELECT COUNT(*) FROM ops WHERE (purged <> 1 OR revertible <> 0) AND op_id IN (SELECT op_id FROM events WHERE (entity IN ('task','task_tags','triage') AND entity_id = 42) OR (entity = 'entry' AND entity_id IN (100,101,102)))`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d affected ops not marked purged", n)
	}
	if err := s.Store.Reader.QueryRowContext(s.Ctx(), `SELECT COUNT(*) FROM tags`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("orphaned tags remain")
	}
	if err := store.FTSIntegrityCheck(s.Ctx(), s.Store.Writer); err != nil {
		t.Fatal(err)
	}
	if err := service.CheckAll(s.Ctx(), s.Store.Reader, s.Clock.Now(), testutil.Zone); err != nil {
		t.Fatal(err)
	}
}

func TestPRG09(t *testing.T) {
	s, ledger := purgeScenario(t)
	purgeFixture(t, s, true)
	before, count, task := s.StateDigest(), s.OpCount(), s.Task(42)
	failure := errors.New("injected failure after append")
	service.SetPurgeAfterAppendHook(s.Svc, func() error {
		if len(purgeRecords(t, ledger)) != 1 || purgeEntry(t, s, 101).PurgedAt.Valid {
			t.Error("append must precede scrub")
		}
		return failure
	})
	_, err := s.Svc.PurgeLive(s.Ctx(), s.Meta(), service.PurgeLiveIn{Target: "task", ID: 42})
	testutil.RequireCode(t, err, domain.CodeInternal)
	if !errors.Is(err, failure) || s.StateDigest() != before || s.OpCount() != count || !reflect.DeepEqual(task, s.Task(42)) {
		t.Fatalf("failure changed DB or lost cause: %v", err)
	}
	service.SetPurgeAfterAppendHook(s.Svc, nil)
	if err := s.Svc.ReapplyLedger(s.Ctx()); err != nil {
		t.Fatal(err)
	}
	requireScrubbed(t, s, []int64{42}, []int64{101})
	op := latestPurgeOp(t, s, domain.OpSystemLedgerReplay)
	if op.Actor != "system" || op.Channel != domain.ChanSystem || op.Scopes != 0 || op.Revertible || s.OpCount() != count+1 {
		t.Fatal(op)
	}
	requirePurgeEvent(t, s, op.ID, domain.EntTask, 42, `{"ids":[42,101]}`)
	if recs := purgeRecords(t, ledger); len(recs) != 1 || recs[0].State != backup.LedgerPending {
		t.Fatal(recs)
	}
}

func TestPRG10(t *testing.T) {
	live, ledger := purgeScenario(t)
	purgeFixture(t, live, true)
	purge(t, live, "task", 42, false)
	s := testutil.NewScenario(t).At("10-26 08:00")
	s.SeqFloor("tasks", 42)
	id := s.Add("historical keycloak")
	s.SeqFloor("time_blocks", 9)
	b9 := s.Block(id, "10-27 09:00-10:00")
	other := s.Add("unrelated historical task")
	b10 := s.Block(other, "10-26 09:00-10:00")
	s.SetFocus(id, false)
	// AIDEV-NOTE: F14 fixture shortcut: Block/SetFocus clear new; the catalog
	// snapshot has both a planned block and new, so restore that reason only.
	s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), nil, func(q store.Q) error {
		_, err := store.AddReason(s.Ctx(), q, domain.TriageRow{TaskID: id, Reason: domain.ReasonNew,
			Since: s.Clock.Now(), Seq: s.Op(s.Last.OpID).Seq})
		return err
	}))
	focus := s.Focus()
	untouched, err := store.GetBlock(s.Ctx(), s.Store.Reader, b10)
	if err != nil {
		t.Fatal(err)
	}
	s.At(testutil.DefaultNow)
	count := s.OpCount()
	r := s.Must(s.Svc.ReplayLedger(s.Ctx(), purgeRecords(t, ledger), service.ReplayHistorical))
	if r.NoChange || s.OpCount() != count+1 || b9 != 9 {
		t.Fatal(r)
	}
	b, err := store.GetBlock(s.Ctx(), s.Store.Reader, b9)
	if err != nil || b.Status != domain.BlockCancelled || b.OutcomeSource != "" || b.Slipped || b.CancelReason != domain.CancelTaskClosed || b.SlippedAt.Valid || b.SlippedSeq.Valid {
		t.Fatalf("B9 = %+v, %v", b, err)
	}
	got, err := store.GetBlock(s.Ctx(), s.Store.Reader, b10)
	if err != nil || !reflect.DeepEqual(got, untouched) {
		t.Fatalf("historical block changed: %+v, %v", got, err)
	}
	reasons, err := store.Reasons(s.Ctx(), s.Store.Reader, id)
	if err != nil || len(reasons) != 0 || !focus.TaskID.Valid || s.Focus().TaskID.Valid {
		t.Fatalf("reasons = %v, focus = %+v, %v", reasons, s.Focus(), err)
	}
	v, err := store.GetTask(s.Ctx(), s.Store.Reader, id)
	if err != nil || v.Title != "[purged]" || !v.DeletedAt.Valid || !v.PurgedAt.Valid {
		t.Fatalf("historical task = %+v, %v", v, err)
	}
	requirePurgeEvent(t, s, r.OpID, domain.EntTask, 42, `{"ids":[42]}`)
	requireEventKeys(t, s, r.OpID, "block:update 9", "triage:remove 42 new", "focus:set", "task:tombstone 42", "task:purge 42")
	if err := service.CheckStructural(s.Ctx(), s.Store.Reader); err != nil {
		t.Fatal(err)
	}
	// Preserve the historical state for the fixture's structural-only cleanup.
	s.Svc.BeginShutdown()
}

func TestPRG11(t *testing.T) {
	s, ledger := purgeScenario(t)
	purgeFixture(t, s, true)
	purge(t, s, "task", 42, false)
	count, before := s.OpCount(), s.StateDigest()
	for range 2 {
		if err := s.Svc.ReapplyLedger(s.Ctx()); err != nil {
			t.Fatal(err)
		}
		if s.OpCount() != count || s.StateDigest() != before {
			t.Fatal("committed scrub replay was not a true no-op")
		}
	}
	if recs := purgeRecords(t, ledger); len(recs) != 1 || recs[0].State != backup.LedgerPending {
		t.Fatal(recs)
	}
}

func TestPRG16(t *testing.T) {
	s, ledger := purgeScenario(t)
	purgeFixture(t, s, true)
	purge(t, s, "task", 42, false)
	count := s.OpCount()
	r := s.Must(s.Svc.ReplayLedger(s.Ctx(), purgeRecords(t, ledger), service.ReplayLive))
	if !r.NoChange {
		t.Fatal(r)
	}
	got := purge(t, s, "task", 42, false)
	if got.Changed || s.OpCount() != count || len(purgeRecords(t, ledger)) != 1 ||
		!slices.Equal(got.TaskIDs, []int64{42}) || !slices.Equal(got.EntryIDs, []int64{101}) || !slices.Equal(got.LinkedNotIncluded, []int64{100, 102}) {
		t.Fatal(got)
	}
	// A larger set is a new intent, even though the task itself is purged.
	got = purge(t, s, "task", 42, true)
	if !got.Changed || len(purgeRecords(t, ledger)) != 2 {
		t.Fatal(got)
	}
	requireScrubbed(t, s, got.TaskIDs, got.EntryIDs)
}

func TestPRG17(t *testing.T) {
	s, _ := purgeScenario(t)
	add, log := purgeFixture(t, s, true)
	purge(t, s, "task", 42, true)
	for _, id := range []domain.OpID{add, log} {
		_, err := s.Svc.Revert(s.Ctx(), s.Meta(), service.RevertIn{OpID: id})
		testutil.RequireCode(t, err, domain.CodeNotRevertible)
	}
	// Pop the later purged owner operations until A is on top, using real Undo.
	for stack := s.Stack("owner", store.StackUndo); len(stack) > 1; stack = s.Stack("owner", store.StackUndo) {
		_, err := s.Svc.Undo(s.Ctx(), s.Meta())
		testutil.RequireCode(t, err, domain.CodeNotRevertible)
	}
	requireStacks(t, s, []domain.OpID{add}, nil)
	requireCommittedReplay(t, s, false, domain.CodeNotRevertible, "operation is not revertible", nil, nil)
	history, err := s.Svc.History(s.Ctx(), s.Actor, service.HistoryIn{TaskID: 42, Limit: 500})
	if err != nil || len(history.Items) == 0 {
		t.Fatalf("history = %+v, %v", history, err)
	}
	body, err := json.Marshal(history)
	if err != nil || strings.Contains(string(body), "keycloak") || !strings.Contains(string(body), "[purged]") {
		t.Fatalf("history retained content: %s, %v", body, err)
	}
}

func TestPRG19(t *testing.T) {
	s, _ := purgeScenario(t)
	purgeFixture(t, s, true)
	purge(t, s, "task", 42, true)
	r := readReport(t, s, catalogReportIn())
	if len(r.Days) != 0 || len(r.Tasks) != 0 {
		t.Fatal(r)
	}
	in := service.ListEntriesIn{From: testutil.D("10-23"), To: testutil.D("10-28")}
	page, err := s.Svc.ListEntries(s.Ctx(), s.Actor, in)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("live entries = %+v, %v", page, err)
	}
	in.IncludeDeleted = true
	page, err = s.Svc.ListEntries(s.Ctx(), s.Actor, in)
	if err != nil || len(page.Items) != 3 {
		t.Fatalf("deleted entries = %+v, %v", page, err)
	}
	for _, e := range page.Items {
		if e.Title != "[purged]" || e.Notes != "" || !e.DeletedAt.Valid || !e.PurgedAt.Valid {
			t.Fatal(e)
		}
	}
	requireScrubbed(t, s, []int64{42}, []int64{100, 101, 102})
	if hits := readSearch(t, s, service.SearchIn{Query: "keycloak"}); len(hits.Items) != 0 {
		t.Fatal(hits)
	}
}

func TestRPT07(t *testing.T) {
	s, _ := purgeScenario(t)
	s.At("10-23 08:00")
	s.SeqFloor("tasks", 7)
	id := s.Add("Write report")
	e := s.Log(id, "Drafted outline", "10-23")
	s.Update(id, func(in *service.UpdateTaskIn) { in.Title = ptr("Current report title") })
	s.Delete(id)
	purgeFixture(t, s, true)
	purge(t, s, "task", 42, false)
	r := readReport(t, s, catalogReportIn())
	var ids []domain.EntryID
	for _, day := range r.Days {
		for _, line := range day.Lines {
			ids = append(ids, line.EntryID)
			if line.EntryID == 100 && line.Title != "keycloak work one" || line.EntryID == 102 && line.Title != "keycloak work two" {
				t.Fatal(line)
			}
		}
	}
	if !slices.Equal(ids, []domain.EntryID{e, 100, 102}) || r.Tasks[7].Title != "Current report title" || !r.Tasks[7].Deleted || r.Tasks[42].Title != "[purged]" {
		t.Fatalf("report = %+v; ids = %v", r, ids)
	}
	purge(t, s, "task", 42, true)
	r = readReport(t, s, catalogReportIn())
	if len(r.Days) != 1 || len(r.Days[0].Lines) != 1 || r.Days[0].Lines[0].EntryID != e {
		t.Fatal(r)
	}
}

func TestUND13(t *testing.T) {
	s, _ := purgeScenario(t)
	id := s.Add("task")
	b := s.Last.OpID
	e := s.Log(id, "entry", "")
	a := s.Last.OpID
	s.As(testutil.TokenActor("deleter", domain.AllScopes)).DeleteEntry(e)
	purge(t, s, "entry", int64(e), false)
	_, err := s.Svc.Revert(s.Ctx(), s.Meta(), service.RevertIn{OpID: a})
	testutil.RequireCode(t, err, domain.CodeNotRevertible)
	requireCommittedReplay(t, s, false, domain.CodeNotRevertible, "operation is not revertible", []domain.OpID{b}, nil)
}

func TestUND26(t *testing.T) {
	s, _ := purgeScenario(t)
	s.Log(0, "B", "")
	b := s.Last.OpID
	e := s.Log(0, "A", "")
	a := s.Last.OpID
	s.As(testutil.TokenActor("deleter", domain.AllScopes)).DeleteEntry(e)
	purge(t, s, "entry", int64(e), false)
	requireStacks(t, s, []domain.OpID{b, a}, nil)
	requireCommittedReplay(t, s, false, domain.CodeNotRevertible, "operation is not revertible", []domain.OpID{b}, nil)
	r := s.Undo()
	requireReversal(t, s, r, b, domain.OpUndo, "undo")
	requireStacks(t, s, nil, []domain.OpID{b})
}

func completionEntryID(t *testing.T, s *testutil.Scenario, id domain.TaskID) domain.EntryID {
	t.Helper()
	entries, err := store.CompleteEntries(s.Ctx(), s.Store.Reader, id)
	if err != nil || len(entries) == 0 {
		t.Fatalf("completion entries = %v, %v", entries, err)
	}
	return entries[len(entries)-1].ID
}

func ptr[T any](v T) *T { return &v }

func TestI4TwoPurgedCompletions(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "keep", true: "remove"}[remove], func(t *testing.T) {
			s := testutil.NewScenario(t)
			id := s.Add("task")
			s.Done(id)
			s.Reopen(id, true)
			first := completionEntryID(t, s, id)
			purge(t, s, "entry", int64(first), false)
			s.Done(id)
			second := completionEntryID(t, s, id)
			s.Must(s.Svc.ReplayLedger(s.Ctx(), []backup.LedgerRecord{{Entries: []domain.EntryID{second}}}, service.ReplayLive))
			if err := service.CheckAll(s.Ctx(), s.Store.Reader, s.Clock.Now(), testutil.Zone); err != nil {
				t.Fatal(err)
			}
			if s.Task(id).Status != domain.StatusDone {
				t.Fatal("purging completion changed task status")
			}
			before := purgeEntry(t, s, second)
			s.Reopen(id, remove)
			after := purgeEntry(t, s, second)
			if !after.Reopened || after.Title != before.Title || after.Notes != before.Notes || after.DeletedAt != before.DeletedAt || after.PurgedAt != before.PurgedAt {
				t.Fatal(after)
			}
		})
	}
}

func TestPurgedEntitiesImmutable(t *testing.T) {
	s, _ := purgeScenario(t)
	purgeFixture(t, s, true)
	purge(t, s, "task", 42, true)
	count := s.OpCount()
	calls := []func() (*service.OpResult, error){
		func() (*service.OpResult, error) {
			return s.Svc.Undelete(s.Ctx(), s.Meta(), service.TaskRef{ID: 42, Expect: s.Expect(42)})
		},
		func() (*service.OpResult, error) {
			return s.Svc.UpdateTask(s.Ctx(), s.Meta(), service.UpdateTaskIn{ID: 42, Expect: s.Expect(42), Title: ptr("restored")})
		},
	}
	for _, id := range []domain.EntryID{100, 101} {
		calls = append(calls,
			func() (*service.OpResult, error) {
				return s.Svc.UpdateEntry(s.Ctx(), s.Meta(), service.UpdateEntryIn{ID: id, Expect: s.EntryExpect(id), Notes: ptr("restored")})
			},
			func() (*service.OpResult, error) {
				return s.Svc.RelinkEntry(s.Ctx(), s.Meta(), service.RelinkEntryIn{ID: id, Expect: s.EntryExpect(id)})
			},
			func() (*service.OpResult, error) {
				return s.Svc.UndeleteEntry(s.Ctx(), s.Meta(), service.EntryRef{ID: id, Expect: s.EntryExpect(id)})
			},
		)
	}
	for _, call := range calls {
		_, err := call()
		testutil.RequireCode(t, err, domain.CodeConflict)
		de, _ := domain.AsError(err)
		if !strings.Contains(de.Detail, "purged") {
			t.Fatal(de)
		}
	}
	if s.OpCount() != count {
		t.Fatal("refused edit wrote an op")
	}
	requireScrubbed(t, s, []int64{42}, []int64{100, 101, 102})
}

func TestSequentialPurgesLeaveNoSecret(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("plain")
	s.Done(id, func(in *service.CompleteIn) { in.As = ptr("hunter2-secret") })
	s.Reopen(id, true)
	purge(t, s, "entry", int64(completionEntryID(t, s, id)), false)
	s.Delete(id)
	purge(t, s, "task", int64(id), true)
	var n int
	if err := s.Store.Reader.QueryRowContext(s.Ctx(), `SELECT COUNT(*) FROM events WHERE before_json LIKE '%hunter2%' OR after_json LIKE '%hunter2%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d events still hold the secret", n)
	}
}

func TestPurgeExactSet(t *testing.T) {
	s, ledger := purgeScenario(t)
	purgeFixture(t, s, true)
	purge(t, s, "task", 42, true)
	count := s.OpCount()
	// An existing superset is not the exact intent requested by this command.
	got := purge(t, s, "task", 42, false)
	if !got.Changed || s.OpCount() != count+1 || len(purgeRecords(t, ledger)) != 2 {
		t.Fatalf("new exact intent = %+v; operations = %d", got, s.OpCount())
	}
	op := latestPurgeOp(t, s, domain.OpPurgeTask)
	requirePurgeEvent(t, s, op.ID, domain.EntTask, 42, `{"ids":[42,101]}`)
	got = purge(t, s, "task", 42, false)
	if got.Changed || s.OpCount() != count+1 || len(purgeRecords(t, ledger)) != 2 {
		t.Fatal("exact intent rerun was not a no-op")
	}
}

func TestPurgeReplayUnion(t *testing.T) {
	s, ledger := purgeScenario(t)
	a, b := s.Add("first"), s.Add("second")
	e1, e2 := s.Log(a, "first work", ""), s.Log(b, "second work", "")
	keep := s.Add("keep")
	recs := []backup.LedgerRecord{
		{State: backup.LedgerPending, Tasks: []domain.TaskID{b, a, 999}, Entries: []domain.EntryID{e2, 999}},
		{State: backup.LedgerComplete, Tasks: []domain.TaskID{a}, Entries: []domain.EntryID{e1, e2}},
	}
	before := slices.Clone(recs[0].Tasks)
	count := s.OpCount()
	r := s.Must(s.Svc.ReplayLedger(s.Ctx(), recs, service.ReplayLive))
	if r.NoChange || s.OpCount() != count+1 || !slices.Equal(recs[0].Tasks, before) || len(purgeRecords(t, ledger)) != 0 {
		t.Fatal(r)
	}
	requirePurgeEvent(t, s, r.OpID, domain.EntTask, int64(a), `{"ids":[1,2,1,2]}`)
	requireScrubbed(t, s, []int64{int64(a), int64(b)}, []int64{int64(e1), int64(e2)})
	if s.Task(keep).Title != "keep" || !slices.Equal(s.Reasons(keep), []domain.TriageReason{domain.ReasonNew}) {
		t.Fatal("unrelated task changed")
	}
	count = s.OpCount()
	for _, mode := range []service.ReplayMode{service.ReplayLive, service.ReplayHistorical} {
		r = s.Must(s.Svc.ReplayLedger(s.Ctx(), recs, mode))
		if !r.NoChange || s.OpCount() != count {
			t.Fatal("union replay recorded redundant changes")
		}
		r = s.Must(s.Svc.ReplayLedger(s.Ctx(), []backup.LedgerRecord{{Tasks: []domain.TaskID{999}, Entries: []domain.EntryID{999}}}, mode))
		if !r.NoChange || s.OpCount() != count {
			t.Fatal("missing-only replay recorded changes")
		}
	}
}

func TestPurgePreservesResolvedBlocks(t *testing.T) {
	s := testutil.NewScenario(t).At("10-27 08:00")
	id := s.Add("task")
	b := s.Block(id, "09:00-10:00")
	e := s.Log(id, "work", "")
	s.At(testutil.DefaultNow).Reconcile()
	before := s.BlockView(b)
	if s.BlockState(b) != "{worked, auto, 0, –}" {
		t.Fatal(s.BlockState(b))
	}
	r := s.Must(s.Svc.ReplayLedger(s.Ctx(), []backup.LedgerRecord{{Entries: []domain.EntryID{e}}}, service.ReplayLive))
	requirePurgeEvent(t, s, r.OpID, domain.EntEntry, int64(e), `{"ids":[1]}`)
	if !reflect.DeepEqual(s.BlockView(b), before) || !slices.Equal(s.Reasons(id), []domain.TriageReason{domain.ReasonContinue}) {
		t.Fatal("scrubbing recomputed a resolved block")
	}
	if l, since := s.Slips(id); l != 0 || since != 0 {
		t.Fatalf("slips = %d/%d", l, since)
	}
}

func TestPurgeHistoricalPreservesUnrelatedFocus(t *testing.T) {
	s := testutil.NewScenario(t).At("10-26 08:00")
	id := s.Add("purge me")
	other := s.Add("keep")
	b := s.Block(other, "09:00-10:00")
	s.SetFocus(other, false)
	s.MarkManual(other)
	focus, reasons := s.Focus(), s.Reasons(other)
	s.At(testutil.DefaultNow)
	s.Must(s.Svc.ReplayLedger(s.Ctx(), []backup.LedgerRecord{{Tasks: []domain.TaskID{id}}}, service.ReplayHistorical))
	block, err := store.GetBlock(s.Ctx(), s.Store.Reader, b)
	if err != nil || block.Status != domain.BlockPlanned || !reflect.DeepEqual(focus, s.Focus()) {
		t.Fatalf("historical state changed: %+v; %+v; %v", block, s.Focus(), err)
	}
	rows, err := store.Reasons(s.Ctx(), s.Store.Reader, other)
	if err != nil || len(rows) != len(reasons) || rows[0].Reason != reasons[0] {
		t.Fatalf("unrelated reasons = %v; %v", rows, err)
	}
	if err := service.CheckStructural(s.Ctx(), s.Store.Reader); err != nil {
		t.Fatal(err)
	}
	s.Svc.BeginShutdown()
}

func TestPurgeAppendFailure(t *testing.T) {
	ledger, _, err := backup.OpenLedger(t.TempDir() + "/missing/ledger")
	if err != nil {
		t.Fatal(err)
	}
	s := testutil.NewScenario(t, func(o *service.Options) { o.Ledger = ledger })
	id := s.Add("preserved")
	s.Delete(id)
	before, count := s.Task(id), s.OpCount()
	_, err = s.Svc.PurgeLive(s.Ctx(), s.Meta(), service.PurgeLiveIn{Target: "task", ID: int64(id)})
	testutil.RequireCode(t, err, domain.CodeInternal)
	de, _ := domain.AsError(err)
	if strings.Contains(de.Detail, ledger.Path) || !reflect.DeepEqual(s.Task(id), before) || s.OpCount() != count || len(purgeRecords(t, ledger)) != 0 {
		t.Fatalf("failed append altered DB or exposed cause: %v", err)
	}
}

func TestPurgeAuthorizationAndValidation(t *testing.T) {
	s, ledger := purgeScenario(t)
	id := s.Add("task")
	s.Delete(id)
	count := s.OpCount()
	in := service.PurgeLiveIn{Target: "task", ID: int64(id)}
	m := s.As(testutil.TokenActor("writer", domain.ScopeWrite)).Meta()
	_, err := s.Svc.PurgeLive(s.Ctx(), m, in)
	testutil.RequireCode(t, err, domain.CodeForbidden)
	m = s.Meta()
	m.Gen = "old-generation"
	_, err = s.Svc.PurgeLive(s.Ctx(), m, in)
	testutil.RequireCode(t, err, domain.CodeGenerationMismatch)
	_, err = s.Svc.PurgeLive(s.Ctx(), s.Meta(), service.PurgeLiveIn{Target: "block", ID: int64(id)})
	testutil.RequireCode(t, err, domain.CodeValidation)
	_, err = s.Svc.ReplayLedger(s.Ctx(), nil, 0)
	testutil.RequireCode(t, err, domain.CodeValidation)
	if s.OpCount() != count || len(purgeRecords(t, ledger)) != 0 {
		t.Fatal("rejected purge changed state")
	}
}
