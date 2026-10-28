package offline_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"taskronin/internal/backup"
	"taskronin/internal/domain"
	"taskronin/internal/lock"
	"taskronin/internal/offline"
	"taskronin/internal/service"
	"taskronin/internal/store"
	"taskronin/internal/testutil"
)

func TestOfflineBackup(t *testing.T) {
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
	added, err := svc.AddTask(t.Context(), testutil.Meta(testutil.Owner(), svc.Generation()), service.AddTaskIn{Title: "erase before backup"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Append(backup.LedgerRecord{V: 1, Rec: domain.NewOpID().String(), At: e.Clock.Now().UnixMilli(),
		State: backup.LedgerPending, Tasks: []domain.TaskID{added.Task.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := offline.Backup(t.Context(), e)
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != backup.KindManual || filepath.Dir(info.Path) != e.Server.Backup.Dir || info.SchemaVersion != 1 || !info.At.Equal(e.Clock.Now()) {
		t.Fatalf("snapshot = %+v", info)
	}
	stat, err := os.Stat(info.Path)
	if err != nil || stat.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode: %v, %v", stat, err)
	}
	wal, err := os.Stat(e.Paths.DB + "-wal")
	if !errors.Is(err, os.ErrNotExist) && (err != nil || wal.Size() != 0) {
		t.Fatalf("WAL was not truncated: %v, %v", wal, err)
	}
	for _, path := range []string{e.Paths.DB, info.Path} {
		copied := openDB(t, path)
		task, err := store.GetTask(t.Context(), copied.Reader, added.Task.ID)
		if err != nil || task.Title != "[purged]" || !task.PurgedAt.Valid {
			t.Fatalf("ledger was not replayed before snapshot: %+v, %v", task, err)
		}
	}
	ledger, _, err = backup.OpenLedger(e.Paths.LedgerFile)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := ledger.Pending()
	if err != nil || len(pending) != 1 {
		t.Fatalf("backup incorrectly completed ledger records: %v", err)
	}
}

func TestOfflineBackupRequiresCheckpoint(t *testing.T) {
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
	// Hold a WAL snapshot to exercise the fail-loudly branch: an offline
	// checkpoint must be confirmed even when another SQLite reader exists.
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
	if _, err := offline.Backup(t.Context(), e); err == nil {
		t.Fatal("backup succeeded without a confirmed checkpoint")
	}
	infos, err := backup.List(e.Server.Backup.Dir)
	if err != nil || len(infos) != 0 {
		t.Fatalf("backup ran before checkpoint succeeded: %+v, %v", infos, err)
	}
}

func TestListBackupsWithoutLock(t *testing.T) {
	e := newEnv(t)
	info, err := offline.Backup(t.Context(), e)
	if err != nil {
		t.Fatal(err)
	}
	held, err := lock.Acquire(e.Paths.LockFile())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Release() }()
	writeFile(t, e.Paths.SwapJournal(), "interrupted")
	temp := filepath.Join(e.Paths.StagingDir, "x")
	writeFile(t, temp, "leftover")
	infos, err := offline.ListBackups(e)
	if err != nil || len(infos) != 1 || infos[0].Path != info.Path {
		t.Fatalf("list = %+v, %v", infos, err)
	}
	if _, err := os.Stat(temp); err != nil {
		t.Fatalf("listing cleaned temporary files: %v", err)
	}
}
