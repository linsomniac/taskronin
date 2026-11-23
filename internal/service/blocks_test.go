package service_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/service"
	"github.com/linsomniac/taskronin/internal/store"
	"github.com/linsomniac/taskronin/internal/testutil"
)

func blockInput(s *testutil.Scenario, task domain.TaskID, span string) service.BlockIn {
	var add service.AddTaskIn
	testutil.BlockAt(span)(&add)
	if add.Place.Block.Date.IsZero() {
		add.Place.Block.Date = testutil.Zone.DateOf(s.Clock.Now())
	}
	return service.BlockIn{TaskID: task, Expect: s.Expect(task), Span: add.Place.Block}
}

func requirePlanned(t *testing.T, s *testutil.Scenario, id domain.BlockID) {
	t.Helper()
	b := s.BlockView(id)
	if s.BlockState(id) != "{planned, –, 0, –}" || b.ResolvedAt.Valid || b.ResolvedSeq.Valid ||
		b.SlippedAt.Valid || b.SlippedSeq.Valid || b.DeletedAt.Valid || b.Version != 1 {
		t.Fatalf("new block = %+v", b)
	}
}

func TestBLK01(t *testing.T) {
	s := testutil.NewScenario(t)
	task := s.Add("A")
	b := s.Block(task, "10-28 13:00-15:00")
	requirePlanned(t, s, b)
	requirePlaced(t, s, task, 2)
	v, r := s.BlockView(b), s.Last
	if b != 1 || v.TaskID != task || v.TaskTitle != "A" || v.TaskVersion != 2 ||
		v.Span != (domain.BlockSpan{Date: testutil.D("10-28"), Start: 780, End: 900}) ||
		!v.CreatedAt.Equal(s.Clock.Now()) || v.Current || v.Started || v.Ended || v.NeedsReblock {
		t.Fatalf("block = %+v", v)
	}
	if r.Task == nil || r.Task.ID != task || r.Block == nil || r.Block.ID != b || !r.Created || r.NoChange || r.Replayed ||
		len(r.Refs) != 2 || !slices.Contains(r.Refs, domain.EntityRef{Entity: domain.EntTask, ID: int64(task)}) ||
		!slices.Contains(r.Refs, domain.EntityRef{Entity: domain.EntBlock, ID: int64(b)}) {
		t.Fatalf("result = %+v", r)
	}
	if got := s.EventKeys(r.OpID); !slices.Equal(got, []string{"block:insert 1", "triage:remove 1 new", "task:update 1"}) {
		t.Fatalf("events = %v", got)
	}
	e := s.Events(r.OpID)[0]
	var image map[string]any
	if err := json.Unmarshal(e.After, &image); err != nil {
		t.Fatal(err)
	}
	if !e.TaskID.Valid || e.TaskID.V != task || e.Derived || len(e.Before) != 0 ||
		image["status"] != "planned" || image["start_min"] != float64(780) || image["end_min"] != float64(900) ||
		image["outcome_source"] != nil || image["resolved_at"] != nil || image["slipped"] != float64(0) {
		t.Fatalf("insert event = %+v, columns = %v", e, image)
	}
	op, undo := s.Op(r.OpID), s.Stack("owner", store.StackUndo)
	if op.Kind != domain.OpBlockCreate || op.Scopes != domain.ScopeWrite || !op.Revertible || undo[len(undo)-1] != r.OpID {
		t.Fatalf("op = %+v, undo = %v", op, undo)
	}
	if got := s.BlockExpect(b); got != (domain.Expect{Gen: s.Svc.Generation(), Version: 1}) {
		t.Fatalf("block expect = %+v", got)
	}
	detail, err := s.Svc.GetTask(s.Ctx(), s.Actor, task)
	if err != nil || len(detail.Blocks) != 1 || detail.Blocks[0] != v {
		t.Fatalf("task blocks = %+v, %v", detail.Blocks, err)
	}
}

func TestBLK02(t *testing.T) {
	s := testutil.NewScenario(t).At("10-28 09:00")
	a, b := s.Add("A"), s.Add("B")
	s.Block(a, "13:00-15:00")
	s.At("10-28 10:00")
	for _, span := range []string{"15:00-16:00", "12:00-13:00"} {
		requirePlanned(t, s, s.Block(b, span))
	}
	requirePlaced(t, s, b, 3)
}

func TestBLK03(t *testing.T) {
	for _, tc := range []struct {
		span string
		same bool
	}{
		{"14:59-16:00", false}, {"12:00-13:01", false}, {"13:30-14:00", false},
		{"12:00-16:00", false}, {"13:00-15:00", false}, {"14:00-16:00", true},
	} {
		t.Run(tc.span, func(t *testing.T) {
			s := testutil.NewScenario(t).At("10-28 09:00")
			a, b := s.Add("A"), s.Add("B")
			block := s.Block(a, "13:00-15:00")
			s.At("10-28 10:00")
			target := b
			if tc.same {
				target = a
			}
			beforeA, beforeB, count := s.Task(a), s.Task(b), s.OpCount()
			_, err := s.Svc.Block(s.Ctx(), s.Meta(), blockInput(s, target, tc.span))
			testutil.RequireCode(t, err, domain.CodeConflict)
			de, _ := domain.AsError(err)
			if de.Detail != "overlaps b1 13:00-15:00 (#1)" {
				t.Fatalf("overlap detail = %q", de.Detail)
			}
			if s.OpCount() != count || !reflect.DeepEqual(s.Task(a), beforeA) || !reflect.DeepEqual(s.Task(b), beforeB) {
				t.Fatal("overlap changed tasks or wrote an op")
			}
			blocks, err := store.BlocksBetween(s.Ctx(), s.Store.Reader, civil.Date{}, civil.Date{}, sql.Null[domain.TaskID]{})
			if err != nil || len(blocks) != 1 || blocks[0].ID != block {
				t.Fatalf("blocks after overlap = %v, %v", blocks, err)
			}
		})
	}
}

func TestBLK05(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	before, count := s.Task(id), s.OpCount()
	for _, tc := range []struct{ start, end int16 }{{900, 780}, {780, 780}, {1380, 60}, {1440, 1500}, {-1, 60}, {0, 1441}, {0, 0}, {0, -1}} {
		t.Run(fmt.Sprintf("%d-%d", tc.start, tc.end), func(t *testing.T) {
			_, err := s.Svc.Block(s.Ctx(), s.Meta(), service.BlockIn{TaskID: id, Expect: s.Expect(id),
				Span: domain.BlockSpan{Date: testutil.D("10-28"), Start: civil.Minute(tc.start), End: civil.EndMinute(tc.end)}})
			testutil.RequireCode(t, err, domain.CodeValidation)
			if s.OpCount() != count || !reflect.DeepEqual(s.Task(id), before) {
				t.Fatal("invalid span changed task or wrote an op")
			}
		})
	}
	b := s.Block(id, "23:00-24:00")
	requirePlanned(t, s, b)
	if s.BlockView(b).Span.End != 1440 {
		t.Fatal("24:00 was not stored as 1440")
	}
}

func TestBLK06(t *testing.T) {
	for _, at := range []string{"10-28 16:00", "10-28 15:00"} {
		t.Run(at, func(t *testing.T) {
			s := testutil.NewScenario(t).At(at)
			id := s.Add("A")
			_, err := s.Svc.Block(s.Ctx(), s.Meta(), blockInput(s, id, "13:00-15:00"))
			testutil.RequireCode(t, err, domain.CodeValidation)
			if s.OpCount() != 1 || s.Task(id).Version != 1 || !slices.Equal(s.Reasons(id), []domain.TriageReason{domain.ReasonNew}) {
				t.Fatal("past block changed task or wrote an op")
			}
		})
	}
}

func TestBLK07(t *testing.T) {
	s := testutil.NewScenario(t)
	b := s.Block(s.Add("A"), "09:00-11:00")
	requirePlanned(t, s, b)
	if v := s.BlockView(b); !v.Current || !v.Started || v.Ended {
		t.Fatalf("current block = %+v", v)
	}
}

func TestBLK11(t *testing.T) {
	for _, tc := range []struct{ span, gap string }{{"02:00-03:00", "02:00"}, {"02:30-04:00", "02:30"}, {"01:00-02:30", "02:30"}} {
		t.Run(tc.span, func(t *testing.T) { requireBlockGap(t, tc.span, tc.gap) })
	}
}

func TestBLK12(t *testing.T) { requireBlockGap(t, "01:00-02:00", "02:00") }

func requireBlockGap(t *testing.T, span, gap string) {
	t.Helper()
	s := testutil.NewScenario(t).At("2027-03-13 12:00")
	id := s.Add("A")
	_, err := s.Svc.Block(s.Ctx(), s.Meta(), blockInput(s, id, "2027-03-14 "+span))
	testutil.RequireCode(t, err, domain.CodeValidation)
	de, _ := domain.AsError(err)
	if want := gap + " does not exist on 2027-03-14; the clocks jump to 03:00"; de.Detail != want {
		t.Fatalf("gap detail = %q, want %q", de.Detail, want)
	}
	if s.OpCount() != 1 || s.Task(id).Version != 1 || !slices.Equal(s.Reasons(id), []domain.TriageReason{domain.ReasonNew}) {
		t.Fatal("gap block changed task or wrote an op")
	}
}

func TestBLK13(t *testing.T) {
	for _, tc := range []struct{ span, before, start, middle, end string }{
		{"01:30-03:30", "08:29", "08:30", "09:00", "09:30"},
		{"03:00-04:00", "08:59", "09:00", "09:30", "10:00"},
	} {
		t.Run(tc.span, func(t *testing.T) {
			s := testutil.NewScenario(t).At("2027-03-13 12:00")
			b := s.Block(s.Add("A"), "2027-03-14 "+tc.span)
			requireBlockWindow(t, s, b, "2027-03-14T"+tc.before+"Z", "2027-03-14T"+tc.start+"Z", "2027-03-14T"+tc.middle+"Z", "2027-03-14T"+tc.end+"Z")
		})
	}
}

func TestBLK14(t *testing.T) {
	s := testutil.NewScenario(t).At("10-31 12:00")
	b := s.Block(s.Add("A"), "2026-11-01 01:30-02:30")
	for _, tc := range []struct {
		at   string
		want bool
	}{{"2026-11-01T07:29Z", false}, {"2026-11-01T07:30Z", true}, {"2026-11-01T07:45Z", true}, {"2026-11-01T08:15Z", true}, {"2026-11-01T09:00Z", true}, {"2026-11-01T09:30Z", false}} {
		if got := s.At(tc.at).BlockView(b).Current; got != tc.want {
			t.Fatalf("Current at %s = %v", tc.at, got)
		}
	}
}

func TestBLK15(t *testing.T) {
	s := testutil.NewScenario(t).At("10-31 12:00")
	b := s.Block(s.Add("A"), "2026-11-01 00:00-24:00")
	requireBlockWindow(t, s, b, "2026-11-01T05:59Z", "2026-11-01T06:00Z", "2026-11-02T06:30Z", "2026-11-02T07:00Z")
	// Midnight resolution belongs to Task 23; leave cleanup on the block's date.
	s.At("2026-11-02T06:30Z")
}

func TestBLK16(t *testing.T) {
	for _, tc := range []struct{ date, before, start, middle, end string }{
		{"2027-03-14", "18:59", "19:00", "20:00", "21:00"},
		{"2026-11-01", "19:59", "20:00", "21:00", "22:00"},
	} {
		t.Run(tc.date, func(t *testing.T) {
			s := testutil.NewScenario(t)
			b := s.Block(s.Add("A"), tc.date+" 13:00-15:00")
			requireBlockWindow(t, s, b, tc.date+"T"+tc.before+"Z", tc.date+"T"+tc.start+"Z", tc.date+"T"+tc.middle+"Z", tc.date+"T"+tc.end+"Z")
		})
	}
}

func TestBLK48(t *testing.T) {
	s := testutil.NewScenario(t)
	b := s.Block(s.Add("A"), "13:00-15:00")
	requireBlockWindow(t, s, b, "10-28 12:59", "10-28 13:00", "10-28 14:30", "10-28 15:00")
}

func requireBlockWindow(t *testing.T, s *testutil.Scenario, b domain.BlockID, before, start, middle, end string) {
	t.Helper()
	for i, at := range []string{before, start, middle, end} {
		v := s.At(at).BlockView(b)
		if v.Current != (i == 1 || i == 2) || v.Started != (i >= 1) || v.Ended != (i == 3) {
			t.Fatalf("window at %s = %+v", at, v)
		}
	}
}

func TestTRI06(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("B", testutil.BlockAt("13:00-15:00"))
	requirePlaced(t, s, id, 1)
	requireBacklog(t, s, id)
	if s.Last.Block == nil {
		t.Fatal("AddTask returned no block")
	}
	b := s.Last.Block.ID
	requirePlanned(t, s, b)
	if v := s.BlockView(b); v.TaskID != id || v.Span.String() != "2026-10-28 13:00-15:00" || v.TaskVersion != 1 {
		t.Fatalf("add block = %+v", v)
	}
	if keys := s.EventKeys(s.Last.OpID); !slices.Equal(keys, []string{"task:insert 1", "block:insert 1", "task:update 1"}) {
		t.Fatalf("events = %v", keys)
	}
}

func TestTRI14(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	requirePlanned(t, s, s.Block(id, "13:00-15:00"))
	requirePlaced(t, s, id, 2)
}

func TestBlockPastDate(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	_, err := s.Svc.Block(s.Ctx(), s.Meta(), blockInput(s, id, "10-27 13:00-15:00"))
	testutil.RequireCode(t, err, domain.CodeValidation)
	if s.OpCount() != 1 || s.Task(id).Version != 1 {
		t.Fatal("past date changed task or wrote an op")
	}
}

func TestBlockPreconditionsAndReadScope(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	in := blockInput(s, id, "13:00-15:00")
	for _, tc := range []struct {
		name   string
		expect domain.Expect
		code   domain.Code
	}{
		{"missing", domain.Expect{}, domain.CodePreconditionReq},
		{"stale", domain.Expect{Gen: s.Svc.Generation(), Version: 2}, domain.CodePrecondition},
		{"generation", domain.Expect{Gen: domain.NewGeneration(), Version: 1}, domain.CodePrecondition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := in
			bad.Expect = tc.expect
			_, err := s.Svc.Block(s.Ctx(), s.Meta(), bad)
			testutil.RequireCode(t, err, tc.code)
			if tc.code == domain.CodePrecondition {
				de, _ := domain.AsError(err)
				current, ok := de.Current.(domain.TaskView)
				if !ok || current.ID != id || current.Version != 1 {
					t.Fatalf("precondition current = %+v", de.Current)
				}
			}
		})
	}
	missing := in
	missing.TaskID = 999
	_, err := s.Svc.Block(s.Ctx(), s.Meta(), missing)
	testutil.RequireCode(t, err, domain.CodeNotFound)
	reader := testutil.TokenActor("reader", domain.ScopeRead)
	_, err = s.Svc.Block(s.Ctx(), s.As(reader).Meta(), in)
	testutil.RequireCode(t, err, domain.CodeForbidden)
	if s.OpCount() != 1 || s.Task(id).Version != 1 {
		t.Fatal("rejected block wrote data")
	}
	b := s.Block(id, "13:00-15:00")
	count := s.OpCount()
	v, err := s.Svc.GetBlock(s.Ctx(), reader, b)
	if err != nil || v.ID != b || v.TaskTitle != "A" || v.TaskVersion != 2 {
		t.Fatalf("GetBlock = %+v, %v", v, err)
	}
	_, err = s.Svc.GetBlock(s.Ctx(), reader, 999)
	testutil.RequireCode(t, err, domain.CodeNotFound)
	_, err = s.Svc.GetBlock(s.Ctx(), testutil.TokenActor("capture", domain.ScopeCapture), b)
	testutil.RequireCode(t, err, domain.CodeForbidden)
	if s.OpCount() != count {
		t.Fatal("GetBlock wrote an op")
	}
}

func TestBlockReplay(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	ai := s.As(testutil.AI())
	in, meta := blockInput(s, id, "13:00-15:00"), ai.MetaWith("block-create-once", "block body")
	first := ai.Must(ai.Svc.Block(ai.Ctx(), meta, in))
	requirePlaced(t, ai, id, 2)
	count := s.OpCount()
	replay := ai.Must(ai.Svc.Block(ai.Ctx(), meta, in))
	if !replay.Replayed || replay.OpID != first.OpID || !replay.Created || replay.Block == nil ||
		replay.Block.ID != first.Block.ID || replay.Task == nil || replay.Task.Version != 2 || s.OpCount() != count {
		t.Fatalf("replay = %+v, first = %+v", replay, first)
	}
}

func TestAddTaskBlockRules(t *testing.T) {
	for _, tc := range []struct {
		name string
		span domain.BlockSpan
		code domain.Code
	}{
		{"overlap", domain.BlockSpan{Date: testutil.D("10-28"), Start: 840, End: 960}, domain.CodeConflict},
		{"past date", domain.BlockSpan{Date: testutil.D("10-27"), Start: 780, End: 900}, domain.CodeValidation},
		{"past end", domain.BlockSpan{Date: testutil.D("10-28"), Start: 480, End: 600}, domain.CodeValidation},
		{"gap start", domain.BlockSpan{Date: testutil.D("2027-03-14"), Start: 150, End: 240}, domain.CodeValidation},
		{"gap end", domain.BlockSpan{Date: testutil.D("2027-03-14"), Start: 60, End: 120}, domain.CodeValidation},
		{"bad span", domain.BlockSpan{Date: testutil.D("10-28"), Start: 900, End: 780}, domain.CodeValidation},
		{"missing date", domain.BlockSpan{Start: 780, End: 900}, domain.CodeValidation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testutil.NewScenario(t).At("10-28 09:00")
			id := s.Add("A")
			s.Block(id, "13:00-15:00")
			s.At("10-28 10:00")
			before, count := s.Task(id), s.OpCount()
			_, err := s.Svc.AddTask(s.Ctx(), s.Meta(), service.AddTaskIn{Title: "B", Tags: []string{"tag"},
				Place: &service.AddPlacement{How: "block", Block: tc.span}})
			testutil.RequireCode(t, err, tc.code)
			if s.OpCount() != count || !reflect.DeepEqual(s.Task(id), before) {
				t.Fatal("failed block add changed existing state")
			}
			var tasks, blocks, tags int
			if err := s.Store.Reader.QueryRowContext(s.Ctx(), `SELECT
				(SELECT COUNT(*) FROM tasks), (SELECT COUNT(*) FROM time_blocks), (SELECT COUNT(*) FROM tags)`).Scan(&tasks, &blocks, &tags); err != nil {
				t.Fatal(err)
			}
			if tasks != 1 || blocks != 1 || tags != 0 {
				t.Fatalf("failed add left tasks/blocks/tags = %d/%d/%d", tasks, blocks, tags)
			}
		})
	}
	t.Run("owner only", func(t *testing.T) {
		s := testutil.NewScenario(t)
		var in service.AddTaskIn
		in.Title = "B"
		testutil.BlockAt("10-28 13:00-15:00")(&in)
		_, err := s.Svc.AddTask(s.Ctx(), s.As(testutil.AI()).Meta(), in)
		testutil.RequireCode(t, err, domain.CodeForbidden)
		if s.OpCount() != 0 {
			t.Fatal("token placement wrote an op")
		}
	})
	t.Run("today and bottom rank", func(t *testing.T) {
		s := testutil.NewScenario(t).At("10-29 10:00")
		a := s.Add("A", testutil.Bottom())
		b := s.Add("B", testutil.BlockAt("13:00-15:00"))
		requireBacklog(t, s, a, b)
		requirePlaced(t, s, b, 1)
		if s.Last.Block == nil || s.Last.Block.Span.Date != testutil.D("10-29") || s.Task(b).Rank <= s.Task(a).Rank {
			t.Fatalf("block add = %+v", s.Last)
		}
	})
}
