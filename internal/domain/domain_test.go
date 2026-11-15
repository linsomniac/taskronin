package domain

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"taskronin/internal/civil"
)

func TestParseScopes(t *testing.T) {
	for _, tc := range []struct {
		input     string
		want      Scopes
		canonical string
	}{
		{"write,read,capture", ScopeRead | ScopeCapture | ScopeWrite, "read,capture,write"},
		{"", 0, ""},
		{"admin,destroy,write,capture,read", AllScopes, "read,capture,write,destroy,admin"},
		{"read,read", ScopeRead, "read"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			s, err := ParseScopes(tc.input)
			if err != nil || s != tc.want || s.String() != tc.canonical {
				t.Fatalf("ParseScopes(%q) = %v, %q, %v", tc.input, s, s.String(), err)
			}
		})
	}
	for _, input := range []string{"read,bogus", "Read", "read,", ",read", "read, write"} {
		if _, err := ParseScopes(input); err == nil {
			t.Errorf("ParseScopes(%q) succeeded", input)
		}
	}
	if !AllScopes.Has(ScopeDestroy|ScopeAdmin) || MCPDefaultScopes.Has(ScopeDestroy) || !ScopeRead.Has(0) || ScopeRead.Has(ScopeRead|ScopeWrite) {
		t.Fatal("Has does not require all requested bits")
	}
	if ScopeRead != 1 || ScopeCapture != 2 || ScopeWrite != 4 || ScopeDestroy != 8 || ScopeAdmin != 16 || AllScopes != 31 || MCPDefaultScopes != 7 {
		t.Fatal("scope bit values differ from A§2.5")
	}
}

func TestActorKey(t *testing.T) {
	for _, tc := range []struct {
		actor Actor
		key   string
		ai    bool
	}{
		{Actor{Kind: ActorOwner}, "owner", false},
		{Actor{Kind: ActorToken, TokenName: "ai"}, "token:ai", true},
		{Actor{Kind: ActorToken, TokenName: "automation"}, "token:automation", true},
		{SystemActor(), "system", false},
	} {
		if tc.actor.Key() != tc.key || tc.actor.IsAI() != tc.ai {
			t.Errorf("actor %+v: Key=%q IsAI=%v", tc.actor, tc.actor.Key(), tc.actor.IsAI())
		}
	}
	if got := SystemActor(); got != (Actor{Kind: ActorSystem, Channel: ChanSystem}) {
		t.Fatalf("SystemActor = %+v", got)
	}
	if ActorOwner != 1 || ActorToken != 2 || ActorSystem != 3 {
		t.Fatal("actor kind values differ from A§2.5")
	}
}

func TestOpID(t *testing.T) {
	id := NewOpID()
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id.String()) || id.IsZero() || id == NewOpID() || !(OpID{}).IsZero() {
		t.Fatalf("invalid generated OpID: %q", id)
	}
	const text = "0123456789abcdef0123456789abcdef"
	want, err := ParseOpID(text)
	if err != nil || want.String() != text {
		t.Fatalf("ParseOpID = %v, %v", want, err)
	}
	for _, input := range []string{"", text[:31], text + "0", strings.ToUpper(text), strings.Repeat("g", 32), " " + text} {
		if _, err := ParseOpID(input); err == nil {
			t.Errorf("ParseOpID(%q) succeeded", input)
		}
	}
	value, err := want.Value()
	if err != nil || value != text {
		t.Fatalf("Value = %v, %v", value, err)
	}
	for _, src := range []any{value, []byte(text)} {
		var got OpID
		if err := got.Scan(src); err != nil || got != want {
			t.Fatalf("Scan(%T) = %v, %v", src, got, err)
		}
	}
	for _, src := range []any{nil, int64(1), []byte("bad"), strings.ToUpper(text)} {
		got := want
		if err := got.Scan(src); err == nil || got != want {
			t.Fatalf("invalid Scan(%T) = %v, %v; want error and unchanged receiver", src, got, err)
		}
	}
	data, err := id.MarshalText()
	if err != nil || string(data) != id.String() {
		t.Fatalf("MarshalText = %q, %v", data, err)
	}
	var got OpID
	if err := got.UnmarshalText(data); err != nil || got != id {
		t.Fatalf("UnmarshalText = %v, %v", got, err)
	}
	if err := got.UnmarshalText([]byte("bad")); err == nil || got != id {
		t.Fatal("invalid UnmarshalText must fail without modifying receiver")
	}
	data, err = json.Marshal(id)
	if err != nil || string(data) != `"`+id.String()+`"` {
		t.Fatalf("JSON OpID = %s, %v", data, err)
	}
	if err := json.Unmarshal(data, &got); err != nil || got != id {
		t.Fatalf("JSON round trip = %v, %v", got, err)
	}
}

func TestNewGeneration(t *testing.T) {
	a, b := NewGeneration(), NewGeneration()
	pattern := regexp.MustCompile(`^[0-9a-f]{16}$`)
	if !pattern.MatchString(string(a)) || !pattern.MatchString(string(b)) || a == b {
		t.Fatalf("invalid generations %q, %q", a, b)
	}
}

func TestTriageReasonRank(t *testing.T) {
	want := [...]TriageReason{ReasonSlipped, ReasonOverdue, ReasonDue, ReasonContinue, ReasonSurfaced, ReasonReopened, ReasonManual, ReasonNew}
	if TriageOrder != want {
		t.Fatalf("TriageOrder = %v", TriageOrder)
	}
	for i, reason := range want {
		if got := reason.Rank(); got != 8-i {
			t.Errorf("%s.Rank() = %d, want %d", reason, got, 8-i)
		}
	}
	if TriageReason("bogus").Rank() != 0 || TriageReason("").Rank() != 0 {
		t.Fatal("unknown reason has nonzero rank")
	}
}

func TestEnumsValid(t *testing.T) {
	checkEnum(t, []TaskStatus{StatusOpen, StatusDone, StatusDropped}, "open,done,dropped", TaskStatus("bogus"))
	checkEnum(t, TriageOrder[:], "slipped,overdue,due,continue,surfaced,reopened,manual,new", TriageReason("bogus"))
	checkEnum(t, []EntryKind{EntryWork, EntryComplete}, "work,complete", EntryKind("bogus"))
	checkEnum(t, []BlockStatus{BlockPlanned, BlockDone, BlockWorked, BlockMissed, BlockCancelled}, "planned,done,worked,missed,cancelled", BlockStatus("bogus"))
	checkEnum(t, []OutcomeSource{OutcomeAuto, OutcomeUser}, "auto,user", OutcomeSource("bogus"))
	checkEnum(t, []CancelReason{CancelUser, CancelTaskClosed, CancelDeferred, CancelRescheduled}, "user,task_closed,deferred,rescheduled", CancelReason("bogus"))
	checkEnum(t, []DuePhase{DueNone, DueDue, DueOverdue}, "none,due,overdue", DuePhase("bogus"))
	checkEnum(t, []Priority{PriorityNone, PriorityH, PriorityM, PriorityL}, ",H,M,L", Priority("bogus"))
	checkEnum(t, []View{ViewTriage, ViewBacklog, ViewUpcoming, ViewDone, ViewDropped, ViewDeleted, ViewAll}, "triage,backlog,upcoming,done,dropped,deleted,all", View("bogus"))
	checkEnum(t, []UserOutcome{SetWorked, SetMissed, SetAuto}, "worked,missed,auto", UserOutcome("bogus"))
	checkEnum(t, []PlaceHow{PlaceKeep, PlaceTop, PlaceBottom, PlaceBefore, PlaceAfter}, "keep,top,bottom,before,after", PlaceHow("bogus"))
	checkEnum(t, []Channel{ChanCLI, ChanTUI, ChanWeb, ChanMCP, ChanAPI, ChanSystem}, "cli,tui,web,mcp,api,system", Channel("bogus"))
	if TaskStatus("").Valid() || OutcomeSource("").Valid() || CancelReason("").Valid() || Priority("h").Valid() {
		t.Fatal("accepted a value outside the declared enum")
	}
}

func checkEnum[T interface {
	~string
	Valid() bool
}](t *testing.T, values []T, spellings string, invalid T) {
	t.Helper()
	want := strings.Split(spellings, ",")
	for i, value := range values {
		if !value.Valid() || string(value) != want[i] {
			t.Errorf("%T(%q): valid=%v, want %q", value, value, value.Valid(), want[i])
		}
	}
	if invalid.Valid() {
		t.Errorf("%T(%q) accepted", invalid, invalid)
	}
}

func TestVocabulary(t *testing.T) {
	checkSpellings(t, []OpKind{
		OpTaskAdd, OpTaskUpdate, OpTaskDue, OpTaskPlace, OpTaskAcceptNew, OpTaskDefer, OpTaskManual,
		OpTaskComplete, OpTaskReopen, OpTaskDrop, OpTaskDelete, OpTaskUndelete,
		OpBlockCreate, OpBlockReblock, OpBlockUnblock, OpBlockOutcome,
		OpEntryAdd, OpEntryUpdate, OpEntryRelink, OpEntryDelete, OpEntryUndelete,
		OpFocusSet, OpRevert, OpUndo, OpRedo, OpAIReview, OpTokenCreate, OpTokenRevoke,
		OpSettingsUpdate, OpSettingsRetentionLowered, OpAuthPassword, OpPurgeTask, OpPurgeEntry,
		OpSystemReconcile, OpSystemLedgerReplay, OpSystemImport, OpSystemRestore,
	}, "task.add task.update task.due task.place task.accept_new task.defer task.manual task.complete task.reopen task.drop task.delete task.undelete block.create block.reblock block.unblock block.outcome entry.add entry.update entry.relink entry.delete entry.undelete focus.set op.revert op.undo op.redo ai.review token.create token.revoke settings.update settings.retention_lowered auth.password purge.task purge.entry system.reconcile system.ledger_replay system.import system.restore")
	checkSpellings(t, []OpState{OpActive, OpReverted}, "active reverted")
	checkSpellings(t, []EntityKind{EntTask, EntTriage, EntTaskTags, EntEntry, EntBlock, EntFocus, EntInterruption, EntToken, EntSetting, EntAuth}, "task triage task_tags entry block focus interruption token setting auth")
	checkSpellings(t, []Action{ActInsert, ActUpdate, ActMove, ActTombstone, ActUntombstone, ActAdd, ActRemove, ActSet, ActRevoke, ActPurge}, "insert update move tombstone untombstone add remove set revoke purge")
	checkSpellings(t, []Code{
		CodeMalformed, CodeUnauthenticated, CodeForbidden, CodePeerRejected, CodePasswordRequired, CodePasswordInvalid,
		CodeNotFound, CodeDeleted, CodeConflict, CodeRevertConflict, CodeNotRevertible, CodePrecondition, CodeTooLarge,
		CodeUnsupportedMedia, CodeValidation, CodeIdemMismatch, CodeNothingToUndo, CodeNothingToRedo,
		CodePreconditionReq, CodeIdemRequired, CodeGenerationMismatch, CodeRateLimited, CodeInternal, CodeBusy,
	}, "malformed unauthenticated forbidden peer_rejected password_required password_invalid not_found deleted conflict revert_conflict not_revertible precondition_failed body_too_large unsupported_media_type validation idempotency_mismatch nothing_to_undo nothing_to_redo precondition_required idempotency_key_required generation_mismatch rate_limited internal busy")
}

func checkSpellings[T ~string](t *testing.T, values []T, spellings string) {
	t.Helper()
	want := strings.Fields(spellings)
	if len(values) != len(want) {
		t.Fatalf("vocabulary length %d, want %d", len(values), len(want))
	}
	for i, value := range values {
		if string(value) != want[i] {
			t.Errorf("%T value %q, want %q", value, value, want[i])
		}
	}
}

func TestErrors(t *testing.T) {
	e := Errorf(CodeConflict, "x %d", 1)
	if e.Code != CodeConflict || e.Detail != "x 1" {
		t.Fatalf("Errorf = %+v", e)
	}
	cause := errors.New("private cause")
	e.Err = cause
	wrapped := fmt.Errorf("outer: %w", e)
	var found *Error
	if !errors.As(wrapped, &found) || found != e || !errors.Is(wrapped, cause) || !errors.Is(e.Unwrap(), cause) {
		t.Fatal("error chain not preserved")
	}
	if got, ok := AsError(wrapped); !ok || got != e {
		t.Fatalf("AsError(wrapped) = %+v, %v", got, ok)
	}
	got, ok := AsError(io.EOF)
	if ok || got.Code != CodeInternal || !errors.Is(got.Err, io.EOF) || got.Detail != "" || !errors.Is(got, io.EOF) {
		t.Fatalf("AsError(EOF) = %+v, %v", got, ok)
	}
	if strings.Contains(e.Error(), cause.Error()) || strings.Contains(got.Error(), io.EOF.Error()) || !strings.Contains(e.Error(), e.Detail) {
		t.Fatal("Error text must contain the safe detail without the cause")
	}
	fields := []FieldError{{Field: "title", Message: "required"}, {Field: "priority", Message: "invalid"}}
	validation := ValidationError(fields...)
	if validation.Code != CodeValidation || validation.Detail != "invalid input" || !reflect.DeepEqual(validation.Fields, fields) {
		t.Fatalf("ValidationError = %+v", validation)
	}
}

func TestMomentAndSpanString(t *testing.T) {
	date := civil.MustDate("2026-10-30")
	for _, tc := range []struct {
		moment Moment
		want   string
	}{
		{Moment{Date: date}, "2026-10-30"},
		{Moment{Date: date, Min: sql.Null[civil.Minute]{V: 540, Valid: true}}, "2026-10-30 09:00"},
		{Moment{Date: date, Min: sql.Null[civil.Minute]{V: 0, Valid: true}}, "2026-10-30 00:00"},
	} {
		if got := tc.moment.String(); got != tc.want {
			t.Errorf("Moment.String = %q, want %q", got, tc.want)
		}
	}
	span := BlockSpan{Date: civil.MustDate("2026-10-28"), Start: 780, End: 900}
	if got := span.String(); got != "2026-10-28 13:00-15:00" {
		t.Fatalf("BlockSpan.String = %q", got)
	}
	span.End = civil.EndMinute(civil.EndOfDay)
	if got := span.String(); got != "2026-10-28 13:00-24:00" {
		t.Fatalf("BlockSpan.String = %q", got)
	}
}

func TestETag(t *testing.T) {
	const gen Generation = "0123456789abcdef"
	for _, version := range []Version{1, 42, 9223372036854775807} {
		encoded := FormatETag(gen, version)
		if encoded != fmt.Sprintf(`"0123456789abcdef-%d"`, version) {
			t.Fatalf("FormatETag = %q", encoded)
		}
		got, err := ParseETag(encoded)
		if err != nil || got != (Expect{Gen: gen, Version: version}) {
			t.Fatalf("ParseETag(%q) = %+v, %v", encoded, got, err)
		}
	}
	for _, input := range []string{
		"", "*", `W/"0123456789abcdef-1"`, "0123456789abcdef-1", `"0123456789abcdef-1", "0123456789abcdef-2"`,
		`"zzzzzzzzzzzzzzzz-1"`, `"0123456789ABCDEF-1"`, `"0123456789abcde-1"`, `"0123456789abcdef0-1"`,
		`"0123456789abcdef-"`, `"0123456789abcdef-0"`, `"0123456789abcdef--1"`, `"0123456789abcdef-+1"`,
		`"0123456789abcdef-01"`, `"0123456789abcdef-1.0"`, `"0123456789abcdef-9223372036854775808"`,
	} {
		_, err := ParseETag(input)
		requireMalformed(t, input, err)
	}
}

func TestCursor(t *testing.T) {
	for _, tc := range []struct {
		cursor  Cursor
		json    string
		decoded Cursor
	}{
		{Cursor{Keys: []any{3, "2026-10-28", 540}, ID: 7}, `{"v":1,"k":[3,"2026-10-28",540],"id":7}`, Cursor{Keys: []any{json.Number("3"), "2026-10-28", json.Number("540")}, ID: 7}},
		{Cursor{Offset: 40}, `{"v":1,"o":40}`, Cursor{Offset: 40}},
		{Cursor{}, `{"v":1,"o":0}`, Cursor{}},
		{Cursor{Keys: []any{}, ID: 7}, `{"v":1,"k":[],"id":7}`, Cursor{Keys: []any{}, ID: 7}},
		{Cursor{Keys: []any{json.Number("9007199254740993")}, ID: 9223372036854775807}, `{"v":1,"k":[9007199254740993],"id":9223372036854775807}`, Cursor{Keys: []any{json.Number("9007199254740993")}, ID: 9223372036854775807}},
	} {
		encoded := EncodeCursor(tc.cursor)
		if encoded != cursorBase64(tc.json) {
			t.Errorf("EncodeCursor = %q, want encoding of %s", encoded, tc.json)
		}
		got, err := DecodeCursor(encoded)
		if err != nil || !reflect.DeepEqual(got, tc.decoded) {
			t.Fatalf("DecodeCursor = %#v, %v; want %#v", got, err, tc.decoded)
		}
	}
	for _, input := range []string{"", "garbage", "e30", cursorBase64(`{"v":1,"o":0}`) + "=", cursorBase64(`{"v":1,"o":0}`) + "\n"} {
		_, err := DecodeCursor(input)
		requireMalformed(t, input, err)
	}
	for _, input := range []string{
		`{"v":2,"o":0}`, `{"v":1,"o":0,"unknown":1}`, `{"v":1,"o":0} {}`, `{"v":1,"o":0} trailing`,
		`null`, `[]`, `{"o":0}`, `{"v":1}`, `{"v":null,"o":0}`, `{"v":"1","o":0}`,
		`{"v":1,"k":[]}`, `{"v":1,"id":1}`, `{"v":1,"o":null}`, `{"v":1,"k":[],"id":null}`,
		`{"v":1,"k":[],"id":1,"o":0}`, `{"v":1,"o":0,"v":1}`, `{"V":1,"o":0}`,
		`{"v":1,"k":{},"id":1}`, `{"v":1,"k":[],"id":1.5}`, `{"v":1,"o":1.5}`,
		`{"v":1,"k":[],"id":9223372036854775808}`,
		`{"v":1,"k":null,"id":1}`,
	} {
		_, err := DecodeCursor(cursorBase64(input))
		requireMalformed(t, input, err)
	}
}

func cursorBase64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func requireMalformed(t *testing.T, input string, err error) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeMalformed {
		t.Fatalf("input %q: got %v, want *Error CodeMalformed", input, err)
	}
}

func FuzzCursor(f *testing.F) {
	for _, input := range []string{
		"", "garbage", "e30", cursorBase64(`{"v":2,"o":0}`),
		cursorBase64(`{"v":1,"k":[3,"2026-10-28",540],"id":7}`),
		cursorBase64(`{"v":1,"o":40}`), cursorBase64(`{"v":1,"k":[],"id":1}`),
		cursorBase64(`{"v":1,"k":[null,true,1.5,{"a":9007199254740993},["x"]],"id":1}`),
	} {
		f.Add(input)
	}
	f.Fuzz(func(t *testing.T, input string) {
		got, err := DecodeCursor(input)
		if err != nil {
			requireMalformed(t, input, err)
			return
		}
		again, err := DecodeCursor(EncodeCursor(got))
		if err != nil || !reflect.DeepEqual(got, again) {
			t.Fatalf("cursor round trip: %#v -> %#v, %v", got, again, err)
		}
	})
}

func TestParseTokenExpiry(t *testing.T) {
	zone, err := civil.LoadZone("America/Denver")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 28, 16, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		in   string
		want sql.Null[time.Time]
	}{
		{"never", sql.Null[time.Time]{}},
		{"12h", sql.Null[time.Time]{V: now.Add(12 * time.Hour), Valid: true}},
		{"30d", sql.Null[time.Time]{V: now.Add(30 * 24 * time.Hour), Valid: true}},
		{"36500d", sql.Null[time.Time]{V: now.Add(36500 * 24 * time.Hour), Valid: true}},
		{"2026-12-31", sql.Null[time.Time]{V: zone.StartOfDay(civil.MustDate("2026-12-31")), Valid: true}},
	} {
		got, err := ParseTokenExpiry(tc.in, now, zone)
		if err != nil || got.Valid != tc.want.Valid || !got.V.Equal(tc.want.V) {
			t.Errorf("%q = %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}
	for _, in := range []string{"", "soon", "720", "1h30m", "90m", "0h", "0d", "36501d", "999999d", "1234567h", "-1d", " 30d", "2026-10-28", "2020-01-01", "Never"} {
		if got, err := ParseTokenExpiry(in, now, zone); err == nil || got.Valid {
			t.Errorf("%q accepted: %v", in, got)
		}
	}
}

func TestValidOpIDPrefix(t *testing.T) {
	full := NewOpID().String()
	for _, tc := range []struct {
		s    string
		want bool
	}{
		{"a", true}, {"0123456789abcdef", true}, {full, true},
		{"", false}, {full + "0", false}, {"A", false}, {"abcg", false}, {"ab-c", false}, {" ab", false},
	} {
		if got := ValidOpIDPrefix(tc.s); got != tc.want {
			t.Errorf("ValidOpIDPrefix(%q) = %v, want %v", tc.s, got, tc.want)
		}
	}
}
