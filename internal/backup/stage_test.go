package backup_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/linsomniac/taskronin/internal/backup"
	"github.com/linsomniac/taskronin/internal/store"
	"github.com/linsomniac/taskronin/internal/testutil"
)

func TestStageRecoversWALOnlyChange(t *testing.T) {
	src := stageSource(t)
	root := t.TempDir()
	staged, err := backup.Stage(t.Context(), root, backup.FileSet{DB: src})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(staged.Dir) != root || !strings.HasPrefix(filepath.Base(staged.Dir), "stage-") || filepath.Dir(staged.Path) != staged.Dir {
		t.Fatalf("staged paths = %+v", staged)
	}
	for path, want := range map[string]fs.FileMode{staged.Dir: 0o700, staged.Path: 0o600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("mode of %s = %v, %v; want %o", path, info, err, want)
		}
	}
	requireSwapAbsent(t, staged.Path+"-wal", staged.Path+"-shm", staged.Path+"-journal")
	st, err := store.OpenStaging(t.Context(), staged.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	seqs, err := store.Sequences(t.Context(), st.Writer)
	if err != nil || seqs["tags"] != 1 {
		t.Fatalf("staged sequences = %v, %v; want tags=1", seqs, err)
	}
	var name, mode string
	if err := st.Writer.QueryRowContext(t.Context(), "SELECT name FROM tags WHERE id = ?", 1).Scan(&name); err != nil || name != "walonly" {
		t.Fatalf("recovered tag = %q, %v", name, err)
	}
	if err := st.Writer.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "delete" {
		t.Fatalf("staged journal mode = %q, %v", mode, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	requireSwapAbsent(t, staged.Path+"-wal", staged.Path+"-shm", staged.Path+"-journal")
	writeStageFile(t, filepath.Join(staged.Dir, "nested", "leftover"), "temporary content")
	for range 2 {
		if err := staged.Remove(); err != nil {
			t.Fatal(err)
		}
	}
	requireStageEmpty(t, root)
}

func TestStageLeavesSourceUntouched(t *testing.T) {
	for _, shm := range []bool{false, true} {
		name := "without SHM"
		if shm {
			name = "with unusable SHM"
		}
		t.Run(name, func(t *testing.T) {
			src := stageSource(t)
			before := make(map[string][sha256.Size]byte)
			for _, suffix := range []string{"", "-wal"} {
				before[suffix] = stageFileHash(t, src+suffix)
			}
			if shm {
				// SHM is an ephemeral index, never part of a staging copy.
				writeStageFile(t, filepath.Join(src+"-shm", "sentinel"), "leave alone")
			}
			staged, err := backup.Stage(t.Context(), t.TempDir(), backup.FileSet{DB: src})
			if err != nil {
				t.Fatal(err)
			}
			for suffix, want := range before {
				if got := stageFileHash(t, src+suffix); got != want {
					t.Errorf("source %s changed", src+suffix)
				}
			}
			if shm {
				requireSwapFile(t, filepath.Join(src+"-shm", "sentinel"), "leave alone")
			} else {
				requireSwapAbsent(t, src+"-shm")
			}
			if err := staged.Remove(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStageWithoutWAL(t *testing.T) {
	src := stageSource(t)
	if err := os.Remove(src + "-wal"); err != nil {
		t.Fatal(err)
	}
	staged, err := backup.Stage(t.Context(), t.TempDir(), backup.FileSet{DB: src})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenStaging(t.Context(), staged.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	seqs, err := store.Sequences(t.Context(), st.Writer)
	if err != nil || seqs["tags"] != 0 {
		t.Fatalf("DB without WAL = %v, %v; tag must exist only in WAL", seqs, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := staged.Remove(); err != nil {
		t.Fatal(err)
	}
}

func TestStageRejectsMissing(t *testing.T) {
	root := t.TempDir()
	staged, err := backup.Stage(t.Context(), root, backup.FileSet{DB: "/nope.db"})
	if !errors.Is(err, fs.ErrNotExist) || staged != nil {
		t.Fatalf("missing source = %+v, %v", staged, err)
	}
	requireStageEmpty(t, root)
}

func TestStageFailureCleanup(t *testing.T) {
	for _, failure := range []string{"corrupt DB", "DB directory", "WAL directory", "canceled"} {
		t.Run(failure, func(t *testing.T) {
			src := filepath.Join(t.TempDir(), "source.db")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch failure {
			case "corrupt DB":
				writeStageFile(t, src, "not SQLite")
			case "DB directory":
				if err := os.Mkdir(src, 0o700); err != nil {
					t.Fatal(err)
				}
			case "WAL directory":
				writeStageFile(t, src, "copy succeeds before WAL fails")
				if err := os.Mkdir(src+"-wal", 0o700); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				src = stageSource(t)
				cancel()
			}
			root := t.TempDir()
			staged, err := backup.Stage(ctx, root, backup.FileSet{DB: src})
			if err == nil || staged != nil {
				t.Fatalf("failed staging = %+v, %v", staged, err)
			}
			if failure == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled Stage = %v", err)
			}
			requireStageEmpty(t, root)
		})
	}
}

func TestPRG21_CleanupTemps(t *testing.T) {
	data, backups := t.TempDir(), t.TempDir()
	stage := filepath.Join(data, "staging", "stage-1")
	preRestore := filepath.Join(data, "taskronin.db.pre-restore-T")
	want := []string{stage, filepath.Join(data, ".tkr-tmp-a"), filepath.Join(backups, ".tkr-tmp-b.db"), filepath.Join(preRestore, ".tkr-tmp-c")}
	writeStageFile(t, filepath.Join(stage, "x.db"), "interrupted stage")
	for _, path := range want[1:] {
		writeStageFile(t, path, "temporary content")
	}
	kept := []string{filepath.Join(data, ".tkr-tmp-keep"), filepath.Join(data, "taskronin.db"), filepath.Join(backups, "taskronin-20261028T160000.000Z-abcd-manual.db"), filepath.Join(preRestore, "taskronin.db"), filepath.Join(preRestore, "taskronin.db-wal")}
	for _, path := range kept {
		writeStageFile(t, path, "keep")
	}
	j := &backup.SwapJournal{Kind: backup.SwapRestore, New: kept[0], Target: kept[1], Aside: kept[1] + ".pre-restore-next"}
	journal := filepath.Join(data, "swap.journal")
	requireSwapCrash(t, backup.Swap(swapCrashAt(t.Context(), "prepared"), journal, j, nil))
	removed, err := backup.CleanupTemps(data, backups)
	if err != nil {
		t.Fatal(err)
	}
	requireStageRemoved(t, removed, want)
	for _, path := range kept {
		requireSwapFile(t, path, "keep")
	}
	requireSwapJournal(t, journal)
	requireStageEmpty(t, filepath.Join(data, "staging"))
	removed, err = backup.CleanupTemps(data, backups)
	if err != nil || len(removed) != 0 {
		t.Fatalf("second cleanup = %v, %v", removed, err)
	}
}

func TestCleanupTempsProtectsJournalPaths(t *testing.T) {
	data, backups := t.TempDir(), t.TempDir()
	stage := filepath.Join(data, "staging", "stage-keep")
	newDB := filepath.Join(stage, "nested", "new.db")
	aside := filepath.Join(data, "taskronin.db.pre-import-T")
	discard := filepath.Join(data, ".tkr-tmp-rollback-0123456789abcdef")
	scrubNew := filepath.Join(backups, ".tkr-tmp-new.db")
	scrubAside := filepath.Join(backups, ".tkr-tmp-quarantine")
	kept := []string{newDB, filepath.Join(aside, "taskronin.db"), filepath.Join(aside, ".tkr-tmp-inside"), discard, scrubNew, filepath.Join(scrubAside, "snapshot.db-wal")}
	for _, path := range kept {
		writeStageFile(t, path, "recovery content")
	}
	want := []string{filepath.Join(stage, "unreferenced.db"), filepath.Join(stage, "nested", "unreferenced.db"), filepath.Join(data, "staging", "stage-keep-other"), filepath.Join(data, ".tkr-tmp-extra"), filepath.Join(backups, ".tkr-tmp-quarantine-other"), filepath.Join(backups, "snapshot.db.pre-import-T", ".tkr-tmp-old")}
	for _, path := range want {
		writeStageFile(t, path, "temporary content")
	}
	// Model interrupted forward/rollback journals, including a trailing slash
	// on Aside. Cleanup must preserve their complete recovery sets.
	writeStageJournal(t, filepath.Join(data, "swap.journal"), backup.SwapJournal{
		Version: 1, Kind: backup.SwapRestore, Step: "installed", New: newDB,
		Target: filepath.Join(data, "taskronin.db"), Aside: aside + "/", Rollback: "discarding", Discard: discard,
	})
	writeStageJournal(t, filepath.Join(backups, "scrub.journal"), backup.SwapJournal{
		Version: 1, Kind: backup.SwapScrub, Step: "moved-old", New: scrubNew,
		Target: filepath.Join(backups, "snapshot.db"), Aside: scrubAside, DeleteAside: true,
	})
	removed, err := backup.CleanupTemps(data, backups)
	if err != nil {
		t.Fatal(err)
	}
	requireStageRemoved(t, removed, want)
	for _, path := range kept {
		requireSwapFile(t, path, "recovery content")
	}
}

func TestCleanupTempsProtectsAliasedScrubJournal(t *testing.T) {
	for _, aliasData := range []bool{false, true} {
		name := "backup alias"
		if aliasData {
			name = "data alias"
		}
		t.Run(name, func(t *testing.T) {
			realDir := t.TempDir()
			alias := filepath.Join(t.TempDir(), "alias")
			if err := os.Symlink(realDir, alias); err != nil {
				t.Fatal(err)
			}
			data, backups := realDir, alias
			if aliasData {
				data, backups = alias, realDir
			}
			j := &backup.SwapJournal{
				Kind: backup.SwapScrub, New: filepath.Join(backups, ".tkr-tmp-new.db"),
				Target: filepath.Join(backups, "snapshot.db"), Aside: filepath.Join(backups, ".tkr-tmp-old"), DeleteAside: true,
			}
			writeStageFile(t, j.New, "NEW")
			writeStageFile(t, j.Target, "OLD")
			writeStageFile(t, j.Target+"-wal", "OLDWAL")
			journal := filepath.Join(backups, "scrub.journal")
			requireSwapCrash(t, backup.Swap(swapCrashAt(t.Context(), "moved-old"), journal, j, nil))
			extra := filepath.Join(data, ".tkr-tmp-extra")
			writeStageFile(t, extra, "unreferenced")
			removed, err := backup.CleanupTemps(data, backups)
			if err != nil {
				t.Fatal(err)
			}
			requireStageRemoved(t, removed, []string{extra})
			requireSwapFile(t, j.New, "NEW")
			requireSwapFile(t, filepath.Join(j.Aside, "snapshot.db"), "OLD")
			requireSwapFile(t, filepath.Join(j.Aside, "snapshot.db-wal"), "OLDWAL")
			if err := backup.ResumeSwap(t.Context(), journal); err != nil {
				t.Fatal(err)
			}
			requireSwapFile(t, j.Target, "NEW")
			requireSwapAbsent(t, j.New, j.Aside, journal)
		})
	}
}

func TestCleanupTempsProtectsAliasedRecoveryPaths(t *testing.T) {
	for _, missingNew := range []bool{false, true} {
		name := "new present"
		if missingNew {
			name = "new parent missing"
		}
		t.Run(name, func(t *testing.T) {
			data, backups := t.TempDir(), t.TempDir()
			alias := filepath.Join(t.TempDir(), "alias")
			if err := os.Symlink(data, alias); err != nil {
				t.Fatal(err)
			}
			stage := filepath.Join(data, "staging", "stage-keep")
			j := backup.SwapJournal{
				Version: 1, Kind: backup.SwapRestore, Step: "installed", Rollback: "discarding",
				New: filepath.Join(alias, "staging", "stage-keep", "nested", "new.db"), Target: filepath.Join(alias, "taskronin.db"),
				Aside: filepath.Join(alias, ".tkr-tmp-aside"), Discard: filepath.Join(alias, ".tkr-tmp-rollback-0123456789abcdef"),
			}
			kept := []string{filepath.Join(j.Aside, "taskronin.db"), filepath.Join(j.Aside, ".tkr-tmp-inside"), j.Discard}
			if !missingNew {
				kept = append(kept, j.New)
			}
			for _, path := range kept {
				writeStageFile(t, path, "recovery content")
			}
			want := []string{filepath.Join(stage, "unreferenced.db"), filepath.Join(data, ".tkr-tmp-aside-other")}
			for _, path := range want {
				writeStageFile(t, path, "unreferenced")
			}
			link := filepath.Join(data, ".tkr-tmp-link")
			if err := os.Symlink(j.Aside, link); err != nil {
				t.Fatal(err)
			}
			want = append(want, link)
			// The journal may outlive New or its parent after an interrupted
			// install/cleanup. Missing paths still need alias normalization.
			writeStageJournal(t, filepath.Join(data, "swap.journal"), j)
			removed, err := backup.CleanupTemps(data, backups)
			if err != nil {
				t.Fatal(err)
			}
			requireStageRemoved(t, removed, want)
			for _, path := range kept {
				requireSwapFile(t, path, "recovery content")
			}
		})
	}
}

func TestCleanupTempsRejectsUnresolvableJournalPaths(t *testing.T) {
	data, backups := t.TempDir(), t.TempDir()
	path := filepath.Join(data, "staging", "stage-1", "copy.db")
	writeStageFile(t, path, "keep until journal paths are understood")
	loop := filepath.Join(data, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	writeStageJournal(t, filepath.Join(backups, "scrub.journal"), backup.SwapJournal{
		Version: 1, Kind: backup.SwapScrub, Step: "moved-old", New: filepath.Join(loop, "new.db"),
		Target: filepath.Join(backups, "snapshot.db"), Aside: filepath.Join(backups, ".tkr-tmp-aside"), DeleteAside: true,
	})
	removed, err := backup.CleanupTemps(data, backups)
	if err == nil || len(removed) != 0 {
		t.Fatalf("cleanup with unresolvable journal path = %v, %v", removed, err)
	}
	requireSwapFile(t, path, "keep until journal paths are understood")
}

func TestCleanupTempsRejectsUnreadableJournalsBeforeDeleting(t *testing.T) {
	for _, name := range []string{"swap.journal", "scrub.journal"} {
		t.Run(name, func(t *testing.T) {
			data, backups := t.TempDir(), t.TempDir()
			path := filepath.Join(data, "staging", "stage-1", "copy.db")
			writeStageFile(t, path, "keep until journal is understood")
			journalDir := data
			if name == "scrub.journal" {
				journalDir = backups
			}
			writeStageFile(t, filepath.Join(journalDir, name), "broken JSON")
			removed, err := backup.CleanupTemps(data, backups)
			if err == nil || len(removed) != 0 {
				t.Fatalf("cleanup with bad journal = %v, %v", removed, err)
			}
			requireSwapFile(t, path, "keep until journal is understood")
		})
	}
}

func TestCleanupTempsMissingDirectories(t *testing.T) {
	root := t.TempDir()
	removed, err := backup.CleanupTemps(filepath.Join(root, "data"), filepath.Join(root, "backups"))
	if err != nil || len(removed) != 0 {
		t.Fatalf("cleanup missing dirs = %v, %v", removed, err)
	}
	requireStageEmpty(t, root)
}

func TestCleanupTempsDoesNotFollowSymlinks(t *testing.T) {
	data, backups, outside := t.TempDir(), t.TempDir(), t.TempDir()
	content := filepath.Join(outside, ".tkr-tmp-external")
	writeStageFile(t, content, "external content")
	stageLink := filepath.Join(data, "staging", "stage-link")
	if err := os.Mkdir(filepath.Dir(stageLink), 0o700); err != nil {
		t.Fatal(err)
	}
	tempLink := filepath.Join(backups, ".tkr-tmp-link")
	recoveryLink := filepath.Join(data, "taskronin.db.pre-restore-link")
	for _, path := range []string{stageLink, tempLink, recoveryLink} {
		if err := os.Symlink(outside, path); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := backup.CleanupTemps(data, backups)
	if err != nil {
		t.Fatal(err)
	}
	requireStageRemoved(t, removed, []string{stageLink, tempLink})
	requireSwapFile(t, content, "external content")
	if _, err := os.Lstat(recoveryLink); err != nil {
		t.Fatal(err)
	}
}

func stageSource(t *testing.T) string {
	t.Helper()
	st := testutil.Store(t)
	if err := st.CheckpointTruncate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer.ExecContext(t.Context(), `INSERT INTO tags (name, created_at) VALUES (?, ?)`, "walonly", 0); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "src", "taskronin.db")
	for _, suffix := range []string{"", "-wal"} {
		contents, err := os.ReadFile(st.Path + suffix)
		if err != nil || len(contents) == 0 {
			t.Fatalf("read WAL fixture %s: size=%d, %v", suffix, len(contents), err)
		}
		writeStageFile(t, src+suffix, string(contents))
	}
	return src
}

func stageFileHash(t *testing.T, path string) [sha256.Size]byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(contents)
}

func writeStageFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeStageJournal(t *testing.T, path string, j backup.SwapJournal) {
	t.Helper()
	contents, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	writeStageFile(t, path, string(contents))
}

func requireStageEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("directory %s = %v, %v; want empty", dir, entries, err)
	}
}

func requireStageRemoved(t *testing.T, got, want []string) {
	t.Helper()
	slices.Sort(got)
	want = slices.Sorted(slices.Values(want))
	if !slices.Equal(got, want) {
		t.Fatalf("removed = %v; want %v", got, want)
	}
	requireSwapAbsent(t, got...)
}
