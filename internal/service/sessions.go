package service

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"taskronin/internal/auth"
	"taskronin/internal/domain"
	"taskronin/internal/store"
)

// LoginIn supplies the password, password-gate key and session to replace.
type LoginIn struct{ RemoteKey, Password, OldCookie string }

// Session contains the credentials and absolute expiration of a new web session.
type Session struct {
	CookieValue, CSRFToken string
	ExpiresAt              time.Time
}

// WebAuth contains the authenticated web owner and the session's CSRF token.
type WebAuth struct {
	Actor     domain.Actor
	CSRFToken string
}

const (
	// SessionIdle is the maximum time a web session may remain inactive.
	SessionIdle = 7 * 24 * time.Hour
	// SessionMax is the absolute lifetime of a web session.
	SessionMax = 30 * 24 * time.Hour
)

// Login verifies the owner password and atomically replaces any presented session.
func (s *Service) Login(ctx context.Context, in LoginIn) (Session, error) {
	// AIDEV-NOTE: The password gate wait (up to 15 s) and argon2 run on the
	// READER before any writer transaction opens, so a slow login never blocks
	// the single writer. The writer tx then confirms auth_state is unchanged.
	a, err := store.GetAuthState(ctx, s.store.Reader)
	if err != nil {
		return Session{}, serviceError(err)
	}
	if a.WebLoginDisabled {
		return Session{}, domain.Errorf(domain.CodeForbidden, "web login is disabled")
	}
	if in.Password == "" {
		return Session{}, domain.Errorf(domain.CodePasswordRequired, "password is required")
	}
	ok, err := s.passwords.Verify(ctx, in.RemoteKey, a.PasswordHash, in.Password)
	if err != nil {
		return Session{}, serviceError(err)
	}
	if !ok {
		return Session{}, domain.Errorf(domain.CodePasswordInvalid, "invalid password")
	}
	var result Session
	err = s.writeTx(ctx, func(tx *sql.Tx, now time.Time) error {
		cur, err := store.GetAuthState(ctx, tx)
		if err != nil {
			return err
		}
		if cur.PasswordHash != a.PasswordHash || cur.WebLoginDisabled {
			return domain.Errorf(domain.CodeConflict, "credentials changed during login; retry")
		}
		if old, err := auth.ParseSessionCookie(in.OldCookie); err == nil {
			if err := store.DeleteSession(ctx, tx, auth.HashSessionID(old)); err != nil {
				return err
			}
		}
		raw, cookie, err := auth.NewSessionID()
		if err != nil {
			return err
		}
		csrf, err := auth.NewCSRFSecret()
		if err != nil {
			return err
		}
		row := store.Session{IDHash: auth.HashSessionID(raw), CSRFSecret: csrf, Remote: in.RemoteKey,
			CreatedAt: now, ExpiresAt: now.Add(SessionMax), LastSeenAt: now}
		if err := store.InsertSession(ctx, tx, row); err != nil {
			return err
		}
		result = Session{CookieValue: cookie, CSRFToken: auth.CSRFToken(csrf), ExpiresAt: row.ExpiresAt}
		return nil
	})
	if err != nil {
		return Session{}, err
	}
	return result, nil
}

// Logout deletes the presented session, if present.
func (s *Service) Logout(ctx context.Context, cookie string) error {
	return s.writeTx(ctx, func(tx *sql.Tx, _ time.Time) error {
		if raw, err := auth.ParseSessionCookie(cookie); err == nil {
			return store.DeleteSession(ctx, tx, auth.HashSessionID(raw))
		}
		return nil
	})
}

// Authenticate checks session lifetimes and queues successful activity in memory.
func (s *Service) Authenticate(ctx context.Context, cookie string) (WebAuth, error) {
	raw, err := auth.ParseSessionCookie(cookie)
	if err != nil {
		return WebAuth{}, domain.Errorf(domain.CodeUnauthenticated, "invalid session")
	}
	id := auth.HashSessionID(raw)
	s.usage.flushMu.RLock()
	defer s.usage.flushMu.RUnlock()
	row, err := store.GetSession(ctx, s.store.Reader, id)
	if errors.Is(err, store.ErrNotFound) {
		return WebAuth{}, domain.Errorf(domain.CodeUnauthenticated, "invalid session")
	}
	if err != nil {
		return WebAuth{}, serviceError(err)
	}
	now := s.clock.Now().Truncate(time.Millisecond)
	s.usage.mu.Lock()
	defer s.usage.mu.Unlock()
	lastSeen := row.LastSeenAt
	if at := s.usage.sessions[id]; at.After(lastSeen) {
		lastSeen = at
	}
	// AIDEV-NOTE: In-memory activity renews only the idle window. Absolute
	// expiry is inclusive and never changes, even while actively in use.
	if !row.ExpiresAt.After(now) || !lastSeen.After(now.Add(-SessionIdle)) {
		return WebAuth{}, domain.Errorf(domain.CodeUnauthenticated, "invalid session")
	}
	if s.usage.sessions == nil {
		s.usage.sessions = make(map[[32]byte]time.Time)
	}
	if now.After(lastSeen) {
		lastSeen = now
	}
	s.usage.sessions[id] = lastSeen
	return WebAuth{Actor: domain.Actor{Kind: domain.ActorOwner, Channel: domain.ChanWeb,
		Scopes: domain.AllScopes, RemoteKey: row.Remote}, CSRFToken: auth.CSRFToken(row.CSRFSecret)}, nil
}

// ExpireSessions persists pending activity and deletes sessions at absolute or idle expiry.
func (s *Service) ExpireSessions(ctx context.Context) (int, error) {
	leave, err := s.enter()
	if err != nil {
		return 0, err
	}
	defer leave()
	// AIDEV-NOTE: Authentication may run after the scheduler's last flush.
	// Hold the same handoff lock as FlushUsage before acquiring the writer,
	// and touch pending activity in the deletion transaction. On rollback,
	// leave the queue intact so retry cannot expire a recently used session.
	s.usage.flushMu.Lock()
	defer s.usage.flushMu.Unlock()
	s.usage.mu.Lock()
	sessions := s.usage.sessions
	s.usage.mu.Unlock()
	var n int64
	err = s.writeTxEntered(ctx, func(tx *sql.Tx, now time.Time) error {
		if err := store.TouchSessions(ctx, tx, sessions); err != nil {
			return err
		}
		var err error
		n, err = store.DeleteExpiredSessions(ctx, tx, now, SessionIdle)
		return err
	})
	if err != nil {
		return 0, err
	}
	s.usage.mu.Lock()
	s.usage.sessions = make(map[[32]byte]time.Time)
	s.usage.mu.Unlock()
	return int(n), nil
}
