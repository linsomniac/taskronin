package tui

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/client"
)

type call struct {
	Method string
	ID     int64      // task/entry/block id when the method takes one
	Mut    client.Mut // mutations
	INM    string     // Today's ifNoneMatch
	Q      any        // query struct / civil.Date / opID / all-flag for reads and Revert
	Req    any        // request body
}

type fakeAPI struct {
	mu          sync.Mutex
	calls       []call
	clock       api.Clock
	gen         string // returned by Generation and RefreshGeneration
	today       api.Today
	todayETag   string
	tasks       map[string][]api.Task // by view
	upcoming    api.Upcoming
	entries     []api.Entry
	entriesNext *string
	days        map[civil.Date]api.Day
	blocks      []api.Block
	details     map[int64]api.TaskDetail
	history     map[int64][]api.EventView
	hits        []api.SearchHit
	aiPages     []api.AIChanges // page i is returned for the i-th distinct cursor ("" first)
	aiCursors   []string
	resp        api.OpResponse   // returned (copied) by every mutation
	errs        map[string]error // Method → error, one-shot
}

var _ API = (*fakeAPI)(nil)

func newFake() *fakeAPI {
	c := testClock()
	opID := "fake-op"
	return &fakeAPI{
		clock:     c,
		gen:       c.Generation,
		today:     api.Today{Clock: c},
		tasks:     make(map[string][]api.Task),
		days:      make(map[civil.Date]api.Day),
		details:   make(map[int64]api.TaskDetail),
		history:   make(map[int64][]api.EventView),
		aiCursors: []string{""},
		resp:      api.OpResponse{OpID: &opID},
		errs:      make(map[string]error),
	}
}

func (f *fakeAPI) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Method == method {
			n++
		}
	}
	return n
}

func (f *fakeAPI) mustLast(t *testing.T, method string) call {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].Method == method {
			return f.calls[i]
		}
	}
	t.Fatalf("no %s call recorded", method)
	return call{}
}

func (f *fakeAPI) setToday(td api.Today, etag string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.today, f.todayETag = td, etag
}

// AIDEV-NOTE: callers hold mu across recording, one-shot error consumption and
// response selection so concurrent commands observe one consistent fixture.
func (f *fakeAPI) record(c call) error {
	f.calls = append(f.calls, c)
	err := f.errs[c.Method]
	delete(f.errs, c.Method)
	return err
}

func (f *fakeAPI) Clock(_ context.Context) (*api.Clock, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(call{Method: "Clock"}); err != nil {
		return nil, err
	}
	c := f.clock
	return &c, nil
}

func (f *fakeAPI) Generation(_ context.Context) (string, error) {
	return f.generation("Generation")
}

func (f *fakeAPI) RefreshGeneration(_ context.Context) (string, error) {
	return f.generation("RefreshGeneration")
}

func (f *fakeAPI) generation(method string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(call{Method: method}); err != nil {
		return "", err
	}
	return f.gen, nil
}

func (f *fakeAPI) Today(_ context.Context, ifNoneMatch string) (*api.Today, string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(call{Method: "Today", INM: ifNoneMatch}); err != nil {
		return nil, "", false, err
	}
	if ifNoneMatch != "" && ifNoneMatch == f.todayETag {
		return nil, f.todayETag, true, nil
	}
	td := f.today
	return &td, f.todayETag, false, nil
}

func (f *fakeAPI) Day(_ context.Context, d civil.Date) (*api.Day, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(call{Method: "Day", Q: d}); err != nil {
		return nil, err
	}
	day := f.days[d]
	return &day, nil
}

func (f *fakeAPI) Upcoming(_ context.Context) (*api.Upcoming, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(call{Method: "Upcoming"}); err != nil {
		return nil, err
	}
	u := f.upcoming
	return &u, nil
}

func (f *fakeAPI) Search(_ context.Context, q client.SearchQuery) (*api.Page[api.SearchHit], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(call{Method: "Search", Q: q}); err != nil {
		return nil, err
	}
	return &api.Page[api.SearchHit]{Items: slices.Clone(f.hits)}, nil
}

func (f *fakeAPI) ListTasks(_ context.Context, q client.TaskQuery) (*api.Page[api.Task], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(call{Method: "ListTasks", Q: q}); err != nil {
		return nil, err
	}
	return &api.Page[api.Task]{Items: slices.Clone(f.tasks[q.View])}, nil
}

func (f *fakeAPI) GetTask(_ context.Context, id int64) (*api.TaskDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(call{Method: "GetTask", ID: id}); err != nil {
		return nil, err
	}
	d := f.details[id]
	return &d, nil
}

func (f *fakeAPI) TaskHistory(_ context.Context, id int64, p client.PageQuery) (*api.Page[api.EventView], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(call{Method: "TaskHistory", ID: id, Q: p}); err != nil {
		return nil, err
	}
	return &api.Page[api.EventView]{Items: slices.Clone(f.history[id])}, nil
}

func (f *fakeAPI) ListEntries(_ context.Context, q client.EntryQuery) (*api.Page[api.Entry], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(call{Method: "ListEntries", Q: q}); err != nil {
		return nil, err
	}
	page := &api.Page[api.Entry]{Items: slices.Clone(f.entries)}
	if q.Cursor == "" {
		page.NextCursor = f.entriesNext
	}
	return page, nil
}

func (f *fakeAPI) ListBlocks(_ context.Context, q client.BlockQuery) ([]api.Block, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(call{Method: "ListBlocks", Q: q}); err != nil {
		return nil, err
	}
	return slices.Clone(f.blocks), nil
}

func (f *fakeAPI) AIChanges(_ context.Context, all bool, p client.PageQuery) (*api.AIChanges, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(call{Method: "AIChanges", Q: all}); err != nil {
		return nil, err
	}
	// AIDEV-NOTE: repeated cursors keep their page, including a reload of "";
	// request count would incorrectly advance pages after retries or refreshes.
	i := slices.Index(f.aiCursors, p.Cursor)
	if i < 0 {
		i = len(f.aiCursors)
		f.aiCursors = append(f.aiCursors, p.Cursor)
	}
	var page api.AIChanges
	if i < len(f.aiPages) {
		page = f.aiPages[i]
	}
	return &page, nil
}

func (f *fakeAPI) mutation(c call) (*api.OpResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(c); err != nil {
		return nil, err
	}
	resp := f.resp
	return &resp, nil
}

func (f *fakeAPI) AddTask(_ context.Context, m client.Mut, r api.AddTaskRequest) (*api.OpResponse, error) {
	return f.mutation(call{Method: "AddTask", Mut: m, Req: r})
}

func (f *fakeAPI) UpdateTask(_ context.Context, m client.Mut, id int64, r api.UpdateTaskRequest) (*api.OpResponse, error) {
	return f.mutation(call{Method: "UpdateTask", ID: id, Mut: m, Req: r})
}

func (f *fakeAPI) PlaceTask(_ context.Context, m client.Mut, id int64, r api.PlaceRequest) (*api.OpResponse, error) {
	return f.mutation(call{Method: "PlaceTask", ID: id, Mut: m, Req: r})
}

func (f *fakeAPI) DeferTask(_ context.Context, m client.Mut, id int64, r api.DeferRequest) (*api.OpResponse, error) {
	return f.mutation(call{Method: "DeferTask", ID: id, Mut: m, Req: r})
}

func (f *fakeAPI) BlockTask(_ context.Context, m client.Mut, id int64, r api.BlockRequest) (*api.OpResponse, error) {
	return f.mutation(call{Method: "BlockTask", ID: id, Mut: m, Req: r})
}

func (f *fakeAPI) CompleteTask(_ context.Context, m client.Mut, id int64, r api.CompleteRequest) (*api.OpResponse, error) {
	return f.mutation(call{Method: "CompleteTask", ID: id, Mut: m, Req: r})
}

func (f *fakeAPI) DropTask(_ context.Context, m client.Mut, id int64) (*api.OpResponse, error) {
	return f.mutation(call{Method: "DropTask", ID: id, Mut: m})
}

func (f *fakeAPI) AcceptNew(_ context.Context, m client.Mut, r api.AcceptNewRequest) (*api.OpResponse, error) {
	return f.mutation(call{Method: "AcceptNew", Mut: m, Req: r})
}

func (f *fakeAPI) AddEntry(_ context.Context, m client.Mut, r api.AddEntryRequest) (*api.OpResponse, error) {
	return f.mutation(call{Method: "AddEntry", Mut: m, Req: r})
}

func (f *fakeAPI) UpdateEntry(_ context.Context, m client.Mut, id int64, r api.UpdateEntryRequest) (*api.OpResponse, error) {
	return f.mutation(call{Method: "UpdateEntry", ID: id, Mut: m, Req: r})
}

func (f *fakeAPI) RelinkEntry(_ context.Context, m client.Mut, id int64, r api.RelinkRequest) (*api.OpResponse, error) {
	return f.mutation(call{Method: "RelinkEntry", ID: id, Mut: m, Req: r})
}

func (f *fakeAPI) DeleteEntry(_ context.Context, m client.Mut, id int64) (*api.OpResponse, error) {
	return f.mutation(call{Method: "DeleteEntry", ID: id, Mut: m})
}

func (f *fakeAPI) Reblock(_ context.Context, m client.Mut, blockID int64, r api.ReblockRequest) (*api.OpResponse, error) {
	return f.mutation(call{Method: "Reblock", ID: blockID, Mut: m, Req: r})
}

func (f *fakeAPI) Unblock(_ context.Context, m client.Mut, blockID int64) (*api.OpResponse, error) {
	return f.mutation(call{Method: "Unblock", ID: blockID, Mut: m})
}

func (f *fakeAPI) SetOutcome(_ context.Context, m client.Mut, blockID int64, r api.OutcomeRequest) (*api.OpResponse, error) {
	return f.mutation(call{Method: "SetOutcome", ID: blockID, Mut: m, Req: r})
}

func (f *fakeAPI) SetFocus(_ context.Context, m client.Mut, r api.FocusRequest) (*api.OpResponse, error) {
	return f.mutation(call{Method: "SetFocus", Mut: m, Req: r})
}

func (f *fakeAPI) Revert(_ context.Context, m client.Mut, opID string) (*api.OpResponse, error) {
	return f.mutation(call{Method: "Revert", Mut: m, Q: opID})
}

func (f *fakeAPI) Undo(_ context.Context, m client.Mut) (*api.OpResponse, error) {
	return f.mutation(call{Method: "Undo", Mut: m})
}

func (f *fakeAPI) Redo(_ context.Context, m client.Mut) (*api.OpResponse, error) {
	return f.mutation(call{Method: "Redo", Mut: m})
}

func (f *fakeAPI) ReviewAIChanges(_ context.Context, m client.Mut, r api.ReviewRequest) (*api.OpResponse, error) {
	return f.mutation(call{Method: "ReviewAIChanges", Mut: m, Req: r})
}

func testClock() api.Clock {
	return api.Clock{
		Now:         api.Time{Time: time.Date(2026, time.October, 28, 10, 0, 0, 0, time.FixedZone("America/Denver", -6*60*60))},
		Today:       civil.MustDate("2026-10-28"),
		Timezone:    "America/Denver",
		WeekStart:   "monday",
		DueSoonDays: 3,
		Generation:  "0123456789abcdef",
	}
}

func task(id int64, title string, reasons ...string) api.Task {
	t := api.Task{ID: id, Title: title, Status: "open", View: "backlog", ETag: fmt.Sprintf("g-%d", id)}
	for _, reason := range reasons {
		t.Reasons = append(t.Reasons, api.Reason{Reason: reason, Since: testClock().Now})
	}
	if len(reasons) > 0 {
		t.View = "triage"
	}
	return t
}

func problem(code string, status int, detail string) error {
	return &client.ProblemError{Status: status, Problem: api.Problem{Code: code, Status: status, Detail: detail}}
}

func notSent() error { return fmt.Errorf("dial: %w", client.ErrNotSent) }

func outcomeUnknown(key string) error {
	return &client.OutcomeUnknownError{Key: key, Err: client.ErrOutcomeUnknown}
}
