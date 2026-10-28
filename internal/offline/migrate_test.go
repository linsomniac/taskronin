package offline_test

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"taskronin/internal/backup"
	"taskronin/internal/domain"
	"taskronin/internal/offline"
	"taskronin/internal/service"
	"taskronin/internal/store"
	"taskronin/internal/testutil"
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

func TestMigrateReappliesLedgerOnCurrentSchema(t *testing.T) {
	e := newEnv(t)
	st := openDB(t, e.Paths.DB)
	ledger, _, err := backup.OpenLedger(e.Paths.LedgerFile)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := offline.NewService(e, st, ledger)
	if err != nil {
		t.Fatal(err)
	}
	added, err := svc.AddTask(t.Context(), testutil.Meta(testutil.Owner(), svc.Generation()), service.AddTaskIn{Title: "pending purge"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Append(backup.LedgerRecord{V: 1, Rec: domain.NewOpID().String(), At: e.Clock.Now().UnixMilli(),
		State: backup.LedgerPending, Tasks: []domain.TaskID{added.Task.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := offline.Migrate(t.Context(), e, false); err != nil {
		t.Fatal(err)
	}
	task, err := store.GetTask(t.Context(), st.Reader, added.Task.ID)
	if err != nil || task.Title != "[purged]" || !task.PurgedAt.Valid {
		t.Fatalf("current-schema migration did not replay purge intent: %+v, %v", task, err)
	}
	// Keep the fixture's connections open: last-connection cleanup must not
	// hide a missing explicit checkpoint after the replay's writes.
	wal, err := os.Stat(e.Paths.DB + "-wal")
	if !errors.Is(err, os.ErrNotExist) && (err != nil || wal.Size() != 0) {
		t.Fatalf("migration did not truncate WAL after replay: %v, %v", wal, err)
	}
	ledger, _, err = backup.OpenLedger(e.Paths.LedgerFile)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := ledger.Pending()
	if err != nil || len(pending) != 1 {
		t.Fatalf("migration incorrectly completed purge intent: %+v, %v", pending, err)
	}
	infos, err := backup.List(e.Server.Backup.Dir)
	if err != nil || len(infos) != 0 {
		t.Fatalf("current-schema recovery took a migration snapshot: %+v, %v", infos, err)
	}
}

func TestMigrateRequiresCheckpointOnCurrentSchema(t *testing.T) {
	e := newEnv(t)
	st := openDB(t, e.Paths.DB)
	ledger, _, err := backup.OpenLedger(e.Paths.LedgerFile)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := offline.NewService(e, st, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddTask(t.Context(), testutil.Meta(testutil.Owner(), svc.Generation()), service.AddTaskIn{Title: "before reader"}); err != nil {
		t.Fatal(err)
	}
	// An empty ledger makes replay a no-op. Checkpointing must still fail
	// loudly when an outstanding reader prevents truncation (R3-2).
	tx, err := st.Reader.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var count int
	if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM tasks").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddTask(t.Context(), testutil.Meta(testutil.Owner(), svc.Generation()), service.AddTaskIn{Title: "after reader"}); err != nil {
		t.Fatal(err)
	}
	err = offline.Migrate(t.Context(), e, false)
	if err == nil {
		t.Fatal("current-schema migration succeeded without a confirmed checkpoint")
	}
	de, _ := domain.AsError(err)
	if de.Err == nil || !strings.Contains(de.Err.Error(), "incomplete truncate checkpoint") {
		t.Fatalf("migration failed for a reason other than checkpoint refusal: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := offline.Migrate(t.Context(), e, false); err != nil {
		t.Fatalf("migration failed after reader released: %v", err)
	}
}

func TestOfflineRefusesIncompatibleSchema(t *testing.T) {
	for _, newer := range []bool{false, true} {
		t.Run(map[bool]string{false: "checksum", true: "newer"}[newer], func(t *testing.T) {
			e := newEnv(t)
			st := openDB(t, e.Paths.DB)
			ledger, _, err := backup.OpenLedger(e.Paths.LedgerFile)
			if err != nil {
				t.Fatal(err)
			}
			svc, err := offline.NewService(e, st, ledger)
			if err != nil {
				t.Fatal(err)
			}
			added, err := svc.AddTask(t.Context(), testutil.Meta(testutil.Owner(), svc.Generation()), service.AddTaskIn{Title: "must remain unchanged"})
			if err != nil {
				t.Fatal(err)
			}
			if err := ledger.Append(backup.LedgerRecord{V: 1, Rec: domain.NewOpID().String(), At: e.Clock.Now().UnixMilli(),
				State: backup.LedgerPending, Tasks: []domain.TaskID{added.Task.ID}}); err != nil {
				t.Fatal(err)
			}
			ledgerBefore, err := os.ReadFile(e.Paths.LedgerFile)
			if err != nil {
				t.Fatal(err)
			}
			// Raw crash fixture: opening this ledger would truncate its torn tail.
			ledgerBefore = append(ledgerBefore, []byte("torn tail")...)
			writeFile(t, e.Paths.LedgerFile, string(ledgerBefore))
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
			before, err := os.ReadFile(e.Paths.DB)
			if err != nil {
				t.Fatal(err)
			}
			for _, command := range []struct {
				name string
				run  func() error
			}{
				{"status", func() error { return offline.Migrate(t.Context(), e, true) }},
				{"migrate", func() error { return offline.Migrate(t.Context(), e, false) }},
				{"backup", func() error { _, err := offline.Backup(t.Context(), e); return err }},
			} {
				t.Run(command.name, func(t *testing.T) {
					if err := command.run(); !errors.Is(err, want) {
						t.Errorf("%s error = %v, want %v", command.name, err, want)
					}
					after, err := os.ReadFile(e.Paths.DB)
					if err != nil || !bytes.Equal(before, after) {
						t.Errorf("incompatible database modified: %v", err)
					}
					ledgerAfter, err := os.ReadFile(e.Paths.LedgerFile)
					if err != nil || !bytes.Equal(ledgerBefore, ledgerAfter) {
						t.Errorf("ledger opened before compatibility check: %v", err)
					}
					infos, err := backup.List(e.Server.Backup.Dir)
					if err != nil || len(infos) != 0 {
						t.Errorf("incompatible schema took a snapshot: %+v, %v", infos, err)
					}
				})
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
