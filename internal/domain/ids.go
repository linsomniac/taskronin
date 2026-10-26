// Package domain defines the shared vocabulary for Taskronin entities and operations.
package domain

import (
	"crypto/rand"
	"database/sql/driver"
	"encoding/hex"
	"fmt"
	"strconv"
)

type (
	// TaskID identifies a task.
	TaskID int64
	// EntryID identifies a log entry.
	EntryID int64
	// BlockID identifies a time block.
	BlockID int64
	// TagID identifies a tag.
	TagID int64
	// InterruptionID identifies an interruption.
	InterruptionID int64
	// TokenID identifies an authentication token.
	TokenID int64
	// EventID identifies an event.
	EventID int64
	// OpSeq is the monotonic ops.seq used for ordering and acknowledgement.
	OpSeq int64
)

// OpID is a random operation identifier, stored as 32 lowercase hex characters.
type OpID [16]byte

// NewOpID returns a cryptographically random operation identifier.
func NewOpID() OpID {
	var id OpID
	if _, err := rand.Read(id[:]); err != nil {
		panic(fmt.Errorf("domain: generate op ID: %w", err))
	}
	return id
}

// ParseOpID parses exactly 32 lowercase hex characters.
func ParseOpID(s string) (OpID, error) {
	var id OpID
	if !lowerHex(s, 32) {
		return id, Errorf(CodeMalformed, "op ID must be 32 lowercase hex characters")
	}
	if _, err := hex.Decode(id[:], []byte(s)); err != nil {
		return OpID{}, fmt.Errorf("domain: decode op ID: %w", err)
	}
	return id, nil
}

func lowerHex(s string, size int) bool {
	if len(s) != size {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// String returns the 32-character lowercase hex representation.
func (id OpID) String() string { return hex.EncodeToString(id[:]) }

// IsZero reports whether id is the zero identifier.
func (id OpID) IsZero() bool { return id == OpID{} }

// Value implements driver.Valuer using the lowercase hex representation.
func (id OpID) Value() (driver.Value, error) { return id.String(), nil }

// Scan implements sql.Scanner from a string or byte slice.
func (id *OpID) Scan(src any) error {
	switch v := src.(type) {
	case string:
		return id.UnmarshalText([]byte(v))
	case []byte:
		return id.UnmarshalText(v)
	default:
		return fmt.Errorf("domain: cannot scan %T into OpID", src)
	}
}

// MarshalText encodes id as 32 lowercase hex characters.
func (id OpID) MarshalText() ([]byte, error) { return []byte(id.String()), nil }

// UnmarshalText decodes exactly 32 lowercase hex characters.
func (id *OpID) UnmarshalText(text []byte) error {
	parsed, err := ParseOpID(string(text))
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

// Version is a per-row version, starting at 1.
type Version int64

// Generation identifies a database generation with 16 lowercase hex characters.
type Generation string

// NewGeneration returns a cryptographically random database generation.
func NewGeneration() Generation {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(fmt.Errorf("domain: generate database generation: %w", err))
	}
	return Generation(hex.EncodeToString(bytes[:]))
}

// Expect is the generation and row version required by an entity precondition.
type Expect struct {
	Gen     Generation
	Version Version
}

// FormatETag returns the quoted strong ETag for g and v.
func FormatETag(g Generation, v Version) string {
	return fmt.Sprintf(`"%s-%d"`, g, v)
}

// ParseETag parses exactly one strong entity ETag with a positive row version.
func ParseETag(s string) (Expect, error) {
	if len(s) < 20 || s[0] != '"' || s[len(s)-1] != '"' || s[17] != '-' || !lowerHex(s[1:17], 16) {
		return Expect{}, Errorf(CodeMalformed, "invalid entity ETag")
	}
	version := s[18 : len(s)-1]
	if version[0] < '1' || version[0] > '9' {
		return Expect{}, Errorf(CodeMalformed, "invalid entity ETag version")
	}
	for _, c := range version {
		if c < '0' || c > '9' {
			return Expect{}, Errorf(CodeMalformed, "invalid entity ETag version")
		}
	}
	v, err := strconv.ParseInt(version, 10, 64)
	if err != nil {
		return Expect{}, Errorf(CodeMalformed, "invalid entity ETag version")
	}
	return Expect{Gen: Generation(s[1:17]), Version: Version(v)}, nil
}
