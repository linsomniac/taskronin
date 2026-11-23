package server_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"testing"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/client"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/testutil"
)

func TestFOC09_EntryTaskIDRequired(t *testing.T) {
	srv := testutil.StartServer(t)
	status, _, body := mutateAPI(t, srv, http.MethodPost, "/entries", `{"title":"x"}`, "", nil)
	p := taskProblem(t, status, 422, body, domain.CodeValidation, "task_id")
	if len(p.Errors) == 0 || p.Errors[0].Field != "task_id" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	status, headers, body := mutateAPI(t, srv, http.MethodPost, "/entries", `{"task_id":null,"title":"x"}`, "", nil)
	op := taskOp(t, status, 201, body)
	if headers.Get("Location") != "/api/v1/entries/1" || op.Entry == nil || op.Entry.ID != 1 || op.Entry.TaskID != nil || op.Entry.TaskTitle != nil || op.Entry.Title != "x" || op.Entry.WorkDate != civil.MustDate("2026-10-28") || op.Entry.Time != nil {
		t.Fatalf("standalone = %+v, headers = %v", op, headers)
	}
}

func TestEntryFutureDate(t *testing.T) {
	srv := testutil.StartServer(t)
	status, _, body := mutateAPI(t, srv, http.MethodPost, "/entries", `{"task_id":null,"title":"x","work_date":"2026-10-29"}`, "", nil)
	taskProblem(t, status, 422, body, domain.CodeValidation, "work_date")
}

func TestReblockTaskETag(t *testing.T) {
	srv := testutil.StartServer(t)
	task := addHTTPTask(t, srv, `{"title":"scheduled"}`)
	op, err := srv.Client.BlockTask(t.Context(), client.Mut{IfMatch: task.ETag}, task.ID, api.BlockRequest{Date: civil.MustDate("2026-10-28"), Start: 11 * 60, End: 12 * 60})
	if err != nil || op.Block == nil || op.Task == nil {
		t.Fatalf("block = %+v, %v", op, err)
	}
	for _, tc := range []struct {
		etag   string
		status int
		code   domain.Code
	}{{task.ETag, 412, domain.CodePrecondition}, {"nope", 400, domain.CodeMalformed}, {"", 400, domain.CodeMalformed}, {op.Task.ETag, 200, ""}} {
		request := api.ReblockRequest{BlockRequest: api.BlockRequest{Date: civil.MustDate("2026-10-28"), Start: 13 * 60, End: 14 * 60}, TaskETag: tc.etag}
		data, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		status, _, body := mutateAPI(t, srv, http.MethodPost, "/blocks/1/reblock", string(data), op.Block.ETag, nil)
		if tc.status != 200 {
			p := taskProblem(t, status, tc.status, body, tc.code, "")
			if tc.status == 412 {
				var current api.Task
				if err := json.Unmarshal(p.Current, &current); err != nil || !reflect.DeepEqual(&current, op.Task) {
					t.Fatalf("current task = %s, %v", p.Current, err)
				}
			}
			continue
		}
		result := taskOp(t, status, 200, body)
		if result.Block == nil || result.Task == nil || result.Block.ID != op.Block.ID || result.Block.Start != request.Start || result.Block.End != request.End || result.Block.TaskETag != result.Task.ETag {
			t.Fatalf("reblock = %+v", result)
		}
	}
}

func TestFocusNoIfMatch(t *testing.T) {
	srv := testutil.StartServer(t)
	addHTTPTask(t, srv, `{"title":"focus"}`)
	status, _, body := mutateAPI(t, srv, http.MethodPost, "/focus", `{"task_id":1}`, "", nil)
	op := taskOp(t, status, 200, body)
	if op.OpID == nil || len(op.Effects) == 0 {
		t.Fatalf("focus = %+v", op)
	}
	status, _, body = mutateAPI(t, srv, http.MethodPost, "/focus", `{"task_id":1}`, "", http.Header{api.HeaderIdempotencyKey: nil})
	taskProblem(t, status, 428, body, domain.CodeIdemRequired, "")
}

func TestUND18_EmptyStacksHTTP(t *testing.T) {
	srv := testutil.StartServer(t)
	for _, tc := range []struct {
		path string
		code domain.Code
	}{{"/undo", domain.CodeNothingToUndo}, {"/redo", domain.CodeNothingToRedo}} {
		status, _, body := mutateAPI(t, srv, http.MethodPost, tc.path, `{}`, "", nil)
		taskProblem(t, status, 409, body, tc.code, "")
	}
}

func TestRevertParams(t *testing.T) {
	srv := testutil.StartServer(t)
	for _, tc := range []struct {
		id     string
		status int
		code   domain.Code
	}{{"zz", 400, domain.CodeMalformed}, {"0123456789abcdef0123456789abcdef", 404, domain.CodeNotFound}} {
		status, _, body := mutateAPI(t, srv, http.MethodPost, "/ops/"+tc.id+"/revert", `{}`, "", nil)
		taskProblem(t, status, tc.status, body, tc.code, "")
	}
}

func TestUndoAfterAdd(t *testing.T) {
	srv := testutil.StartServer(t)
	task := addHTTPTask(t, srv, `{"title":"undo me"}`)
	m := client.Mut{Key: api.NewIdempotencyKey()}
	first, err := srv.Client.Undo(t.Context(), m)
	if err != nil || first.OpID == nil || len(first.Effects) == 0 {
		t.Fatalf("undo = %+v, %v", first, err)
	}
	current, err := srv.Client.GetTask(t.Context(), task.ID)
	if err != nil || current.View != "deleted" {
		t.Fatalf("undone task = %+v, %v", current, err)
	}
	replay, err := srv.Client.Undo(t.Context(), m)
	if err != nil || !replay.Replayed || replay.OpID == nil || *replay.OpID != *first.OpID {
		t.Fatalf("undo replay = %+v, %v", replay, err)
	}
	redo, err := srv.Client.Redo(t.Context(), client.Mut{})
	if err != nil || redo.OpID == nil || len(redo.Effects) == 0 {
		t.Fatalf("redo = %+v, %v", redo, err)
	}
	current, err = srv.Client.GetTask(t.Context(), task.ID)
	if err != nil || current.View != "triage" {
		t.Fatalf("redone task = %+v, %v", current, err)
	}
}

func TestAIReviewOwnerOnly(t *testing.T) {
	srv := testutil.StartServer(t)
	c := srv.TokenClient(t, "reviewer", domain.AllScopes, 100, "api")
	_, err := c.ReviewAIChanges(t.Context(), client.Mut{}, api.ReviewRequest{})
	requireReadProblem(t, err, 403, domain.CodeForbidden, "")
	_, err = srv.Client.ReviewAIChanges(t.Context(), client.Mut{}, api.ReviewRequest{ThroughSeq: 999999})
	requireReadProblem(t, err, 422, domain.CodeValidation, "")
	op, err := c.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: "AI capture"})
	if err != nil || op.OpID == nil {
		t.Fatalf("AI add = %+v, %v", op, err)
	}
	detail, err := srv.Client.GetOp(t.Context(), *op.OpID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := srv.Client.ReviewAIChanges(t.Context(), client.Mut{}, api.ReviewRequest{ThroughSeq: detail.Op.Seq})
	if err != nil || result.OpID == nil || len(result.Effects) == 0 {
		t.Fatalf("review = %+v, %v", result, err)
	}
	changes, err := srv.Client.AIChanges(t.Context(), false, client.PageQuery{})
	if err != nil || changes.ReviewedThrough != detail.Op.Seq || changes.Unreviewed != 0 {
		t.Fatalf("reviewed changes = %+v, %v", changes, err)
	}
}

func TestEntryMutationClients(t *testing.T) {
	srv := testutil.StartServer(t)
	ctx, c := t.Context(), srv.Client
	task := addHTTPTask(t, srv, `{"title":"linked task"}`)
	next := func(op *api.OpResponse, err error) api.Entry {
		t.Helper()
		if err != nil || op.Entry == nil {
			t.Fatalf("entry mutation = %+v, %v", op, err)
		}
		return *op.Entry
	}
	date, minute := civil.MustDate("2026-10-27"), civil.Minute(9*60)
	entry := next(c.AddEntry(ctx, client.Mut{}, api.AddEntryRequest{TaskID: api.Opt[int64]{Set: true, V: task.ID}, Title: "work", Notes: "notes", WorkDate: &date, Time: &minute}))
	if entry.TaskID == nil || *entry.TaskID != task.ID || entry.TaskTitle == nil || *entry.TaskTitle != task.Title || entry.Title != "work" || entry.Notes != "notes" || entry.WorkDate != date || entry.Time == nil || *entry.Time != minute {
		t.Fatalf("entry = %+v", entry)
	}
	mut := func() client.Mut { return client.Mut{IfMatch: entry.ETag} }
	entry = next(c.UpdateEntry(ctx, mut(), entry.ID, api.UpdateEntryRequest{Title: api.Opt[string]{Set: true, V: "edited"}, Notes: api.Opt[string]{Set: true, V: "changed"}, WorkDate: api.Opt[civil.Date]{Set: true, V: civil.MustDate("2026-10-28")}, Time: api.Opt[civil.Minute]{Set: true, V: 8 * 60}}))
	if entry.Title != "edited" || entry.Notes != "changed" || entry.WorkDate != civil.MustDate("2026-10-28") || entry.Time == nil || *entry.Time != 8*60 {
		t.Fatalf("updated = %+v", entry)
	}
	before := entry
	entry = next(c.UpdateEntry(ctx, mut(), entry.ID, api.UpdateEntryRequest{Time: api.Opt[civil.Minute]{Set: true, Null: true}}))
	if entry.Time != nil || entry.Title != before.Title || entry.Notes != before.Notes || entry.WorkDate != before.WorkDate {
		t.Fatalf("clear time = %+v", entry)
	}
	entry = next(c.RelinkEntry(ctx, mut(), entry.ID, api.RelinkRequest{TaskID: api.Opt[int64]{Set: true, Null: true}}))
	if entry.TaskID != nil || entry.TaskTitle != nil {
		t.Fatalf("unlinked = %+v", entry)
	}
	entry = next(c.RelinkEntry(ctx, mut(), entry.ID, api.RelinkRequest{TaskID: api.Opt[int64]{Set: true, V: task.ID}}))
	if entry.TaskID == nil || *entry.TaskID != task.ID || entry.TaskTitle == nil || *entry.TaskTitle != task.Title {
		t.Fatalf("relinked = %+v", entry)
	}
	entry = next(c.DeleteEntry(ctx, mut(), entry.ID))
	if entry.DeletedAt == nil {
		t.Fatalf("deleted = %+v", entry)
	}
	entry = next(c.UndeleteEntry(ctx, mut(), entry.ID))
	if entry.DeletedAt != nil {
		t.Fatalf("undeleted = %+v", entry)
	}
}

func TestEntryValidationAndReplay(t *testing.T) {
	srv := testutil.StartServer(t)
	task := addHTTPTask(t, srv, `{"title":"linked"}`)
	headers := http.Header{api.HeaderIdempotencyKey: {api.NewIdempotencyKey()}}
	status, _, body := mutateAPI(t, srv, http.MethodPost, "/entries", `{"task_id":1,"title":"work"}`, "", headers)
	first := taskOp(t, status, 201, body)
	if first.Entry == nil {
		t.Fatal("missing entry")
	}
	for _, field := range []string{"title", "notes", "work_date"} {
		status, _, body := mutateAPI(t, srv, http.MethodPatch, "/entries/1", `{"`+field+`":null}`, first.Entry.ETag, nil)
		taskProblem(t, status, 422, body, domain.CodeValidation, field)
	}
	status, _, body = mutateAPI(t, srv, http.MethodPost, "/entries/1/relink", `{}`, first.Entry.ETag, nil)
	taskProblem(t, status, 422, body, domain.CodeValidation, "task_id")
	status, _, body = mutateAPI(t, srv, http.MethodPatch, "/entries/1", `{"notes":"edited"}`, first.Entry.ETag, nil)
	edited := taskOp(t, status, 200, body)
	status, _, body = mutateAPI(t, srv, http.MethodPatch, "/entries/1", `{"title":"stale"}`, first.Entry.ETag, nil)
	p := taskProblem(t, status, 412, body, domain.CodePrecondition, "")
	var current api.Entry
	if err := json.Unmarshal(p.Current, &current); err != nil || !reflect.DeepEqual(&current, edited.Entry) || current.TaskTitle == nil || *current.TaskTitle != task.Title {
		t.Fatalf("current entry = %s, %v", p.Current, err)
	}
	status, location, body := mutateAPI(t, srv, http.MethodPost, "/entries", ` { "title":"work", "task_id":1 } `, "", headers)
	replay := taskOp(t, status, 201, body)
	if !replay.Replayed || replay.OpID == nil || *replay.OpID != *first.OpID || !reflect.DeepEqual(replay.Entry, edited.Entry) || location.Get("Location") != "/api/v1/entries/1" {
		t.Fatalf("entry replay = %+v, headers = %v", replay, location)
	}
	status, _, body = mutateAPI(t, srv, http.MethodPost, "/entries", `{"task_id":null,"title":"work"}`, "", headers)
	taskProblem(t, status, 422, body, domain.CodeIdemMismatch, "")
}

func TestBlockMutationClients(t *testing.T) {
	srv := testutil.StartServer(t)
	ctx, c := t.Context(), srv.Client
	task := addHTTPTask(t, srv, `{"title":"block"}`)
	next := func(op *api.OpResponse, err error) api.Block {
		t.Helper()
		if err != nil || op.Block == nil {
			t.Fatalf("block mutation = %+v, %v", op, err)
		}
		return *op.Block
	}
	span := api.BlockRequest{Date: civil.MustDate("2026-10-28"), Start: 11 * 60, End: 12 * 60}
	block := next(c.BlockTask(ctx, client.Mut{IfMatch: task.ETag}, task.ID, span))
	mut := func() client.Mut { return client.Mut{IfMatch: block.ETag} }
	span.Start, span.End = 13*60, 14*60
	block = next(c.Reblock(ctx, mut(), block.ID, api.ReblockRequest{BlockRequest: span, TaskETag: block.TaskETag}))
	if block.Start != span.Start || block.End != span.End {
		t.Fatalf("reblocked = %+v", block)
	}
	block = next(c.Unblock(ctx, mut(), block.ID))
	if block.Status != "cancelled" || block.CancelReason == nil || *block.CancelReason != "user" {
		t.Fatalf("unblocked = %+v", block)
	}
	span.Start, span.End = 9*60, 11*60
	block = next(c.Reblock(ctx, mut(), block.ID, api.ReblockRequest{BlockRequest: span, TaskETag: block.TaskETag}))
	for _, outcome := range []string{"worked", "missed", "auto"} {
		block = next(c.SetOutcome(ctx, mut(), block.ID, api.OutcomeRequest{Outcome: outcome}))
		want := outcome
		if outcome == "auto" {
			want = "planned"
		}
		if block.Status != want || (outcome != "auto" && (block.OutcomeSource == nil || *block.OutcomeSource != "user")) {
			t.Fatalf("outcome %s = %+v", outcome, block)
		}
	}
	for _, outcome := range []string{"", "done", "WORKED", "nope"} {
		_, err := c.SetOutcome(ctx, mut(), block.ID, api.OutcomeRequest{Outcome: outcome})
		requireReadProblem(t, err, 422, domain.CodeValidation, "")
	}
}

func TestFocusAndRevertClients(t *testing.T) {
	srv := testutil.StartServer(t)
	ctx, c := t.Context(), srv.Client
	first := addHTTPTask(t, srv, `{"title":"first"}`)
	second := addHTTPTask(t, srv, `{"title":"second"}`)
	status, _, body := mutateAPI(t, srv, http.MethodPost, "/focus", `{}`, "", nil)
	taskProblem(t, status, 422, body, domain.CodeValidation, "task_id")
	_, err := c.SetFocus(ctx, client.Mut{}, api.FocusRequest{TaskID: api.Opt[int64]{Set: true, V: first.ID}})
	if err != nil {
		t.Fatal(err)
	}
	op, err := c.SetFocus(ctx, client.Mut{}, api.FocusRequest{TaskID: api.Opt[int64]{Set: true, V: second.ID}, Interrupt: true})
	if err != nil || op.OpID == nil {
		t.Fatalf("focus switch = %+v, %v", op, err)
	}
	today, _, _, err := c.Today(ctx, "")
	if err != nil || today.Focus == nil || today.Focus.ID != second.ID || len(today.Interruptions) != 1 {
		t.Fatalf("focused today = %+v, %v", today, err)
	}
	result, err := c.Revert(ctx, client.Mut{}, *op.OpID)
	if err != nil || result.OpID == nil || len(result.Effects) == 0 {
		t.Fatalf("revert = %+v, %v", result, err)
	}
	today, _, _, err = c.Today(ctx, "")
	if err != nil || today.Focus == nil || today.Focus.ID != first.ID || len(today.Interruptions) != 0 {
		t.Fatalf("reverted focus = %+v, %v", today, err)
	}
	_, err = c.SetFocus(ctx, client.Mut{}, api.FocusRequest{TaskID: api.Opt[int64]{Set: true, Null: true}})
	if err != nil {
		t.Fatal(err)
	}
	today, _, _, err = c.Today(ctx, "")
	if err != nil || today.Focus != nil {
		t.Fatalf("cleared focus = %+v, %v", today, err)
	}
}

func TestEntryBlockPreconditions(t *testing.T) {
	srv := testutil.StartServer(t)
	task := addHTTPTask(t, srv, `{"title":"target"}`)
	status, _, body := mutateAPI(t, srv, http.MethodPost, "/entries", `{"task_id":null,"title":"work"}`, "", nil)
	entry := taskOp(t, status, 201, body).Entry
	op, err := srv.Client.BlockTask(t.Context(), client.Mut{IfMatch: task.ETag}, task.ID, api.BlockRequest{Date: civil.MustDate("2026-10-28"), Start: 11 * 60, End: 12 * 60})
	if err != nil || op.Block == nil || entry == nil {
		t.Fatalf("fixtures = %+v, %v; entry = %+v", op, err, entry)
	}
	reblock, err := json.Marshal(api.ReblockRequest{BlockRequest: api.BlockRequest{Date: civil.MustDate("2026-10-28"), Start: 13 * 60, End: 14 * 60}, TaskETag: op.Block.TaskETag})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPatch, "/entries/1", `{"notes":"edit"}`},
		{http.MethodPost, "/entries/1/relink", `{"task_id":1}`},
		{http.MethodPost, "/entries/1/delete", `{}`},
		{http.MethodPost, "/entries/1/undelete", `{}`},
		{http.MethodPost, "/blocks/1/reblock", string(reblock)},
		{http.MethodPost, "/blocks/1/unblock", `{}`},
		{http.MethodPost, "/blocks/1/outcome", `{"outcome":"auto"}`},
	} {
		for _, check := range []struct {
			etag   string
			status int
			code   domain.Code
		}{{"", 428, domain.CodePreconditionReq}, {"nope", 400, domain.CodeMalformed}, {api.FormatETag(srv.Svc.Generation(), domain.Version(999)), 412, domain.CodePrecondition}} {
			t.Run(tc.path+"/"+strconv.Itoa(check.status), func(t *testing.T) {
				status, _, body := mutateAPI(t, srv, tc.method, tc.path, tc.body, check.etag, nil)
				taskProblem(t, status, check.status, body, check.code, "")
			})
		}
	}
}
