package tui

import (
	"errors"
	"sync"
	"testing"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/client"
)

func TestFakeImplementsAPI(_ *testing.T) { var _ API = newFake() }

func TestFakeErrorsClassify(t *testing.T) {
	var pe *client.ProblemError
	if !errors.As(problem("precondition_failed", 412, "x"), &pe) || pe.Problem.Code != "precondition_failed" {
		t.Fatal("problem")
	}
	if !errors.Is(notSent(), client.ErrNotSent) || !errors.Is(outcomeUnknown("K"), client.ErrOutcomeUnknown) {
		t.Fatal("sentinels")
	}
	eq(t, pe.Status, 412)
	eq(t, pe.Problem.Status, 412)
	eq(t, pe.Problem.Detail, "x")
	var unknown *client.OutcomeUnknownError
	if !errors.As(outcomeUnknown("K"), &unknown) {
		t.Fatal("outcome unknown type")
	}
	eq(t, unknown.Key, "K")
}

func TestFakeTodayETag(t *testing.T) {
	f := newFake()
	f.todayETag = `"a-1"`
	_, _, nm, err := f.Today(t.Context(), `"a-1"`)
	if err != nil || !nm {
		t.Fatal("want 304", err)
	}
	_, et, nm, err := f.Today(t.Context(), "")
	if err != nil || nm || et != `"a-1"` {
		t.Fatal(et, err)
	}
	eq(t, f.mustLast(t, "Today").INM, "")

	td := api.Today{Clock: testClock(), TriageCount: 7}
	f.setToday(td, `"a-2"`)
	got, et, nm, err := f.Today(t.Context(), `"a-1"`)
	if err != nil || nm {
		t.Fatal("want changed dashboard", err)
	}
	eq(t, *got, td)
	eq(t, et, `"a-2"`)
	f.setToday(td, "")
	_, _, nm, err = f.Today(t.Context(), "")
	if err != nil || nm {
		t.Fatal("empty ETag must not match", err)
	}
}

func TestFakeFixturesAndGeneration(t *testing.T) {
	f := newFake()
	c, err := f.Clock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	eq(t, c.Now.Format("2006-01-02T15:04:05.000Z07:00"), "2026-10-28T10:00:00.000-06:00")
	eq(t, c.Today.String(), "2026-10-28")
	eq(t, c.Timezone, "America/Denver")
	eq(t, c.WeekStart, "monday")
	eq(t, c.DueSoonDays, 3)
	eq(t, c.Generation, "0123456789abcdef")
	if f.resp.OpID == nil {
		t.Fatal("missing operation ID")
	}
	for _, gen := range []string{c.Generation, "fedcba9876543210"} {
		f.gen = gen
		got, err := f.Generation(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		eq(t, got, gen)
		got, err = f.RefreshGeneration(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		eq(t, got, gen)
	}
	eq(t, f.count("Generation"), 2)
	eq(t, f.count("RefreshGeneration"), 2)

	backlog := task(42, "Backlog")
	eq(t, backlog.ID, int64(42))
	eq(t, backlog.Title, "Backlog")
	eq(t, backlog.Status, "open")
	eq(t, backlog.View, "backlog")
	eq(t, backlog.ETag, "g-42")
	triage := task(7, "Triage", "new", "manual")
	eq(t, triage.View, "triage")
	eq(t, len(triage.Reasons), 2)
	eq(t, triage.Reasons[0].Reason, "new")
	eq(t, triage.Reasons[1].Reason, "manual")
}

func TestFakeReads(t *testing.T) {
	f := newFake()
	d := civil.MustDate("2026-10-27")
	f.days[d] = api.Day{Date: d, Entries: []api.Entry{{ID: 8}}}
	f.upcoming = api.Upcoming{Deferred: []api.Task{task(3, "Later")}}
	f.tasks["backlog"] = []api.Task{task(4, "Backlog")}
	f.tasks["triage"] = []api.Task{task(5, "Triage", "new")}
	f.details[4] = api.TaskDetail{Task: f.tasks["backlog"][0]}
	f.history[4] = []api.EventView{{ID: 9, EntityID: 4}}
	f.hits = []api.SearchHit{{ID: 4, Kind: "task", Title: "Backlog"}}
	f.entries = []api.Entry{{ID: 8, Title: "Work"}}
	f.blocks = []api.Block{{ID: 6, TaskID: 4}}

	day, err := f.Day(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, *day, f.days[d])
	eq(t, f.mustLast(t, "Day").Q, any(d))
	upcoming, err := f.Upcoming(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	eq(t, *upcoming, f.upcoming)
	q := client.TaskQuery{View: "backlog", Tag: "work", Project: "tui", PageQuery: client.PageQuery{Limit: 25}}
	tasks, err := f.ListTasks(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, *tasks, api.Page[api.Task]{Items: f.tasks["backlog"]})
	eq(t, f.mustLast(t, "ListTasks").Q, any(q))
	detail, err := f.GetTask(t.Context(), 4)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, *detail, f.details[4])
	eq(t, f.mustLast(t, "GetTask").ID, int64(4))
	p := client.PageQuery{Limit: 10, Cursor: "history"}
	history, err := f.TaskHistory(t.Context(), 4, p)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, *history, api.Page[api.EventView]{Items: f.history[4]})
	eq(t, f.mustLast(t, "TaskHistory"), call{Method: "TaskHistory", ID: 4, Q: p})
	sq := client.SearchQuery{Q: "Backlog", Kind: "task", PageQuery: p}
	hits, err := f.Search(t.Context(), sq)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, *hits, api.Page[api.SearchHit]{Items: f.hits})
	eq(t, f.mustLast(t, "Search").Q, any(sq))
	eqQuery := client.EntryQuery{From: &d, To: &d, IncludeDeleted: true, PageQuery: p}
	entries, err := f.ListEntries(t.Context(), eqQuery)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, *entries, api.Page[api.Entry]{Items: f.entries})
	eq(t, f.mustLast(t, "ListEntries").Q, any(eqQuery))
	bq := client.BlockQuery{From: &d, To: &d}
	blocks, err := f.ListBlocks(t.Context(), bq)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, blocks, f.blocks)
	eq(t, f.mustLast(t, "ListBlocks").Q, any(bq))
}

func TestFakePagination(t *testing.T) {
	f := newFake()
	next := "c2"
	f.entries = []api.Entry{{ID: 9}}
	f.entriesNext = &next
	first, err := f.ListEntries(t.Context(), client.EntryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, *first, api.Page[api.Entry]{Items: f.entries, NextCursor: &next})
	f.entries = []api.Entry{{ID: 10}}
	last, err := f.ListEntries(t.Context(), client.EntryQuery{PageQuery: client.PageQuery{Cursor: next}})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, *last, api.Page[api.Entry]{Items: f.entries})

	f.aiPages = []api.AIChanges{
		{ReviewedThrough: 3, Unreviewed: 2, Ops: []api.OpSummary{{Seq: 5}}, NextCursor: &next},
		{ReviewedThrough: 3, Unreviewed: 2, Ops: []api.OpSummary{{Seq: 4}}},
	}
	for _, tc := range []struct {
		cursor string
		page   int
	}{{"", 0}, {next, 1}, {"", 0}, {next, 1}} {
		got, err := f.AIChanges(t.Context(), true, client.PageQuery{Cursor: tc.cursor})
		if err != nil {
			t.Fatal(err)
		}
		eq(t, *got, f.aiPages[tc.page])
	}
	eq(t, f.mustLast(t, "AIChanges").Q, any(true))
	got, err := f.AIChanges(t.Context(), false, client.PageQuery{Cursor: "c3"})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, *got, api.AIChanges{})
	got, err = newFake().AIChanges(t.Context(), false, client.PageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, *got, api.AIChanges{})
}

func TestFakeOneShotErrors(t *testing.T) {
	f := newFake()
	want := notSent()
	for _, tc := range []struct {
		method string
		invoke func() error
	}{
		{"Clock", func() error { _, err := f.Clock(t.Context()); return err }},
		{"Generation", func() error { _, err := f.Generation(t.Context()); return err }},
		{"RefreshGeneration", func() error { _, err := f.RefreshGeneration(t.Context()); return err }},
		{"Today", func() error { _, _, _, err := f.Today(t.Context(), ""); return err }},
		{"Day", func() error { _, err := f.Day(t.Context(), testClock().Today); return err }},
		{"Upcoming", func() error { _, err := f.Upcoming(t.Context()); return err }},
		{"Search", func() error { _, err := f.Search(t.Context(), client.SearchQuery{}); return err }},
		{"ListTasks", func() error { _, err := f.ListTasks(t.Context(), client.TaskQuery{}); return err }},
		{"GetTask", func() error { _, err := f.GetTask(t.Context(), 1); return err }},
		{"TaskHistory", func() error { _, err := f.TaskHistory(t.Context(), 1, client.PageQuery{}); return err }},
		{"ListEntries", func() error { _, err := f.ListEntries(t.Context(), client.EntryQuery{}); return err }},
		{"ListBlocks", func() error { _, err := f.ListBlocks(t.Context(), client.BlockQuery{}); return err }},
		{"AIChanges", func() error { _, err := f.AIChanges(t.Context(), false, client.PageQuery{}); return err }},
	} {
		t.Run(tc.method, func(t *testing.T) {
			f.errs[tc.method] = want
			if err := tc.invoke(); !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
			if err := tc.invoke(); err != nil {
				t.Fatalf("error was not consumed: %v", err)
			}
			eq(t, f.count(tc.method), 2)
		})
	}
}

func TestFakeMutations(t *testing.T) {
	f := newFake()
	ctx := t.Context()
	m := client.Mut{IfMatch: "g-42", Key: "retry-key"}
	add := api.AddTaskRequest{Title: "New task"}
	update := api.UpdateTaskRequest{Title: api.Opt[string]{Set: true, V: "Edited"}}
	place := api.PlaceRequest{How: "before", Ref: 7}
	deferReq := api.DeferRequest{Until: api.Opt[api.Moment]{Set: true, Null: true}}
	block := api.BlockRequest{Date: testClock().Today, Start: 600, End: 660}
	complete := api.CompleteRequest{Log: "Finished"}
	accept := api.AcceptNewRequest{}
	accept.Items = append(accept.Items, struct {
		ID   int64  `json:"id"`
		ETag string `json:"etag"`
	}{42, "g-42"})
	entry := api.AddEntryRequest{Title: "Work", TaskID: api.Opt[int64]{Set: true, V: 42}}
	updateEntry := api.UpdateEntryRequest{Title: api.Opt[string]{Set: true, V: "Edited work"}}
	relink := api.RelinkRequest{TaskID: api.Opt[int64]{Set: true, V: 7}}
	reblock := api.ReblockRequest{BlockRequest: block, TaskETag: "g-42"}
	outcome := api.OutcomeRequest{Outcome: "worked"}
	focus := api.FocusRequest{TaskID: api.Opt[int64]{Set: true, V: 42}, Interrupt: true}
	review := api.ReviewRequest{ThroughSeq: 100}
	f.tasks["backlog"] = []api.Task{task(42, "Existing")}
	wantErr := notSent()
	for _, tc := range []struct {
		want   call
		invoke func() (*api.OpResponse, error)
	}{
		{call{Method: "AddTask", Mut: m, Req: add}, func() (*api.OpResponse, error) { return f.AddTask(ctx, m, add) }},
		{call{Method: "UpdateTask", ID: 42, Mut: m, Req: update}, func() (*api.OpResponse, error) { return f.UpdateTask(ctx, m, 42, update) }},
		{call{Method: "PlaceTask", ID: 42, Mut: m, Req: place}, func() (*api.OpResponse, error) { return f.PlaceTask(ctx, m, 42, place) }},
		{call{Method: "DeferTask", ID: 42, Mut: m, Req: deferReq}, func() (*api.OpResponse, error) { return f.DeferTask(ctx, m, 42, deferReq) }},
		{call{Method: "BlockTask", ID: 42, Mut: m, Req: block}, func() (*api.OpResponse, error) { return f.BlockTask(ctx, m, 42, block) }},
		{call{Method: "CompleteTask", ID: 42, Mut: m, Req: complete}, func() (*api.OpResponse, error) { return f.CompleteTask(ctx, m, 42, complete) }},
		{call{Method: "DropTask", ID: 42, Mut: m}, func() (*api.OpResponse, error) { return f.DropTask(ctx, m, 42) }},
		{call{Method: "AcceptNew", Mut: m, Req: accept}, func() (*api.OpResponse, error) { return f.AcceptNew(ctx, m, accept) }},
		{call{Method: "AddEntry", Mut: m, Req: entry}, func() (*api.OpResponse, error) { return f.AddEntry(ctx, m, entry) }},
		{call{Method: "UpdateEntry", ID: 9, Mut: m, Req: updateEntry}, func() (*api.OpResponse, error) { return f.UpdateEntry(ctx, m, 9, updateEntry) }},
		{call{Method: "RelinkEntry", ID: 9, Mut: m, Req: relink}, func() (*api.OpResponse, error) { return f.RelinkEntry(ctx, m, 9, relink) }},
		{call{Method: "DeleteEntry", ID: 9, Mut: m}, func() (*api.OpResponse, error) { return f.DeleteEntry(ctx, m, 9) }},
		{call{Method: "Reblock", ID: 3, Mut: m, Req: reblock}, func() (*api.OpResponse, error) { return f.Reblock(ctx, m, 3, reblock) }},
		{call{Method: "Unblock", ID: 3, Mut: m}, func() (*api.OpResponse, error) { return f.Unblock(ctx, m, 3) }},
		{call{Method: "SetOutcome", ID: 3, Mut: m, Req: outcome}, func() (*api.OpResponse, error) { return f.SetOutcome(ctx, m, 3, outcome) }},
		{call{Method: "SetFocus", Mut: m, Req: focus}, func() (*api.OpResponse, error) { return f.SetFocus(ctx, m, focus) }},
		{call{Method: "Revert", Mut: m, Q: "op-42"}, func() (*api.OpResponse, error) { return f.Revert(ctx, m, "op-42") }},
		{call{Method: "Undo", Mut: m}, func() (*api.OpResponse, error) { return f.Undo(ctx, m) }},
		{call{Method: "Redo", Mut: m}, func() (*api.OpResponse, error) { return f.Redo(ctx, m) }},
		{call{Method: "ReviewAIChanges", Mut: m, Req: review}, func() (*api.OpResponse, error) { return f.ReviewAIChanges(ctx, m, review) }},
	} {
		t.Run(tc.want.Method, func(t *testing.T) {
			f.errs[tc.want.Method] = wantErr
			if _, err := tc.invoke(); !errors.Is(err, wantErr) {
				t.Fatalf("error = %v, want %v", err, wantErr)
			}
			got, err := tc.invoke()
			if err != nil {
				t.Fatal(err)
			}
			eq(t, *got, f.resp)
			if got == &f.resp {
				t.Fatal("mutation returned fixture address instead of a copy")
			}
			got.Replayed = true
			eq(t, f.resp.Replayed, false)
			eq(t, f.mustLast(t, tc.want.Method), tc.want)
			eq(t, f.count(tc.want.Method), 2)
			eq(t, f.tasks["backlog"], []api.Task{task(42, "Existing")})
		})
	}
}

func TestFakeConcurrentToday(t *testing.T) {
	f := newFake()
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 100 {
			f.setToday(api.Today{Clock: testClock(), TriageCount: 1}, `"a-1"`)
			f.setToday(api.Today{Clock: testClock(), TriageCount: 2}, `"a-2"`)
		}
	})
	for range 100 {
		td, etag, _, err := f.Today(t.Context(), "")
		if err != nil {
			t.Fatal(err)
		}
		switch etag {
		case `"a-1"`:
			eq(t, td.TriageCount, 1)
		case `"a-2"`:
			eq(t, td.TriageCount, 2)
		}
		_ = f.count("Today")
		_ = f.mustLast(t, "Today")
	}
	wg.Wait()
	eq(t, f.count("Today"), 100)
}
