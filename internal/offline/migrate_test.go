package offline_test

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"taskronin/internal/backup"
	"taskronin/internal/offline"
	"taskronin/internal/store"
)

func TestMigrateStatus(t *testing.T) {
	e := newEnv(t)
	// Put the fixture into live WAL mode before comparing bytes: store.Open's
	// required mode change is independent of schema or application mutations.
	st := openDB(t, e.Paths.DB)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(e.Paths.DB)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, e.Paths.LedgerFile, "torn tail")
	if err := offline.Migrate(t.Context(), e, true); err != nil {
		t.Fatal(err)
	}
	if got := e.Out.(*bytes.Buffer).String(); got != "schema 1 (binary 1), nothing pending\n" {
		t.Fatalf("status = %q", got)
	}
	after, err := os.ReadFile(e.Paths.DB)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("status modified database: %v", err)
	}
	ledger, err := os.ReadFile(e.Paths.LedgerFile)
	if err != nil || string(ledger) != "torn tail" {
		t.Fatalf("status modified ledger: %q, %v", ledger, err)
	}
	infos, err := backup.List(e.Server.Backup.Dir)
	if err != nil || len(infos) != 0 {
		t.Fatalf("status took a snapshot: %+v, %v", infos, err)
	}
}

func TestMigrateCurrentSchema(t *testing.T) {
	e := newEnv(t)
	if err := offline.Migrate(t.Context(), e, false); err != nil {
		t.Fatal(err)
	}
	infos, err := backup.List(e.Server.Backup.Dir)
	if err != nil || len(infos) != 0 {
		t.Fatalf("no pending migrations should need no snapshot: %+v, %v", infos, err)
	}
}

func TestMigrateRefusesIncompatibleSchema(t *testing.T) {
	for _, newer := range []bool{false, true} {
		t.Run(map[bool]string{false: "checksum", true: "newer"}[newer], func(t *testing.T) {
			e := newEnv(t)
			st := openDB(t, e.Paths.DB)
			// Raw fixture: incompatible history cannot be produced by real migrations.
			query, arg := "UPDATE schema_migrations SET checksum = ?", any("invalid")
			want := store.ErrChecksumMismatch
			if newer {
				query, arg, want = "UPDATE schema_migrations SET version = ?", 2, store.ErrSchemaTooNew
			}
			if _, err := st.Writer.ExecContext(t.Context(), query, arg); err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			for _, statusOnly := range []bool{true, false} {
				if err := offline.Migrate(t.Context(), e, statusOnly); !errors.Is(err, want) {
					t.Fatalf("migration error = %v, want %v", err, want)
				}
			}
			infos, err := backup.List(e.Server.Backup.Dir)
			if err != nil || len(infos) != 0 {
				t.Fatalf("incompatible schema took a snapshot: %+v, %v", infos, err)
			}
		})
	}
}

func TestMigrateSnapshotFailureLeavesPendingSchema(t *testing.T) {
	e := newEnv(t)
	if err := os.Remove(e.Paths.DB); err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenStaging(t.Context(), e.Paths.DB)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	// An existing empty database can report pending migrations without auth
	// tables, but cannot pass snapshot verification. Init owns empty-DB creation.
	if err := offline.Migrate(t.Context(), e, true); err != nil {
		t.Fatal(err)
	}
	if got := e.Out.(*bytes.Buffer).String(); !strings.Contains(got, "schema 0 (binary 1)") || !strings.Contains(got, "pending") {
		t.Fatalf("pending status = %q", got)
	}
	if err := offline.Migrate(t.Context(), e, false); err == nil {
		t.Fatal("migration proceeded without a verified snapshot")
	}
	st = openDB(t, e.Paths.DB)
	status, err := st.SchemaStatus(t.Context())
	if err != nil || status.Current != 0 || len(status.Pending) != 1 {
		t.Fatalf("schema changed after failed snapshot: %+v, %v", status, err)
	}
}

func TestMigrateMissingDBDoesNotCreate(t *testing.T) {
	e := newEnv(t)
	if err := os.Remove(e.Paths.DB); err != nil {
		t.Fatal(err)
	}
	if err := offline.Migrate(t.Context(), e, false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing database error = %v", err)
	}
	requireAbsent(t, e.Paths.DB)
}
