package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
)

// NewSessionID generates a random session ID and its unpadded base64url cookie.
func NewSessionID() (raw [32]byte, cookie string, err error) {
	raw, err = NewSecret()
	if err != nil {
		return raw, "", err
	}
	return raw, base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// ParseSessionCookie decodes a canonical unpadded base64url session ID.
func ParseSessionCookie(cookie string) ([32]byte, error) { return parseRawSecret(cookie) }

// HashSessionID returns the SHA-256 digest stored for a session ID.
func HashSessionID(raw [32]byte) [32]byte { return sha256.Sum256(raw[:]) }

// NewCSRFSecret generates an independent random 32-byte CSRF secret.
func NewCSRFSecret() ([32]byte, error) { return NewSecret() }

// CSRFToken computes the unpadded base64url HMAC-SHA256 of "tkr-csrf-v1".
func CSRFToken(secret [32]byte) string {
	mac := hmac.New(sha256.New, secret[:])
	_, _ = mac.Write([]byte("tkr-csrf-v1")) // hash.Hash.Write never returns an error.
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
