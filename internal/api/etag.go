package api

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/linsomniac/taskronin/internal/domain"
)

// FormatETag returns the quoted strong entity ETag for gen and v.
func FormatETag(gen domain.Generation, v domain.Version) string {
	return domain.FormatETag(gen, v)
}

// ParseETag parses exactly one strong entity ETag with a positive row version.
func ParseETag(s string) (domain.Expect, error) {
	return domain.ParseETag(s)
}

// AggregateETag returns a quoted ETag using the first 16 hex digits of SHA-256(body).
func AggregateETag(body []byte) string {
	sum := sha256.Sum256(body)
	return `"a-` + hex.EncodeToString(sum[:8]) + `"`
}
