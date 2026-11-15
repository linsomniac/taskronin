package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/service"
)

func (s *Server) clockView(w http.ResponseWriter, r *http.Request) {
	v, err := s.svc.Clock(r.Context(), state(r).actor)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, s.clockResponse(v))
}

func (s *Server) whoAmI(w http.ResponseWriter, r *http.Request) {
	v, err := s.svc.WhoAmI(r.Context(), state(r).actor)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, api.WhoAmI{Actor: v.Actor.Key(), Scopes: v.Actor.Scopes.String(), Owner: v.Actor.Kind == domain.ActorOwner, Generation: string(v.Generation)})
}

func (s *Server) today(w http.ResponseWriter, r *http.Request) {
	v, err := s.svc.Today(r.Context(), state(r).actor)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeAggregate(w, r, s.todayResponse(v))
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	v, err := s.svc.Status(r.Context(), state(r).actor)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeAggregate(w, r, statusResponse(v))
}

func (s *Server) writeAggregate(w http.ResponseWriter, r *http.Request, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	// AIDEV-NOTE: Hash and send the same bytes from one snapshot/marshal.
	// Even adding writeJSON's trailing newline would change this strong ETag.
	etag := api.AggregateETag(body)
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", api.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (s *Server) day(w http.ResponseWriter, r *http.Request) {
	d, err := civil.ParseDate(r.PathValue("date"))
	if err != nil {
		s.writeProblem(w, r, domain.Errorf(domain.CodeMalformed, "invalid date"))
		return
	}
	v, err := s.svc.Day(r.Context(), state(r).actor, d)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, s.dayResponse(v))
}

func (s *Server) upcoming(w http.ResponseWriter, r *http.Request) {
	v, err := s.svc.Upcoming(r.Context(), state(r).actor)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, api.Upcoming{Deferred: convertSlice(v.Deferred, s.conv.Task), DueSoon: convertSlice(v.DueSoon, s.conv.Task)})
}

func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	q := newReadQuery(r)
	in := service.ReportIn{From: q.date("from"), To: q.date("to"), Tag: q.values.Get("tag"), Project: q.values.Get("project"), Todo: q.boolean("todo"), Upcoming: q.boolean("upcoming")}
	if q.err != nil {
		s.writeProblem(w, r, q.err)
		return
	}
	v, err := s.svc.Report(r.Context(), state(r).actor, in)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, s.reportResponse(v))
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := newReadQuery(r)
	in := service.SearchIn{Query: q.values.Get("q"), Kind: q.values.Get("kind"), Limit: q.limit(), Cursor: q.values.Get("cursor")}
	if in.Kind != "task" && in.Kind != "entry" && in.Kind != "all" {
		q.fail(domain.ValidationError(domain.FieldError{Field: "kind", Message: "must be task, entry or all"}))
	}
	if q.err != nil {
		s.writeProblem(w, r, q.err)
		return
	}
	v, err := s.svc.Search(r.Context(), state(r).actor, in)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, pageResponse(v, searchHitResponse))
}

func (s *Server) tags(w http.ResponseWriter, r *http.Request) {
	v, err := s.svc.ListTags(r.Context(), state(r).actor)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, convertSlice(v, func(v service.TagCount) api.TagCount { return api.TagCount{Name: v.Name, Count: v.Count} }))
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	q := newReadQuery(r)
	in := service.ListTasksIn{View: domain.View(q.values.Get("view")), Tag: q.values.Get("tag"), Project: q.values.Get("project"), Limit: q.limit(), Cursor: q.values.Get("cursor")}
	if q.err != nil {
		s.writeProblem(w, r, q.err)
		return
	}
	v, err := s.svc.ListTasks(r.Context(), state(r).actor, in)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, pageResponse(v, s.conv.Task))
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	v, err := s.svc.GetTask(r.Context(), state(r).actor, domain.TaskID(id))
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	out := s.conv.TaskDetail(v)
	w.Header().Set("ETag", out.ETag)
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) taskHistory(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	q := newReadQuery(r)
	in := service.HistoryIn{TaskID: domain.TaskID(id), Limit: q.limit(), Cursor: q.values.Get("cursor")}
	if q.err != nil {
		s.writeProblem(w, r, q.err)
		return
	}
	v, err := s.svc.History(r.Context(), state(r).actor, in)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, pageResponse(v, s.eventResponse))
}

func (s *Server) listEntries(w http.ResponseWriter, r *http.Request) {
	q := newReadQuery(r)
	in := service.ListEntriesIn{From: q.date("from"), To: q.date("to"), TaskID: q.task(true), IncludeDeleted: q.boolean("include_deleted"), Limit: q.limit(), Cursor: q.values.Get("cursor")}
	if q.err != nil {
		s.writeProblem(w, r, q.err)
		return
	}
	v, err := s.svc.ListEntries(r.Context(), state(r).actor, in)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, pageResponse(v, s.conv.Entry))
}

func (s *Server) getEntry(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	v, err := s.svc.GetEntry(r.Context(), state(r).actor, domain.EntryID(id))
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	out := s.conv.Entry(v)
	w.Header().Set("ETag", out.ETag)
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) listBlocks(w http.ResponseWriter, r *http.Request) {
	q := newReadQuery(r)
	in := service.ListBlocksIn{From: q.date("from"), To: q.date("to")}
	if task := q.task(false); task != nil {
		in.TaskID = *task
	}
	if q.err != nil {
		s.writeProblem(w, r, q.err)
		return
	}
	v, err := s.svc.ListBlocks(r.Context(), state(r).actor, in)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, convertSlice(v, s.conv.Block))
}

func (s *Server) getBlock(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	v, err := s.svc.GetBlock(r.Context(), state(r).actor, domain.BlockID(id))
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	out := s.conv.Block(v)
	w.Header().Set("ETag", out.ETag)
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) listOps(w http.ResponseWriter, r *http.Request) {
	q := newReadQuery(r)
	in := service.ListOpsIn{Actor: q.values.Get("actor"), OpPrefix: q.values.Get("op_prefix"), SinceSeq: domain.OpSeq(q.integer("since_seq")), Limit: q.limit(), Cursor: q.values.Get("cursor")}
	if q.err != nil {
		s.writeProblem(w, r, q.err)
		return
	}
	v, err := s.svc.ListOps(r.Context(), state(r).actor, in)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, pageResponse(v, s.opSummaryResponse))
}

func (s *Server) getOp(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseOpID(r.PathValue("op_id"))
	if err != nil {
		s.writeProblem(w, r, domain.Errorf(domain.CodeMalformed, "invalid op_id"))
		return
	}
	v, err := s.svc.GetOp(r.Context(), state(r).actor, id)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, api.OpDetail{Op: s.opSummaryResponse(v.Op), Events: convertSlice(v.Events, s.eventResponse)})
}

func (s *Server) aiChanges(w http.ResponseWriter, r *http.Request) {
	q := newReadQuery(r)
	in := service.ListAIChangesIn{IncludeReviewed: q.boolean("all"), Limit: q.limit(), Cursor: q.values.Get("cursor")}
	if q.err != nil {
		s.writeProblem(w, r, q.err)
		return
	}
	v, err := s.svc.ListAIChanges(r.Context(), state(r).actor, in)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, api.AIChanges{ReviewedThrough: int64(v.ReviewedThrough), Unreviewed: v.Unreviewed, Ops: convertSlice(v.Ops, s.opSummaryResponse), NextCursor: optionalString(v.NextCursor)})
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	v, err := s.svc.Settings(r.Context(), state(r).actor)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, api.Settings{BackupKeepDaily: v.KeepDaily, BackupKeepWeekly: v.KeepWeekly})
}

// readQuery retains the first transport decoding error; handlers check it before
// their single service call. Semantic range and view validation stays in service.
type readQuery struct {
	values url.Values
	err    error
}

func newReadQuery(r *http.Request) *readQuery {
	values, err := url.ParseQuery(r.URL.RawQuery)
	q := &readQuery{values: values}
	if err != nil {
		q.fail(domain.Errorf(domain.CodeMalformed, "invalid query string"))
	}
	return q
}

func (q *readQuery) fail(err error) {
	if q.err == nil {
		q.err = err
	}
}

func (q *readQuery) integer(field string) int64 {
	if !q.values.Has(field) {
		return 0
	}
	v, err := strconv.ParseInt(q.values.Get(field), 10, 64)
	if err != nil {
		q.fail(domain.Errorf(domain.CodeMalformed, "invalid %s", field))
	}
	return v
}

func (q *readQuery) limit() int {
	if !q.values.Has("limit") {
		return 100
	}
	v := q.integer("limit")
	if v < 1 || v > 500 {
		q.fail(domain.Errorf(domain.CodeMalformed, "limit must be an integer from 1 to 500"))
		return 0
	}
	return int(v)
}

func (q *readQuery) date(field string) civil.Date {
	if !q.values.Has(field) {
		return civil.Date{}
	}
	v, err := civil.ParseDate(q.values.Get(field))
	if err != nil {
		q.fail(domain.Errorf(domain.CodeMalformed, "invalid %s date", field))
	}
	return v
}

func (q *readQuery) boolean(field string) bool {
	if !q.values.Has(field) {
		return false
	}
	switch q.values.Get(field) {
	case "true", "1":
		return true
	case "false", "0":
		return false
	default:
		q.fail(domain.Errorf(domain.CodeMalformed, "%s must be true, false, 1 or 0", field))
		return false
	}
}

func (q *readQuery) task(allowNone bool) *sql.Null[domain.TaskID] {
	if !q.values.Has("task") {
		return nil
	}
	v := q.values.Get("task")
	if allowNone && v == "none" {
		return new(sql.Null[domain.TaskID])
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id < 1 {
		message := "must be a positive task id"
		if allowNone {
			message += " or none"
		}
		q.fail(domain.ValidationError(domain.FieldError{Field: "task", Message: message}))
		return nil
	}
	return &sql.Null[domain.TaskID]{V: domain.TaskID(id), Valid: true}
}
