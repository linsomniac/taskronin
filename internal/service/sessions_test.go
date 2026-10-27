package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"taskronin/internal/auth"
	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/store"
	"taskronin/internal/testutil"
)

func loginSession(t *testing.T, s *testutil.Scenario, old string) service.Session {
	t.Helper()
	sess, err := s.Svc.Login(s.Ctx(), service.LoginIn{RemoteKey: "127.0.0.1", Password: testutil.Password, OldCookie: old})
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func sessionHash(t *testing.T, cookie string) [32]byte {
	t.Helper()
	raw, err := auth.ParseSessionCookie(cookie)
	if err != nil {
		t.Fatal(err)
	}
	return auth.HashSessionID(raw)
}

func sessionRow(t *testing.T, s *testutil.Scenario, cookie string) store.Session {
	t.Helper()
	row, err := store.GetSession(s.Ctx(), s.Store.Reader, sessionHash(t, cookie))
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func requireUnauthenticatedSession(t *testing.T, s *testutil.Scenario, cookie string) {
	t.Helper()
	got, err := s.Svc.Authenticate(s.Ctx(), cookie)
	testutil.RequireCode(t, err, domain.CodeUnauthenticated)
	if got != (service.WebAuth{}) {
		t.Fatal("failed authentication returned authority or CSRF token")
	}
}

func TestLoginAndSessions(t *testing.T) {
	t.Run("credentials", func(t *testing.T) {
		s := testutil.NewScenario(t)
		for _, tc := range []struct {
			password string
			code     domain.Code
		}{
			{"", domain.CodePasswordRequired},
			{auth.FormatToken(1, mintSecret(t)), domain.CodePasswordInvalid},
		} {
			sess, err := s.Svc.Login(s.Ctx(), service.LoginIn{RemoteKey: "127.0.0.1", Password: tc.password})
			testutil.RequireCode(t, err, tc.code)
			if sess != (service.Session{}) {
				t.Fatal("failed login returned session credentials")
			}
		}
		requireSQLCount(t, s, `SELECT COUNT(*) FROM web_sessions`, 0)
		requireSQLCount(t, s, `SELECT COUNT(*) FROM ops`, 0)
	})
	t.Run("disabled precedes password", func(t *testing.T) {
		s := testutil.NewScenario(t)
		// AIDEV-NOTE: F14 fixture shortcut: disabling web login requires the
		// unreadable-database restore path, which arrives in Plan B.
		s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), nil, func(q store.Q) error {
			a, err := store.GetAuthState(s.Ctx(), q)
			if err != nil {
				return err
			}
			a.WebLoginDisabled = true
			return store.PutAuthState(s.Ctx(), q, a)
		}))
		for _, password := range []string{"", testutil.Password} {
			sess, err := s.Svc.Login(s.Ctx(), service.LoginIn{RemoteKey: "127.0.0.1", Password: password})
			testutil.RequireCode(t, err, domain.CodeForbidden)
			if sess != (service.Session{}) {
				t.Fatal("disabled login returned session credentials")
			}
		}
		requireSQLCount(t, s, `SELECT COUNT(*) FROM web_sessions`, 0)
	})
	t.Run("identity rotation and logout", func(t *testing.T) {
		s := testutil.NewScenario(t)
		s.Clock.Advance(1234567 * time.Nanosecond)
		sess := loginSession(t, s, "malformed")
		row := sessionRow(t, s, sess.CookieValue)
		now := s.Clock.Now().Truncate(time.Millisecond)
		if !row.CreatedAt.Equal(now) || !row.LastSeenAt.Equal(now) || !row.ExpiresAt.Equal(now.Add(30*24*time.Hour)) ||
			!sess.ExpiresAt.Equal(row.ExpiresAt) || row.Remote != "127.0.0.1" || sess.CSRFToken != auth.CSRFToken(row.CSRFSecret) {
			t.Fatal("session storage, lifetimes or CSRF derivation incorrect")
		}
		got, err := s.Svc.Authenticate(s.Ctx(), sess.CookieValue)
		want := domain.Actor{Kind: domain.ActorOwner, Channel: domain.ChanWeb, Scopes: domain.AllScopes, RemoteKey: row.Remote}
		if err != nil || got.Actor != want || got.CSRFToken != sess.CSRFToken {
			t.Fatal("session authentication returned incorrect identity or CSRF", err)
		}
		_, unknown, err := auth.NewSessionID()
		if err != nil {
			t.Fatal(err)
		}
		for _, cookie := range []string{"", "garbage", sess.CookieValue + "=", sess.CookieValue + "\n", unknown} {
			requireUnauthenticatedSession(t, s, cookie)
		}
		_, err = s.Svc.Login(s.Ctx(), service.LoginIn{Password: auth.FormatToken(1, mintSecret(t)), OldCookie: sess.CookieValue})
		testutil.RequireCode(t, err, domain.CodePasswordInvalid)
		if _, err := s.Svc.Authenticate(s.Ctx(), sess.CookieValue); err != nil {
			t.Fatal("failed password verification invalidated the existing session", err)
		}
		rotated := loginSession(t, s, sess.CookieValue)
		if rotated.CookieValue == sess.CookieValue || rotated.CSRFToken == sess.CSRFToken {
			t.Fatal("login did not rotate credentials")
		}
		requireUnauthenticatedSession(t, s, sess.CookieValue)
		if _, err := store.GetSession(s.Ctx(), s.Store.Reader, row.IDHash); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("old session row survived regeneration", err)
		}
		for _, cookie := range []string{rotated.CookieValue, rotated.CookieValue, unknown, "garbage", ""} {
			if err := s.Svc.Logout(s.Ctx(), cookie); err != nil {
				t.Fatal(err)
			}
		}
		requireUnauthenticatedSession(t, s, rotated.CookieValue)
		if err := s.Svc.FlushUsage(s.Ctx()); err != nil {
			t.Fatal(err)
		}
		requireSQLCount(t, s, `SELECT COUNT(*) FROM web_sessions`, 0)
		requireSQLCount(t, s, `SELECT COUNT(*) FROM ops`, 0)
		requireSQLCount(t, s, `SELECT COUNT(*) FROM events`, 0)
		requireSQLCount(t, s, `SELECT COUNT(*) FROM idempotency`, 0)
	})
	t.Run("idle boundary", func(t *testing.T) {
		s := testutil.NewScenario(t)
		sess := loginSession(t, s, "")
		s.Clock.Advance(7 * 24 * time.Hour)
		requireUnauthenticatedSession(t, s, sess.CookieValue)
	})
}

func TestSessionActivityRenewsIdle(t *testing.T) {
	for _, flush := range []bool{true, false} {
		t.Run(map[bool]string{true: "flushed", false: "in memory"}[flush], func(t *testing.T) {
			s := testutil.NewScenario(t)
			sess := loginSession(t, s, "")
			for day := 3; day <= 27; day += 3 {
				s.Clock.Advance(72 * time.Hour)
				if _, err := s.Svc.Authenticate(s.Ctx(), sess.CookieValue); err != nil {
					t.Fatalf("day %d: %v", day, err)
				}
				if flush {
					if err := s.Svc.FlushUsage(s.Ctx()); err != nil {
						t.Fatal(err)
					}
				}
			}
			s.Clock.Advance(72*time.Hour - time.Millisecond)
			if _, err := s.Svc.Authenticate(s.Ctx(), sess.CookieValue); err != nil {
				t.Fatal("session expired before absolute boundary", err)
			}
			s.Clock.Advance(time.Millisecond)
			requireUnauthenticatedSession(t, s, sess.CookieValue)
		})
	}
}

func TestFlushUsageAfterDrain(t *testing.T) {
	s := testutil.NewScenario(t)
	token := createTestToken(t, s, "usage")
	sess := loginSession(t, s, "")
	created := sessionRow(t, s, sess.CookieValue).LastSeenAt
	s.Clock.Advance(time.Minute)
	used := s.Clock.Now().Truncate(time.Millisecond)
	if _, err := s.Svc.AuthenticateToken(s.Ctx(), token.Secret, false, domain.ChanAPI, "remote"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Svc.Authenticate(s.Ctx(), sess.CookieValue); err != nil {
		t.Fatal(err)
	}
	if !sessionRow(t, s, sess.CookieValue).LastSeenAt.Equal(created) {
		t.Fatal("authentication wrote session usage immediately")
	}
	s.Svc.BeginShutdown()
	if err := s.Svc.Drain(s.Ctx()); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.Svc.FlushUsage(s.Ctx()); err != nil {
			t.Fatal(err)
		}
	}
	row, err := store.GetToken(s.Ctx(), s.Store.Reader, token.Token.ID)
	if err != nil || !row.LastUsedAt.Valid || !row.LastUsedAt.V.Equal(used) || !sessionRow(t, s, sess.CookieValue).LastSeenAt.Equal(used) {
		t.Fatal("final flush did not persist token and session usage", err)
	}
	if err := s.Store.CheckpointTruncate(s.Ctx()); err != nil {
		t.Fatal(err)
	}
	_, err = s.Svc.Login(s.Ctx(), service.LoginIn{Password: testutil.Password})
	testutil.RequireCode(t, err, domain.CodeBusy)
	testutil.RequireCode(t, s.Svc.Logout(s.Ctx(), sess.CookieValue), domain.CodeBusy)
	_, err = s.Svc.ExpireSessions(s.Ctx())
	testutil.RequireCode(t, err, domain.CodeBusy)
	requireSQLCount(t, s, `SELECT COUNT(*) FROM ops`, 1)
}

func TestExpireSessions(t *testing.T) {
	s := testutil.NewScenario(t)
	idle := loginSession(t, s, "")
	active := loginSession(t, s, "")
	created := s.Clock.Now()
	for _, day := range []int{3, 6, 7, 9, 12, 15, 18, 21, 24, 27, 30} {
		s.Clock.Set(created.Add(time.Duration(day) * 24 * time.Hour))
		if day < 30 {
			if _, err := s.Svc.Authenticate(s.Ctx(), active.CookieValue); err != nil {
				t.Fatal(err)
			}
			if err := s.Svc.FlushUsage(s.Ctx()); err != nil {
				t.Fatal(err)
			}
		}
		want := 0
		if day == 7 || day == 30 {
			want = 1
		}
		if n, err := s.Svc.ExpireSessions(s.Ctx()); err != nil || n != want {
			t.Fatalf("day %d: expired %d, want %d: %v", day, n, want, err)
		}
	}
	requireUnauthenticatedSession(t, s, idle.CookieValue)
	requireUnauthenticatedSession(t, s, active.CookieValue)
	requireSQLCount(t, s, `SELECT COUNT(*) FROM web_sessions`, 0)
	requireSQLCount(t, s, `SELECT COUNT(*) FROM ops`, 0)
}

func TestExpireSessionsPendingActivity(t *testing.T) {
	s := testutil.NewScenario(t)
	active := loginSession(t, s, "")
	idle := loginSession(t, s, "")
	created := s.Clock.Now()
	s.Clock.Advance(service.SessionIdle - time.Millisecond)
	if err := s.Svc.FlushUsage(s.Ctx()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Svc.Authenticate(s.Ctx(), active.CookieValue); err != nil {
		t.Fatal(err)
	}
	used := s.Clock.Now()
	s.Clock.Advance(time.Millisecond)
	if n, err := s.Svc.ExpireSessions(s.Ctx()); err != nil || n != 1 {
		t.Fatalf("expiry after activity between flush and expiry = %d, %v; want only the idle session deleted", n, err)
	}
	if !sessionRow(t, s, active.CookieValue).LastSeenAt.Equal(used) {
		t.Fatal("expiry did not persist pending session activity")
	}
	requireUnauthenticatedSession(t, s, idle.CookieValue)
	if _, err := s.Svc.Authenticate(s.Ctx(), active.CookieValue); err != nil {
		t.Fatal("recently active session did not survive expiry", err)
	}
	for day := 10; day <= 28; day += 3 {
		s.Clock.Set(created.Add(time.Duration(day) * 24 * time.Hour))
		if _, err := s.Svc.Authenticate(s.Ctx(), active.CookieValue); err != nil {
			t.Fatal(err)
		}
	}
	s.Clock.Set(created.Add(service.SessionMax))
	if n, err := s.Svc.ExpireSessions(s.Ctx()); err != nil || n != 1 {
		t.Fatalf("absolute expiry with pending activity = %d, %v; want 1", n, err)
	}
	requireUnauthenticatedSession(t, s, active.CookieValue)
	requireSQLCount(t, s, `SELECT COUNT(*) FROM ops`, 0)
}

func TestExpireSessionsRollback(t *testing.T) {
	for _, tc := range []struct{ name, trigger string }{
		{"touch", `CREATE TEMP TRIGGER fail_session_expiry BEFORE UPDATE ON web_sessions BEGIN SELECT RAISE(ABORT, 'injected failure'); END`},
		{"delete", `CREATE TEMP TRIGGER fail_session_expiry BEFORE DELETE ON web_sessions BEGIN SELECT RAISE(ABORT, 'injected failure'); END`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testutil.NewScenario(t)
			active := loginSession(t, s, "")
			idle := loginSession(t, s, "")
			created := s.Clock.Now()
			s.Clock.Advance(6 * 24 * time.Hour)
			if _, err := s.Svc.Authenticate(s.Ctx(), active.CookieValue); err != nil {
				t.Fatal(err)
			}
			used := s.Clock.Now()
			s.Clock.Advance(24 * time.Hour)
			if _, err := s.Store.Writer.ExecContext(s.Ctx(), tc.trigger); err != nil {
				t.Fatal(err)
			}
			n, err := s.Svc.ExpireSessions(s.Ctx())
			testutil.RequireCode(t, err, domain.CodeInternal)
			if n != 0 || !sessionRow(t, s, active.CookieValue).LastSeenAt.Equal(created) {
				t.Fatal("failed expiry returned a count or committed a pending touch")
			}
			requireSQLCount(t, s, `SELECT COUNT(*) FROM web_sessions`, 2)
			if _, err := s.Store.Writer.ExecContext(s.Ctx(), `DROP TRIGGER fail_session_expiry`); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(s.Ctx())
			cancel()
			if n, err := s.Svc.ExpireSessions(ctx); n != 0 || !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled expiry = %d, %v", n, err)
			}
			if n, err := s.Svc.ExpireSessions(s.Ctx()); err != nil || n != 1 {
				t.Fatalf("expiry retry = %d, %v; want only the idle session deleted", n, err)
			}
			if !sessionRow(t, s, active.CookieValue).LastSeenAt.Equal(used) {
				t.Fatal("failed expiry discarded queued activity")
			}
			requireUnauthenticatedSession(t, s, idle.CookieValue)
			if _, err := s.Svc.Authenticate(s.Ctx(), active.CookieValue); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionPasswordRevocation(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(map[bool]string{false: "change", true: "offline reset"}[reset], func(t *testing.T) {
			s := testutil.NewScenario(t)
			sess := loginSession(t, s, "")
			if _, err := s.Svc.Authenticate(s.Ctx(), sess.CookieValue); err != nil {
				t.Fatal(err)
			}
			if reset {
				if _, err := s.Svc.ResetPasswordOffline(s.Ctx(), service.ResetPasswordIn{NewPassword: testutil.Password, OwnerSecret: mintSecret(t)}); err != nil {
					t.Fatal(err)
				}
			} else if err := s.Svc.ChangePassword(s.Ctx(), s.Meta(), service.ChangePasswordIn{Current: testutil.Password, New: testutil.Password}); err != nil {
				t.Fatal(err)
			}
			requireUnauthenticatedSession(t, s, sess.CookieValue)
			if err := s.Svc.FlushUsage(s.Ctx()); err != nil {
				t.Fatal(err)
			}
			requireSQLCount(t, s, `SELECT COUNT(*) FROM web_sessions`, 0)
		})
	}
}

func TestLoginSessionRollback(t *testing.T) {
	s := testutil.NewScenario(t)
	sess := loginSession(t, s, "")
	if _, err := s.Store.Writer.ExecContext(s.Ctx(), `CREATE TEMP TRIGGER fail_session_insert BEFORE INSERT ON web_sessions BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	got, err := s.Svc.Login(s.Ctx(), service.LoginIn{Password: testutil.Password, OldCookie: sess.CookieValue})
	testutil.RequireCode(t, err, domain.CodeInternal)
	if got != (service.Session{}) {
		t.Fatal("rolled back login leaked credentials")
	}
	if _, err := s.Svc.Authenticate(s.Ctx(), sess.CookieValue); err != nil {
		t.Fatal("failed login deleted the existing session", err)
	}
	requireSQLCount(t, s, `SELECT COUNT(*) FROM web_sessions`, 1)
}

func TestFlushUsageRollback(t *testing.T) {
	s := testutil.NewScenario(t)
	token := createTestToken(t, s, "retry")
	sess := loginSession(t, s, "")
	created := s.Clock.Now()
	s.Clock.Advance(time.Hour)
	used := s.Clock.Now()
	if _, err := s.Svc.AuthenticateToken(s.Ctx(), token.Secret, false, domain.ChanAPI, "remote"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Svc.Authenticate(s.Ctx(), sess.CookieValue); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Writer.ExecContext(s.Ctx(), `CREATE TEMP TRIGGER fail_session_touch BEFORE UPDATE ON web_sessions BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	testutil.RequireCode(t, s.Svc.FlushUsage(s.Ctx()), domain.CodeInternal)
	row, err := store.GetToken(s.Ctx(), s.Store.Reader, token.Token.ID)
	if err != nil || row.LastUsedAt.Valid || !sessionRow(t, s, sess.CookieValue).LastSeenAt.Equal(created) {
		t.Fatal("failed flush committed partial usage", err)
	}
	if _, err := s.Store.Writer.ExecContext(s.Ctx(), `DROP TRIGGER fail_session_touch`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(s.Ctx())
	cancel()
	if err := s.Svc.FlushUsage(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled flush lost its cause", err)
	}
	if err := s.Svc.FlushUsage(s.Ctx()); err != nil {
		t.Fatal(err)
	}
	row, err = store.GetToken(s.Ctx(), s.Store.Reader, token.Token.ID)
	if err != nil || !row.LastUsedAt.Valid || !row.LastUsedAt.V.Equal(used) || !sessionRow(t, s, sess.CookieValue).LastSeenAt.Equal(used) {
		t.Fatal("retry lost queued usage", err)
	}
}

func TestSessionConcurrentUsage(t *testing.T) {
	s := testutil.NewScenario(t)
	sess := loginSession(t, s, "")
	token := createTestToken(t, s, "concurrent-usage")
	s.Clock.Advance(6 * 24 * time.Hour)
	if _, err := s.Svc.Authenticate(s.Ctx(), sess.CookieValue); err != nil {
		t.Fatal(err)
	}
	s.Clock.Advance(2 * 24 * time.Hour)
	used := s.Clock.Now()
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := s.Svc.Authenticate(s.Ctx(), sess.CookieValue); err != nil {
				t.Error(err)
			}
			if _, err := s.Svc.AuthenticateToken(s.Ctx(), token.Secret, false, domain.ChanAPI, "remote"); err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			if err := s.Svc.FlushUsage(s.Ctx()); err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			if n, err := s.Svc.ExpireSessions(s.Ctx()); err != nil || n != 0 {
				t.Errorf("concurrent expiry = %d, %v; want no active sessions deleted", n, err)
			}
		})
	}
	wg.Wait()
	s.Clock.Set(used.Add(-time.Hour))
	if _, err := s.Svc.Authenticate(s.Ctx(), sess.CookieValue); err != nil {
		t.Fatal(err)
	}
	if err := s.Svc.FlushUsage(s.Ctx()); err != nil {
		t.Fatal(err)
	}
	row, err := store.GetToken(s.Ctx(), s.Store.Reader, token.Token.ID)
	if err != nil || !row.LastUsedAt.Valid || !row.LastUsedAt.V.Equal(used) || !sessionRow(t, s, sess.CookieValue).LastSeenAt.Equal(used) {
		t.Fatal("concurrent flush lost usage or moved timestamps backward", err)
	}
}
