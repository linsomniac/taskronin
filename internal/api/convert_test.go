package api_test

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/domain"
)

func converter(t *testing.T) api.Conv {
	t.Helper()
	denver, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Fatal(err)
	}
	return api.Conv{Gen: "0123456789abcdef", Loc: denver}
}

func instant(t *testing.T, value string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func requireJSON(t *testing.T, value any, want string) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var gotValue, wantValue any
	if err := json.Unmarshal(body, &gotValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON = %s; want %s", body, want)
	}
}

func TestConvTime(t *testing.T) {
	conv := converter(t)
	for _, tc := range []struct{ input, want string }{
		{"2026-10-29T06:00:00Z", `"2026-10-29T00:00:00.000-06:00"`},
		{"2026-11-02T07:00:00.123456789Z", `"2026-11-02T00:00:00.123-07:00"`},
	} {
		at := instant(t, tc.input)
		got := conv.Time(at)
		if !got.Equal(at) || got.Location() != conv.Loc {
			t.Fatalf("converted time = %v in %v", got, got.Location())
		}
		requireJSON(t, got, tc.want)
		requireJSON(t, conv.OptTime(sql.Null[time.Time]{V: at, Valid: true}), tc.want)
		if conv.OptTime(sql.Null[time.Time]{V: at}) != nil {
			t.Fatal("invalid optional time was retained")
		}
	}
	if got := conv.ETag(3); got != `"0123456789abcdef-3"` {
		t.Fatalf("ETag = %q", got)
	}
}

func taskView(t *testing.T) domain.TaskView {
	t.Helper()
	at := instant(t, "2026-10-29T06:00:00Z")
	return domain.TaskView{
		Task: domain.Task{
			ID: 42, Title: "Set up keycloak", Status: domain.StatusOpen, Version: 3,
			DueDate: sql.Null[civil.Date]{V: civil.MustDate("2026-10-30"), Valid: true},
			DueMin:  sql.Null[civil.Minute]{V: 540, Valid: true}, Origin: "owner",
			CreatedAt: at, UpdatedAt: at,
		},
		View: domain.ViewTriage, DuePhase: domain.DueNone, DueSoon: true,
		Reasons: []domain.TriageRow{
			{TaskID: 42, Reason: domain.ReasonSlipped, Since: at},
			{TaskID: 42, Reason: domain.ReasonNew, Since: at.Add(-time.Hour)},
		},
		Tags: []string{"db", "infra"}, SlipsLifetime: 2, SlipsSinceTriage: 1,
	}
}

func TestConvTask(t *testing.T) {
	conv := converter(t)
	v := taskView(t)
	got := conv.Task(v)
	requireJSON(t, got, `{
		"id":42,"title":"Set up keycloak","notes":"","status":"open","view":"triage",
		"reasons":[{"reason":"slipped","since":"2026-10-29T00:00:00.000-06:00"},
		           {"reason":"new","since":"2026-10-28T23:00:00.000-06:00"}],
		"defer":null,"deferred":false,"due":{"date":"2026-10-30","time":"09:00"},
		"due_phase":"none","due_soon":true,"project":null,"tags":["db","infra"],
		"priority":null,"origin":"owner","slips":{"lifetime":2,"since_triage":1},
		"last_placed_at":null,"completed_at":null,"completed_date":null,
		"created_at":"2026-10-29T00:00:00.000-06:00","updated_at":"2026-10-29T00:00:00.000-06:00",
		"deleted_at":null,"version":3,"etag":"\"0123456789abcdef-3\""}`)

	v.Tags, v.Reasons = nil, nil
	got = conv.Task(v)
	requireJSON(t, got.Tags, `[]`)
	requireJSON(t, got.Reasons, `[]`)
}

func TestConvTaskOptionalFields(t *testing.T) {
	conv := converter(t)
	v := taskView(t)
	v.Notes, v.Project, v.Priority = "notes", "Infra", domain.PriorityH
	v.DeferDate = sql.Null[civil.Date]{V: civil.MustDate("2026-11-03"), Valid: true}
	v.DeferMin = sql.Null[civil.Minute]{V: 0, Valid: true}
	v.Deferred = true
	v.DueMin.Valid = false
	at := instant(t, "2026-11-02T07:00:00Z")
	v.LastPlacedAt = sql.Null[time.Time]{V: at, Valid: true}
	v.CompletedAt, v.DeletedAt = v.LastPlacedAt, v.LastPlacedAt
	v.CompletedDate = sql.Null[civil.Date]{V: civil.MustDate("2026-11-02"), Valid: true}
	got := conv.Task(v)
	if got.Notes != "notes" || got.Project == nil || *got.Project != "Infra" || got.Priority == nil || *got.Priority != "H" || !got.Deferred {
		t.Fatalf("optional fields = %+v", got)
	}
	requireJSON(t, got.Defer, `{"date":"2026-11-03","time":"00:00"}`)
	requireJSON(t, got.Due, `{"date":"2026-10-30","time":null}`)
	requireJSON(t, got.CompletedDate, `"2026-11-02"`)
	for _, value := range []*api.Time{got.LastPlacedAt, got.CompletedAt, got.DeletedAt} {
		requireJSON(t, value, `"2026-11-02T00:00:00.000-07:00"`)
	}
	v.DeferMin.Valid, v.DueDate.Valid = false, false
	got = conv.Task(v)
	requireJSON(t, got.Defer, `{"date":"2026-11-03","time":null}`)
	if got.Due != nil {
		t.Fatal("missing due date produced a moment")
	}
}

func TestConvEntry(t *testing.T) {
	conv := converter(t)
	at := instant(t, "2026-10-29T06:00:00Z")
	v := domain.EntryView{LogEntry: domain.LogEntry{
		ID: 7, Kind: domain.EntryWork, Title: "Investigated", Notes: "notes",
		WorkDate: civil.MustDate("2026-10-28"), CreatedAt: at, UpdatedAt: at, Version: 2,
	}}
	requireJSON(t, conv.Entry(v), `{
		"id":7,"task_id":null,"task_title":null,"kind":"work","title":"Investigated","notes":"notes",
		"work_date":"2026-10-28","time":null,"reopened":false,
		"created_at":"2026-10-29T00:00:00.000-06:00","updated_at":"2026-10-29T00:00:00.000-06:00",
		"deleted_at":null,"version":2,"etag":"\"0123456789abcdef-2\""}`)
	v.TaskID, v.TaskTitle = sql.Null[domain.TaskID]{V: 42, Valid: true}, "Current task title"
	v.OccurredMin = sql.Null[civil.Minute]{V: 0, Valid: true}
	v.DeletedAt = sql.Null[time.Time]{V: at, Valid: true}
	v.Reopened = true
	got := conv.Entry(v)
	if got.TaskID == nil || *got.TaskID != 42 || got.TaskTitle == nil || *got.TaskTitle != v.TaskTitle || !got.Reopened {
		t.Fatalf("linked entry = %+v", got)
	}
	requireJSON(t, got.Time, `"00:00"`)
	requireJSON(t, got.DeletedAt, `"2026-10-29T00:00:00.000-06:00"`)
	if raw := conv.LogEntry(v.LogEntry, v.TaskTitle); !reflect.DeepEqual(raw, got) {
		t.Fatalf("LogEntry = %+v; want %+v", raw, got)
	}
}

func TestConvBlock(t *testing.T) {
	conv := converter(t)
	at := instant(t, "2026-10-29T06:00:00Z")
	v := domain.BlockView{
		TimeBlock: domain.TimeBlock{
			ID: 9, TaskID: 42, Span: domain.BlockSpan{Date: civil.MustDate("2026-10-28"), Start: 1380, End: 1440},
			Status: domain.BlockPlanned, CreatedAt: at, Version: 2,
		},
		TaskTitle: "Set up keycloak", TaskVersion: 5, Started: true, Ended: true, NeedsReblock: true,
	}
	requireJSON(t, conv.Block(v), `{
		"id":9,"task_id":42,"task_title":"Set up keycloak","task_etag":"\"0123456789abcdef-5\"",
		"date":"2026-10-28","start":"23:00","end":"24:00","status":"planned",
		"outcome_source":null,"cancel_reason":null,"slipped":false,"slipped_at":null,
		"current":false,"started":true,"ended":true,"needs_reblock":true,
		"created_at":"2026-10-29T00:00:00.000-06:00","resolved_at":null,
		"version":2,"etag":"\"0123456789abcdef-2\""}`)
	v.OutcomeSource, v.Slipped = domain.OutcomeAuto, true
	v.SlippedAt = sql.Null[time.Time]{V: at, Valid: true}
	v.ResolvedAt = v.SlippedAt
	got := conv.Block(v)
	requireJSON(t, got.OutcomeSource, `"auto"`)
	requireJSON(t, got.SlippedAt, `"2026-10-29T00:00:00.000-06:00"`)
	requireJSON(t, got.ResolvedAt, `"2026-10-29T00:00:00.000-06:00"`)
	if !got.Slipped {
		t.Fatal("slipped flag lost")
	}
	v.CancelReason, v.Current = domain.CancelRescheduled, true
	got = conv.Block(v)
	requireJSON(t, got.CancelReason, `"rescheduled"`)
	if !got.Current {
		t.Fatal("current flag lost")
	}
}

func TestConvInterruption(t *testing.T) {
	conv := converter(t)
	v := domain.Interruption{ID: 8, At: instant(t, "2026-10-29T06:00:00Z"), Date: civil.MustDate("2026-10-29"), ToTaskID: 42}
	requireJSON(t, conv.Interruption(v), `{"id":8,"at":"2026-10-29T00:00:00.000-06:00","date":"2026-10-29","from_task_id":null,"from_block_id":null,"to_task_id":42}`)
	v.FromTaskID = sql.Null[domain.TaskID]{V: 3, Valid: true}
	v.FromBlockID = sql.Null[domain.BlockID]{V: 4, Valid: true}
	requireJSON(t, conv.Interruption(v), `{"id":8,"at":"2026-10-29T00:00:00.000-06:00","date":"2026-10-29","from_task_id":3,"from_block_id":4,"to_task_id":42}`)
}

func TestConvTaskDetail(t *testing.T) {
	conv := converter(t)
	v := domain.TaskDetail{TaskView: taskView(t)}
	got := conv.TaskDetail(v)
	for _, value := range []any{got.Entries, got.Blocks, got.Interruptions} {
		requireJSON(t, value, `[]`)
	}
	v.Entries = []domain.EntryView{{LogEntry: domain.LogEntry{ID: 7}}, {LogEntry: domain.LogEntry{ID: 6}}}
	v.Blocks = []domain.BlockView{{TimeBlock: domain.TimeBlock{ID: 4}, TaskVersion: 5}}
	v.Interruptions = []domain.Interruption{{ID: 3}}
	got = conv.TaskDetail(v)
	if !reflect.DeepEqual(got.Task, conv.Task(v.TaskView)) ||
		!reflect.DeepEqual(got.Entries, []api.Entry{conv.Entry(v.Entries[0]), conv.Entry(v.Entries[1])}) ||
		!reflect.DeepEqual(got.Blocks, []api.Block{conv.Block(v.Blocks[0])}) ||
		!reflect.DeepEqual(got.Interruptions, []api.Interruption{conv.Interruption(v.Interruptions[0])}) {
		t.Fatalf("task detail = %+v", got)
	}
}

func TestConvTokenNoSecret(t *testing.T) {
	conv := converter(t)
	at := instant(t, "2026-10-29T06:00:00Z")
	token := domain.Token{ID: 11, Name: "assistant", Scopes: domain.MCPDefaultScopes, MutationsPerHour: 60, CreatedAt: at}
	if _, err := rand.Read(token.SecretHash[:]); err != nil {
		t.Fatal(err)
	}
	// The exact key allowlist also rejects any spelling of a hash or secret field.
	requireJSON(t, conv.Token(token), `{"id":11,"name":"assistant","scopes":"read,capture,write","owner":false,"mutations_per_hour":60,"created_at":"2026-10-29T00:00:00.000-06:00","expires_at":null,"last_used_at":null,"revoked_at":null}`)
	token.IsOwner, token.Scopes = true, domain.AllScopes
	token.ExpiresAt = sql.Null[time.Time]{V: at, Valid: true}
	token.LastUsedAt, token.RevokedAt = token.ExpiresAt, token.ExpiresAt
	got := conv.Token(token)
	if !got.Owner || got.Scopes != "read,capture,write,destroy,admin" {
		t.Fatalf("token metadata = %+v", got)
	}
	for _, value := range []*api.Time{got.ExpiresAt, got.LastUsedAt, got.RevokedAt} {
		requireJSON(t, value, `"2026-10-29T00:00:00.000-06:00"`)
	}
}

func TestRequestsStrict(t *testing.T) {
	for _, tc := range []struct {
		wire string
		want api.Opt[int64]
	}{
		{`{"title":"x"}`, api.Opt[int64]{}},
		{`{"task_id":null,"title":"x"}`, api.Opt[int64]{Set: true, Null: true}},
		{`{"task_id":42,"title":"x"}`, api.Opt[int64]{Set: true, V: 42}},
	} {
		var req api.AddEntryRequest
		dec := json.NewDecoder(strings.NewReader(tc.wire))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || req.TaskID != tc.want || req.Title != "x" {
			t.Fatalf("decode(%s) = %+v, %v", tc.wire, req, err)
		}
	}
	var due api.DueRequest
	dec := json.NewDecoder(strings.NewReader(`{}`))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&due); err != nil || due.Due.Set {
		t.Fatalf("absent due = %+v, %v", due, err)
	}
	for _, tc := range []struct {
		wire string
		req  any
	}{
		{`{"task_id":null,"title":"x","extra":1}`, &api.AddEntryRequest{}},
		{`{"due":{"date":"2026-10-30","extra":1}}`, &api.DueRequest{}},
		{`{"until":{"date":"2026-10-30","extra":1}}`, &api.DeferRequest{}},
		{`{"title":"x","place":{"how":"top","extra":1}}`, &api.AddTaskRequest{}},
		{`{"items":[{"id":42,"etag":"x","extra":1}]}`, &api.AcceptNewRequest{}},
	} {
		dec := json.NewDecoder(strings.NewReader(tc.wire))
		dec.DisallowUnknownFields()
		if err := dec.Decode(tc.req); err == nil || !strings.Contains(err.Error(), `unknown field "extra"`) {
			t.Fatalf("decode(%s) = %v; want unknown-field error", tc.wire, err)
		}
	}
}

func TestRequestPresenceJSON(t *testing.T) {
	for _, req := range []any{api.UpdateTaskRequest{}, api.UpdateEntryRequest{}} {
		requireJSON(t, req, `{}`)
	}
	requireJSON(t, api.UpdateTaskRequest{Project: api.Opt[string]{Set: true, Null: true}}, `{"project":null}`)
	requireJSON(t, api.UpdateTaskRequest{Priority: api.Opt[string]{Set: true, V: "H"}}, `{"priority":"H"}`)
	requireJSON(t, api.UpdateEntryRequest{Time: api.Opt[civil.Minute]{Set: true, Null: true}}, `{"time":null}`)
	for _, tc := range []struct {
		req  any
		wire string
	}{
		{api.DueRequest{}, `{"due":null}`},
		{api.DeferRequest{}, `{"until":null}`},
		{api.RelinkRequest{}, `{"task_id":null}`},
		{api.FocusRequest{}, `{"task_id":null}`},
		{api.CreateTokenRequest{}, `{"name":"","scopes":"","expires_at":null,"mutations_per_hour":0}`},
		{api.ReblockRequest{BlockRequest: api.BlockRequest{Date: civil.MustDate("2026-10-30"), Start: 1380, End: 1440}, TaskETag: `"0123456789abcdef-5"`},
			`{"date":"2026-10-30","start":"23:00","end":"24:00","task_etag":"\"0123456789abcdef-5\""}`},
	} {
		requireJSON(t, tc.req, tc.wire)
	}
}

func TestPasswordRequestRedacted(t *testing.T) {
	current, next, password := rand.Text(), rand.Text(), rand.Text()
	for name, value := range map[string]slog.LogValuer{
		"password value":   api.PasswordRequest{Current: current, New: next},
		"password pointer": &api.PasswordRequest{Current: current, New: next},
		"settings value":   api.SettingsRequest{Password: password},
		"settings pointer": &api.SettingsRequest{Password: password},
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, nil))
			logger.Info("request", "body", value)
			output := buf.String()
			if !strings.Contains(output, `"body":"[redacted]"`) || strings.Contains(output, current) || strings.Contains(output, next) || strings.Contains(output, password) {
				t.Fatal("password-bearing request was not fully redacted")
			}
		})
	}
}
