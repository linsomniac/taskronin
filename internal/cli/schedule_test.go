package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/client"
	"taskronin/internal/testutil"
)

func scheduleTask(t *testing.T, srv *testutil.Server, title string) *api.Task {
	t.Helper()
	res, err := srv.Client.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{
		Title: title, Place: &api.AddPlacement{How: "bottom"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return res.Task
}

func TestMoveKeep(t *testing.T) {
	srv, sys := captureServer(t)
	for _, title := range []string{"First", "Second", "Third"} {
		scheduleTask(t, srv, title)
	}
	for _, tc := range []struct {
		args        []string
		want, order string
	}{
		{[]string{"move", "3", "before", "1"}, "#3 moved before #1\n", "[3 1 2]"},
		{[]string{"keep", "2"}, "#2 kept in place\n", "[3 1 2]"},
		{[]string{"move", "#3", "after", "#2"}, "#3 moved after #2\n", "[1 2 3]"},
		{[]string{"move", "2", "top"}, "#2 moved top\n", "[2 1 3]"},
		{[]string{"move", "2", "bottom"}, "#2 moved bottom\n", "[1 3 2]"},
	} {
		out := captureRun(t, sys, "", tc.args...)
		if out != tc.want {
			t.Errorf("%v: stdout=%q; want %q", tc.args, out, tc.want)
		}
		page, err := srv.Client.ListTasks(t.Context(), client.TaskQuery{View: "backlog"})
		if err != nil {
			t.Fatal(err)
		}
		var ids []int64
		for _, task := range page.Items {
			ids = append(ids, task.ID)
		}
		if fmt.Sprint(ids) != tc.order {
			t.Fatalf("%v: order=%v; want %s", tc.args, ids, tc.order)
		}
	}
}

func TestDeferNone(t *testing.T) {
	srv, sys := captureServer(t)
	scheduleTask(t, srv, "First")
	for _, tc := range []struct{ date, want string }{
		{"fri", "#1 deferred to Fri 2026-10-30\n"},
		{"fri 9:00", "#1 deferred to Fri 2026-10-30 09:00\n"},
		{"none", "#1 defer cleared\n"},
	} {
		if out := captureRun(t, sys, "", "defer", "1", tc.date); out != tc.want {
			t.Errorf("%s: stdout=%q; want %q", tc.date, out, tc.want)
		}
		task, err := srv.Client.GetTask(t.Context(), 1)
		if err != nil {
			t.Fatal(err)
		}
		if tc.date == "none" {
			if task.Defer != nil || task.View != "backlog" {
				t.Fatalf("cleared task=%+v", task)
			}
		} else if task.Defer == nil || task.Defer.Date.String() != "2026-10-30" || task.View != "upcoming" {
			t.Fatalf("deferred task=%+v", task)
		}
	}
}

func TestDueNone(t *testing.T) {
	srv, sys := captureServer(t)
	scheduleTask(t, srv, "First")
	for _, tc := range []struct{ date, want string }{
		{"fri", "#1 due Fri 2026-10-30\n"},
		{"wed 16:00", "#1 due Wed 2026-11-04 16:00\n"},
		{"none", "#1 due date cleared\n"},
	} {
		if out := captureRun(t, sys, "", "due", "#1", tc.date); out != tc.want {
			t.Errorf("%s: stdout=%q; want %q", tc.date, out, tc.want)
		}
		task, err := srv.Client.GetTask(t.Context(), 1)
		if err != nil {
			t.Fatal(err)
		}
		if tc.date == "none" {
			if task.Due != nil {
				t.Fatalf("due not cleared: %+v", task.Due)
			}
		} else if task.Due == nil || (tc.date == "wed 16:00" && (task.Due.Date.String() != "2026-11-04" || task.Due.Time == nil || *task.Due.Time != 960)) {
			t.Fatalf("due=%+v", task.Due)
		}
	}
}

func TestBlockSpans(t *testing.T) {
	srv, sys := captureServer(t)
	scheduleTask(t, srv, "First")
	if out := captureRun(t, sys, "", "block", "1", "13:00-15:00"); out != "b1 today 13:00–15:00 #1 'First'\n" {
		t.Fatalf("stdout=%q", out)
	}
	for _, span := range []string{"15:00-13:00", "1pm-3pm"} {
		out, stderr, code := runCLI(t, sys, "", "block", "1", span)
		if code != 2 || out != "" || stderr == "" {
			t.Fatalf("%s: exit=%d stdout=%q stderr=%q", span, code, out, stderr)
		}
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"block", "1", "23:00-24:00", "--date", "wed"}, "b2 Wed 2026-11-04 23:00–24:00 #1 'First'\n"},
		{[]string{"block", "1", "fri 13:00-15:00"}, "b3 Fri 2026-10-30 13:00–15:00 #1 'First'\n"},
	} {
		if out := captureRun(t, sys, "", tc.args...); out != tc.want {
			t.Errorf("%v: stdout=%q; want %q", tc.args, out, tc.want)
		}
	}
	task, err := srv.Client.GetTask(t.Context(), 1)
	if err != nil || len(task.Blocks) != 3 {
		t.Fatalf("blocks=%+v, err=%v", task, err)
	}
}

func TestBLK12_BlockSpansGap(t *testing.T) {
	srv := testutil.StartServer(t, testutil.WithClock("2027-03-14 00:30"))
	scheduleTask(t, srv, "DST gap")
	out, stderr, code := runCLI(t, sysDeps{Getenv: os.Getenv}, "", "block", "1", "01:00-02:00")
	if code != 1 || out != "" || !strings.Contains(stderr, "03:00") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	task, err := srv.Client.GetTask(t.Context(), 1)
	if err != nil || len(task.Blocks) != 0 {
		t.Fatalf("rejected block was saved: task=%+v err=%v", task, err)
	}
}

func TestReblockSendsTaskETag(t *testing.T) {
	srv, sys := captureServer(t)
	scheduleTask(t, srv, "First")
	captureRun(t, sys, "", "block", "1", "13:00-15:00")
	if out := captureRun(t, sys, "", "reblock", "b1", "16:00-17:00"); out != "b1 today 16:00–17:00 #1 'First'\n" {
		t.Errorf("stdout=%q", out)
	}
	block, err := srv.Client.GetBlock(t.Context(), 1)
	if err != nil || block.ID != 1 || block.Start != 960 || block.End != 1020 || block.Started || block.Status != "planned" {
		t.Fatalf("block=%+v err=%v", block, err)
	}
	srv.Clock.Set(testutil.Local(t, "2026-10-28 10:20"))
	out := captureRun(t, sys, "", "reblock", "1", "16:00-17:00", "--date", "fri")
	if !strings.HasPrefix(out, "b2 Fri 2026-10-30 16:00–17:00 #1 'First'\n") || !strings.Contains(out, "b1 cancelled") || !strings.Contains(out, "b1 slipped") {
		t.Fatalf("replacement stdout=%q", out)
	}
	old, err := srv.Client.GetBlock(t.Context(), 1)
	if err != nil || old.Status != "cancelled" || old.CancelReason == nil || *old.CancelReason != "rescheduled" {
		t.Fatalf("replaced block=%+v err=%v", old, err)
	}
}

func TestUnblockOutcome(t *testing.T) {
	srv, sys := captureServer(t)
	scheduleTask(t, srv, "First")
	captureRun(t, sys, "", "block", "1", "13:00-15:00")
	srv.Clock.Set(testutil.Local(t, "2026-10-28 10:20"))
	out := captureRun(t, sys, "", "unblock", "b1")
	if !strings.HasPrefix(out, "b1 cancelled\n") || !strings.Contains(out, "b1 slipped\n") || strings.Count(out, "b1 cancelled") != 1 {
		t.Fatalf("cancel stdout=%q", out)
	}
	captureRun(t, sys, "", "block", "1", "13:00-15:00")
	srv.Clock.Set(testutil.Local(t, "2026-10-28 14:00"))
	out = captureRun(t, sys, "", "unblock", "2")
	if !strings.HasPrefix(out, "b2 missed\n") || !strings.Contains(out, "b2 slipped\n") || strings.Count(out, "b2 missed\n") != 1 {
		t.Fatalf("resolve stdout=%q", out)
	}
	srv.Clock.Set(testutil.Local(t, "2026-10-29 10:00"))
	for _, outcome := range []string{"worked", "missed", "auto"} {
		out = captureRun(t, sys, "", "outcome", "b2", outcome)
		if !strings.HasPrefix(out, "b2 marked "+outcome+"\n") || strings.Count(out, "b2 marked ") != 1 {
			t.Errorf("%s: stdout=%q", outcome, out)
		}
		block, err := srv.Client.GetBlock(t.Context(), 2)
		wantStatus, wantSource := outcome, "user"
		if outcome == "auto" {
			wantStatus, wantSource = "missed", "auto"
		}
		if err != nil || block.Status != wantStatus || block.OutcomeSource == nil || *block.OutcomeSource != wantSource {
			t.Fatalf("%s: block=%+v err=%v", outcome, block, err)
		}
	}
}

func TestNowInterrupt(t *testing.T) {
	srv, sys := captureServer(t)
	scheduleTask(t, srv, "First")
	scheduleTask(t, srv, "Second")
	for _, id := range []string{"2", "1", "2"} {
		out := captureRun(t, sys, "", "now", id)
		if strings.Contains(out, "interrupted") || strings.Count(out, "focus →") != 1 {
			t.Fatalf("implicit interrupt or duplicate focus: %q", out)
		}
	}
	out := captureRun(t, sys, "", "now", "#1", "--interrupt")
	if out != "focus → #1 'First'\ninterrupted #2\n" {
		t.Fatalf("stdout=%q", out)
	}
	if out := captureRun(t, sys, "", "now", "1"); out != "focus → #1 'First'\n" {
		t.Fatalf("unchanged focus stdout=%q", out)
	}
	for range 2 {
		if out := captureRun(t, sys, "", "now", "--clear"); out != "focus cleared\n" {
			t.Fatalf("clear stdout=%q", out)
		}
	}
	status, _, _, err := srv.Client.Status(t.Context(), "")
	if err != nil || status.Focus != nil {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestStaleIfMatchRendering(t *testing.T) {
	sys, _ := testSys(t)
	var gets, posts atomic.Int32
	etag := `"test-generation:3"`
	socket, token := fakeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/tasks/1":
			gets.Add(1)
			writeJSON(t, w, api.TaskDetail{Task: api.Task{ID: 1, Version: 3, ETag: etag}})
		case "POST /api/v1/tasks/1/place":
			posts.Add(1)
			if gets.Load() != 1 || r.Header.Get("If-Match") != etag {
				t.Errorf("GETs=%d If-Match=%q", gets.Load(), r.Header.Get("If-Match"))
			}
			w.WriteHeader(http.StatusPreconditionFailed)
			writeJSON(t, w, api.Problem{Status: 412, Code: "precondition_failed", Detail: "task changed", Current: json.RawMessage(`{"id":1,"version":4}`)})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	out, stderr, code := runCLI(t, sys, "", "--socket", socket, "--token-file", token, "keep", "1")
	if code != 6 || out != "" || !strings.Contains(stderr, "  current: #1 version 4\n") || posts.Load() != 1 {
		t.Fatalf("exit=%d stdout=%q stderr=%q POSTs=%d", code, out, stderr, posts.Load())
	}
}

func TestSchedulingUsage(t *testing.T) {
	srv, sys := captureServer(t)
	task := scheduleTask(t, srv, "First")
	for _, args := range [][]string{
		{"move", "1"}, {"move", "1", "keep"}, {"move", "1", "before"}, {"move", "1", "top", "2"},
		{"move", "1", "after", "b2"}, {"keep", "0"}, {"defer", "1", "next friday"},
		{"due", "1", "9am"}, {"block", "1", "13:00-15:00", "--date", "fri 9:00"},
		{"block", "1", "13:00-15:00", "--date", ""},
		{"block", "1", "fri 13:00-15:00", "--date", "tomorrow"},
		{"reblock", "b1", "15:00-13:00"}, {"unblock", "#1"}, {"outcome", "b1", "done"},
		{"now"}, {"now", "--interrupt"}, {"now", "1", "--clear"}, {"now", "--clear", "--interrupt"},
	} {
		out, stderr, code := runCLI(t, sys, "", args...)
		if code != 2 || out != "" || stderr == "" {
			t.Errorf("%v: exit=%d stdout=%q stderr=%q", args, code, out, stderr)
		}
	}
	current, err := srv.Client.GetTask(t.Context(), 1)
	if err != nil || current.Version != task.Version || len(current.Blocks) != 0 {
		t.Fatalf("invalid command mutated task: %+v, err=%v", current, err)
	}
}

func TestSchedulingJSON(t *testing.T) {
	srv, sys := captureServer(t)
	scheduleTask(t, srv, "First")
	for _, args := range [][]string{
		{"move", "1", "top"}, {"keep", "1"}, {"defer", "1", "fri"}, {"defer", "1", "none"},
		{"due", "1", "fri"}, {"block", "1", "13:00-15:00"}, {"reblock", "b1", "16:00-17:00"},
		{"now", "1"}, {"now", "--clear"}, {"unblock", "b1"},
	} {
		out := captureRun(t, sys, "", append([]string{"--json"}, args...)...)
		var res api.OpResponse
		if err := json.Unmarshal([]byte(out), &res); err != nil || res.Effects == nil || res.Warnings == nil {
			t.Fatalf("%v: stdout=%q err=%v", args, out, err)
		}
	}
}

func TestReblockFreshTaskETag(t *testing.T) {
	sys, _ := testSys(t)
	var taskGets, posts atomic.Int32
	socket, token := fakeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/clock":
			writeJSON(t, w, api.Clock{Today: civil.MustDate("2026-10-28"), WeekStart: "monday"})
		case "GET /api/v1/blocks/1":
			writeJSON(t, w, api.Block{ID: 1, TaskID: 7, Date: civil.MustDate("2026-10-28"), Start: 780, End: 900, ETag: `"block-etag"`, TaskETag: `"older-task-etag"`})
		case "GET /api/v1/tasks/7":
			taskGets.Add(1)
			writeJSON(t, w, api.TaskDetail{Task: api.Task{ID: 7, ETag: `"fresh-task-etag"`}})
		case "POST /api/v1/blocks/1/reblock":
			posts.Add(1)
			var req api.ReblockRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if taskGets.Load() != 1 || req.TaskETag != `"fresh-task-etag"` || r.Header.Get("If-Match") != `"block-etag"` {
				t.Errorf("task GETs=%d req=%+v If-Match=%q", taskGets.Load(), req, r.Header.Get("If-Match"))
			}
			writeJSON(t, w, api.OpResponse{Block: &api.Block{ID: 1, TaskID: 7, TaskTitle: "First", Date: req.Date, Start: req.Start, End: req.End}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	out, stderr, code := runCLI(t, sys, "", "--socket", socket, "--token-file", token, "reblock", "b1", "16:00-17:00")
	if code != 0 || stderr != "" || posts.Load() != 1 || out != "b1 today 16:00–17:00 #7 'First'\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q POSTs=%d", code, out, stderr, posts.Load())
	}
}
