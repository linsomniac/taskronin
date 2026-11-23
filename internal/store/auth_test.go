package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/linsomniac/taskronin/internal/domain"
)

func randomHash(t *testing.T) [32]byte {
	t.Helper()
	var h [32]byte
	if _, err := rand.Read(h[:]); err != nil {
		t.Fatal(err)
	}
	return h
}

func tokenFixture(t *testing.T, name string) domain.Token {
	t.Helper()
	return domain.Token{Name: name, SecretHash: randomHash(t), Scopes: domain.MCPDefaultScopes,
		MutationsPerHour: 60, CreatedAt: time.UnixMilli(1793203200123).UTC()}
}

func TestTokens(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	if _, err := GetToken(ctx, st.Reader, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing token = %v", err)
	}
	a, b, c := tokenFixture(t, "ai"), tokenFixture(t, "agent"), tokenFixture(t, "revoked")
	b.ExpiresAt, b.LastUsedAt = present(b.CreatedAt.Add(time.Hour)), present(b.CreatedAt.Add(time.Minute))
	c.RevokedAt = present(c.CreatedAt.Add(time.Second))
	for _, tok := range []*domain.Token{&a, &b, &c} {
		tok.ID = 999
		if err := InsertToken(ctx, st.Writer, tok); err != nil {
			t.Fatal(err)
		}
		if tok.ID <= 0 || tok.ID == 999 {
			t.Fatalf("assigned token ID = %d", tok.ID)
		}
		if got, err := GetToken(ctx, st.Reader, tok.ID); err != nil || got != *tok {
			t.Fatalf("token round trip failed: %v", err)
		}
	}
	if got, err := ListTokens(ctx, st.Reader); err != nil || !slices.Equal(got, []domain.Token{a, b, c}) {
		t.Fatalf("token list failed: %v", err)
	}
	duplicate := tokenFixture(t, a.Name)
	var de *domain.Error
	if err := InsertToken(ctx, st.Writer, &duplicate); !errors.As(err, &de) || de.Code != domain.CodeConflict || duplicate.ID != 0 {
		t.Fatalf("duplicate token = %v", err)
	}
	later := a.CreatedAt.Add(2 * time.Minute)
	if err := TouchTokens(ctx, st.Writer, map[domain.TokenID]time.Time{a.ID: later, b.ID: b.CreatedAt, 999: later}); err != nil {
		t.Fatal(err)
	}
	if err := TouchTokens(ctx, st.Writer, map[domain.TokenID]time.Time{a.ID: a.CreatedAt, b.ID: later}); err != nil {
		t.Fatal(err)
	}
	a.LastUsedAt, b.LastUsedAt = present(later), present(later)
	for _, want := range []domain.Token{a, b} {
		if got, err := GetToken(ctx, st.Reader, want.ID); err != nil || got != want {
			t.Fatalf("monotonic token touch failed: %v", err)
		}
	}
	if err := RevokeToken(ctx, st.Writer, a.ID, later); err != nil {
		t.Fatal(err)
	}
	a.RevokedAt = present(later)
	if got, err := GetToken(ctx, st.Reader, a.ID); err != nil || got != a {
		t.Fatalf("token revoke failed: %v", err)
	}
	owner := tokenFixture(t, "owner."+rand.Text())
	owner.IsOwner, owner.Scopes, owner.MutationsPerHour = true, domain.AllScopes, 0
	if err := InsertToken(ctx, st.Writer, &owner); err != nil {
		t.Fatal(err)
	}
	if ids, err := RevokeAllTokens(ctx, st.Writer, later.Add(time.Minute)); err != nil || !slices.Equal(ids, []domain.TokenID{b.ID, owner.ID}) {
		t.Fatalf("RevokeAllTokens = %v, %v", ids, err)
	}
	b.RevokedAt = present(later.Add(time.Minute))
	owner.RevokedAt = b.RevokedAt
	if got, err := ListTokens(ctx, st.Reader); err != nil || !slices.Equal(got, []domain.Token{a, b, c, owner}) {
		t.Fatalf("revocation timestamps failed: %v", err)
	}
	if ids, err := RevokeAllTokens(ctx, st.Writer, later.Add(time.Hour)); err != nil || len(ids) != 0 {
		t.Fatalf("repeat revoke all = %v, %v", ids, err)
	}
	if err := RevokeToken(ctx, st.Writer, 999, later); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing revoke = %v", err)
	}
}

func sessionFixture(t *testing.T, now time.Time) Session {
	t.Helper()
	return Session{IDHash: randomHash(t), CSRFSecret: randomHash(t), Remote: "socket",
		CreatedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour), LastSeenAt: now}
}

func TestSessions(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	now := time.UnixMilli(1793203200123).UTC()
	s := sessionFixture(t, now)
	if _, err := GetSession(ctx, st.Reader, s.IDHash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing session = %v", err)
	}
	if err := InsertSession(ctx, st.Writer, s); err != nil {
		t.Fatal(err)
	}
	if got, err := GetSession(ctx, st.Reader, s.IDHash); err != nil || got != s {
		t.Fatalf("session round trip failed: %v", err)
	}
	var de *domain.Error
	if err := InsertSession(ctx, st.Writer, s); !errors.As(err, &de) || de.Code != domain.CodeConflict {
		t.Fatalf("duplicate session = %v", err)
	}
	later := now.Add(time.Minute)
	if err := TouchSessions(ctx, st.Writer, map[[32]byte]time.Time{s.IDHash: later, randomHash(t): later}); err != nil {
		t.Fatal(err)
	}
	if err := TouchSessions(ctx, st.Writer, map[[32]byte]time.Time{s.IDHash: now}); err != nil {
		t.Fatal(err)
	}
	s.LastSeenAt = later
	if got, err := GetSession(ctx, st.Reader, s.IDHash); err != nil || got != s {
		t.Fatalf("monotonic session touch failed: %v", err)
	}
	if err := DeleteSession(ctx, st.Writer, s.IDHash); err != nil {
		t.Fatal(err)
	}
	if _, err := GetSession(ctx, st.Reader, s.IDHash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted session = %v", err)
	}
	if err := DeleteSession(ctx, st.Writer, s.IDHash); err != nil {
		t.Fatal(err)
	}
	idle := 7 * 24 * time.Hour
	var sessions []Session
	for _, tc := range []struct{ expiry, seen time.Time }{
		{now, now}, {now.Add(-time.Millisecond), now},
		{now.Add(time.Hour), now.Add(-idle)}, {now.Add(time.Hour), now.Add(-idle - time.Millisecond)},
		{now.Add(time.Millisecond), now.Add(-idle + time.Millisecond)}, {now.Add(time.Hour), now},
	} {
		session := sessionFixture(t, now.Add(-30*24*time.Hour))
		session.ExpiresAt, session.LastSeenAt = tc.expiry, tc.seen
		if err := InsertSession(ctx, st.Writer, session); err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, session)
	}
	if n, err := DeleteExpiredSessions(ctx, st.Writer, now, idle); err != nil || n != 4 {
		t.Fatalf("expired session count = %d, %v", n, err)
	}
	for i, session := range sessions {
		got, err := GetSession(ctx, st.Reader, session.IDHash)
		if i < 4 {
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("expired session still present: %v", err)
			}
		} else if err != nil || got != session {
			t.Fatalf("live session changed: %v", err)
		}
	}
	if n, err := DeleteAllSessions(ctx, st.Writer); err != nil || n != 2 {
		t.Fatalf("delete all sessions = %d, %v", n, err)
	}
	if n, err := DeleteAllSessions(ctx, st.Writer); err != nil || n != 0 {
		t.Fatalf("delete empty sessions = %d, %v", n, err)
	}
}

func TestBootstrapDB(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	owner := tokenFixture(t, "unused")
	suffix := randomHash(t)
	owner.Name = fmt.Sprintf("owner.%d-%s", owner.CreatedAt.UnixMilli(), hex.EncodeToString(suffix[:4]))
	owner.ID, owner.IsOwner, owner.Scopes, owner.MutationsPerHour = 999, true, domain.AllScopes, 0
	b := Bootstrap{Generation: domain.NewGeneration(), TZ: "America/Denver", Owner: owner,
		Auth: AuthState{PasswordHash: "$argon2id$" + rand.Text(), PasswordChangedAt: owner.CreatedAt, IdemHMACKey: randomHash(t)}}
	tx, err := st.Writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	id, err := BootstrapDB(ctx, tx, b)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if id <= 0 || id == owner.ID {
		t.Fatalf("bootstrap ID = %d", id)
	}
	owner.ID = id
	if got, err := GetToken(ctx, st.Reader, id); err != nil || got != owner {
		t.Fatalf("bootstrap owner failed: %v", err)
	}
	for key, want := range map[string]string{"db_generation": string(b.Generation), "tz_last_seen": "America/Denver", "backup_keep_daily": "7"} {
		if got, err := GetSetting(ctx, st.Reader, key); err != nil || got != want {
			t.Fatalf("setting %s = %q, %v", key, got, err)
		}
	}
	if got, err := GetAuthState(ctx, st.Reader); err != nil || got != b.Auth {
		t.Fatalf("bootstrap auth failed: %v", err)
	}
	var isOwner, opCount, eventCount int
	var expires sql.Null[int64]
	if err := st.Reader.QueryRowContext(ctx, `SELECT is_owner, expires_at FROM api_tokens WHERE id = ?`, id).Scan(&isOwner, &expires); err != nil || isOwner != 1 || expires.Valid {
		t.Fatalf("owner SQL fields = %d/%v, %v", isOwner, expires, err)
	}
	if err := st.Reader.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM ops), (SELECT COUNT(*) FROM events)`).Scan(&opCount, &eventCount); err != nil || opCount != 0 || eventCount != 0 {
		t.Fatalf("bootstrap history = %d/%d, %v", opCount, eventCount, err)
	}
}
