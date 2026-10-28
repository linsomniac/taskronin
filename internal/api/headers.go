// Package api defines Taskronin's HTTP wire types and codecs.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// Protocol headers, media types, limits, and problem type namespace.
const (
	HeaderIdempotencyKey = "Idempotency-Key"
	HeaderGeneration     = "Taskronin-Generation"
	HeaderChannel        = "X-Taskronin-Channel"
	HeaderVersion        = "X-Taskronin-Version"
	HeaderRequestID      = "X-Request-ID"

	ContentTypeJSON    = "application/json"
	ContentTypeProblem = "application/problem+json"

	MaxBodyBytes = 1 << 20
	DefaultLimit = 100
	MaxLimit     = 500

	ProblemTypePrefix = "urn:taskronin:problem:"
)

// ValidIdempotencyKey reports whether k is 16..128 ASCII letters, digits, '_' or '-'.
func ValidIdempotencyKey(k string) bool {
	if len(k) < 16 || len(k) > 128 {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// NewIdempotencyKey returns a cryptographically random 32-character lowercase hex key.
func NewIdempotencyKey() string {
	var key [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		panic(fmt.Errorf("api: generate idempotency key: %w", err))
	}
	return hex.EncodeToString(key[:])
}
