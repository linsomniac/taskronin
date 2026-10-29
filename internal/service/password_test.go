package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"taskronin/internal/auth"
	"taskronin/internal/clock"
	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/store"
	"taskronin/internal/testutil"
)

func TestChangePasswordRejectsBeforeVerification(t *testing.T) {
	for _, state := range []string{"missing generation", "stale generation", "stopping"} {
		t.Run(state, func(t *testing.T) {
			s := testutil.NewScenario(t)
			m, want := s.Meta(), domain.CodeBusy
			switch state {
			case "missing generation":
				m.Gen, want = "", domain.CodePreconditionReq
			case "stale generation":
				m.Gen, want = domain.NewGeneration(), domain.CodeGenerationMismatch
			case "stopping":
				s.Svc.BeginShutdown()
			}
			// An invalid password must not win over admission/generation checks.
			err := s.Svc.ChangePassword(t.Context(), m, service.ChangePasswordIn{Current: "invalid", New: testutil.Password})
			testutil.RequireCode(t, err, want)
		})
	}
}

type passwordWaitClock struct {
	clock.Clock
	armed chan struct{}
}

func (c passwordWaitClock) After(d time.Duration) <-chan time.Time {
	ch := c.Clock.After(d)
	c.armed <- struct{}{}
	return ch
}

func TestShutdownDrainsPasswordVerification(t *testing.T) {
	armed := make(chan struct{}, 1)
	s := testutil.NewScenario(t, func(o *service.Options) {
		o.Passwords = auth.NewPasswordGate(passwordWaitClock{Clock: o.Clock, armed: armed})
	})
	exhaustGate(t, s, "socket")
	done := make(chan error, 1)
	go func() {
		done <- s.Svc.ChangePassword(context.Background(), s.Meta(), service.ChangePasswordIn{Current: testutil.Password, New: auth.FormatToken(1, mintSecret(t))})
	}()
	select {
	case <-armed:
	case <-time.After(5 * time.Second):
		t.Fatal("password did not reach gate")
	}
	s.Svc.BeginShutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := s.Svc.Drain(ctx)
	// Unblock on failure as well, so the fixture always joins accepted work.
	s.Clock.Advance(10 * time.Second)
	if result := <-done; result != nil {
		t.Errorf("accepted password change: %v", result)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Drain skipped password verification: %v", err)
	}
	if err := s.Svc.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func requireSQLCount(t *testing.T, s *testutil.Scenario, query string, want int) {
	t.Helper()
	var got int
	if err := s.Store.Reader.QueryRowContext(s.Ctx(), query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s = %d, want %d", query, got, want)
	}
}

func authState(t *testing.T, s *testutil.Scenario) store.AuthState {
	t.Helper()
	a, err := store.GetAuthState(s.Ctx(), s.Store.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func authFixture(t *testing.T, s *testutil.Scenario, disabled bool) {
	t.Helper()
	raw, csrf := mintSecret(t), mintSecret(t)
	// AIDEV-NOTE: F14 fixture shortcut: Login/session creation and disabling
	// web login through restore are not available until Tasks 35b and Plan B.
	s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), nil, func(q store.Q) error {
		a, err := store.GetAuthState(s.Ctx(), q)
		if err != nil {
			return err
		}
		a.WebLoginDisabled = disabled
		if err := store.PutAuthState(s.Ctx(), q, a); err != nil {
			return err
		}
		return store.InsertSession(s.Ctx(), q, store.Session{IDHash: auth.HashSecret(raw), CSRFSecret: csrf,
			Remote: "192.0.2.1", CreatedAt: s.Clock.Now(), LastSeenAt: s.Clock.Now(), ExpiresAt: s.Clock.Now().Add(24 * time.Hour)})
	}))
}

func requirePasswordEvent(t *testing.T, e domain.Event) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(e.After, &got); err != nil {
		t.Fatal(err)
	}
	if e.Entity != domain.EntAuth || e.Action != domain.ActUpdate || e.TaskID.Valid || e.Derived || len(e.Before) != 0 ||
		!reflect.DeepEqual(got, map[string]any{"what": "password_changed"}) {
		t.Fatalf("password event does not match content-free contract: %+v", e)
	}
}

func TestUpdateSettings(t *testing.T) {
	s := testutil.NewScenario(t)
	daily, weekly := 9, 5
	m := s.Meta()
	m.Idem.Body = []byte(testutil.Password)
	v, err := s.Svc.UpdateSettings(s.Ctx(), m, service.UpdateSettingsIn{KeepDaily: &daily, KeepWeekly: &weekly})
	if err != nil || v != (service.SettingsView{KeepDaily: daily, KeepWeekly: weekly}) {
		t.Fatalf("raise = %+v, %v", v, err)
	}
	op := latestOp(t, s)
	if op.Kind != domain.OpSettingsUpdate || op.Revertible || op.Scopes != domain.ScopeAdmin {
		t.Fatalf("raise op = %+v", op)
	}
	events := s.Events(op.ID)
	if len(events) != 2 {
		t.Fatalf("setting events = %d", len(events))
	}
	for i, key := range []string{"backup_keep_daily", "backup_keep_weekly"} {
		var before, after map[string]string
		if err := json.Unmarshal(events[i].Before, &before); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(events[i].After, &after); err != nil {
			t.Fatal(err)
		}
		if events[i].Entity != domain.EntSetting || events[i].Action != domain.ActUpdate || events[i].EntityID != 0 ||
			!reflect.DeepEqual(before, map[string]string{"key": key, "value": []string{"7", "4"}[i]}) ||
			!reflect.DeepEqual(after, map[string]string{"key": key, "value": []string{"9", "5"}[i]}) {
			t.Fatalf("setting event = %+v", events[i])
		}
	}
	for _, in := range []service.UpdateSettingsIn{{}, {KeepDaily: &daily, KeepWeekly: &weekly}} {
		got, err := s.Svc.UpdateSettings(s.Ctx(), m, in)
		if err != nil || got != v || latestOp(t, s).ID != op.ID {
			t.Fatalf("no-op changed settings or audit: %+v, %v", got, err)
		}
	}
	daily, weekly = 3, 1
	v, err = s.Svc.UpdateSettings(s.Ctx(), m, service.UpdateSettingsIn{KeepDaily: &daily, KeepWeekly: &weekly, Password: testutil.Password})
	if err != nil || v != (service.SettingsView{KeepDaily: 3, KeepWeekly: 1}) {
		t.Fatalf("lower = %+v, %v", v, err)
	}
	op = latestOp(t, s)
	if op.Kind != domain.OpSettingsRetentionLowered || op.Revertible || len(s.Events(op.ID)) != 2 {
		t.Fatalf("lowering op = %+v", op)
	}
	ai, err := s.Svc.ListAIChanges(s.Ctx(), s.Actor, service.ListAIChangesIn{Limit: 10})
	if err != nil || ai.Unreviewed != 1 || len(ai.Ops) != 1 || ai.Ops[0].OpID != op.ID {
		t.Fatalf("retention lowering missing from AI changes: %+v, %v", ai, err)
	}
	requireSQLCount(t, s, `SELECT COUNT(*) FROM idempotency`, 0)
	requireSQLCount(t, s, `SELECT COUNT(*) FROM undo_stack`, 0)
}

func TestUpdateSettingsFailures(t *testing.T) {
	for _, tc := range []struct {
		name          string
		daily, weekly int
		password      bool
		code          domain.Code
	}{
		{"daily floor", 2, 4, false, domain.CodeValidation},
		{"weekly floor", 7, 0, false, domain.CodeValidation},
		{"required daily", 3, 4, false, domain.CodePasswordRequired},
		{"required weekly", 8, 1, false, domain.CodePasswordRequired},
		{"invalid", 3, 4, true, domain.CodePasswordInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testutil.NewScenario(t)
			in := service.UpdateSettingsIn{KeepDaily: &tc.daily, KeepWeekly: &tc.weekly}
			if tc.password {
				in.Password = auth.FormatToken(1, mintSecret(t))
			}
			_, err := s.Svc.UpdateSettings(s.Ctx(), s.Meta(), in)
			testutil.RequireCode(t, err, tc.code)
			v, err := s.Svc.Settings(s.Ctx(), s.Actor)
			if err != nil || v != (service.SettingsView{KeepDaily: 7, KeepWeekly: 4}) {
				t.Fatal("failed update changed settings")
			}
			requireSQLCount(t, s, `SELECT COUNT(*) FROM ops`, 0)
		})
	}
	s := testutil.NewScenario(t)
	s = s.As(testutil.TokenActor("reader", domain.ScopeRead))
	_, err := s.Svc.UpdateSettings(s.Ctx(), s.Meta(), service.UpdateSettingsIn{})
	testutil.RequireCode(t, err, domain.CodeForbidden)
	s = s.As(testutil.TokenActor("admin", domain.ScopeAdmin))
	daily := 10
	if _, err := s.Svc.UpdateSettings(s.Ctx(), s.Meta(), service.UpdateSettingsIn{KeepDaily: &daily}); err != nil {
		t.Fatal(err)
	}
}

func TestChangePassword(t *testing.T) {
	s := testutil.NewScenario(t)
	created := createTestToken(t, s, "survives-change")
	authFixture(t, s, true)
	before := authState(t, s)
	s.Clock.Set(s.Clock.Now().Add(time.Minute))
	next := auth.FormatToken(1, mintSecret(t))
	m := s.Meta()
	m.Idem.Body = []byte(next)
	if err := s.Svc.ChangePassword(s.Ctx(), m, service.ChangePasswordIn{Current: testutil.Password, New: next}); err != nil {
		t.Fatal(err)
	}
	after := authState(t, s)
	valid, err := auth.VerifyPassword(after.PasswordHash, next)
	if err != nil || !valid || after.PasswordHash == before.PasswordHash || !after.PasswordChangedAt.Equal(s.Clock.Now()) ||
		after.IdemHMACKey != before.IdemHMACKey || after.WebLoginDisabled != before.WebLoginDisabled {
		t.Fatal("password change did not update only the expected auth state")
	}
	if valid, err := auth.VerifyPassword(after.PasswordHash, testutil.Password); err != nil || valid {
		t.Fatal("old password still verifies")
	}
	requireSQLCount(t, s, `SELECT COUNT(*) FROM web_sessions`, 0)
	requireSQLCount(t, s, `SELECT COUNT(*) FROM idempotency`, 0)
	if _, err := s.Svc.AuthenticateToken(s.Ctx(), created.Secret, false, domain.ChanAPI, "remote"); err != nil {
		t.Fatal("password change revoked an API token", err)
	}
	op := latestOp(t, s)
	if op.Kind != domain.OpAuthPassword || op.Revertible || op.Actor != "owner" || op.Scopes != domain.ScopeAdmin {
		t.Fatalf("password op = %+v", op)
	}
	events := s.Events(op.ID)
	if len(events) != 1 {
		t.Fatalf("password event count = %d", len(events))
	}
	requirePasswordEvent(t, events[0])
}

func TestChangePasswordFailures(t *testing.T) {
	for _, mode := range []string{"token", "scope", "wrong", "empty current", "short", "long", "utf8"} {
		t.Run(mode, func(t *testing.T) {
			s := testutil.NewScenario(t)
			authFixture(t, s, false)
			before, last := authState(t, s), latestOp(t, s)
			in := service.ChangePasswordIn{Current: testutil.Password, New: testutil.Password}
			code := domain.CodeValidation
			switch mode {
			case "token":
				s = s.As(testutil.TokenActor("admin", domain.AllScopes))
				code = domain.CodeForbidden
			case "scope":
				s.Actor.Scopes = domain.ScopeRead
				code = domain.CodeForbidden
			case "wrong":
				in.Current, code = auth.FormatToken(1, mintSecret(t)), domain.CodePasswordInvalid
			case "empty current":
				in.Current, code = "", domain.CodePasswordInvalid
			case "short":
				in.New = ""
			case "long":
				in.New = strings.Repeat(testutil.Password, 60)
			case "utf8":
				in.New = testutil.Password + string([]byte{0xff})
			}
			err := s.Svc.ChangePassword(s.Ctx(), s.Meta(), in)
			testutil.RequireCode(t, err, code)
			if authState(t, s) != before || latestOp(t, s).ID != last.ID {
				t.Fatal("failed password change modified state or audit")
			}
			requireSQLCount(t, s, `SELECT COUNT(*) FROM web_sessions`, 1)
		})
	}
}

func TestResetPasswordOffline(t *testing.T) {
	s := testutil.NewScenario(t)
	first := createTestToken(t, s, "first")
	second := createTestToken(t, s, "second")
	if _, err := s.Svc.RevokeToken(s.Ctx(), s.Meta(), service.RevokeTokenIn{ID: second.Token.ID}); err != nil {
		t.Fatal(err)
	}
	authFixture(t, s, true)
	before, last := authState(t, s), latestOp(t, s)
	s.Clock.Set(s.Clock.Now().Add(time.Minute))
	raw := mintSecret(t)
	next := auth.FormatToken(1, mintSecret(t))
	id, err := s.Svc.ResetPasswordOffline(s.Ctx(), service.ResetPasswordIn{NewPassword: next, OwnerSecret: raw})
	if err != nil {
		t.Fatal(err)
	}
	after := authState(t, s)
	valid, err := auth.VerifyPassword(after.PasswordHash, next)
	if err != nil || !valid || after.WebLoginDisabled || !after.PasswordChangedAt.Equal(s.Clock.Now()) || after.IdemHMACKey != before.IdemHMACKey {
		t.Fatal("offline reset auth state incorrect")
	}
	requireSQLCount(t, s, `SELECT COUNT(*) FROM web_sessions`, 0)
	requireSQLCount(t, s, `SELECT COUNT(*) FROM api_tokens WHERE revoked_at IS NULL`, 1)
	rows, err := store.ListTokens(s.Ctx(), s.Store.Reader)
	if err != nil || len(rows) != 4 {
		t.Fatalf("reset token count = %d, %v", len(rows), err)
	}
	owner := rows[len(rows)-1]
	if owner.ID != id || !owner.IsOwner || owner.Scopes != domain.AllScopes || owner.MutationsPerHour != 0 || owner.ExpiresAt.Valid ||
		owner.SecretHash != auth.HashSecret(raw) || !strings.HasPrefix(owner.Name, "owner."+s.Clock.Now().UTC().Format("20060102T150405.000Z")+"-") {
		t.Fatal("new owner token row is incorrect")
	}
	for _, row := range rows[:len(rows)-1] {
		if !row.RevokedAt.Valid {
			t.Fatal("reset left an old token active")
		}
		if row.ID != second.Token.ID && !row.RevokedAt.V.Equal(s.Clock.Now()) {
			t.Fatal("reset revocation time differs from op time")
		}
	}
	_, err = s.Svc.AuthenticateToken(s.Ctx(), first.Secret, true, domain.ChanAPI, "socket")
	testutil.RequireCode(t, err, domain.CodeUnauthenticated)
	a, err := s.Svc.AuthenticateToken(s.Ctx(), auth.FormatToken(id, raw), true, domain.ChanCLI, "socket")
	if err != nil || a.Kind != domain.ActorOwner {
		t.Fatalf("new owner authentication: %+v, %v", a, err)
	}
	ops, err := store.ListOps(s.Ctx(), s.Store.Reader, store.OpQuery{SinceSeq: last.Seq, Limit: 10})
	if err != nil || len(ops) != 1 {
		t.Fatalf("reset ops = %+v, %v", ops, err)
	}
	op := ops[0]
	if op.Kind != domain.OpAuthPassword || op.Actor != "system" || op.Channel != domain.ChanSystem || op.Scopes != 0 || op.Revertible {
		t.Fatalf("reset op = %+v", op)
	}
	events := s.Events(op.ID)
	if len(events) != 4 {
		t.Fatalf("reset event count = %d", len(events))
	}
	requireTokenEvent(t, events[0], rows[0], domain.ActRevoke)
	requireTokenEvent(t, events[1], rows[1], domain.ActRevoke)
	requireTokenEvent(t, events[2], owner, domain.ActInsert)
	requirePasswordEvent(t, events[3])
	var count int
	if err := s.Store.Reader.QueryRowContext(s.Ctx(), `SELECT COUNT(*) FROM idempotency WHERE op_id = ?`, op.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("offline reset wrote idempotency state", err)
	}
}

func TestResetPasswordOfflineRollback(t *testing.T) {
	for _, failInsert := range []bool{false, true} {
		t.Run(map[bool]string{false: "validation", true: "insert failure"}[failInsert], func(t *testing.T) {
			s := testutil.NewScenario(t)
			authFixture(t, s, true)
			before, last := authState(t, s), latestOp(t, s)
			tokens, err := store.ListTokens(s.Ctx(), s.Store.Reader)
			if err != nil {
				t.Fatal(err)
			}
			in := service.ResetPasswordIn{OwnerSecret: mintSecret(t)}
			code := domain.CodeValidation
			if failInsert {
				// Fault injection after auth update, revocation and session deletion.
				if _, err := s.Store.Writer.ExecContext(s.Ctx(), `CREATE TEMP TRIGGER fail_owner_insert BEFORE INSERT ON api_tokens BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
					t.Fatal(err)
				}
				in.NewPassword, code = testutil.Password, domain.CodeInternal
			}
			id, err := s.Svc.ResetPasswordOffline(s.Ctx(), in)
			testutil.RequireCode(t, err, code)
			if id != 0 || authState(t, s) != before || latestOp(t, s).ID != last.ID {
				t.Fatal("failed reset exposed an ID or changed authentication/audit")
			}
			after, err := store.ListTokens(s.Ctx(), s.Store.Reader)
			if err != nil || !reflect.DeepEqual(after, tokens) {
				t.Fatal("failed reset changed token rows", err)
			}
			requireSQLCount(t, s, `SELECT COUNT(*) FROM web_sessions`, 1)
		})
	}
}

func TestUpdateSettingsConcurrent(t *testing.T) {
	for range 10 {
		s := testutil.NewScenario(t)
		var wg sync.WaitGroup
		for _, daily := range []int{8, 9} {
			wg.Go(func() {
				_, err := s.Svc.UpdateSettings(s.Ctx(), s.Meta(), service.UpdateSettingsIn{KeepDaily: &daily})
				if err != nil {
					de, ok := domain.AsError(err)
					if !ok || de.Code != domain.CodePasswordRequired || daily != 8 {
						t.Errorf("concurrent retention update: %v", err)
					}
				}
			})
		}
		wg.Wait()
		view, err := s.Svc.Settings(s.Ctx(), s.Actor)
		if err != nil || view.KeepDaily != 9 {
			t.Fatalf("concurrent raises allowed a password-free reduction: %+v, %v", view, err)
		}
	}
}

func TestChangePasswordRollback(t *testing.T) {
	s := testutil.NewScenario(t)
	authFixture(t, s, false)
	before, last := authState(t, s), latestOp(t, s)
	// Fault injection after the password hash has been updated.
	if _, err := s.Store.Writer.ExecContext(s.Ctx(), `CREATE TEMP TRIGGER fail_session_delete BEFORE DELETE ON web_sessions BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	err := s.Svc.ChangePassword(s.Ctx(), s.Meta(), service.ChangePasswordIn{Current: testutil.Password, New: auth.FormatToken(1, mintSecret(t))})
	testutil.RequireCode(t, err, domain.CodeInternal)
	if authState(t, s) != before || latestOp(t, s).ID != last.ID {
		t.Fatal("failed session deletion left a changed password or audit")
	}
	requireSQLCount(t, s, `SELECT COUNT(*) FROM web_sessions`, 1)
}

func TestSettingsPasswordGateDoesNotHoldWriter(t *testing.T) {
	armed := make(chan struct{}, 2)
	s := testutil.NewScenario(t, func(o *service.Options) {
		o.Passwords = auth.NewPasswordGate(passwordWaitClock{Clock: o.Clock, armed: armed})
	})
	exhaustGate(t, s, "socket")
	daily := 5
	done := make(chan error, 1)
	go func() {
		_, err := s.Svc.UpdateSettings(context.Background(), s.Meta(), service.UpdateSettingsIn{KeepDaily: &daily, Password: testutil.Password})
		done <- err
	}()
	select {
	case <-armed:
	case <-time.After(5 * time.Second):
		t.Fatal("settings did not reach password gate")
	}
	add := make(chan error, 1)
	go func() {
		_, err := s.Svc.AddTask(context.Background(), s.Meta(), service.AddTaskIn{Title: "during retention verification"})
		add <- err
	}()
	select {
	case err := <-add:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Error("retention password verification holds the writer")
	}
	s.Svc.BeginShutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	drainErr := s.Svc.Drain(ctx)
	s.Clock.Advance(10 * time.Second)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !errors.Is(drainErr, context.DeadlineExceeded) {
		t.Fatal("Drain skipped admitted settings verification", drainErr)
	}
	if err := s.Svc.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsPasswordChangedDuringVerification(t *testing.T) {
	armed := make(chan struct{}, 2)
	s := testutil.NewScenario(t, func(o *service.Options) {
		o.Passwords = auth.NewPasswordGate(passwordWaitClock{Clock: o.Clock, armed: armed})
	})
	exhaustGate(t, s, "socket")
	daily := 5
	done := make(chan error, 1)
	go func() {
		_, err := s.Svc.UpdateSettings(context.Background(), s.Meta(), service.UpdateSettingsIn{KeepDaily: &daily, Password: testutil.Password})
		done <- err
	}()
	select {
	case <-armed:
	case <-time.After(5 * time.Second):
		t.Fatal("settings did not reach gate")
	}
	changed := make(chan error, 1)
	m := s.Meta()
	m.Actor.RemoteKey = "other-socket"
	next := auth.FormatToken(1, mintSecret(t))
	go func() {
		changed <- s.Svc.ChangePassword(context.Background(), m, service.ChangePasswordIn{Current: testutil.Password, New: next})
	}()
	select {
	case <-armed:
	case <-time.After(5 * time.Second):
		t.Fatal("password change did not reach gate")
	}
	// The other key waits only for the global bucket (2 s); the retention
	// request still waits on the exhausted per-key bucket (5 s).
	s.Clock.Advance(2 * time.Second)
	select {
	case err := <-changed:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(2 * time.Second):
		t.Error("password change blocked behind retention verifier")
	}
	s.Clock.Advance(10 * time.Second)
	err := <-done
	testutil.RequireCode(t, err, domain.CodeConflict)
	v, err := s.Svc.Settings(s.Ctx(), s.Actor)
	if err != nil || v.KeepDaily != 7 {
		t.Fatalf("stale credentials lowered retention: %+v %v", v, err)
	}
}
