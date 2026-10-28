package server

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"taskronin/internal/api"
	"taskronin/internal/domain"
)

func FuzzDecodeRequests(f *testing.F) {
	for _, seed := range [][]byte{nil, []byte(`{}`), []byte(`{} {}`), []byte(`{"title":"x"}`), []byte(`{"until":null}`), []byte(`{"place":{"surprise":1}}`), {0xff}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		fuzzDecode[api.AddTaskRequest](t, b)
		fuzzDecode[api.UpdateTaskRequest](t, b)
		fuzzDecode[api.DueRequest](t, b)
		fuzzDecode[api.DeferRequest](t, b)
		fuzzDecode[api.PlaceRequest](t, b)
		fuzzDecode[api.BlockRequest](t, b)
		fuzzDecode[api.ReblockRequest](t, b)
		fuzzDecode[api.OutcomeRequest](t, b)
		fuzzDecode[api.CompleteRequest](t, b)
		fuzzDecode[api.ReopenRequest](t, b)
		fuzzDecode[api.AcceptNewRequest](t, b)
		fuzzDecode[api.AddEntryRequest](t, b)
		fuzzDecode[api.UpdateEntryRequest](t, b)
		fuzzDecode[api.RelinkRequest](t, b)
		fuzzDecode[api.FocusRequest](t, b)
		fuzzDecode[api.ReviewRequest](t, b)
		fuzzDecode[api.PasswordRequest](t, b)
		fuzzDecode[api.SettingsRequest](t, b)
		fuzzDecode[api.CreateTokenRequest](t, b)
	})
}

func fuzzDecode[T any](t *testing.T, b []byte) {
	r := httptest.NewRequestWithContext(t.Context(), "POST", "/api/v1/x", bytes.NewReader(b))
	_, _, _ = decodeBody[T](r)
}

func TestPasswordBodiesNeverCanonicalized(t *testing.T) {
	// Random credentials, never fixed secrets, even in decoder tests.
	secret := api.NewIdempotencyKey()
	for _, body := range []string{`{"current":"` + secret + `","new":"` + secret + `"}`, `{"password":"` + secret + `"}`} {
		r := httptest.NewRequestWithContext(t.Context(), "POST", "/api/v1/x", bytes.NewBufferString(body))
		if bytes.Contains([]byte(body), []byte(`"current"`)) {
			v, canonical, err := decodeBody[api.PasswordRequest](r)
			if err != nil || v.Current != secret || v.New != secret || canonical != nil {
				t.Fatal("password canonicalized or decode failed")
			}
		} else {
			v, canonical, err := decodeBody[api.SettingsRequest](r)
			if err != nil || v.Password != secret || canonical != nil {
				t.Fatal("settings password canonicalized or decode failed")
			}
		}
	}
}

func TestDecodeCanonicalAndEmpty(t *testing.T) {
	for _, body := range []string{"", ` { "title": "", "tags": [] } `} {
		v, canonical, err := decodeBody[api.AddTaskRequest](httptest.NewRequestWithContext(t.Context(), "POST", "/", bytes.NewBufferString(body)))
		if err != nil || v.Title != "" || string(canonical) != `{"title":""}` {
			t.Fatalf("canonical = %s, error = %v", canonical, err)
		}
	}
}

func TestDecodeUnrepresentableRequestIsMalformed(t *testing.T) {
	// A missing civil date decodes to zero but cannot be marshaled. Invalid
	// client input must not turn a canonicalization error into HTTP 500.
	_, canonical, err := decodeBody[api.BlockRequest](httptest.NewRequestWithContext(t.Context(), "POST", "/", bytes.NewBufferString(`{}`)))
	de, ok := domain.AsError(err)
	if !ok || de.Code != domain.CodeMalformed || canonical != nil {
		t.Fatalf("canonical = %s, error = %v; want malformed", canonical, err)
	}
}
