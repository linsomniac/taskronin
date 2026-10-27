package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	// ArgonTime is the number of passes used for new password hashes.
	ArgonTime = 3
	// ArgonMemoryKiB is the memory cost of new password hashes, in KiB.
	ArgonMemoryKiB = 64 * 1024
	// ArgonThreads is the parallelism of new password hashes.
	ArgonThreads = 1
	// ArgonKeyLen is the derived key length in bytes.
	ArgonKeyLen = 32
	// SaltLen is the random password salt length in bytes.
	SaltLen = 16
)

var errPasswordHash = errors.New("invalid password hash encoding or parameters")

// HashPassword validates and hashes a new password using a fresh random salt.
func HashPassword(pw string) (string, error) {
	if err := ValidateNewPassword(pw); err != nil {
		return "", err
	}
	var salt [SaltLen]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key := argon2.IDKey([]byte(pw), salt[:], ArgonTime, ArgonMemoryKiB, ArgonThreads, ArgonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		ArgonMemoryKiB, ArgonTime, ArgonThreads,
		base64.RawStdEncoding.EncodeToString(salt[:]), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword derives a key using bounded encoded parameters and compares it
// in constant time. A password mismatch is false, nil; a malformed hash is an error.
func VerifyPassword(encoded, pw string) (bool, error) {
	parts := strings.SplitN(encoded, "$", 7)
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false, errPasswordHash
	}
	params := strings.SplitN(parts[3], ",", 4)
	if len(params) != 3 {
		return false, errPasswordHash
	}
	// AIDEV-NOTE: bound every cost before calling argon2, which otherwise
	// permits excessive allocation/work and panics for zero time or threads.
	memory, err := passwordParam(params[0], "m=", 1024*1024)
	if err != nil {
		return false, err
	}
	passes, err := passwordParam(params[1], "t=", 10)
	if err != nil {
		return false, err
	}
	threads, err := passwordParam(params[2], "p=", 4)
	if err != nil || memory < 8*threads {
		return false, errPasswordHash
	}
	salt, err := passwordHashBytes(parts[4], SaltLen)
	if err != nil {
		return false, err
	}
	want, err := passwordHashBytes(parts[5], ArgonKeyLen)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(pw), salt, passes, memory, uint8(threads), ArgonKeyLen) //nolint:gosec // G115: passwordParam bounds threads to 1..4.
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func passwordParam(s, prefix string, maximum uint32) (uint32, error) {
	number, ok := strings.CutPrefix(s, prefix)
	if !ok || number == "" || number[0] < '1' || number[0] > '9' {
		return 0, errPasswordHash
	}
	n, err := strconv.ParseUint(number, 10, 32)
	if err != nil || n == 0 || n > uint64(maximum) {
		return 0, errPasswordHash
	}
	return uint32(n), nil
}

func passwordHashBytes(s string, size int) ([]byte, error) {
	if len(s) != base64.RawStdEncoding.EncodedLen(size) {
		return nil, errPasswordHash
	}
	b, err := base64.RawStdEncoding.Strict().DecodeString(s)
	if err != nil || len(b) != size || base64.RawStdEncoding.EncodeToString(b) != s {
		return nil, errPasswordHash
	}
	return b, nil
}

// ValidateNewPassword requires valid UTF-8 containing 8 through 1024 bytes.
func ValidateNewPassword(pw string) error {
	if len(pw) < 8 || len(pw) > 1024 || !utf8.ValidString(pw) {
		return errors.New("password must be valid UTF-8 and contain 8..1024 bytes")
	}
	return nil
}
