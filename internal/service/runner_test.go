package service_test

import (
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"taskronin/internal/auth"
	"taskronin/internal/domain"
	"taskronin/internal/store"
	"taskronin/internal/testutil"
)

func TestRunForbidden(t *testing.T) {
	s := testutil.NewScenario(t).As(testutil.TokenActor("ro", domain.ScopeRead))
	called := false
	_, err := s.Svc.TestOpRaw(s.Ctx(), s.Meta(), nil, func(store.Q) error { called = true; return nil })
	testutil.RequireCode(t, err, domain.CodeForbidden)
	if called || s.OpCount() != 0 {
		t.Fatal("forbidden op touched the database")
	}
}

func TestRunErrorRollsBack(t *testing.T) {
	s := testutil.NewScenario(t)
	cause := errors.New("private failure")
	_, err := s.Svc.TestOpRaw(s.Ctx(), s.Meta(), nil, func(q store.Q) error {
		if err := store.SetSetting(s.Ctx(), q, "test.rollback", "changed"); err != nil {
			return err
		}
		return cause
	})
	testutil.RequireCode(t, err, domain.CodeInternal)
	if !errors.Is(err, cause) {
		t.Fatalf("lost cause: %v", err)
	}
	if de, _ := domain.AsError(err); de.Detail != "" {
		t.Fatalf("internal detail leaked: %q", de.Detail)
	}
	if s.OpCount() != 0 {
		t.Fatal("failed op persisted")
	}
	if _, err := store.GetSetting(s.Ctx(), s.Store.Reader, "test.rollback"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("setting survived: %v", err)
	}
	var n int
	if err := s.Store.Reader.QueryRowContext(s.Ctx(), `SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("events = %d", n)
	}
}

func TestReconcileWithoutSteps(t *testing.T) {
	s := testutil.NewScenario(t)
	// Holding the only writer proves the cheap check never asks for it.
	tx, err := s.Store.Writer.BeginTx(s.Ctx(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.Svc.Reconcile(s.Ctx()); err != nil {
		t.Fatal(err)
	}
	if s.OpCount() != 0 {
		t.Fatal("empty reconcile wrote an op")
	}
}

func TestHarnessStore(t *testing.T) {
	s := testutil.Store(t)
	g, err := store.GetSetting(t.Context(), s.Reader, "db_generation")
	if err != nil {
		t.Fatal(err)
	}
	if b, err := hex.DecodeString(g); err != nil || len(b) != 8 {
		t.Fatalf("generation = %q", g)
	}
	a, err := store.GetAuthState(t.Context(), s.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := auth.VerifyPassword(a.PasswordHash, testutil.Password); err != nil || !ok {
		t.Fatalf("password: %v, %v", ok, err)
	}
	if a.IdemHMACKey == [32]byte{} {
		t.Fatal("empty HMAC key")
	}
	tokens, err := store.ListTokens(t.Context(), s.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || !tokens[0].IsOwner || tokens[0].RevokedAt.Valid || tokens[0].Scopes != domain.AllScopes {
		t.Fatal("missing live owner token")
	}
}

func TestHarnessLocal(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"10-28 10:00", "2026-10-28T16:00:00Z"},
		{"2026-11-02T07:00Z", "2026-11-02T07:00:00Z"},
		{"2026-11-01 01:30", "2026-11-01T07:30:00Z"},
		{"2026-10-28 10:00:12", "2026-10-28T16:00:12Z"},
		{"2026-03-08 02:30", "2026-03-08T09:00:00Z"},
	} {
		t.Run(tt.in, func(t *testing.T) {
			if got := testutil.Local(t, tt.in).UTC().Format(time.RFC3339); got != tt.want {
				t.Fatalf("got %s, want %s", got, tt.want)
			}
		})
	}
	if testutil.D("10-28") != testutil.D("2026-10-28") {
		t.Fatal("date shorthand")
	}
	if got := testutil.Moment("10-30 09:00"); got.Date != testutil.D("10-30") || !got.Min.Valid || got.Min.V != 540 {
		t.Fatalf("moment: %+v", got)
	}
	if testutil.Moment("2026-10-30").Min.Valid {
		t.Fatal("date-only moment has minute")
	}
}

func TestVersionBumpOncePerOp(t *testing.T) {
	s := testutil.NewScenario(t)
	var id domain.TaskID
	s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), nil, func(q store.Q) error {
		tk := domain.Task{Title: "A", Status: domain.StatusOpen, Rank: 1, DueAck: domain.DueNone, Origin: "owner",
			CreatedAt: testutil.Local(t, testutil.DefaultNow), UpdatedAt: testutil.Local(t, testutil.DefaultNow)}
		err := store.InsertTask(s.Ctx(), q, &tk)
		id = tk.ID
		return err
	}))
	ref := domain.EntityRef{Entity: domain.EntTask, ID: int64(id)}
	s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), []domain.EntityRef{ref, ref}, func(q store.Q) error {
		for _, r := range []domain.TriageReason{domain.ReasonNew, domain.ReasonManual} {
			if _, err := store.AddReason(s.Ctx(), q, domain.TriageRow{TaskID: id, Reason: r, Seq: 1}); err != nil {
				return err
			}
		}
		return nil
	}))
	var v int
	if err := s.Store.Reader.QueryRowContext(s.Ctx(), `SELECT version FROM tasks WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 2 {
		t.Fatalf("version = %d, want 2", v)
	}
}
