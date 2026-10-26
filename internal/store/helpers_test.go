package store

import (
	"path/filepath"
	"testing"
)

func newTestStore(t testing.TB) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "taskronin.db")
	staging, err := OpenStaging(t.Context(), path)
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
	if err := staging.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func mustExec(t testing.TB, q Q, query string, args ...any) {
	t.Helper()
	if _, err := q.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}
