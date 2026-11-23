package service_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/service"
	"github.com/linsomniac/taskronin/internal/store"
	"github.com/linsomniac/taskronin/internal/testutil"
)

func readAIChanges(t *testing.T, s *testutil.Scenario, in service.ListAIChangesIn) service.AIChanges {
	t.Helper()
	v, err := s.Svc.ListAIChanges(s.Ctx(), s.Actor, in)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func requireAIOpState(t *testing.T, s *testutil.Scenario, id domain.OpID, state domain.OpState) {
	t.Helper()
	v := readAIChanges(t, s, service.ListAIChangesIn{})
	if len(v.Ops) != 1 || v.Ops[0].OpID != id || v.Ops[0].State != state || v.Unreviewed != 1 {
		t.Fatalf("AI changes = %+v, want op %s in state %s", v, id, state)
	}
}

func requireHistoryOp(t *testing.T, s *testutil.Scenario, task domain.TaskID, op domain.OpID) {
	t.Helper()
	p, err := s.Svc.History(s.Ctx(), s.Actor, service.HistoryIn{TaskID: task})
	if err != nil {
		t.Fatal(err)
	}
	var got []service.EventView
	for _, e := range p.Items {
		if e.Op.OpID == op {
			got = append(got, e)
		}
	}
	want := s.Events(op)
	slices.Reverse(want)
	if len(got) != len(want) {
		t.Fatalf("history for %s has %d events, want %d", op, len(got), len(want))
	}
	for i, e := range want {
		requireEventView(t, got[i], e, s.Op(op))
	}
}

func requireEventView(t *testing.T, got service.EventView, want domain.Event, op domain.OpRecord) {
	t.Helper()
	if got.ID != want.ID || got.Entity != want.Entity || got.EntityID != want.EntityID || got.Action != want.Action ||
		!reflect.DeepEqual(got.Before, want.Before) || !reflect.DeepEqual(got.After, want.After) ||
		got.Op.OpID != op.ID || got.Op.Seq != op.Seq || got.Op.Actor != op.Actor || got.Op.Kind != op.Kind ||
		got.Op.Channel != op.Channel || !got.Op.At.Equal(op.At) || got.Op.State != op.State ||
		got.Op.Revertible != op.Revertible || got.Op.Via != op.Via || got.Op.Effects != nil {
		t.Fatalf("event view = %+v, want event %+v and op %+v (nil effects)", got, want, op)
	}
}

func TestTRI25(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	requireDueReasons(t, s, id, domain.ReasonNew)
	before := s.Task(id)
	ai := s.As(testutil.AI())
	ai.Place(id, domain.PlaceKeep)
	requirePlaced(t, ai, id, before.Version+1)
	op := s.Op(ai.Last.OpID)
	after := s.Task(id)
	requireDueReasons(t, s, id)
	if op.Actor != "token:ai" || op.Scopes != domain.ScopeWrite || after.Rank != before.Rank ||
		after.Version != before.Version+1 || after.LastPlacedSeq.V != op.Seq || !after.LastPlacedAt.V.Equal(s.Clock.Now()) {
		t.Fatalf("placement = %+v; op = %+v", after, op)
	}
	requireAIOpState(t, s, op.ID, domain.OpActive)
	v := readAIChanges(t, s, service.ListAIChangesIn{})
	if v.ReviewedThrough != 0 || !slices.Equal(v.Ops[0].TaskIDs, []domain.TaskID{id}) || !reflect.DeepEqual(v.Ops[0].Effects, ai.Last.Effects) {
		t.Fatal(v)
	}
}

func TestSCP11(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	ai := s.As(testutil.AI())
	ai.Log(id, "AI work", "")
	op := s.Op(ai.Last.OpID)
	if op.Actor != "token:ai" {
		t.Fatal(op)
	}
	requireAIOpState(t, s, op.ID, domain.OpActive)
	status, err := s.Svc.Status(s.Ctx(), s.Actor)
	if err != nil || status.AI != 1 {
		t.Fatalf("status = %+v, %v", status, err)
	}
	_, err = s.Svc.MarkAIReviewed(s.Ctx(), ai.Meta(), service.MarkAIReviewedIn{Through: op.Seq})
	testutil.RequireCode(t, err, domain.CodeForbidden)
	_, err = s.Svc.MarkAIReviewed(s.Ctx(), s.Meta(), service.MarkAIReviewedIn{Through: op.Seq + 1})
	testutil.RequireCode(t, err, domain.CodeValidation)
	r := s.Must(s.Svc.MarkAIReviewed(s.Ctx(), s.Meta(), service.MarkAIReviewedIn{Through: op.Seq}))
	if s.Op(r.OpID).Revertible {
		t.Fatal("review is revertible")
	}
	value, err := store.GetSetting(s.Ctx(), s.Store.Reader, "ai_reviewed_through")
	if err != nil || value != strconv.FormatInt(int64(op.Seq), 10) {
		t.Fatalf("reviewed through = %q, %v", value, err)
	}
	status, err = s.Svc.Status(s.Ctx(), s.Actor)
	if err != nil || status.AI != 0 {
		t.Fatalf("reviewed status = %+v, %v", status, err)
	}
}

func TestHistoryUnknownTask(t *testing.T) {
	s := testutil.NewScenario(t)
	_, err := s.Svc.History(s.Ctx(), s.Actor, service.HistoryIn{TaskID: 999})
	testutil.RequireCode(t, err, domain.CodeNotFound)
}

func TestHistory(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	add := s.Last.OpID
	want := s.Events(add)
	s.Block(id, "10-28 13:00-15:00")
	want = append(want, s.Events(s.Last.OpID)...)
	s.At("10-29 00:30").Reconcile()
	want = append(want, s.Events(latestOp(t, s).ID)...)
	s.Log(id, "work", "10-28")
	log := s.Last.OpID
	want = append(want, s.Events(log)...)
	unrelated := s.Add("unrelated")
	if !want[len(want)-1].Derived {
		t.Fatal("fixture must include derived events")
	}
	slices.Reverse(want)
	p, err := s.Svc.History(s.Ctx(), s.Actor, service.HistoryIn{TaskID: id, Limit: 2})
	if err != nil || len(p.Items) != 2 || p.NextCursor == "" {
		t.Fatalf("first history page = %+v, %v", p, err)
	}
	got := p.Items
	for p.NextCursor != "" {
		p, err = s.Svc.History(s.Ctx(), s.Actor, service.HistoryIn{TaskID: id, Limit: 2, Cursor: p.NextCursor})
		if err != nil || len(p.Items) == 0 || len(p.Items) > 2 {
			t.Fatalf("history page = %+v, %v", p, err)
		}
		got = append(got, p.Items...)
		if len(got) > len(want) {
			t.Fatal("history repeated events")
		}
	}
	if len(got) != len(want) {
		t.Fatalf("history has %d events, want %d", len(got), len(want))
	}
	for i, event := range got {
		requireEventView(t, event, want[i], s.Op(want[i].OpID))
		if !slices.Equal(event.Op.TaskIDs, []domain.TaskID{id}) {
			t.Fatal(event.Op.TaskIDs)
		}
	}
	p, err = s.Svc.History(s.Ctx(), s.Actor, service.HistoryIn{TaskID: unrelated, Limit: 2})
	if err != nil || len(p.Items) != 2 || p.NextCursor != "" {
		t.Fatalf("exact history page = %+v, %v", p, err)
	}
	// Audit reads neither reconcile pending work nor hide tombstoned tasks.
	s.Delete(id)
	requireHistoryOp(t, s, id, add)
	s.Defer(unrelated, "10-30")
	s.At("10-30 00:00")
	count := s.OpCount()
	requireHistoryOp(t, s, id, log)
	if s.OpCount() != count {
		t.Fatal("history reconciled")
	}
}

func TestGetOp(t *testing.T) {
	s := testutil.NewScenario(t)
	a, b := s.Add("A"), s.Add("B")
	s.Block(a, "10-28 13:00-14:00")
	s.Block(b, "10-28 14:00-15:00")
	s.At("10-29 10:00").Reconcile()
	entry := s.Log(a, "work", "10-28")
	s.Relink(entry, b) // Derived continue reasons touch both the former and new task.
	r := s.Last
	v, err := s.Svc.GetOp(s.Ctx(), s.Actor, r.OpID)
	if err != nil {
		t.Fatal(err)
	}
	events := s.Events(r.OpID)
	ids := []domain.TaskID{a, b}
	if !slices.Equal(v.Op.TaskIDs, ids) || len(v.Events) != len(events) || !reflect.DeepEqual(v.Op.Effects, r.Effects) {
		t.Fatalf("op detail = %+v, IDs = %v, effects = %+v, %v", v, ids, r.Effects, err)
	}
	for i, e := range events {
		requireEventView(t, v.Events[i], e, s.Op(r.OpID))
		if !slices.Equal(v.Events[i].Op.TaskIDs, ids) {
			t.Fatal(v.Events[i].Op.TaskIDs)
		}
	}
	s.Revert(r.OpID)
	v, err = s.Svc.GetOp(s.Ctx(), s.Actor, r.OpID)
	if err != nil || v.Op.State != domain.OpReverted {
		t.Fatalf("reverted detail = %+v, %v", v, err)
	}
	v, err = s.Svc.GetOp(s.Ctx(), s.Actor, s.Last.OpID)
	if err != nil || v.Op.Via != "revert" || !reflect.DeepEqual(v.Op.Effects, s.Last.Effects) {
		t.Fatalf("reversal detail = %+v, %v", v, err)
	}
	_, err = s.Svc.GetOp(s.Ctx(), s.Actor, domain.NewOpID())
	testutil.RequireCode(t, err, domain.CodeNotFound)
}

func TestListOps(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	first := s.Last.OpID
	ai := s.As(testutil.AI())
	ai.Log(id, "AI work", "")
	second := ai.Last.OpID
	s.At("10-28 09:00").Log(id, "owner work", "") // sequence order survives backward wall time
	third := s.Last.OpID
	p, err := s.Svc.ListOps(s.Ctx(), s.Actor, service.ListOpsIn{Limit: 2})
	if err != nil || len(p.Items) != 2 || p.Items[0].OpID != third || p.Items[1].OpID != second || p.NextCursor == "" {
		t.Fatalf("first ops page = %+v, %v", p, err)
	}
	p, err = s.Svc.ListOps(s.Ctx(), s.Actor, service.ListOpsIn{Limit: 2, Cursor: p.NextCursor})
	if err != nil || len(p.Items) != 1 || p.Items[0].OpID != first || p.NextCursor != "" {
		t.Fatalf("last ops page = %+v, %v", p, err)
	}
	for _, in := range []service.ListOpsIn{{Actor: "owner", Limit: 2}, {SinceSeq: s.Op(first).Seq, Limit: 2}, {Actor: "token:ai", SinceSeq: s.Op(first).Seq, Limit: 1}} {
		p, err = s.Svc.ListOps(s.Ctx(), s.Actor, in)
		want := []domain.OpID{third, first}
		switch in.Actor {
		case "":
			want = []domain.OpID{third, second}
		case "token:ai":
			want = []domain.OpID{second}
		default:
		}
		if err != nil || len(p.Items) != len(want) || p.NextCursor != "" {
			t.Fatalf("filtered ops = %+v, %v", p, err)
		}
		for i, op := range p.Items {
			if op.OpID != want[i] || !slices.Equal(op.TaskIDs, []domain.TaskID{id}) || len(op.Effects) == 0 {
				t.Fatal(op)
			}
		}
	}
}

func TestListOpsPrefix(t *testing.T) {
	s := testutil.NewScenario(t)
	s.Add("A")
	first := s.Last.OpID
	s.Add("B")
	for _, prefix := range []string{first.String(), first.String()[:31]} {
		p, err := s.Svc.ListOps(s.Ctx(), s.Actor, service.ListOpsIn{OpPrefix: prefix, Limit: 2})
		if err != nil || len(p.Items) != 1 || p.Items[0].OpID != first || p.NextCursor != "" {
			t.Fatalf("ListOps(%q) = %+v, %v", prefix, p, err)
		}
	}
	for _, prefix := range []string{"ABC", "xyz", first.String() + "0", "a-b"} {
		_, err := s.Svc.ListOps(s.Ctx(), s.Actor, service.ListOpsIn{OpPrefix: prefix})
		if de, ok := domain.AsError(err); !ok || de.Code != domain.CodeValidation || len(de.Fields) != 1 || de.Fields[0].Field != "op_prefix" {
			t.Fatalf("ListOps(%q) error = %v", prefix, err)
		}
	}
}

func TestListAIChanges(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	if v := readAIChanges(t, s, service.ListAIChangesIn{}); v.Unreviewed != 0 || len(v.Ops) != 0 || v.NextCursor != "" {
		t.Fatal(v)
	}
	ai := s.As(testutil.AI())
	ai.Log(id, "AI work", "")
	first := ai.Last.OpID
	s.As(testutil.TokenActor("other", domain.ScopeCapture)).Log(id, "other token work", "")
	second := latestOp(t, s).ID
	// AIDEV-NOTE: F14 fixture shortcut: UpdateSettings arrives in Task 35a.
	// Insert the retention-lowering Given through the store; all reads/reviews use real service ops.
	retention := domain.OpRecord{ID: domain.NewOpID(), Kind: domain.OpSettingsRetentionLowered, Actor: "owner",
		Channel: domain.ChanCLI, At: s.Clock.Now(), Scopes: domain.ScopeAdmin, State: domain.OpActive}
	s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), nil, func(q store.Q) error {
		return store.InsertOp(s.Ctx(), q, &retention)
	}))
	v := readAIChanges(t, s, service.ListAIChangesIn{Limit: 2})
	if v.Unreviewed != 3 || v.ReviewedThrough != 0 || len(v.Ops) != 2 || v.Ops[0].OpID != retention.ID || v.Ops[1].OpID != second || v.NextCursor == "" {
		t.Fatal(v)
	}
	v = readAIChanges(t, s, service.ListAIChangesIn{Limit: 2, Cursor: v.NextCursor})
	if v.Unreviewed != 3 || len(v.Ops) != 1 || v.Ops[0].OpID != first || v.NextCursor != "" {
		t.Fatal(v)
	}
	seq := s.Op(first).Seq
	s.Must(s.Svc.MarkAIReviewed(s.Ctx(), s.Meta(), service.MarkAIReviewedIn{Through: seq}))
	v = readAIChanges(t, s, service.ListAIChangesIn{Limit: 2})
	if v.ReviewedThrough != seq || v.Unreviewed != 2 || len(v.Ops) != 2 || v.NextCursor != "" {
		t.Fatal(v)
	}
	v = readAIChanges(t, s, service.ListAIChangesIn{IncludeReviewed: true, Limit: 3})
	if v.ReviewedThrough != seq || v.Unreviewed != 2 || len(v.Ops) != 3 || v.Ops[2].OpID != first || v.NextCursor != "" {
		t.Fatal(v)
	}
}

func TestMarkAIReviewedBound(t *testing.T) {
	s := testutil.NewScenario(t)
	ai := s.As(testutil.AI())
	ai.Log(0, "AI work", "")
	seq, count := s.Op(ai.Last.OpID).Seq, s.OpCount()
	_, err := s.Svc.MarkAIReviewed(s.Ctx(), s.Meta(), service.MarkAIReviewedIn{Through: seq + 1})
	testutil.RequireCode(t, err, domain.CodeValidation)
	if s.OpCount() != count {
		t.Fatal("invalid bound recorded an op")
	}
	m := s.MetaWith("review-0123456789", `{"through":1}`)
	r := s.Must(s.Svc.MarkAIReviewed(s.Ctx(), m, service.MarkAIReviewedIn{Through: seq}))
	op := s.Op(r.OpID)
	if op.Kind != "ai.review" || op.Revertible || op.Scopes != domain.ScopeWrite || op.Actor != "owner" || r.NoChange ||
		!reflect.DeepEqual(r.Effects, []service.Effect{{Kind: service.EffReviewed, To: strconv.FormatInt(int64(seq), 10)}}) {
		t.Fatalf("review = %+v / %+v", r, op)
	}
	events := s.Events(r.OpID)
	if len(events) != 1 || events[0].Entity != domain.EntSetting || events[0].Action != domain.ActUpdate || events[0].TaskID.Valid || events[0].Derived {
		t.Fatal(events)
	}
	for i, raw := range []json.RawMessage{events[0].Before, events[0].After} {
		var image map[string]string
		if err := json.Unmarshal(raw, &image); err != nil {
			t.Fatal(err)
		}
		want := "0"
		if i == 1 {
			want = strconv.FormatInt(int64(seq), 10)
		}
		if len(image) != 2 || image["key"] != "ai_reviewed_through" || image["value"] != want {
			t.Fatal(image)
		}
	}
	v := readAIChanges(t, s, service.ListAIChangesIn{})
	if v.Unreviewed != 0 || v.ReviewedThrough != seq || len(v.Ops) != 0 {
		t.Fatal(v)
	}
	replay := s.Must(s.Svc.MarkAIReviewed(s.Ctx(), m, service.MarkAIReviewedIn{Through: seq}))
	if !replay.Replayed || replay.OpID != r.OpID || s.OpCount() != count+1 || !reflect.DeepEqual(replay.Effects, r.Effects) {
		t.Fatal(replay)
	}
	for _, through := range []domain.OpSeq{seq, seq - 1} {
		noop := s.Must(s.Svc.MarkAIReviewed(s.Ctx(), s.Meta(), service.MarkAIReviewedIn{Through: through}))
		if !noop.NoChange || len(noop.Effects) != 0 || noop.OpID != (domain.OpID{}) || s.OpCount() != count+1 {
			t.Fatal(noop)
		}
	}
	_, err = s.Svc.MarkAIReviewed(s.Ctx(), ai.Meta(), service.MarkAIReviewedIn{Through: seq})
	testutil.RequireCode(t, err, domain.CodeForbidden)
	owner := testutil.Owner()
	owner.Scopes = domain.ScopeRead
	_, err = s.Svc.MarkAIReviewed(s.Ctx(), s.As(owner).Meta(), service.MarkAIReviewedIn{Through: seq})
	testutil.RequireCode(t, err, domain.CodeForbidden)
	_ = revertRefusal(t, s, r.OpID, domain.CodeNotRevertible)
}

func TestHistoryOpsPagingInputs(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	for _, limit := range []int{-1, 501} {
		_, err := s.Svc.History(s.Ctx(), s.Actor, service.HistoryIn{TaskID: id, Limit: limit})
		testutil.RequireCode(t, err, domain.CodeValidation)
		_, err = s.Svc.ListOps(s.Ctx(), s.Actor, service.ListOpsIn{Limit: limit})
		testutil.RequireCode(t, err, domain.CodeValidation)
		_, err = s.Svc.ListAIChanges(s.Ctx(), s.Actor, service.ListAIChangesIn{Limit: limit})
		testutil.RequireCode(t, err, domain.CodeValidation)
	}
	for _, cursor := range []string{"garbage", domain.EncodeCursor(domain.Cursor{Offset: 1}),
		domain.EncodeCursor(domain.Cursor{Keys: []any{1, 2}, ID: 1}),
		domain.EncodeCursor(domain.Cursor{Keys: []any{}, ID: -1})} {
		_, err := s.Svc.History(s.Ctx(), s.Actor, service.HistoryIn{TaskID: id, Cursor: cursor})
		testutil.RequireCode(t, err, domain.CodeMalformed)
		_, err = s.Svc.ListOps(s.Ctx(), s.Actor, service.ListOpsIn{Cursor: cursor})
		testutil.RequireCode(t, err, domain.CodeMalformed)
		_, err = s.Svc.ListAIChanges(s.Ctx(), s.Actor, service.ListAIChangesIn{Cursor: cursor})
		testutil.RequireCode(t, err, domain.CodeMalformed)
	}
	actor := testutil.TokenActor("capture", domain.ScopeCapture)
	_, err := s.Svc.History(s.Ctx(), actor, service.HistoryIn{TaskID: id})
	testutil.RequireCode(t, err, domain.CodeForbidden)
	_, err = s.Svc.ListOps(s.Ctx(), actor, service.ListOpsIn{})
	testutil.RequireCode(t, err, domain.CodeForbidden)
	_, err = s.Svc.ListAIChanges(s.Ctx(), actor, service.ListAIChangesIn{})
	testutil.RequireCode(t, err, domain.CodeForbidden)
	_, err = s.Svc.GetOp(s.Ctx(), actor, s.Last.OpID)
	testutil.RequireCode(t, err, domain.CodeForbidden)

	t.Run("cross_list", func(t *testing.T) {
		s.Add("B")
		ai := s.As(testutil.AI())
		ai.Log(id, "first AI entry", "")
		ai.Log(id, "second AI entry", "")
		lists := []struct {
			name string
			page func(string) (string, error)
		}{
			{"history", func(cursor string) (string, error) {
				p, err := s.Svc.History(s.Ctx(), s.Actor, service.HistoryIn{TaskID: id, Limit: 1, Cursor: cursor})
				return p.NextCursor, err
			}},
			{"ops", func(cursor string) (string, error) {
				p, err := s.Svc.ListOps(s.Ctx(), s.Actor, service.ListOpsIn{Limit: 1, Cursor: cursor})
				return p.NextCursor, err
			}},
			{"ai_changes", func(cursor string) (string, error) {
				p, err := s.Svc.ListAIChanges(s.Ctx(), s.Actor, service.ListAIChangesIn{Limit: 1, Cursor: cursor})
				return p.NextCursor, err
			}},
			{"all_tasks", func(cursor string) (string, error) {
				p, err := s.Svc.ListTasks(s.Ctx(), s.Actor, service.ListTasksIn{View: domain.ViewAll, Limit: 1, Cursor: cursor})
				return p.NextCursor, err
			}},
		}
		for _, source := range lists {
			cursor, err := source.page("")
			if err != nil || cursor == "" {
				t.Fatalf("%s did not return a continuation cursor: %q, %v", source.name, cursor, err)
			}
			if source.name == "history" {
				decoded, err := domain.DecodeCursor(cursor)
				if err != nil || decoded.ID <= int64(latestOp(t, s).Seq) {
					t.Fatalf("fixture must have an event ID beyond the latest op sequence: %+v, %v", decoded, err)
				}
			}
			for _, target := range lists {
				t.Run(source.name+"_to_"+target.name, func(t *testing.T) {
					next, err := target.page(cursor)
					if source.name != target.name {
						testutil.RequireCode(t, err, domain.CodeMalformed)
					} else if err != nil || next == cursor {
						t.Fatalf("same-list continuation failed: next=%q, err=%v", next, err)
					}
				})
			}
		}
	})
}
