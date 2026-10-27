package backup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"taskronin/internal/clock"
	"taskronin/internal/store"
)

func TestSnapshotRoundTrip(t *testing.T) {
	m, st := newSnapshotManager(t)
	// A quote in the directory catches an unescaped, string-built VACUUM.
	m.Dir = filepath.Join(m.Dir, "owner's backups")
	if err := os.Mkdir(m.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	wantAt := time.Date(2026, 10, 28, 10, 0, 0, 123456789, time.FixedZone("test", -6*60*60))
	m.Clock = clock.NewFake(wantAt)
	info, err := m.Snapshot(t.Context(), KindManual)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^taskronin-20261028T160000\.123Z-[0-9a-f]{4}-manual\.db$`).MatchString(info.Name) {
		t.Fatalf("snapshot name = %q", info.Name)
	}
	if info.Kind != KindManual || info.Path != filepath.Join(m.Dir, info.Name) ||
		!info.At.Equal(wantAt.Truncate(time.Millisecond)) || info.SchemaVersion != 1 {
		t.Fatalf("snapshot metadata = %+v", info)
	}
	stat, err := os.Stat(info.Path)
	if err != nil || stat.Mode().Perm() != 0o600 || stat.Size() == 0 || info.Size != stat.Size() {
		t.Fatalf("snapshot stat = %v, metadata = %+v, err = %v", stat, info, err)
	}
	requireVerified(t, info.Path)
	copyDB, err := store.OpenStaging(t.Context(), info.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = copyDB.Close() })
	var title string
	if err := copyDB.Reader.QueryRowContext(t.Context(), "SELECT title FROM tasks WHERE id = ?", 1).Scan(&title); err != nil || title != "snapshot task" {
		t.Fatalf("copied title = %q, err = %v", title, err)
	}
	if err := copyDB.Close(); err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := st.Reader.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("live journal mode = %q, err = %v", mode, err)
	}
	requireDirectoryNames(t, m.Dir, info.Name)
}

func TestSnapshotConcurrent(t *testing.T) {
	m, _ := newSnapshotManager(t)
	var infos [2]SnapshotInfo
	var errs [2]error
	var wg sync.WaitGroup
	for i := range infos {
		wg.Go(func() { infos[i], errs[i] = m.Snapshot(t.Context(), KindManual) })
	}
	wg.Wait()
	for i, info := range infos {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if _, ok := ParseSnapshotName(info.Name); !ok {
			t.Fatalf("invalid snapshot name %q", info.Name)
		}
		requireVerified(t, info.Path)
	}
	if infos[0].Name == infos[1].Name {
		t.Fatal("concurrent snapshots collided")
	}
	requireDirectoryNames(t, m.Dir, infos[0].Name, infos[1].Name)
}

func TestSnapshotCanceled(t *testing.T) {
	for _, step := range []string{"before", "vacuumed", "verified"} {
		t.Run(step, func(t *testing.T) {
			m, _ := newSnapshotManager(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if step == "before" {
				cancel()
			} else {
				setCrashHook(m, func(at string) error {
					if at == step {
						cancel()
					}
					return nil // normal cancellation must clean up
				})
			}
			if _, err := m.Snapshot(ctx, KindManual); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled snapshot = %v", err)
			}
			requireDirectoryNames(t, m.Dir)
		})
	}
}

func TestSnapshotCrashPoints(t *testing.T) {
	for _, step := range []string{"vacuumed", "verified", "renamed"} {
		t.Run(step, func(t *testing.T) {
			m, _ := newSnapshotManager(t)
			older, err := m.Snapshot(t.Context(), KindDaily)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(older.Path)
			if err != nil {
				t.Fatal(err)
			}
			m.Clock.(*clock.Fake).Advance(time.Hour)
			death := errors.New("simulated process death")
			setCrashHook(m, func(at string) error {
				if at == step {
					return death
				}
				return nil
			})
			if _, err := m.Snapshot(t.Context(), KindManual); !errors.Is(err, death) {
				t.Fatalf("crash at %s = %v", step, err)
			}
			after, err := os.ReadFile(older.Path)
			if err != nil || !slices.Equal(before, after) {
				t.Fatalf("older snapshot changed: %v", err)
			}
			requireVerified(t, older.Path)
			listed, err := List(m.Dir)
			wantCount, wantTemps := 1, 1
			if step == "renamed" {
				wantCount, wantTemps = 2, 0
			}
			if err != nil || len(listed) != wantCount {
				t.Fatalf("list after crash = %+v, %v", listed, err)
			}
			for _, info := range listed {
				requireVerified(t, info.Path)
			}
			temps, err := filepath.Glob(filepath.Join(m.Dir, ".tkr-tmp-*"))
			if err != nil || len(temps) != wantTemps {
				t.Fatalf("crash leftovers = %v, %v", temps, err)
			}
			if wantTemps == 1 {
				stat, err := os.Stat(temps[0])
				if err != nil || stat.Size() == 0 {
					t.Fatalf("crash temp is not the vacuum output: %v, %v", stat, err)
				}
			}
			// A restarted manager can proceed without mistaking leftovers for snapshots.
			restarted := &Manager{Dir: m.Dir, Clock: m.Clock, Snap: m.Snap}
			next, err := restarted.Snapshot(t.Context(), KindManual)
			if err != nil {
				t.Fatal(err)
			}
			requireVerified(t, next.Path)
		})
	}
}

func TestSnapshotFailureCleanup(t *testing.T) {
	for _, failure := range []string{"closed pool", "FTS corruption", "invalid kind"} {
		t.Run(failure, func(t *testing.T) {
			m, st := newSnapshotManager(t)
			kind := KindManual
			switch failure {
			case "closed pool":
				if err := st.Snap.Close(); err != nil {
					t.Fatal(err)
				}
			case "FTS corruption":
				if _, err := st.Writer.ExecContext(t.Context(), "INSERT INTO tasks_fts(tasks_fts) VALUES ('delete-all')"); err != nil {
					t.Fatal(err)
				}
			case "invalid kind":
				kind = Kind("unknown")
			}
			if _, err := m.Snapshot(t.Context(), kind); err == nil {
				t.Fatal("snapshot accepted invalid source or kind")
			}
			requireDirectoryNames(t, m.Dir)
		})
	}
}

func TestVerifyRejectsCorrupt(t *testing.T) {
	m, _ := newSnapshotManager(t)
	info, err := m.Snapshot(t.Context(), KindManual)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(info.Path, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(t.Context(), info.Path); err == nil {
		t.Fatal("truncated snapshot verified")
	}
	missing := filepath.Join(m.Dir, "missing.db")
	if _, err := Verify(t.Context(), missing); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing snapshot = %v", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Verify created missing database: %v", err)
	}
}

func TestVerifyRejectsFTSCorruption(t *testing.T) {
	for _, table := range []string{"tasks_fts", "log_entries_fts"} {
		t.Run(table, func(t *testing.T) {
			m, st := newSnapshotManager(t)
			if _, err := st.Writer.ExecContext(t.Context(), `INSERT INTO log_entries
				(title, work_date, kind, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
				"log content", "2026-10-28", "work", 1, 1); err != nil {
				t.Fatal(err)
			}
			info, err := m.Snapshot(t.Context(), KindManual)
			if err != nil {
				t.Fatal(err)
			}
			staged, err := store.OpenStaging(t.Context(), info.Path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = staged.Close() })
			query := "INSERT INTO tasks_fts(tasks_fts) VALUES ('delete-all')"
			if table == "log_entries_fts" {
				query = "INSERT INTO log_entries_fts(log_entries_fts) VALUES ('delete-all')"
			}
			if _, err := staged.Writer.ExecContext(t.Context(), query); err != nil {
				t.Fatal(err)
			}
			if err := store.IntegrityCheck(t.Context(), staged.Reader); err != nil {
				t.Fatalf("fixture has non-FTS corruption: %v", err)
			}
			if err := staged.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := Verify(t.Context(), info.Path); err == nil {
				t.Fatal("FTS content mismatch verified")
			}
		})
	}
}

func TestVerifyRejectsSidecars(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		t.Run(suffix, func(t *testing.T) {
			m, _ := newSnapshotManager(t)
			info, err := m.Snapshot(t.Context(), KindManual)
			if err != nil {
				t.Fatal(err)
			}
			// A directory cannot be cleaned up as a SQLite sidecar on close.
			if err := os.Mkdir(info.Path+suffix, 0o700); err != nil {
				t.Fatal(err)
			}
			if _, err := Verify(t.Context(), info.Path); err == nil {
				t.Fatal("snapshot with persistent sidecar verified")
			}
		})
	}
}

func TestRotate(t *testing.T) {
	dir := t.TempDir()
	names := touchSnapshots(t, dir,
		"2026-10-28T03:30Z", "2026-10-27T03:30Z", "2026-10-27T01:00Z", "2026-10-26T03:30Z", "2026-10-25T03:30Z",
		"2026-10-20T03:30Z", "2026-10-13T03:30Z")
	writeSnapshotFile(t, dir, "notes.txt", "notes")
	if err := os.Mkdir(filepath.Join(dir, "taskronin.db.pre-restore-x"), 0o700); err != nil {
		t.Fatal(err)
	}
	m := &Manager{Dir: dir, Clock: clock.NewFake(time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC))}
	removed, err := m.Rotate(t.Context(), Retention{Daily: 3, Weekly: 2})
	if err != nil || !slices.Equal(removed, names[5:]) {
		t.Fatalf("removed = %v, want %v, err = %v", removed, names[5:], err)
	}
	requireDirectoryNames(t, dir, append(slices.Clone(names[:5]), "notes.txt", "taskronin.db.pre-restore-x")...)
	for _, r := range []Retention{{Daily: 2, Weekly: 1}, {Daily: 3, Weekly: 0}, {Daily: -1, Weekly: 2}} {
		if _, err := m.Rotate(t.Context(), r); err == nil {
			t.Fatalf("below-floor retention accepted: %+v", r)
		}
	}
}

func TestRotateCalendarBoundaries(t *testing.T) {
	dir := t.TempDir()
	names := touchSnapshots(t, dir,
		"2027-01-04T09:00Z", "2027-01-04T08:00Z", "2027-01-04T07:00Z",
		"2027-01-03T12:00Z", "2027-01-03T10:00:00.001Z", "2027-01-03T10:00Z", "2027-01-03T09:00Z",
		"2027-01-02T12:00Z", "2027-01-01T12:00Z", "2026-12-27T12:00Z")
	// Jan 4 is ISO week 1, Jan 3 is week 53 of the preceding ISO year.
	// The clock's local date is still Sunday; retention must use Monday in UTC.
	now := time.Date(2027, 1, 3, 22, 0, 0, 0, time.FixedZone("test", -12*60*60))
	m := &Manager{Dir: dir, Clock: clock.NewFake(now)}
	removed, err := m.Rotate(t.Context(), Retention{Daily: 3, Weekly: 2})
	want := []string{names[5], names[6], names[8], names[9]}
	if err != nil || !slices.Equal(removed, want) {
		t.Fatalf("removed = %v, want %v, err = %v", removed, want, err)
	}
	requireDirectoryNames(t, dir, names[0], names[1], names[2], names[3], names[4], names[7])
}

func TestRotateSparseCalendarAndCanceled(t *testing.T) {
	dir := t.TempDir()
	names := touchSnapshots(t, dir, "2026-10-28T09:00Z", "2026-10-28T08:00Z", "2026-10-28T07:00Z", "2026-10-01T01:00Z", "2026-09-01T01:00Z")
	m := &Manager{Dir: dir, Clock: clock.NewFake(time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC))}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := m.Rotate(ctx, Retention{Daily: 3, Weekly: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled rotation = %v", err)
	}
	requireDirectoryNames(t, dir, names...)
	removed, err := m.Rotate(t.Context(), Retention{Daily: 3, Weekly: 1})
	if err != nil || !slices.Equal(removed, names[3:]) {
		t.Fatalf("sparse rotation = %v, %v", removed, err)
	}
}

func TestList(t *testing.T) {
	dir := t.TempDir()
	names := []string{
		"taskronin-20261028T100000.000Z-ab12-daily.db",
		"taskronin-20261027T100000.000Z-1234-manual.db",
		"taskronin-20261026T100000.000Z-dead-premigrate.db",
		"taskronin-20261025T100000.000Z-beef-prescrub.db",
	}
	kinds := []Kind{KindDaily, KindManual, KindPremigrate, KindPrescrub}
	for _, name := range names {
		writeSnapshotFile(t, dir, name, "not a database")
	}
	for _, name := range []string{"notes.txt", ".tkr-tmp-x.db", names[0] + "-wal", "taskronin.db.pre-import-x"} {
		writeSnapshotFile(t, dir, name, "foreign")
	}
	if err := os.Mkdir(filepath.Join(dir, "taskronin-20261029T100000.000Z-ab12-daily.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	listed, err := List(dir)
	if err != nil || len(listed) != len(names) {
		t.Fatalf("list = %+v, %v", listed, err)
	}
	for i, info := range listed {
		if info.Name != names[i] || info.Path != filepath.Join(dir, names[i]) || info.Kind != kinds[i] || info.Size != 14 || info.SchemaVersion != 0 {
			t.Fatalf("list item %d = %+v", i, info)
		}
	}
}

func TestParseSnapshotName(t *testing.T) {
	valid := "taskronin-20261028T100000.123Z-ab12-manual.db"
	info, ok := ParseSnapshotName(valid)
	if !ok || info.Name != valid || info.Kind != KindManual || !info.At.Equal(time.Date(2026, 10, 28, 10, 0, 0, 123000000, time.UTC)) {
		t.Fatalf("parse = %+v, %v", info, ok)
	}
	for _, name := range []string{
		"../" + valid, valid + "-wal", ".tkr-tmp-" + valid,
		strings.Replace(valid, "20261028", "20260230", 1),
		strings.Replace(valid, ".123Z", "Z", 1),
		strings.Replace(valid, ".123Z", ".1234Z", 1),
		strings.Replace(valid, "ab12", "zzzz", 1),
		strings.Replace(valid, "manual", "unknown", 1),
	} {
		if _, ok := ParseSnapshotName(name); ok {
			t.Errorf("accepted invalid name %q", name)
		}
	}
}

func newSnapshotManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "taskronin.db")
	staged, err := store.OpenStaging(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = staged.Close() })
	if _, err := staged.Migrate(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := staged.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := st.Writer.ExecContext(t.Context(), `INSERT INTO tasks
		(title, rank, origin, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, "snapshot task", 1, "test", 1, 1); err != nil {
		t.Fatal(err)
	}
	return &Manager{Dir: t.TempDir(), Clock: clock.NewFake(time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)), Snap: st.Snap}, st
}

func requireVerified(t *testing.T, path string) {
	t.Helper()
	if version, err := Verify(t.Context(), path); err != nil || version != 1 {
		t.Fatalf("Verify(%q) = %d, %v", path, version, err)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("sidecar %s remains: %v", suffix, err)
		}
	}
}

func touchSnapshots(t *testing.T, dir string, dates ...string) []string {
	t.Helper()
	var names []string
	for i, date := range dates {
		layout := "2006-01-02T15:04Z"
		if strings.Count(date, ":") == 2 {
			layout = "2006-01-02T15:04:05.000Z"
		}
		at, err := time.Parse(layout, date)
		if err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("taskronin-%s-%04x-manual.db", at.Format("20060102T150405.000Z"), i)
		writeSnapshotFile(t, dir, name, "")
		names = append(names, name)
	}
	return names
}

func writeSnapshotFile(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func requireDirectoryNames(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	want = slices.Clone(want)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("directory = %v, want %v", got, want)
	}
}
