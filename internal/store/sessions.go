package store

import (
	"context"
	"fmt"
	"time"
)

// Session stores a web session's hashed ID, CSRF secret, remote and lifetimes.
type Session struct {
	IDHash, CSRFSecret               [32]byte
	Remote                           string
	CreatedAt, ExpiresAt, LastSeenAt time.Time
}

// InsertSession inserts a session without replacing an existing ID hash.
func InsertSession(ctx context.Context, q Q, s Session) error {
	_, err := q.ExecContext(ctx, `INSERT INTO web_sessions
		(id_hash, csrf_secret, remote, created_at, expires_at, last_seen_at) VALUES (?, ?, ?, ?, ?, ?)`,
		s.IDHash[:], s.CSRFSecret[:], s.Remote, s.CreatedAt.UnixMilli(), s.ExpiresAt.UnixMilli(), s.LastSeenAt.UnixMilli())
	return MapErr(err)
}

// GetSession returns a session, including expired sessions, or ErrNotFound if absent.
func GetSession(ctx context.Context, q Q, idHash [32]byte) (Session, error) {
	var s Session
	var id, csrf []byte
	var created, expires, seen int64
	err := q.QueryRowContext(ctx, `SELECT id_hash, csrf_secret, remote, created_at, expires_at, last_seen_at
		FROM web_sessions WHERE id_hash = ?`, idHash[:]).Scan(&id, &csrf, &s.Remote, &created, &expires, &seen)
	if err != nil {
		return Session{}, MapErr(err)
	}
	if len(id) != len(s.IDHash) || len(csrf) != len(s.CSRFSecret) {
		return Session{}, MapErr(fmt.Errorf("store: invalid session hash or secret length"))
	}
	copy(s.IDHash[:], id)
	copy(s.CSRFSecret[:], csrf)
	s.CreatedAt, s.ExpiresAt, s.LastSeenAt = time.UnixMilli(created).UTC(), time.UnixMilli(expires).UTC(), time.UnixMilli(seen).UTC()
	return s, nil
}

// DeleteSession deletes the session with idHash, if present.
func DeleteSession(ctx context.Context, q Q, idHash [32]byte) error {
	_, err := q.ExecContext(ctx, `DELETE FROM web_sessions WHERE id_hash = ?`, idHash[:])
	return MapErr(err)
}

// DeleteAllSessions removes every session and returns the number deleted.
func DeleteAllSessions(ctx context.Context, q Q) (int64, error) {
	result, err := q.ExecContext(ctx, `DELETE FROM web_sessions`)
	if err != nil {
		return 0, MapErr(err)
	}
	n, err := result.RowsAffected()
	return n, MapErr(err)
}

// TouchSessions advances last_seen_at without moving stored times backward.
func TouchSessions(ctx context.Context, q Q, last map[[32]byte]time.Time) error {
	for id, at := range last {
		if _, err := q.ExecContext(ctx, `UPDATE web_sessions SET last_seen_at = ?1
			WHERE id_hash = ?2 AND last_seen_at < ?1`, at.UnixMilli(), id[:]); err != nil {
			return MapErr(err)
		}
	}
	return nil
}

// DeleteExpiredSessions removes sessions at either absolute or idle expiry, inclusively.
func DeleteExpiredSessions(ctx context.Context, q Q, now time.Time, idle time.Duration) (int64, error) {
	result, err := q.ExecContext(ctx, `DELETE FROM web_sessions WHERE expires_at <= ? OR last_seen_at <= ?`,
		now.UnixMilli(), now.Add(-idle).UnixMilli())
	if err != nil {
		return 0, MapErr(err)
	}
	n, err := result.RowsAffected()
	return n, MapErr(err)
}
