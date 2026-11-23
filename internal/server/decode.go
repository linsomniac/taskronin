package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/domain"
)

// decodeBody canonicalizes keyed requests after strict decoding. Redacted
// request types return no canonical bytes; their routes must use exemptMeta.
func decodeBody[T any](r *http.Request) (T, []byte, error) {
	v, err := decodeRequest[T](r)
	if err != nil {
		return v, nil, err
	}
	// AIDEV-NOTE: Password and settings DTOs implement LogValuer. Keep their
	// strict decoding separate from idempotency canonicalization (BF04): no
	// serialized copy of a password may enter Idem.Body, even accidentally.
	if _, sensitive := any(v).(slog.LogValuer); sensitive {
		return v, nil, nil
	}
	body, err := json.Marshal(v)
	if err != nil {
		return v, nil, domain.Errorf(domain.CodeMalformed, "request cannot be represented as JSON")
	}
	return v, body, nil
}

// decodeRequest is also the strict, non-canonicalizing path for exempt requests.
func decodeRequest[T any](r *http.Request) (T, error) {
	var v T
	if r.Body == nil {
		return v, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, api.MaxBodyBytes+1))
	if err != nil {
		return v, bodyReadError(err)
	}
	if len(body) > api.MaxBodyBytes {
		return v, domain.Errorf(domain.CodeTooLarge, "request body exceeds 1 MiB")
	}
	if len(body) == 0 {
		return v, nil
	}
	if !utf8.Valid(body) {
		return v, domain.Errorf(domain.CodeMalformed, "request body must be UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, domain.Errorf(domain.CodeMalformed, "invalid JSON request")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return v, domain.Errorf(domain.CodeMalformed, "request must contain one JSON value")
	}
	return v, nil
}

func bodyReadError(err error) error {
	var limit *http.MaxBytesError
	if errors.As(err, &limit) {
		return domain.Errorf(domain.CodeTooLarge, "request body exceeds 1 MiB")
	}
	return domain.Errorf(domain.CodeMalformed, "cannot read request body")
}

func ifMatch(r *http.Request) (domain.Expect, error) {
	values, exists := r.Header["If-Match"]
	if !exists {
		return domain.Expect{}, nil
	}
	if len(values) != 1 {
		return domain.Expect{}, domain.Errorf(domain.CodeMalformed, "If-Match requires one strong ETag")
	}
	return api.ParseETag(values[0])
}

func pathInt(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil {
		return 0, domain.Errorf(domain.CodeMalformed, "invalid %s", name)
	}
	return id, nil
}
