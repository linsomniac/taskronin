package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"

	"taskronin/internal/civil"
)

// Time encodes an instant with millisecond precision in its existing location.
type Time struct{ time.Time }

// MarshalJSON retains the instant's zone offset and always emits three fractional digits.
func (t Time) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.Format("2006-01-02T15:04:05.000Z07:00"))
}

// UnmarshalJSON decodes an RFC 3339 timestamp, including optional fractional seconds.
func (t *Time) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return err
	}
	t.Time = v
	return nil
}

// Moment is a civil date with an optional minute of day; null time means date-only.
type Moment struct {
	Date civil.Date    `json:"date"`
	Time *civil.Minute `json:"time"`
}

// Opt distinguishes an absent field, an explicit null, and a value.
type Opt[T any] struct {
	Set, Null bool
	V         T
}

// IsZero reports whether the field was absent.
func (o Opt[T]) IsZero() bool { return !o.Set }

// MarshalJSON emits null for unset or explicitly null fields, otherwise V.
func (o Opt[T]) MarshalJSON() ([]byte, error) {
	// AIDEV-NOTE: Optional request fields must use json:",omitzero" so absent
	// fields are omitted before this method runs. This preserves R2-3's distinct
	// canonical idempotency bodies for absent fields and explicit nulls.
	if !o.Set || o.Null {
		return []byte("null"), nil
	}
	return json.Marshal(o.V)
}

// UnmarshalJSON marks a present field as either null or a decoded value.
func (o *Opt[T]) UnmarshalJSON(b []byte) error {
	value := Opt[T]{Set: true}
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		value.Null = true
	} else {
		// AIDEV-NOTE: An outer decoder's DisallowUnknownFields does not reach
		// custom unmarshalers. Enforce A§6.3 here for struct values such as Moment.
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&value.V); err != nil {
			return err
		}
		if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
			if err != nil {
				return err
			}
			return errors.New("api: trailing data after option value")
		}
	}
	*o = value
	return nil
}
