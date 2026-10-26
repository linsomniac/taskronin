package domain

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"
)

// Cursor describes either a keyset position or an offset position.
// Lists validate their own key types, ranges, and pagination mode.
type Cursor struct {
	Keys   []any
	ID     int64
	Offset int
}

// EncodeCursor returns unpadded base64url JSON, using the offset form when
// Keys is nil and ID is zero. Keys must be JSON-encodable; otherwise it panics.
func EncodeCursor(c Cursor) string {
	var wire any
	if c.Keys == nil && c.ID == 0 {
		wire = struct {
			Version int `json:"v"`
			Offset  int `json:"o"`
		}{1, c.Offset}
	} else {
		wire = struct {
			Version int   `json:"v"`
			Keys    []any `json:"k"`
			ID      int64 `json:"id"`
		}{1, c.Keys, c.ID}
	}
	data, err := json.Marshal(wire)
	if err != nil {
		panic(fmt.Errorf("domain: encode cursor: %w", err))
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

// DecodeCursor parses one version-1 cursor with exact field names and preserves
// numbers in Keys as json.Number. Invalid input returns CodeMalformed.
func DecodeCursor(s string) (Cursor, error) {
	data, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil || base64.RawURLEncoding.EncodeToString(data) != s || !utf8.Valid(data) {
		return Cursor{}, Errorf(CodeMalformed, "invalid cursor encoding")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	start, err := dec.Token()
	if err != nil || start != json.Delim('{') {
		return Cursor{}, Errorf(CodeMalformed, "cursor must be a JSON object")
	}

	// AIDEV-NOTE: Decode fields explicitly: encoding/json struct decoding accepts
	// duplicate and case-insensitive names. Cursor envelopes must be unambiguous;
	// list-specific key validation belongs to service, not this codec.
	var c Cursor
	var version, offset *int
	var id *int64
	seen := make(map[string]bool, 4)
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return Cursor{}, Errorf(CodeMalformed, "invalid cursor field")
		}
		name, ok := token.(string)
		if !ok || seen[name] {
			return Cursor{}, Errorf(CodeMalformed, "invalid or duplicate cursor field")
		}
		seen[name] = true
		switch name {
		case "v":
			err = dec.Decode(&version)
		case "k":
			err = dec.Decode(&c.Keys)
		case "id":
			err = dec.Decode(&id)
		case "o":
			err = dec.Decode(&offset)
		default:
			return Cursor{}, Errorf(CodeMalformed, "unknown cursor field")
		}
		if err != nil {
			return Cursor{}, Errorf(CodeMalformed, "invalid cursor field value")
		}
	}
	if end, err := dec.Token(); err != nil || end != json.Delim('}') {
		return Cursor{}, Errorf(CodeMalformed, "invalid cursor object")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return Cursor{}, Errorf(CodeMalformed, "trailing cursor data")
	}
	if version == nil || *version != 1 {
		return Cursor{}, Errorf(CodeMalformed, "unsupported cursor version")
	}
	if seen["o"] {
		if offset == nil || seen["k"] || seen["id"] {
			return Cursor{}, Errorf(CodeMalformed, "invalid offset cursor")
		}
		c.Offset = *offset
	} else {
		if !seen["k"] || id == nil {
			return Cursor{}, Errorf(CodeMalformed, "invalid keyset cursor")
		}
		c.ID = *id
	}
	return c, nil
}
