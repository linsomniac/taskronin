package server_test

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"taskronin/internal/api"
	"taskronin/internal/buildinfo"
	"taskronin/internal/civil"
	"taskronin/internal/client"
	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/testutil"
)

func readAPI(t *testing.T, srv *testutil.Server, path, etag string) (int, http.Header, []byte) {
	t.Helper()
	token, err := os.ReadFile(srv.Paths.OwnerToken)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", srv.Socket)
	}}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://taskronin/api/v1"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("If-None-Match", etag)
	resp, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, body
}

func requireReadProblem(t *testing.T, err error, status int, code domain.Code, field string) {
	t.Helper()
	var p *client.ProblemError
	if !errors.As(err, &p) || p.Status != status || p.Code() != code {
		t.Fatalf("error = %v; want HTTP %d %s", err, status, code)
	}
	requireProblemField(t, p.Problem, field)
}

func requireProblemField(t *testing.T, p api.Problem, field string) {
	t.Helper()
	if field == "" {
		return
	}
	for _, e := range p.Errors {
		if e.Field == field {
			return
		}
	}
	t.Fatalf("problem = %+v; want field %q", p, field)
}

func seedReadTask(t *testing.T, srv *testutil.Server, in service.AddTaskIn) *service.OpResult {
	t.Helper()
	res, err := srv.Svc.AddTask(t.Context(), testutil.Meta(testutil.Owner(), srv.Svc.Generation()), in)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestDAT14_Clock(t *testing.T) {
	srv := testutil.StartServer(t)
	v, err := srv.Client.Clock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	now, err := json.Marshal(v.Now)
	if err != nil {
		t.Fatal(err)
	}
	if string(now) != `"2026-10-28T10:00:00.000-06:00"` || v.Today != civil.MustDate("2026-10-28") || v.Timezone != "America/Denver" || v.WeekStart != "monday" || v.DueSoonDays != 3 || v.ServerVersion != buildinfo.Version {
		t.Fatalf("clock = %+v, now = %s", v, now)
	}
	gen, err := hex.DecodeString(v.Generation)
	if err != nil || len(gen) != 8 {
		t.Fatalf("generation = %q, %v", v.Generation, err)
	}
	who, err := srv.Client.WhoAmI(t.Context())
	if err != nil || who.Generation != v.Generation {
		t.Fatalf("identity = %+v, %v", who, err)
	}
	srv.Clock.Set(testutil.Clock(t, "2026-11-02 09:00").Now())
	v, err = srv.Client.Clock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	now, err = json.Marshal(v.Now)
	if err != nil || string(now) != `"2026-11-02T09:00:00.000-07:00"` {
		t.Fatalf("winter now = %s, %v", now, err)
	}
}

func TestREC06_TodayETagStable(t *testing.T) {
	srv := testutil.StartServer(t)
	for _, path := range []string{"/today", "/status"} {
		t.Run(path, func(t *testing.T) {
			code, headers, body := readAPI(t, srv, path, "")
			etag := headers.Get("ETag")
			if code != 200 || etag != api.AggregateETag(body) {
				t.Fatalf("GET %s = %d, ETag %q for %s", path, code, etag, body)
			}
			code, again, body2 := readAPI(t, srv, path, "")
			if code != 200 || again.Get("ETag") != etag || string(body2) != string(body) {
				t.Fatalf("unstable aggregate: %d, %s", code, body2)
			}
			code, matched, empty := readAPI(t, srv, path, etag)
			if code != 304 || len(empty) != 0 || matched.Get("ETag") != etag {
				t.Fatalf("conditional GET = %d %v %s", code, matched, empty)
			}
			if path == "/today" {
				v, got, unchanged, err := srv.Client.Today(t.Context(), etag)
				if err != nil || v != nil || !unchanged || got != etag {
					t.Fatalf("Today = %+v, %q, %t, %v", v, got, unchanged, err)
				}
			} else {
				v, got, unchanged, err := srv.Client.Status(t.Context(), etag)
				if err != nil || v != nil || !unchanged || got != etag {
					t.Fatalf("Status = %+v, %q, %t, %v", v, got, unchanged, err)
				}
			}
			seedReadTask(t, srv, service.AddTaskIn{Title: path})
			if path == "/today" {
				v, got, unchanged, err := srv.Client.Today(t.Context(), etag)
				if err != nil || v == nil || unchanged || got == etag {
					t.Fatalf("changed Today = %+v, %q, %t, %v", v, got, unchanged, err)
				}
			} else {
				v, got, unchanged, err := srv.Client.Status(t.Context(), etag)
				if err != nil || v == nil || unchanged || got == etag {
					t.Fatalf("changed Status = %+v, %q, %t, %v", v, got, unchanged, err)
				}
			}
		})
	}
}

func TestListTasksValidation(t *testing.T) {
	srv := testutil.StartServer(t)
	for _, tc := range []struct {
		query  string
		status int
		code   domain.Code
		field  string
	}{
		{"", 422, domain.CodeValidation, "view"},
		{"?view=bogus", 422, domain.CodeValidation, "view"},
		{"?view=triage&limit=0", 400, domain.CodeMalformed, ""},
		{"?view=triage&limit=501", 400, domain.CodeMalformed, ""},
		{"?view=triage&limit=x", 400, domain.CodeMalformed, ""},
		{"?view=triage&limit=", 400, domain.CodeMalformed, ""},
	} {
		code, _, body := readAPI(t, srv, "/tasks"+tc.query, "")
		var p api.Problem
		if err := json.Unmarshal(body, &p); err != nil || code != tc.status || p.Code != string(tc.code) {
			t.Fatalf("GET %s: %d %s, %v", tc.query, code, body, err)
		}
		requireProblemField(t, p, tc.field)
	}
}

func TestPagination(t *testing.T) {
	srv := testutil.StartServer(t)
	for i := range 3 {
		seedReadTask(t, srv, service.AddTaskIn{Title: fmt.Sprintf("task %d", i)})
	}
	q := client.TaskQuery{View: "triage", PageQuery: client.PageQuery{Limit: 1}}
	seen := make(map[int64]bool)
	for i := range 3 {
		page, err := srv.Client.ListTasks(t.Context(), q)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || seen[page.Items[0].ID] {
			t.Fatalf("page = %+v, seen = %v", page, seen)
		}
		seen[page.Items[0].ID] = true
		if i == 2 {
			if page.NextCursor != nil {
				t.Fatal("last cursor must be null")
			}
		} else {
			if page.NextCursor == nil {
				t.Fatal("missing cursor")
			}
			q.Cursor = *page.NextCursor
		}
	}
	q.Cursor = "!!"
	_, err := srv.Client.ListTasks(t.Context(), q)
	requireReadProblem(t, err, 400, domain.CodeMalformed, "")
}

func TestEntriesRange(t *testing.T) {
	srv := testutil.StartServer(t)
	seedReadTask(t, srv, service.AddTaskIn{Title: "task"})
	from, to := civil.MustDate("2026-10-28"), civil.MustDate("2026-10-28").AddDays(367)
	for _, tc := range []struct {
		name  string
		q     client.EntryQuery
		field string
	}{
		{"missing", client.EntryQuery{}, "from"},
		{"span", client.EntryQuery{From: &from, To: &to}, "to"},
		{"standalone", client.EntryQuery{From: &from, To: &from, Standalone: true}, ""},
		{"task", client.EntryQuery{Task: new(int64(1))}, ""},
		{"standalone missing", client.EntryQuery{Standalone: true}, "from"},
		{"missing to", client.EntryQuery{From: &from}, "to"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := srv.Client.ListEntries(t.Context(), tc.q)
			if tc.field != "" {
				requireReadProblem(t, err, 422, domain.CodeValidation, tc.field)
			} else if err != nil {
				t.Fatal(err)
			}
			if !tc.q.Standalone {
				_, err = srv.Client.ListBlocks(t.Context(), client.BlockQuery{From: tc.q.From, To: tc.q.To, Task: tc.q.Task})
				if tc.field != "" {
					requireReadProblem(t, err, 422, domain.CodeValidation, tc.field)
				} else if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestReportParams(t *testing.T) {
	srv := testutil.StartServer(t)
	_, err := srv.Client.Report(t.Context(), client.ReportQuery{From: civil.MustDate("2026-10-29"), To: civil.MustDate("2026-10-28")})
	requireReadProblem(t, err, 422, domain.CodeValidation, "to")
	code, _, body := readAPI(t, srv, "/report?to=2026-10-28", "")
	var p api.Problem
	if err := json.Unmarshal(body, &p); err != nil || code != 422 || p.Code != "validation" {
		t.Fatalf("missing from: %d %s, %v", code, body, err)
	}
	requireProblemField(t, p, "from")
}

func TestGetTaskETag(t *testing.T) {
	srv := testutil.StartServer(t)
	seedReadTask(t, srv, service.AddTaskIn{Title: "task"})
	code, headers, body := readAPI(t, srv, "/tasks/1", "")
	var v api.TaskDetail
	if err := json.Unmarshal(body, &v); err != nil || code != 200 {
		t.Fatalf("get task: %d %s, %v", code, body, err)
	}
	want := `"` + string(srv.Svc.Generation()) + `-1"`
	if v.ETag != want || headers.Get("ETag") != want {
		t.Fatalf("ETag = %q / %q; want %q", v.ETag, headers.Get("ETag"), want)
	}
	_, err := srv.Client.GetTask(t.Context(), 999)
	requireReadProblem(t, err, 404, domain.CodeNotFound, "")
	code, _, body = readAPI(t, srv, "/tasks/abc", "")
	var p api.Problem
	if err := json.Unmarshal(body, &p); err != nil || code != 400 || p.Code != "malformed" {
		t.Fatalf("invalid id: %d %s, %v", code, body, err)
	}
}

func TestWhoAmI(t *testing.T) {
	srv := testutil.StartServer(t)
	who, err := srv.Client.WhoAmI(t.Context())
	if err != nil || who.Actor != "owner" || !who.Owner || who.Scopes != "read,capture,write,destroy,admin" || who.Generation != string(srv.Svc.Generation()) {
		t.Fatalf("WhoAmI = %+v, %v", who, err)
	}
	capture := srv.TokenClient(t, "capture", domain.ScopeCapture, 0, "cli")
	who, err = capture.WhoAmI(t.Context())
	if err != nil || who.Actor != "token:capture" || who.Owner || who.Scopes != "capture" || who.Generation != string(srv.Svc.Generation()) {
		t.Fatalf("token WhoAmI = %+v, %v", who, err)
	}
	if _, err := capture.Clock(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = capture.Today(t.Context(), "")
	requireReadProblem(t, err, 403, domain.CodeForbidden, "")
}

func TestReadQueryValidation(t *testing.T) {
	srv := testutil.StartServer(t)
	for _, tc := range []struct {
		path  string
		code  domain.Code
		field string
	}{
		{"/search?q=x", domain.CodeValidation, "kind"},
		{"/search?q=x&kind=bogus", domain.CodeValidation, "kind"},
		{"/entries?task=bogus", domain.CodeValidation, "task"},
		{"/entries?task=0", domain.CodeValidation, "task"},
		{"/blocks?task=none&from=2026-10-28&to=2026-10-28", domain.CodeValidation, "task"},
		{"/blocks?task=none", domain.CodeValidation, "task"},
		{"/blocks?task=-1", domain.CodeValidation, "task"},
		{"/entries?from=tomorrow&to=2026-10-28", domain.CodeMalformed, ""},
		{"/blocks?from=2026-02-30&to=2026-10-28", domain.CodeMalformed, ""},
		{"/report?from=2026-10-28T00:00:00Z&to=2026-10-28", domain.CodeMalformed, ""},
		{"/days/tomorrow", domain.CodeMalformed, ""},
		{"/entries?task=1&include_deleted=yes", domain.CodeMalformed, ""},
		{"/report?from=2026-10-28&to=2026-10-28&todo=TRUE", domain.CodeMalformed, ""},
		{"/ai-changes?all=yes", domain.CodeMalformed, ""},
		{"/ops?since_seq=x", domain.CodeMalformed, ""},
		{"/ops?since_seq=-1", domain.CodeValidation, "since_seq"},
		{"/ops/not-an-op", domain.CodeMalformed, ""},
		{"/search?q=x&kind=all&cursor=!!", domain.CodeMalformed, ""},
		{"/entries?task=1&cursor=!!", domain.CodeMalformed, ""},
		{"/tasks/1/history?cursor=!!", domain.CodeMalformed, ""},
		{"/ops?cursor=!!", domain.CodeMalformed, ""},
		{"/ai-changes?cursor=!!", domain.CodeMalformed, ""},
	} {
		t.Run(tc.path, func(t *testing.T) {
			status, _, body := readAPI(t, srv, tc.path, "")
			var p api.Problem
			want := 400
			if tc.code == domain.CodeValidation {
				want = 422
			}
			if err := json.Unmarshal(body, &p); err != nil || status != want || p.Code != string(tc.code) {
				t.Fatalf("GET = %d %s, %v", status, body, err)
			}
			requireProblemField(t, p, tc.field)
		})
	}
	for _, value := range []string{"true", "false", "1", "0"} {
		for _, path := range []string{"/ai-changes?all=", "/entries?task=1&include_deleted=", "/report?from=2026-10-28&to=2026-10-28&upcoming="} {
			if status, _, body := readAPI(t, srv, path+value, ""); status != 200 {
				t.Fatalf("GET %s%s = %d %s", path, value, status, body)
			}
		}
	}
}

func TestReadViews(t *testing.T) {
	srv := testutil.StartServer(t)
	ctx := t.Context()
	date := civil.MustDate("2026-10-28")
	meta := func() service.Meta { return testutil.Meta(testutil.Owner(), srv.Svc.Generation()) }
	task := seedReadTask(t, srv, service.AddTaskIn{Title: "quartz task", Tags: []string{"read-tag"}, Project: "R-D"})
	entry, err := srv.Svc.AddEntry(ctx, meta(), service.AddEntryIn{TaskID: sql.Null[domain.TaskID]{V: task.Task.ID, Valid: true}, Title: "quartz work", Notes: "entry notes", At: sql.Null[civil.Minute]{V: 9 * 60, Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	standalone, err := srv.Svc.AddEntry(ctx, meta(), service.AddEntryIn{Title: "quartz standalone"})
	if err != nil {
		t.Fatal(err)
	}
	block, err := srv.Svc.Block(ctx, meta(), service.BlockIn{TaskID: task.Task.ID, Expect: domain.Expect{Gen: srv.Svc.Generation(), Version: task.Task.Version}, Span: domain.BlockSpan{Date: date, Start: 10 * 60, End: 11 * 60}})
	if err != nil {
		t.Fatal(err)
	}
	seedReadTask(t, srv, service.AddTaskIn{Title: "quartz future", Due: &domain.Moment{Date: date.AddDays(2)}, Place: &service.AddPlacement{How: "defer", Defer: domain.Moment{Date: date.AddDays(1)}}})
	id := int64(task.Task.ID)
	detail, err := srv.Client.GetTask(ctx, id)
	if err != nil || len(detail.Entries) != 1 || len(detail.Blocks) != 1 || detail.Title != task.Task.Title || detail.Entries[0].TaskTitle == nil || *detail.Entries[0].TaskTitle != task.Task.Title {
		t.Fatalf("detail = %+v, %v", detail, err)
	}
	gotEntry, err := srv.Client.GetEntry(ctx, int64(entry.Entry.ID))
	if err != nil || gotEntry.Notes != "entry notes" || gotEntry.Time == nil || *gotEntry.Time != 9*60 {
		t.Fatalf("entry = %+v, %v", gotEntry, err)
	}
	gotBlock, err := srv.Client.GetBlock(ctx, int64(block.Block.ID))
	if err != nil || gotBlock.TaskETag != detail.ETag || !gotBlock.Current || gotBlock.TaskTitle != task.Task.Title {
		t.Fatalf("block = %+v, %v", gotBlock, err)
	}
	for _, v := range []struct{ path, etag string }{{"/entries/" + strconv.FormatInt(gotEntry.ID, 10), gotEntry.ETag}, {"/blocks/" + strconv.FormatInt(gotBlock.ID, 10), gotBlock.ETag}} {
		code, h, _ := readAPI(t, srv, v.path, "")
		if code != 200 || h.Get("ETag") != v.etag {
			t.Fatalf("GET %s = %d, %v", v.path, code, h)
		}
	}
	entries, err := srv.Client.ListEntries(ctx, client.EntryQuery{From: &date, To: &date, Standalone: true})
	if err != nil || len(entries.Items) != 1 || entries.Items[0].ID != int64(standalone.Entry.ID) {
		t.Fatalf("standalone = %+v, %v", entries, err)
	}
	blocks, err := srv.Client.ListBlocks(ctx, client.BlockQuery{Task: &id})
	if err != nil || len(blocks) != 1 || blocks[0].ID != gotBlock.ID {
		t.Fatalf("blocks = %+v, %v", blocks, err)
	}
	day, err := srv.Client.Day(ctx, date)
	if err != nil || day.Date != date || len(day.Entries) != 2 || len(day.Unlinked) != 1 || len(day.Blocks) != 1 {
		t.Fatalf("day = %+v, %v", day, err)
	}
	upcoming, err := srv.Client.Upcoming(ctx)
	if err != nil || len(upcoming.Deferred) != 1 {
		t.Fatalf("upcoming = %+v, %v", upcoming, err)
	}
	today, _, _, err := srv.Client.Today(ctx, "")
	if err != nil || today.CurrentBlock == nil || today.CurrentBlock.ID != gotBlock.ID || len(today.Entries) != 2 || today.Clock.Generation != string(srv.Svc.Generation()) {
		t.Fatalf("today = %+v, %v", today, err)
	}
	status, _, _, err := srv.Client.Status(ctx, "")
	if err != nil || status.CurrentBlock == nil || status.CurrentBlock.TaskID != id || status.CurrentBlock.End != 11*60 {
		t.Fatalf("status = %+v, %v", status, err)
	}
	tags, err := srv.Client.Tags(ctx)
	if err != nil || len(tags) != 1 || tags[0].Name != "read-tag" || tags[0].Count != 1 {
		t.Fatalf("tags = %+v, %v", tags, err)
	}
	tasks, err := srv.Client.ListTasks(ctx, client.TaskQuery{View: "all", Tag: "read-tag", Project: "R-D"})
	if err != nil || len(tasks.Items) != 1 || tasks.Items[0].ID != id {
		t.Fatalf("filtered tasks = %+v, %v", tasks, err)
	}
	report, err := srv.Client.Report(ctx, client.ReportQuery{From: date, To: date, Tag: "read-tag", Project: "R-D", Todo: true, Upcoming: true})
	if err != nil || len(report.Days) != 1 || len(report.Days[0].Lines) != 1 || report.Todo == nil || report.Tag == nil || *report.Tag != "read-tag" || report.Project == nil || *report.Project != "R-D" || report.Tasks[strconv.FormatInt(id, 10)].Title != task.Task.Title {
		t.Fatalf("report = %+v, %v", report, err)
	}
	search, err := srv.Client.Search(ctx, client.SearchQuery{Q: "quartz", Kind: "entry", PageQuery: client.PageQuery{Limit: 1}})
	if err != nil || len(search.Items) != 1 || search.Items[0].Kind != "entry" || search.Items[0].WorkDate == nil || search.NextCursor == nil {
		t.Fatalf("search = %+v, %v", search, err)
	}
	next, err := srv.Client.Search(ctx, client.SearchQuery{Q: "quartz", Kind: "entry", PageQuery: client.PageQuery{Limit: 1, Cursor: *search.NextCursor}})
	if err != nil || len(next.Items) != 1 || next.Items[0].ID == search.Items[0].ID || next.NextCursor != nil {
		t.Fatalf("next search = %+v, %v", next, err)
	}
	history, err := srv.Client.TaskHistory(ctx, id, client.PageQuery{Limit: 1})
	if err != nil || len(history.Items) != 1 || history.NextCursor == nil || len(history.Items[0].After) == 0 {
		t.Fatalf("history = %+v, %v", history, err)
	}
	history2, err := srv.Client.TaskHistory(ctx, id, client.PageQuery{Limit: 1, Cursor: *history.NextCursor})
	if err != nil || len(history2.Items) != 1 || history.Items[0].ID == history2.Items[0].ID {
		t.Fatalf("next history = %+v, %v", history2, err)
	}
	op, err := srv.Client.GetOp(ctx, task.OpID.String())
	if err != nil || op.Op.OpID != task.OpID.String() || op.Op.Actor != "owner" || len(op.Events) == 0 || len(op.Op.TaskIDs) != 1 || op.Op.TaskIDs[0] != id {
		t.Fatalf("op = %+v, %v", op, err)
	}
	ops, err := srv.Client.ListOps(ctx, client.OpsQuery{Actor: "owner", SinceSeq: op.Op.Seq, PageQuery: client.PageQuery{Limit: 1}})
	if err != nil || len(ops.Items) != 1 || ops.Items[0].Seq <= op.Op.Seq || ops.NextCursor == nil {
		t.Fatalf("ops = %+v, %v", ops, err)
	}
	settings, err := srv.Client.Settings(ctx)
	if err != nil || settings.BackupKeepDaily != 7 || settings.BackupKeepWeekly != 4 {
		t.Fatalf("settings = %+v, %v", settings, err)
	}
	ai, err := srv.Client.AIChanges(ctx, false, client.PageQuery{})
	if err != nil || ai.Unreviewed != 0 || ai.Ops == nil || ai.NextCursor != nil {
		t.Fatalf("ai = %+v, %v", ai, err)
	}
}

func TestPaginationDefaultAndMaximum(t *testing.T) {
	srv := testutil.StartServer(t)
	for i := range 101 {
		seedReadTask(t, srv, service.AddTaskIn{Title: fmt.Sprintf("task %d", i)})
	}
	page, err := srv.Client.ListTasks(t.Context(), client.TaskQuery{View: "all"})
	if err != nil || len(page.Items) != 100 || page.NextCursor == nil {
		t.Fatalf("default page = %+v, %v", page, err)
	}
	page, err = srv.Client.ListTasks(t.Context(), client.TaskQuery{View: "all", PageQuery: client.PageQuery{Limit: 500}})
	if err != nil || len(page.Items) != 101 || page.NextCursor != nil {
		t.Fatalf("maximum page = %+v, %v", page, err)
	}
}

func TestEntriesPaginationAndDeleted(t *testing.T) {
	srv := testutil.StartServer(t)
	ctx := t.Context()
	date := civil.MustDate("2026-10-28")
	meta := func() service.Meta { return testutil.Meta(testutil.Owner(), srv.Svc.Generation()) }
	task := seedReadTask(t, srv, service.AddTaskIn{Title: "linked task"})
	var entries []*service.OpResult
	for i := range 3 {
		entry, err := srv.Svc.AddEntry(ctx, meta(), service.AddEntryIn{Title: fmt.Sprintf("entry %d", i), TaskID: sql.Null[domain.TaskID]{V: task.Task.ID, Valid: true}})
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	q := client.EntryQuery{Task: new(int64(task.Task.ID)), PageQuery: client.PageQuery{Limit: 1}}
	seen := make(map[int64]bool)
	for i := range 3 {
		page, err := srv.Client.ListEntries(ctx, q)
		if err != nil || len(page.Items) != 1 || seen[page.Items[0].ID] {
			t.Fatalf("entries page = %+v, %v", page, err)
		}
		seen[page.Items[0].ID] = true
		if i < 2 {
			if page.NextCursor == nil {
				t.Fatal("missing entries cursor")
			}
			q.Cursor = *page.NextCursor
		} else if page.NextCursor != nil {
			t.Fatal("last entries cursor must be null")
		}
	}
	entry := entries[0].Entry
	if _, err := srv.Svc.DeleteEntry(ctx, meta(), service.EntryRef{ID: entry.ID, Expect: domain.Expect{Gen: srv.Svc.Generation(), Version: entry.Version}}); err != nil {
		t.Fatal(err)
	}
	for _, include := range []bool{false, true} {
		page, err := srv.Client.ListEntries(ctx, client.EntryQuery{From: &date, To: &date, IncludeDeleted: include})
		want := 2
		if include {
			want = 3
		}
		if err != nil || len(page.Items) != want {
			t.Fatalf("include_deleted=%t: %+v, %v", include, page, err)
		}
	}
}

func TestReadAIChangesAndOpsPagination(t *testing.T) {
	srv := testutil.StartServer(t)
	ctx := t.Context()
	for i := range 3 {
		if _, err := srv.Svc.AddTask(ctx, testutil.Meta(testutil.TokenActor("reader-fixture", domain.ScopeCapture), srv.Svc.Generation()), service.AddTaskIn{Title: fmt.Sprintf("AI task %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := srv.Client.AIChanges(ctx, false, client.PageQuery{Limit: 1})
	if err != nil || first.Unreviewed != 3 || first.ReviewedThrough != 0 || len(first.Ops) != 1 || first.NextCursor == nil || first.Ops[0].Actor != "token:reader-fixture" {
		t.Fatalf("AI changes = %+v, %v", first, err)
	}
	second, err := srv.Client.AIChanges(ctx, false, client.PageQuery{Limit: 1, Cursor: *first.NextCursor})
	if err != nil || len(second.Ops) != 1 || second.Ops[0].Seq >= first.Ops[0].Seq {
		t.Fatalf("second AI page = %+v, %v", second, err)
	}
	ops, err := srv.Client.ListOps(ctx, client.OpsQuery{Actor: "token:reader-fixture", PageQuery: client.PageQuery{Limit: 1}})
	if err != nil || len(ops.Items) != 1 || ops.Items[0].OpID != first.Ops[0].OpID || ops.NextCursor == nil {
		t.Fatalf("ops = %+v, %v", ops, err)
	}
	next, err := srv.Client.ListOps(ctx, client.OpsQuery{Actor: "token:reader-fixture", PageQuery: client.PageQuery{Limit: 1, Cursor: *ops.NextCursor}})
	if err != nil || len(next.Items) != 1 || next.Items[0].Seq >= ops.Items[0].Seq {
		t.Fatalf("next ops = %+v, %v", next, err)
	}
	if _, err := srv.Svc.MarkAIReviewed(ctx, testutil.Meta(testutil.Owner(), srv.Svc.Generation()), service.MarkAIReviewedIn{Through: domain.OpSeq(first.Ops[0].Seq)}); err != nil {
		t.Fatal(err)
	}
	unreviewed, err := srv.Client.AIChanges(ctx, false, client.PageQuery{})
	if err != nil || unreviewed.Unreviewed != 0 || len(unreviewed.Ops) != 0 || unreviewed.Ops == nil || unreviewed.ReviewedThrough != first.Ops[0].Seq {
		t.Fatalf("reviewed = %+v, %v", unreviewed, err)
	}
	all, err := srv.Client.AIChanges(ctx, true, client.PageQuery{})
	if err != nil || len(all.Ops) != 3 || all.NextCursor != nil {
		t.Fatalf("all AI changes = %+v, %v", all, err)
	}
}

func TestReadEmptyArrays(t *testing.T) {
	srv := testutil.StartServer(t)
	for _, tc := range []struct {
		path   string
		arrays []string
	}{
		{"/today", []string{"blocks", "triage_top", "overdue", "due_soon", "entries", "interruptions"}},
		{"/days/2026-10-28", []string{"blocks", "entries", "unlinked", "interruptions"}},
		{"/upcoming", []string{"deferred", "due_soon"}},
		{"/tasks?view=all", []string{"items"}},
		{"/entries?from=2026-10-28&to=2026-10-28", []string{"items"}},
		{"/search?q=absent&kind=all", []string{"items"}},
		{"/tasks/1/history", []string{"items"}},
		{"/ai-changes", []string{"ops"}},
		{"/report?from=2026-10-28&to=2026-10-28", []string{"days"}},
	} {
		status, _, body := readAPI(t, srv, tc.path, "")
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil || status != 200 {
			t.Fatalf("GET %s = %d %s, %v", tc.path, status, body, err)
		}
		for _, field := range tc.arrays {
			if string(fields[field]) != "[]" {
				t.Fatalf("GET %s field %s = %s; want []", tc.path, field, fields[field])
			}
		}
		if cursor, ok := fields["next_cursor"]; ok && string(cursor) != "null" {
			t.Fatalf("GET %s cursor = %s", tc.path, cursor)
		}
	}
	for _, path := range []string{"/tags", "/blocks?from=2026-10-28&to=2026-10-28"} {
		status, _, body := readAPI(t, srv, path, "")
		if status != 200 || strings.TrimSpace(string(body)) != "[]" {
			t.Fatalf("GET %s = %d %s", path, status, body)
		}
	}
}
