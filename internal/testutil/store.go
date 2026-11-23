package testutil

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/linsomniac/taskronin/internal/auth"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/service"
	"github.com/linsomniac/taskronin/internal/store"
)

// Password is the one fixed test password permitted by A§13.1.
const Password = "correct horse battery"

var passwordHash = sync.OnceValues(func() (string, error) { return auth.HashPassword(Password) })

// Store creates a migrated, bootstrapped database and checks FTS and structural
// invariants during cleanup.
func Store(t testing.TB) *store.Store {
	t.Helper()
	return newStore(t, true)
}

// StoreNoCheck creates a store without cleanup checks for deliberate corruption tests.
func StoreNoCheck(t testing.TB) *store.Store {
	t.Helper()
	return newStore(t, false)
}

func newStore(t testing.TB, check bool) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "taskronin.db")
	staging, err := store.OpenStaging(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := staging.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := staging.Migrate(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	hash, err := passwordHash()
	if err != nil {
		t.Fatal(err)
	}
	key, err := auth.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := auth.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := Local(t, DefaultNow)
	name, err := auth.OwnerTokenName(now)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := staging.Writer.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = store.BootstrapDB(t.Context(), tx, store.Bootstrap{
		Generation: domain.NewGeneration(), TZ: Zone.Name(),
		Auth:  store.AuthState{PasswordHash: hash, PasswordChangedAt: now, IdemHMACKey: key},
		Owner: domain.Token{Name: name, SecretHash: auth.HashSecret(raw), Scopes: domain.AllScopes, IsOwner: true, CreatedAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := staging.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// testing cancels t.Context before cleanup callbacks run.
		if check {
			if err := store.FTSIntegrityCheck(context.Background(), st.Writer); err != nil {
				t.Error(err)
			}
			if err := service.CheckStructural(context.Background(), st.Reader); err != nil {
				t.Error(err)
			}
		}
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	return st
}
