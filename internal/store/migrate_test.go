package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestMigrationFilesFrozen(t *testing.T) {
	frozen := map[string]string{ // AIDEV-NOTE: a committed migration never changes; add NNNN_*.sql instead
		"0001_init.sql": "a4356db1c714e76e6b87f48daedbc3f767fa35b29d62253fe02c9101423e33c7",
	}
	files, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(frozen) {
		t.Fatalf("embedded files = %d; frozen files = %d", len(files), len(frozen))
	}
	for _, file := range files {
		data, err := migrationFiles.ReadFile("migrations/" + file.Name())
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != frozen[file.Name()] {
			t.Errorf("%s checksum = %s; want %s", file.Name(), got, frozen[file.Name()])
		}
	}
}

func TestMigrateEmpty(t *testing.T) {
	s := stagingStore(t)
	assertSchemaStatus(t, s, 0, 1, []int{1})
	var before, after int64
	if err := s.Writer.QueryRowContext(t.Context(), "SELECT CAST(strftime('%s','now') AS INTEGER) * 1000").Scan(&before); err != nil {
		t.Fatal(err)
	}
	calls := 0
	applied, err := s.Migrate(t.Context(), func(ctx context.Context) error {
		calls++
		if ctx != t.Context() {
			t.Error("preMigrate received a different context")
		}
		var count int
		if err := s.Writer.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name = ?", "tasks").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Error("migration ran before preMigrate")
		}
		return nil
	})
	if err != nil || !slices.Equal(applied, []int{1}) || calls != 1 {
		t.Fatalf("Migrate = %v, %v; hook calls = %d", applied, err, calls)
	}
	if err := s.Writer.QueryRowContext(t.Context(), "SELECT CAST(strftime('%s','now') AS INTEGER) * 1000").Scan(&after); err != nil {
		t.Fatal(err)
	}
	var version, count int
	var name, checksum string
	var at int64
	if err := s.Reader.QueryRowContext(t.Context(), "SELECT version, name, checksum, applied_at FROM schema_migrations").Scan(&version, &name, &checksum, &at); err != nil {
		t.Fatal(err)
	}
	data, err := migrationFiles.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if version != 1 || name != "init" || checksum != fmt.Sprintf("%x", sha256.Sum256(data)) || at < before || at > after || at%1000 != 0 {
		t.Fatalf("migration row = (%d, %q, %q, %d); timestamp bounds [%d, %d]", version, name, checksum, at, before, after)
	}
	if err := s.Reader.QueryRowContext(t.Context(), "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != 1 {
		t.Fatalf("migration rows = %d, %v; want 1", count, err)
	}
	assertSchemaStatus(t, s, 1, 1, nil)
	assertSchemaIntegrity(t, s)
}

func TestMigrateNoop(t *testing.T) {
	s := newTestStore(t)
	applied, err := s.Migrate(t.Context(), func(context.Context) error {
		t.Error("preMigrate called without pending migrations")
		return errors.New("unexpected hook")
	})
	if err != nil || len(applied) != 0 {
		t.Fatalf("Migrate = %v, %v; want empty, nil", applied, err)
	}
	assertSchemaStatus(t, s, 1, 1, nil)
}

func TestPreMigrateFailureAborts(t *testing.T) {
	s := stagingStore(t)
	want := errors.New("snapshot failed")
	applied, err := s.Migrate(t.Context(), func(context.Context) error { return want })
	if !errors.Is(err, want) || len(applied) != 0 {
		t.Fatalf("Migrate = %v, %v; want empty and snapshot error", applied, err)
	}
	var count int
	if err := s.Reader.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE type = ?", "table").Scan(&count); err != nil || count != 0 {
		t.Fatalf("table count after hook failure = %d, %v; want 0", count, err)
	}
	assertSchemaStatus(t, s, 0, 1, []int{1})
	if _, err := s.Migrate(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaTooNew(t *testing.T) {
	s := newTestStore(t)
	mustExec(t, s.Writer, "INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)", 999, "future", "future", 0)
	assertMigrationRefused(t, s, ErrSchemaTooNew)
}

func TestChecksumMismatch(t *testing.T) {
	s := newTestStore(t)
	mustExec(t, s.Writer, "UPDATE schema_migrations SET checksum = ? WHERE version = ?", strings.Repeat("0", 64), 1)
	assertMigrationRefused(t, s, ErrChecksumMismatch)
}

func TestMigrateEveryPriorVersion(t *testing.T) {
	fixtures, err := filepath.Glob("testdata/schema_v*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range append([]string{""}, fixtures...) {
		name := filepath.Base(fixture)
		if fixture == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			s := stagingStore(t)
			if fixture != "" {
				data, err := os.ReadFile(fixture)
				if err != nil {
					t.Fatal(err)
				}
				mustExec(t, s.Writer, string(data))
			}
			before, err := s.SchemaStatus(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			applied, err := s.Migrate(t.Context(), nil)
			if err != nil || !slices.Equal(applied, before.Pending) {
				t.Fatalf("Migrate = %v, %v; want %v", applied, err, before.Pending)
			}
			assertSchemaStatus(t, s, before.Latest, before.Latest, nil)
			assertSchemaIntegrity(t, s)
		})
	}
}

func TestMigrateRollsBackFile(t *testing.T) {
	s := stagingStore(t)
	// Force a failure halfway through 0001 after several tables and triggers exist.
	mustExec(t, s.Writer, "CREATE TABLE events (sentinel TEXT) STRICT")
	applied, err := s.Migrate(t.Context(), nil)
	if err == nil || len(applied) != 0 || !strings.Contains(err.Error(), "events already exists") {
		t.Fatalf("Migrate = %v, %v; want atomic DDL failure", applied, err)
	}
	var count int
	for _, query := range []string{
		"SELECT count(*) FROM schema_migrations",
		"SELECT count(*) FROM sqlite_schema WHERE name = 'tasks'",
	} {
		if err := s.Reader.QueryRowContext(t.Context(), query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s = %d, %v; want 0", query, count, err)
		}
	}
	mustExec(t, s.Writer, "DROP TABLE events")
	if _, err := s.Migrate(t.Context(), nil); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
}

func TestMigrateChecksIntegrity(t *testing.T) {
	for _, tc := range []struct{ name, setup, detail string }{
		{"foreign keys", `CREATE TABLE parent (id INTEGER PRIMARY KEY) STRICT;
			CREATE TABLE child (id INTEGER REFERENCES parent(id)) STRICT;
			PRAGMA foreign_keys=OFF;
			INSERT INTO child VALUES (1);
			PRAGMA foreign_keys=ON;`, "foreign_key_check"},
		{"integrity", `CREATE TABLE invalid (x INTEGER CHECK (x > 0)) STRICT;
			PRAGMA ignore_check_constraints=ON;
			INSERT INTO invalid VALUES (-1);
			PRAGMA ignore_check_constraints=OFF;`, "integrity_check"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := stagingStore(t)
			mustExec(t, s.Writer, tc.setup)
			applied, err := s.Migrate(t.Context(), nil)
			if err == nil || !strings.Contains(err.Error(), tc.detail) || !slices.Equal(applied, []int{1}) {
				t.Fatalf("Migrate = %v, %v; want [1] and %s failure", applied, err, tc.detail)
			}
		})
	}
}

func assertSchemaStatus(t *testing.T, s *Store, current, latest int, pending []int) {
	t.Helper()
	got, err := s.SchemaStatus(t.Context())
	if err != nil || got.Current != current || got.Latest != latest || !slices.Equal(got.Pending, pending) {
		t.Fatalf("SchemaStatus = %+v, %v; want {%d %d %v}", got, err, current, latest, pending)
	}
}

func assertMigrationRefused(t *testing.T, s *Store, want error) {
	t.Helper()
	if _, err := s.SchemaStatus(t.Context()); !errors.Is(err, want) {
		t.Fatalf("SchemaStatus = %v; want %v", err, want)
	}
	applied, err := s.Migrate(t.Context(), func(context.Context) error {
		t.Error("preMigrate called for an incompatible schema")
		return nil
	})
	if !errors.Is(err, want) || len(applied) != 0 {
		t.Fatalf("Migrate = %v, %v; want empty, %v", applied, err, want)
	}
}

func assertSchemaIntegrity(t *testing.T, s *Store) {
	t.Helper()
	var count int
	if err := s.Reader.QueryRowContext(t.Context(), "SELECT count(*) FROM pragma_foreign_key_check").Scan(&count); err != nil || count != 0 {
		t.Fatalf("foreign key violations = %d, %v; want 0", count, err)
	}
	if err := IntegrityCheck(t.Context(), s.Reader); err != nil {
		t.Fatal(err)
	}
	if err := FTSIntegrityCheck(t.Context(), s.Writer); err != nil {
		t.Fatal(err)
	}
}
