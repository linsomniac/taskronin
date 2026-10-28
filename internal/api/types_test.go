package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"taskronin/internal/civil"
)

func TestOptJSON(t *testing.T) {
	type req struct {
		P Opt[string] `json:"p,omitzero"`
	}
	var canonical [][]byte
	for _, tc := range []struct {
		wire string
		want Opt[string]
	}{
		{`{}`, Opt[string]{}},
		{`{"p":null}`, Opt[string]{Set: true, Null: true}},
		{`{"p":"x"}`, Opt[string]{Set: true, V: "x"}},
		{`{"p":""}`, Opt[string]{Set: true}},
	} {
		var got req
		if err := json.Unmarshal([]byte(tc.wire), &got); err != nil || got.P != tc.want {
			t.Fatalf("Unmarshal(%s) = %+v, %v; want %+v", tc.wire, got.P, err, tc.want)
		}
		if got.P.IsZero() != !tc.want.Set {
			t.Errorf("IsZero(%+v) = %v", got.P, got.P.IsZero())
		}
		body, err := json.Marshal(got)
		if err != nil || string(body) != tc.wire {
			t.Fatalf("Marshal(%+v) = %s, %v; want %s", got, body, err, tc.wire)
		}
		canonical = append(canonical, body)
	}
	// AIDEV-NOTE: R2-3 requires distinct canonical idempotency bodies for an
	// absent field and an explicit null that clears it.
	if bytes.Equal(canonical[0], canonical[1]) || sha256.Sum256(canonical[0]) == sha256.Sum256(canonical[1]) {
		t.Fatal("absent and null produced the same canonical body or hash")
	}
	for _, value := range []Opt[string]{{}, {V: "ignored"}, {Set: true, Null: true, V: "ignored"}} {
		body, err := json.Marshal(value)
		if err != nil || string(body) != "null" {
			t.Errorf("Marshal(%+v) = %s, %v; want null", value, body, err)
		}
	}
	for _, tc := range []struct {
		wire string
		want Opt[string]
	}{
		{" \nnull\t ", Opt[string]{Set: true, Null: true}},
		{`"replacement"`, Opt[string]{Set: true, V: "replacement"}},
	} {
		got := Opt[string]{Set: true, Null: true, V: "stale"}
		if err := got.UnmarshalJSON([]byte(tc.wire)); err != nil || got != tc.want {
			t.Errorf("reused Opt.UnmarshalJSON(%q) = %+v, %v; want %+v", tc.wire, got, err, tc.want)
		}
	}
	for _, input := range []string{`42`, `{}`, `true`, `"unterminated`, `null true`} {
		var got Opt[string]
		if err := got.UnmarshalJSON([]byte(input)); err == nil {
			t.Errorf("Opt.UnmarshalJSON(%q) succeeded", input)
		}
	}
	if _, err := json.Marshal(Opt[float64]{Set: true, V: math.Inf(1)}); err == nil {
		t.Fatal("Opt.MarshalJSON failed to propagate an unsupported-value error")
	}
}

func TestOptJSONStrictValues(t *testing.T) {
	type req struct {
		Due Opt[Moment] `json:"due"`
	}
	minute := civil.Minute(540)
	for _, tc := range []struct {
		wire string
		want Opt[Moment]
	}{
		{`{}`, Opt[Moment]{}},
		{`{"due":null}`, Opt[Moment]{Set: true, Null: true}},
		{`{"due":{"date":"2026-10-30","time":null}}`, Opt[Moment]{Set: true, V: Moment{Date: civil.MustDate("2026-10-30")}}},
		{`{"due":{"date":"2026-10-30","time":"09:00"}}`, Opt[Moment]{Set: true, V: Moment{Date: civil.MustDate("2026-10-30"), Time: &minute}}},
	} {
		var got req
		dec := json.NewDecoder(strings.NewReader(tc.wire))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&got); err != nil || !reflect.DeepEqual(got.Due, tc.want) {
			t.Errorf("strict decode(%s) = %+v, %v; want %+v", tc.wire, got.Due, err, tc.want)
		}
	}
	for _, input := range []string{
		`{"due":{"date":"2026-10-30","time":null,"extra":1}}`,
		`{"due":{"extra":1,"date":"2026-10-30","time":"09:00"}}`,
	} {
		want := Opt[Moment]{Set: true, Null: true}
		got := req{Due: want}
		dec := json.NewDecoder(strings.NewReader(input))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&got); err == nil || !strings.Contains(err.Error(), `unknown field "extra"`) {
			t.Errorf("strict decode(%s) = %v; want unknown-field error", input, err)
		}
		if !reflect.DeepEqual(got.Due, want) {
			t.Errorf("failed decode changed option to %+v; want %+v", got.Due, want)
		}
	}
}

func TestOptJSONSingleValue(t *testing.T) {
	const moment = `{"date":"2026-10-30","time":null}`
	want := Opt[Moment]{Set: true, V: Moment{Date: civil.MustDate("2026-10-30")}}
	var got Opt[Moment]
	if err := got.UnmarshalJSON([]byte(" \n" + moment + "\t ")); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("single moment with whitespace = %+v, %v; want %+v", got, err, want)
	}
	for _, input := range []string{moment + " {}", moment + " null", moment + " trailing", "null true"} {
		got := want
		if err := got.UnmarshalJSON([]byte(input)); err == nil {
			t.Errorf("Opt.UnmarshalJSON(%s) accepted trailing data", input)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("failed decode changed option to %+v; want %+v", got, want)
		}
	}
}

func TestTimeJSON(t *testing.T) {
	loc, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		instant time.Time
		wire    string
	}{
		{time.Date(2026, 10, 28, 16, 0, 0, 0, time.UTC).In(loc), `"2026-10-28T10:00:00.000-06:00"`},
		{time.Date(2026, 11, 2, 7, 0, 0, 0, time.UTC).In(loc), `"2026-11-02T00:00:00.000-07:00"`},
		{time.Date(2026, 10, 28, 16, 0, 0, 123456789, time.UTC), `"2026-10-28T16:00:00.123Z"`},
	} {
		body, err := json.Marshal(Time{tc.instant})
		if err != nil || string(body) != tc.wire {
			t.Fatalf("Marshal(Time) = %s, %v; want %s", body, err, tc.wire)
		}
		var got Time
		if err := json.Unmarshal(body, &got); err != nil || !got.Equal(tc.instant.Truncate(time.Millisecond)) {
			t.Errorf("Unmarshal(%s) = %v, %v", body, got, err)
		}
	}
	for _, input := range []string{`"2026-10-28T16:00:00Z"`, `"2026-10-28T10:00:00-06:00"`, `"2026-10-28T16:00:00.123456789Z"`} {
		want, err := time.Parse(time.RFC3339Nano, input[1:len(input)-1])
		if err != nil {
			t.Fatal(err)
		}
		var got Time
		if err := json.Unmarshal([]byte(input), &got); err != nil || !got.Equal(want) {
			t.Errorf("Unmarshal(%s) = %v, %v; want %v", input, got, err, want)
		}
	}
	for _, input := range []string{`null`, `42`, `{}`, `""`, `"2026-10-28"`, `"2026-02-30T16:00:00Z"`, `"2026-10-28T16:00:00"`} {
		var got Time
		if err := json.Unmarshal([]byte(input), &got); err == nil {
			t.Errorf("Unmarshal Time(%s) succeeded", input)
		}
	}
}

func TestMomentJSON(t *testing.T) {
	minute := civil.Minute(540)
	midnight := civil.Minute(0)
	for _, tc := range []struct {
		wire string
		min  *civil.Minute
	}{
		{`{"date":"2026-10-30","time":null}`, nil},
		{`{"date":"2026-10-30","time":"09:00"}`, &minute},
		{`{"date":"2026-10-30","time":"00:00"}`, &midnight},
	} {
		want := Moment{Date: civil.MustDate("2026-10-30"), Time: tc.min}
		var got Moment
		if err := json.Unmarshal([]byte(tc.wire), &got); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("Unmarshal(%s) = %+v, %v; want %+v", tc.wire, got, err, want)
		}
		body, err := json.Marshal(got)
		if err != nil || string(body) != tc.wire {
			t.Errorf("Marshal(Moment) = %s, %v; want %s", body, err, tc.wire)
		}
	}
	for _, input := range []string{`{"date":"2026-02-30","time":null}`, `{"date":"2026-10-30","time":"24:00"}`} {
		var got Moment
		if err := json.Unmarshal([]byte(input), &got); err == nil {
			t.Errorf("Unmarshal Moment(%s) succeeded", input)
		}
	}
}

func TestProblemError(t *testing.T) {
	var err error = &Problem{Code: "conflict", Detail: "x"}
	if got := err.Error(); got != "conflict: x" {
		t.Fatalf("Problem.Error = %q, want %q", got, "conflict: x")
	}
}

func TestProblemJSON(t *testing.T) {
	base := Problem{
		Type: ProblemTypePrefix + "conflict", Title: "Conflict", Status: 409,
		Detail: "x", Code: "conflict", RequestID: "request-1",
	}
	const baseJSON = `{"type":"urn:taskronin:problem:conflict","title":"Conflict","status":409,"detail":"x","code":"conflict","request_id":"request-1"}`
	body, err := json.Marshal(base)
	if err != nil || string(body) != baseJSON {
		t.Fatalf("Marshal(Problem) = %s, %v; want %s", body, err, baseJSON)
	}
	full := base
	full.Errors = []FieldError{{Field: "title", Message: "required"}}
	full.Conflicts = []Conflict{{
		OpID: "0123456789abcdef0123456789abcdef", Kind: "task.update", Actor: "owner",
		At: Time{time.Date(2026, 10, 28, 16, 0, 0, 0, time.UTC)}, Entity: "task", EntityID: 7,
	}}
	full.Current = json.RawMessage(`{"id":7,"version":2}`)
	full.RetryAfter = 3
	const fullJSON = `{"type":"urn:taskronin:problem:conflict","title":"Conflict","status":409,"detail":"x","code":"conflict","request_id":"request-1","errors":[{"field":"title","message":"required"}],"conflicts":[{"op_id":"0123456789abcdef0123456789abcdef","kind":"task.update","actor":"owner","at":"2026-10-28T16:00:00.000Z","entity":"task","entity_id":7}],"current":{"id":7,"version":2},"retry_after":3}`
	body, err = json.Marshal(full)
	if err != nil || string(body) != fullJSON {
		t.Fatalf("Marshal(full Problem) = %s, %v; want %s", body, err, fullJSON)
	}
	var got Problem
	if err := json.Unmarshal(body, &got); err != nil || !reflect.DeepEqual(got, full) {
		t.Fatalf("Unmarshal(Problem) = %+v, %v; want %+v", got, err, full)
	}
}
