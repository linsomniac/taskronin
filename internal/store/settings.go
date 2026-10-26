package store

import (
	"context"
	"fmt"
	"time"
)

// GetSetting returns a setting's value, or ErrNotFound for a missing key.
func GetSetting(ctx context.Context, q Q, key string) (string, error) {
	var value string
	err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	return value, MapErr(err)
}

// SetSetting inserts a setting or replaces its value.
func SetSetting(ctx context.Context, q Q, key, value string) error {
	_, err := q.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return MapErr(err)
}

// AuthState is the singleton authentication configuration; it contains secrets.
type AuthState struct {
	PasswordHash      string
	PasswordChangedAt time.Time
	WebLoginDisabled  bool
	IdemHMACKey       [32]byte
}

func scanAuthState(row rowScanner) (AuthState, error) {
	var a AuthState
	var changed int64
	var key []byte
	if err := row.Scan(&a.PasswordHash, &changed, &a.WebLoginDisabled, &key); err != nil {
		return AuthState{}, MapErr(err)
	}
	if len(key) != len(a.IdemHMACKey) {
		return AuthState{}, MapErr(fmt.Errorf("store: invalid auth HMAC key length"))
	}
	copy(a.IdemHMACKey[:], key)
	a.PasswordChangedAt = time.UnixMilli(changed).UTC()
	return a, nil
}

// GetAuthState returns the authentication singleton, or ErrNotFound before initialization.
func GetAuthState(ctx context.Context, q Q) (AuthState, error) {
	return scanAuthState(q.QueryRowContext(ctx, `SELECT password_hash, password_changed_at,
		web_login_disabled, idem_hmac_key FROM auth_state WHERE id = 1`))
}

// PutAuthState inserts or replaces authentication row 1.
func PutAuthState(ctx context.Context, q Q, a AuthState) error {
	_, err := q.ExecContext(ctx, `INSERT INTO auth_state (id, password_hash, password_changed_at, web_login_disabled, idem_hmac_key)
		VALUES (1, ?, ?, ?, ?) ON CONFLICT (id) DO UPDATE SET password_hash = excluded.password_hash,
		password_changed_at = excluded.password_changed_at, web_login_disabled = excluded.web_login_disabled,
		idem_hmac_key = excluded.idem_hmac_key`, a.PasswordHash, a.PasswordChangedAt.UnixMilli(), a.WebLoginDisabled, a.IdemHMACKey[:])
	return MapErr(err)
}
