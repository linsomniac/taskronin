// Package auth provides token, password, and session authentication primitives.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"taskronin/internal/domain"
)

// TokenPrefix identifies Taskronin API tokens.
const TokenPrefix = "tkr_"

var tokenPattern = regexp.MustCompile(`^tkr_([1-9][0-9]{0,18})_([A-Za-z0-9_-]{43})$`)

// NewSecret generates a cryptographically random 32-byte secret.
func NewSecret() (raw [32]byte, err error) {
	if _, err := rand.Read(raw[:]); err != nil {
		return [32]byte{}, fmt.Errorf("generate secret: %w", err)
	}
	return raw, nil
}

// FormatToken encodes a token ID and secret in the canonical bearer format.
func FormatToken(id domain.TokenID, raw [32]byte) string {
	return TokenPrefix + strconv.FormatInt(int64(id), 10) + "_" + base64.RawURLEncoding.EncodeToString(raw[:])
}

// ParseToken accepts only canonical tokens with a positive int64 ID.
func ParseToken(s string) (id domain.TokenID, raw [32]byte, err error) {
	parts := tokenPattern.FindStringSubmatch(s)
	if parts == nil {
		return 0, raw, errors.New("invalid token format")
	}
	n, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, raw, fmt.Errorf("invalid token ID: %w", err)
	}
	raw, err = parseRawSecret(parts[2])
	if err != nil {
		return 0, [32]byte{}, fmt.Errorf("invalid token secret: %w", err)
	}
	return domain.TokenID(n), raw, nil
}

// HashSecret returns the SHA-256 digest stored for a token secret.
func HashSecret(raw [32]byte) [32]byte { return sha256.Sum256(raw[:]) }

// OwnerTokenName returns a UTC millisecond timestamp with a random 4-hex suffix.
func OwnerTokenName(now time.Time) (string, error) {
	var suffix [2]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("generate owner token name: %w", err)
	}
	return fmt.Sprintf("owner.%s-%x", now.UTC().Format("20060102T150405.000Z"), suffix), nil
}

func parseRawSecret(s string) ([32]byte, error) {
	var raw [32]byte
	if len(s) != 43 {
		return raw, errors.New("invalid secret length")
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil {
		return raw, fmt.Errorf("invalid secret encoding: %w", err)
	}
	// AIDEV-NOTE: base64 decoders ignore CR/LF; require the exact canonical
	// spelling as well as zero padding bits, for both tokens and cookies.
	if len(decoded) != len(raw) || base64.RawURLEncoding.EncodeToString(decoded) != s {
		return raw, errors.New("noncanonical secret encoding")
	}
	copy(raw[:], decoded)
	return raw, nil
}
