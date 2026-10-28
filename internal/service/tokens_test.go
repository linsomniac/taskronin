package service_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"taskronin/internal/auth"
	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/store"
	"taskronin/internal/testutil"
)

func mintSecret(t *testing.T) [32]byte {
	t.Helper()
	raw, err := auth.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func createTestToken(t *testing.T, s *testutil.Scenario, name string) service.TokenCreated {
	t.Helper()
	created, err := s.Svc.CreateToken(s.Ctx(), s.Meta(), service.CreateTokenIn{Name: name, Scopes: domain.AllScopes})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func ownerBearer(t *testing.T, s *testutil.Scenario) (domain.TokenID, string) {
	t.Helper()
	raw := mintSecret(t)
	id, err := s.Svc.ResetPasswordOffline(s.Ctx(), service.ResetPasswordIn{NewPassword: testutil.Password, OwnerSecret: raw})
	if err != nil {
		t.Fatal(err)
	}
	return id, auth.FormatToken(id, raw)
}

func requireTokenEvent(t *testing.T, e domain.Event, token domain.Token, action domain.Action) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(e.After, &got); err != nil {
		t.Fatal(err)
	}
	var expires any
	if token.ExpiresAt.Valid {
		expires = float64(token.ExpiresAt.V.UnixMilli())
	}
	want := map[string]any{"name": token.Name, "scopes": token.Scopes.String(), "expires_at": expires}
	if e.Entity != domain.EntToken || e.EntityID != int64(token.ID) || e.Action != action ||
		e.TaskID.Valid || e.Derived || len(e.Before) != 0 || !reflect.DeepEqual(got, want) {
		t.Fatalf("token event does not match approved metadata: %+v", e)
	}
}

func TestSCP04(t *testing.T) {
	s := testutil.NewScenario(t)
	id, bearer := ownerBearer(t, s)
	a, err := s.Svc.AuthenticateToken(s.Ctx(), bearer, false, domain.ChanAPI, "remote")
	testutil.RequireCode(t, err, domain.CodeUnauthenticated)
	if a != (domain.Actor{}) {
		t.Fatal("failed authentication returned an actor")
	}
	a, err = s.Svc.AuthenticateToken(s.Ctx(), bearer, true, domain.ChanCLI, "socket")
	if err != nil {
		t.Fatal(err)
	}
	want := domain.Actor{Kind: domain.ActorOwner, TokenID: id, Scopes: domain.AllScopes, Channel: domain.ChanCLI, RemoteKey: "socket"}
	if a != want {
		t.Fatalf("owner actor = %+v, want %+v", a, want)
	}
}

func TestSCP07(t *testing.T) {
	s := testutil.NewScenario(t)
	token := createTestToken(t, s, "web-target")
	web := testutil.Owner()
	web.Channel, web.RemoteKey = domain.ChanWeb, "192.0.2.1"
	s = s.As(web)
	_, err := s.Svc.CreateToken(s.Ctx(), s.Meta(), service.CreateTokenIn{Name: "web", Scopes: domain.ScopeRead})
	testutil.RequireCode(t, err, domain.CodeForbidden)
	_, err = s.Svc.RevokeToken(s.Ctx(), s.Meta(), service.RevokeTokenIn{ID: token.Token.ID})
	testutil.RequireCode(t, err, domain.CodeForbidden)
	_, err = s.Svc.ListTokens(s.Ctx(), web)
	testutil.RequireCode(t, err, domain.CodeForbidden)
	daily := 3
	_, err = s.Svc.UpdateSettings(s.Ctx(), s.Meta(), service.UpdateSettingsIn{KeepDaily: &daily})
	testutil.RequireCode(t, err, domain.CodePasswordRequired)
	view, err := s.Svc.UpdateSettings(s.Ctx(), s.Meta(), service.UpdateSettingsIn{KeepDaily: &daily, Password: testutil.Password})
	if err != nil || view.KeepDaily != daily {
		t.Fatalf("web retention update = %+v, %v", view, err)
	}
}

func TestSCP12(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(fmt.Sprint("revoked=", revoked), func(t *testing.T) {
			s := testutil.NewScenario(t)
			expires := s.Clock.Now().Add(time.Minute)
			token, err := s.Svc.CreateToken(s.Ctx(), s.Meta(), service.CreateTokenIn{Name: "limited", Scopes: domain.ScopeRead,
				ExpiresAt: sql.Null[time.Time]{V: expires, Valid: true}})
			if err != nil {
				t.Fatal(err)
			}
			if revoked {
				if _, err := s.Svc.RevokeToken(s.Ctx(), s.Meta(), service.RevokeTokenIn{ID: token.Token.ID}); err != nil {
					t.Fatal(err)
				}
			} else {
				s.Clock.Set(expires.Add(-time.Millisecond))
				if _, err := s.Svc.AuthenticateToken(s.Ctx(), token.Secret, false, domain.ChanAPI, "remote"); err != nil {
					t.Fatal(err)
				}
				s.Clock.Set(expires)
			}
			_, err = s.Svc.AuthenticateToken(s.Ctx(), token.Secret, false, domain.ChanAPI, "remote")
			testutil.RequireCode(t, err, domain.CodeUnauthenticated)
		})
	}
}

func TestSCP13(t *testing.T) {
	s := testutil.NewScenario(t)
	_, bearer := ownerBearer(t, s)
	_, err := s.Svc.AuthenticateToken(s.Ctx(), bearer, true, domain.ChanMCP, "socket")
	testutil.RequireCode(t, err, domain.CodeForbidden)
}

func TestCreateToken(t *testing.T) {
	s := testutil.NewScenario(t)
	expires := s.Clock.Now().Add(time.Hour)
	in := service.CreateTokenIn{Name: "Agent-1._", Scopes: domain.ScopeRead | domain.ScopeCapture,
		ExpiresAt: sql.Null[time.Time]{V: expires, Valid: true}, MutationsPerHour: 60}
	m := s.Meta()
	created, err := s.Svc.CreateToken(s.Ctx(), m, in)
	if err != nil {
		t.Fatal(err)
	}
	id, raw, err := auth.ParseToken(created.Secret)
	if err != nil || id != created.Token.ID {
		t.Fatal("created secret is not a canonical token")
	}
	row, err := store.GetToken(s.Ctx(), s.Store.Reader, id)
	if err != nil {
		t.Fatal(err)
	}
	if row.SecretHash != auth.HashSecret(raw) || row.IsOwner || row.CreatedAt != s.Clock.Now().UTC() {
		t.Fatal("token row does not match creation")
	}
	if created.Token.SecretHash != ([32]byte{}) {
		t.Fatal("creation exposed the secret hash")
	}
	a, err := s.Svc.AuthenticateToken(s.Ctx(), created.Secret, false, domain.ChanMCP, "192.0.2.2")
	if err != nil {
		t.Fatal(err)
	}
	want := domain.Actor{Kind: domain.ActorToken, TokenID: id, TokenName: in.Name, Scopes: in.Scopes,
		Channel: domain.ChanMCP, MutationsPerHour: 60, RemoteKey: "192.0.2.2"}
	if a != want {
		t.Fatalf("token actor = %+v, want %+v", a, want)
	}
	op := latestOp(t, s)
	if op.Kind != domain.OpTokenCreate || op.Revertible || op.Scopes != domain.ScopeAdmin {
		t.Fatalf("create op = %+v", op)
	}
	events := s.Events(op.ID)
	if len(events) != 1 {
		t.Fatalf("create events = %d", len(events))
	}
	requireTokenEvent(t, events[0], row, domain.ActInsert)
	_, err = s.Svc.CreateToken(s.Ctx(), m, in)
	testutil.RequireCode(t, err, domain.CodeConflict)
	rows, err := s.Svc.ListTokens(s.Ctx(), s.Actor)
	if err != nil || len(rows) != 2 {
		t.Fatalf("list tokens count = %d, error = %v", len(rows), err)
	}
	for _, token := range rows {
		if token.SecretHash != ([32]byte{}) {
			t.Fatal("listing exposed a secret hash")
		}
	}
	requireSQLCount(t, s, `SELECT COUNT(*) FROM idempotency`, 0)
	requireSQLCount(t, s, `SELECT COUNT(*) FROM undo_stack`, 0)
}

func TestCreateTokenValidation(t *testing.T) {
	s := testutil.NewScenario(t)
	for _, name := range []string{"", "owner.reserved", "a b", "a\n", "\ufeffagent", "é", "a\u202eb", string([]byte{0xff}), strings.Repeat("a", 65)} {
		t.Run(fmt.Sprintf("name=%q", name), func(t *testing.T) {
			_, err := s.Svc.CreateToken(s.Ctx(), s.Meta(), service.CreateTokenIn{Name: name, Scopes: domain.ScopeRead})
			testutil.RequireCode(t, err, domain.CodeValidation)
		})
	}
	for _, in := range []service.CreateTokenIn{
		{Name: "zero"}, {Name: "unknown", Scopes: domain.Scopes(128)},
		{Name: "mixed", Scopes: domain.ScopeRead | domain.Scopes(128)},
		{Name: "negative", Scopes: domain.ScopeRead, MutationsPerHour: -1},
		{Name: "expired", Scopes: domain.ScopeRead, ExpiresAt: sql.Null[time.Time]{V: s.Clock.Now(), Valid: true}},
		{Name: "past", Scopes: domain.ScopeRead, ExpiresAt: sql.Null[time.Time]{V: s.Clock.Now().Add(-time.Hour), Valid: true}},
	} {
		_, err := s.Svc.CreateToken(s.Ctx(), s.Meta(), in)
		testutil.RequireCode(t, err, domain.CodeValidation)
	}
	requireSQLCount(t, s, `SELECT COUNT(*) FROM api_tokens`, 1)
	requireSQLCount(t, s, `SELECT COUNT(*) FROM ops`, 0)
	createTestToken(t, s, strings.Repeat("a", 64))
}

func TestAuthenticateTokenFailuresAndUsage(t *testing.T) {
	s := testutil.NewScenario(t)
	created := createTestToken(t, s, "usage")
	for _, bearer := range []string{"", "garbage", auth.FormatToken(9223372036854775807, mintSecret(t)), auth.FormatToken(created.Token.ID, mintSecret(t))} {
		a, err := s.Svc.AuthenticateToken(s.Ctx(), bearer, true, domain.ChanAPI, "socket")
		testutil.RequireCode(t, err, domain.CodeUnauthenticated)
		if a != (domain.Actor{}) || len(s.Svc.TokenUsageForTest()) != 0 {
			t.Fatal("failed authentication returned authority or recorded use")
		}
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := s.Svc.AuthenticateToken(s.Ctx(), created.Secret, false, domain.ChanAPI, "remote"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	at := s.Clock.Now().Truncate(time.Millisecond)
	s.Clock.Set(at.Add(-time.Minute))
	if _, err := s.Svc.AuthenticateToken(s.Ctx(), created.Secret, false, domain.ChanAPI, "remote"); err != nil {
		t.Fatal(err)
	}
	used := s.Svc.TokenUsageForTest()
	if len(used) != 1 || !used[created.Token.ID].Equal(at) {
		t.Fatalf("coalesced use = %v", used)
	}
	row, err := store.GetToken(s.Ctx(), s.Store.Reader, created.Token.ID)
	if err != nil || row.LastUsedAt.Valid {
		t.Fatal("authentication must only queue usage in memory")
	}
}

func TestRevokeToken(t *testing.T) {
	s := testutil.NewScenario(t)
	created := createTestToken(t, s, "revoke")
	m := s.MetaWith(domain.NewOpID().String(), `{"id":2}`)
	row, err := s.Svc.RevokeToken(s.Ctx(), m, service.RevokeTokenIn{ID: created.Token.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !row.RevokedAt.Valid || !row.RevokedAt.V.Equal(s.Clock.Now()) || row.SecretHash != ([32]byte{}) {
		t.Fatal("revoked token representation is incorrect or contains its hash")
	}
	op := latestOp(t, s)
	if op.Kind != domain.OpTokenRevoke || op.Revertible || op.Scopes != domain.ScopeAdmin {
		t.Fatalf("revoke op = %+v", op)
	}
	events := s.Events(op.ID)
	if len(events) != 1 {
		t.Fatalf("revoke events = %d", len(events))
	}
	requireTokenEvent(t, events[0], row, domain.ActRevoke)
	for _, meta := range []service.Meta{m, s.Meta()} {
		replay, err := s.Svc.RevokeToken(s.Ctx(), meta, service.RevokeTokenIn{ID: row.ID})
		if err != nil || replay != row {
			t.Fatalf("repeated revoke returned a different token or error: %v", err)
		}
		if latestOp(t, s).ID != op.ID {
			t.Fatal("repeated revoke wrote an op")
		}
	}
	_, err = s.Svc.CreateToken(s.Ctx(), s.Meta(), service.CreateTokenIn{Name: created.Token.Name, Scopes: domain.ScopeRead})
	testutil.RequireCode(t, err, domain.CodeConflict)
	_, err = s.Svc.RevokeToken(s.Ctx(), s.Meta(), service.RevokeTokenIn{ID: 1})
	testutil.RequireCode(t, err, domain.CodeConflict)
	_, err = s.Svc.RevokeToken(s.Ctx(), s.Meta(), service.RevokeTokenIn{ID: 999})
	testutil.RequireCode(t, err, domain.CodeNotFound)
	m.Idem.Key = ""
	_, err = s.Svc.RevokeToken(s.Ctx(), m, service.RevokeTokenIn{ID: row.ID})
	testutil.RequireCode(t, err, domain.CodeIdemRequired)
}

func TestTokenAdminScopes(t *testing.T) {
	s := testutil.NewScenario(t)
	created := createTestToken(t, s, "target")
	s = s.As(testutil.TokenActor("reader", domain.ScopeRead))
	_, err := s.Svc.CreateToken(s.Ctx(), s.Meta(), service.CreateTokenIn{Name: "forbidden", Scopes: domain.ScopeRead})
	testutil.RequireCode(t, err, domain.CodeForbidden)
	_, err = s.Svc.ListTokens(s.Ctx(), s.Actor)
	testutil.RequireCode(t, err, domain.CodeForbidden)
	_, err = s.Svc.RevokeToken(s.Ctx(), s.Meta(), service.RevokeTokenIn{ID: created.Token.ID})
	testutil.RequireCode(t, err, domain.CodeForbidden)
	s = s.As(testutil.TokenActor("admin", domain.ScopeAdmin))
	created = createTestToken(t, s, "admin-created")
	if _, err := s.Svc.ListTokens(s.Ctx(), s.Actor); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Svc.RevokeToken(s.Ctx(), s.Meta(), service.RevokeTokenIn{ID: created.Token.ID}); err != nil {
		t.Fatal(err)
	}
}

func TestRevokeTokenDetachedContext(t *testing.T) {
	s := testutil.NewScenario(t)
	created := createTestToken(t, s, "detached")
	ctx, cancel := context.WithCancel(s.Ctx())
	cancel()
	m := s.Meta()
	for range 2 {
		row, err := s.Svc.RevokeToken(ctx, m, service.RevokeTokenIn{ID: created.Token.ID})
		if err != nil || row.ID != created.Token.ID || !row.RevokedAt.Valid {
			t.Fatalf("detached revoke/replay failed to return its committed result: %v", err)
		}
	}
}

type tokenReadContext struct {
	current, operation context.Context
}

// tokenReadConnector pauses the real SQLite token query without production hooks.
type tokenReadConnector struct {
	driver  driver.Driver
	dsn     string
	started chan<- tokenReadContext
	release <-chan struct{}
}

func (c *tokenReadConnector) Driver() driver.Driver { return c.driver }

func (c *tokenReadConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &tokenReadConn{Conn: conn, observer: c}, nil
}

type tokenReadConn struct {
	driver.Conn
	observer *tokenReadConnector
	opCtx    context.Context
}

func (c *tokenReadConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if c.opCtx == nil {
		c.opCtx = ctx
	}
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

func (c *tokenReadConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "FROM api_tokens") {
		c.observer.started <- tokenReadContext{current: ctx, operation: c.opCtx}
		select {
		case <-c.observer.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func TestRevokeTokenShutdownResult(t *testing.T) {
	for _, mode := range []string{"first", "no change", "replay", "no-change replay"} {
		t.Run(mode, func(t *testing.T) {
			s := testutil.NewScenario(t)
			created := createTestToken(t, s, "shutdown")
			in, m := service.RevokeTokenIn{ID: created.Token.ID}, s.Meta()
			if mode == "no change" || mode == "no-change replay" {
				if _, err := s.Svc.RevokeToken(s.Ctx(), s.Meta(), in); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "replay" || mode == "no-change replay" {
				if _, err := s.Svc.RevokeToken(s.Ctx(), m, in); err != nil {
					t.Fatal(err)
				}
			}

			started, release := make(chan tokenReadContext, 1), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			originalReader := s.Store.Reader
			reader := sql.OpenDB(&tokenReadConnector{driver: originalReader.Driver(),
				dsn: store.DSN(s.Store.Path, store.PoolReader), started: started, release: release})
			reader.SetMaxOpenConns(1)
			s.Store.Reader = reader
			t.Cleanup(func() {
				s.Store.Reader = originalReader
				if err := reader.Close(); err != nil {
					t.Error(err)
				}
			})
			var got domain.Token
			var revokeErr error
			done := make(chan struct{})
			go func() {
				got, revokeErr = s.Svc.RevokeToken(s.Ctx(), m, in)
				close(done)
			}()
			t.Cleanup(func() { unblock(); <-done })

			var read tokenReadContext
			select {
			case read = <-started:
			case <-done:
				t.Fatalf("revocation returned before token result loading: %v", revokeErr)
			case <-s.Ctx().Done():
				t.Fatal(s.Ctx().Err())
			}
			// The write has committed; only loading its representation is paused.
			persisted, err := store.GetToken(s.Ctx(), s.Store.Writer, created.Token.ID)
			if err != nil || !persisted.RevokedAt.Valid {
				t.Fatalf("revocation has not committed: %v", err)
			}
			if read.operation == nil {
				t.Fatal("runner did not start a result transaction")
			}
			// AIDEV-NOTE: Transactions now begin on context.WithoutCancel (so Rollback is
			// synchronous, see beginTx); the runner's deadline is carried by the query ctx.
			resultDeadline, resultOK := read.current.Deadline()
			if !resultOK || time.Until(resultDeadline) > service.OpBudget || read.current.Err() != nil ||
				read.operation.Err() != nil {
				t.Error("token result loading lost the runner's live context or original deadline")
			}
			s.Svc.BeginShutdown()
			drainCtx, cancel := context.WithTimeout(s.Ctx(), 50*time.Millisecond)
			defer cancel()
			if err := s.Svc.Drain(drainCtx); !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("Drain finished before token result loading: %v", err)
			}
			unblock()
			if err := s.Svc.Drain(s.Ctx()); err != nil {
				t.Fatal(err)
			}
			if err := reader.Close(); err != nil {
				t.Fatal(err)
			}
			<-done
			persisted.SecretHash = [32]byte{}
			if revokeErr != nil || got != persisted {
				t.Fatalf("revocation did not return its redacted current representation: %v", revokeErr)
			}
		})
	}
}
