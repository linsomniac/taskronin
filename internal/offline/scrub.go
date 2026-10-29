package offline

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"taskronin/internal/backup"
	"taskronin/internal/config"
	"taskronin/internal/domain"
	"taskronin/internal/fsutil"
	"taskronin/internal/service"
	"taskronin/internal/store"
	"taskronin/internal/textsafe"
)

// Per-set scrub statuses.
const (
	scrubClean     = "clean"
	scrubContains  = "contains"
	scrubRewritten = "rewritten"
	scrubError     = "error"
)

// SetResult is the outcome for one backup file set.
type SetResult struct{ Path, Status, Err string } // Status: clean | contains | rewritten | error

// ScrubOptions are the flags of `tkr scrub-backups`.
type ScrubOptions struct{ Check, Resume, KeepRecoveryFiles, Yes bool }

// ScrubBackups is A§12.7: apply the purge ledger to every backup set.
//
// AIDEV-NOTE: SECURITY-CRITICAL ORDER (R4-8, R2-7). Do not reorder:
//  1. begin(allowScrubJournal): lock, refuse swap.journal, and NO CleanupTemps
//     yet (a scrub.journal's New/Aside must survive until it is resumed).
//  2. password, before any file is touched; only then ResumeSwap of an existing
//     scrub.journal, and only then CleanupTemps.
//  3. OpenLedger once (BF26) -> replay live + confirmed checkpoint -> prescrub
//     snapshot (taken AFTER the live scrub, so it is clean) -> scrubSets.
//     A set judged clean by the logical replay but whose ORIGINAL -wal/-shm is
//     non-empty is NOT clean (superseded WAL frames may hold purged text): it
//     is reported "contains" under --check and rewritten through the swap
//     otherwise, which removes the sidecars.
//  4. Completion lines are appended LAST, only when every set is clean/rewritten,
//     the ledger is healthy and CleanRecoveryFiles returned a mode. Any earlier
//     exit leaves the records pending so the next run finishes the job.
//
// Check mode (P9) never snapshots, never cleans recovery files and never writes
// the ledger.
func ScrubBackups(ctx context.Context, e *Env, o ScrubOptions) (results []SetResult, err error) {
	release, err := e.begin(ctx, allowScrubJournal)
	if err != nil {
		return nil, err
	}
	defer release()
	if e.Server == nil {
		return nil, domain.Errorf(domain.CodeInternal, "server configuration is required")
	}
	st, err := e.openStore(ctx)
	if err != nil {
		return nil, err
	}
	closeStore := func() error {
		if st == nil {
			return nil
		}
		cerr := offlineError(st.Close())
		st = nil
		return cerr
	}
	defer func() { err = errors.Join(err, closeStore()) }()
	if _, err := st.SchemaStatus(ctx); err != nil {
		return nil, offlineError(err)
	}
	if err := e.verifyPassword(ctx, st.Reader); err != nil {
		return nil, err
	}
	resumed, err := e.resumeScrubJournal(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := backup.CleanupTemps(e.Paths.DataDir, e.Paths.BackupDir); err != nil {
		return nil, offlineError(err)
	}
	ledger, health, err := backup.OpenLedger(e.Paths.LedgerFile)
	if err != nil {
		return nil, offlineError(err)
	}
	recs, err := ledger.Records()
	if err != nil {
		return nil, offlineError(err)
	}
	svc, err := e.newService(st, ledger)
	if err != nil {
		return nil, err
	}
	if err := svc.ReapplyLedger(ctx); err != nil {
		return nil, err
	}
	if err := checkpoint(ctx, st); err != nil {
		return nil, err
	}
	var skip []string
	if !o.Check {
		if err := fsutil.EnsureDir0700(e.Paths.BackupDir); err != nil {
			return nil, offlineError(err)
		}
		manager := backup.Manager{Dir: e.Paths.BackupDir, Clock: e.clock(), Snap: st.Snap}
		info, err := manager.Snapshot(ctx, backup.KindPrescrub)
		if err != nil {
			return nil, offlineError(err)
		}
		skip = append(skip, info.Path)
	}
	if err := closeStore(); err != nil {
		return nil, err
	}
	results, scrubErr := scrubSets(ctx, e, recs, o.Check, skip...)
	for i := range results {
		if results[i].Status == scrubClean && resumed[filepath.Clean(results[i].Path)] {
			results[i].Status = scrubRewritten // the resumed swap installed the scrubbed file
		}
	}
	e.printResults(results)
	if scrubErr != nil {
		return results, scrubErr
	}
	failed := 0
	for _, r := range results {
		if r.Status == scrubError {
			failed++
		}
	}
	if failed != 0 {
		return results, domain.Errorf(domain.CodeInternal, "%d backup set(s) could not be scrubbed; nothing was marked complete", failed)
	}
	if o.Check {
		return results, nil
	}
	return results, e.completeScrub(ctx, ledger, health, results, o)
}

// resumeScrubJournal finishes an interrupted scrub swap (always, A§12.7 step 2;
// --resume only makes it explicit) and returns the cleaned paths of its target.
func (e *Env) resumeScrubJournal(ctx context.Context) (map[string]bool, error) {
	journalPath := config.ScrubJournal(e.Paths.BackupDir)
	j, err := backup.ReadJournal(journalPath)
	if errors.Is(err, backup.ErrNoJournal) {
		return nil, nil
	}
	if err != nil {
		return nil, offlineError(err)
	}
	if j.Kind != backup.SwapScrub {
		return nil, domain.Errorf(domain.CodeConflict, "the journal in the backup directory is for a %s, not a scrub", j.Kind)
	}
	if err := backup.ResumeSwap(ctx, journalPath); err != nil {
		return nil, swapRecoveryError(err)
	}
	if _, err := fmt.Fprintf(e.Err, "resumed the interrupted scrub of %s\n", textsafe.SanitizeLine(snapshotLabel(backup.FileSet{DB: j.Target}))); err != nil {
		return nil, offlineError(err)
	}
	return map[string]bool{filepath.Clean(j.Target): true}, nil
}

func (e *Env) printResults(results []SetResult) {
	for _, r := range results {
		line := textsafe.SanitizeLine(snapshotLabel(backup.FileSet{DB: r.Path})) + ": " + r.Status
		if r.Err != "" {
			line += " (" + textsafe.SanitizeLine(r.Err) + ")"
		}
		_, _ = fmt.Fprintln(e.Out, line)
	}
}

// completeScrub is A§12.7 step 7. results are all clean or rewritten here.
func (e *Env) completeScrub(ctx context.Context, ledger *backup.Ledger, health backup.LedgerHealth, results []SetResult, o ScrubOptions) error {
	for _, r := range results {
		if r.Status != scrubClean && r.Status != scrubRewritten {
			return nil
		}
	}
	pending, err := ledger.Pending()
	if err != nil {
		return offlineError(err)
	}
	if len(pending) == 0 {
		return nil
	}
	if health.Damaged {
		_, err := fmt.Fprintln(e.Out, "the purge ledger is damaged, so the purge cannot be marked complete: run `tkr purge --repair-ledger`")
		return offlineError(err)
	}
	mode, err := CleanRecoveryFiles(ctx, e.Paths, o.KeepRecoveryFiles, o.Yes, e.Confirm)
	if err != nil {
		return err
	}
	if mode == "" {
		_, err := fmt.Fprintln(e.Out, "recovery files were kept: re-run with --keep-recovery-files to complete without deleting them")
		return offlineError(err)
	}
	for _, rec := range pending {
		if err := ctx.Err(); err != nil {
			return offlineError(err)
		}
		if err := ledger.Append(backup.LedgerRecord{V: 1, Rec: rec.Rec, At: e.clock().Now().UnixMilli(),
			State: backup.LedgerComplete, RecoveryFiles: mode}); err != nil {
			return offlineError(err)
		}
	}
	return nil
}

// hasSidecarBytes reports whether the ORIGINAL set has a non-empty -wal or -shm.
// The staged copy had its WAL recovered and checkpointed away, so a logical
// "no change" says nothing about superseded frames still in those files.
func hasSidecarBytes(path string) bool {
	for _, suffix := range []string{"-wal", "-shm"} {
		// Any stat error other than "absent" is treated as present: rewriting is the safe side.
		if fi, err := os.Lstat(path + suffix); err == nil && (fi.Size() > 0 || !fi.Mode().IsRegular()) {
			return true
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return true
		}
	}
	return false
}

// scrubSets is A§12.7 steps 5-6 over every snapshot and pre-restore/pre-import
// set (also used by purge). Paths in skip (the fresh prescrub snapshot) are not
// visited. It stops at the first failure that leaves a journal; other failures
// are recorded per set and the batch continues.
func scrubSets(ctx context.Context, e *Env, recs []backup.LedgerRecord, check bool, skip ...string) ([]SetResult, error) {
	paths, err := e.scrubTargets(skip)
	if err != nil {
		return nil, err
	}
	if len(paths) != 0 {
		if err := fsutil.EnsureDir0700(e.Paths.StagingDir); err != nil {
			return nil, offlineError(err)
		}
	}
	var results []SetResult
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return results, offlineError(err)
		}
		status, err := e.scrubOne(ctx, recs, path, check)
		res := SetResult{Path: path, Status: status}
		if err != nil {
			res.Status, res.Err = scrubError, err.Error()
		}
		results = append(results, res)
		if err != nil {
			if _, jerr := os.Lstat(config.ScrubJournal(e.Paths.BackupDir)); jerr == nil {
				return results, err // an interrupted swap must be resumed first (R2-7)
			}
			if cerr := ctx.Err(); cerr != nil {
				return results, offlineError(cerr)
			}
		}
	}
	return results, nil
}

// scrubTargets lists every snapshot file, then the taskronin.db of every
// *.pre-restore-*/*.pre-import-* directory in the data and backup dirs.
func (e *Env) scrubTargets(skip []string) ([]string, error) {
	skipSet := make(map[string]bool, len(skip))
	for _, s := range skip {
		skipSet[filepath.Clean(s)] = true
	}
	var paths []string
	infos, err := backup.List(e.Paths.BackupDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, offlineError(err)
	}
	for _, info := range infos {
		if !skipSet[filepath.Clean(info.Path)] {
			paths = append(paths, info.Path)
		}
	}
	dirs := []string{e.Paths.DataDir}
	if filepath.Clean(e.Paths.BackupDir) != filepath.Clean(e.Paths.DataDir) {
		dirs = append(dirs, e.Paths.BackupDir)
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, offlineError(err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if !entry.IsDir() || strings.HasPrefix(name, ".tkr-tmp-") ||
				(!strings.Contains(name, ".pre-restore-") && !strings.Contains(name, ".pre-import-")) {
				continue
			}
			db := filepath.Join(dir, name, "taskronin.db")
			if fi, err := os.Lstat(db); err == nil && fi.Mode().IsRegular() {
				paths = append(paths, db)
			}
		}
	}
	return paths, nil
}

// scrubOne is A§12.7 step 5 for one set. Every staged copy and temporary file
// holds unscrubbed content, so each is removed on every path out.
func (e *Env) scrubOne(ctx context.Context, recs []backup.LedgerRecord, path string, check bool) (status string, err error) {
	staged, err := backup.Stage(ctx, e.Paths.StagingDir, backup.FileSet{DB: path})
	if err != nil {
		return "", offlineError(err)
	}
	defer func() { err = errors.Join(err, staged.Remove()) }()
	st, err := store.OpenStaging(ctx, staged.Path)
	if err != nil {
		return "", offlineError(err)
	}
	closed := false
	closeStaged := func() error {
		if closed {
			return nil
		}
		closed = true
		return offlineError(st.Close())
	}
	defer func() { err = errors.Join(err, closeStaged()) }()
	if _, err := st.SchemaStatus(ctx); err != nil {
		return "", offlineError(err)
	}
	// R3-6: an old-schema set must replay with current columns.
	if _, err := st.Migrate(ctx, nil); err != nil {
		return "", offlineError(err)
	}
	svc, err := e.newService(st, nil)
	if err != nil {
		return "", err
	}
	res, err := svc.ReplayLedger(ctx, recs, service.ReplayHistorical)
	if err != nil {
		return "", err
	}
	if res.NoChange && !hasSidecarBytes(path) {
		return scrubClean, nil
	}
	if check {
		return scrubContains, nil
	}
	if err := service.CheckStructural(ctx, st.Writer); err != nil {
		return "", err
	}
	if err := store.IntegrityCheck(ctx, st.Writer); err != nil {
		return "", offlineError(err)
	}
	if err := store.FTSIntegrityCheck(ctx, st.Writer); err != nil {
		return "", offlineError(err)
	}
	return scrubRewritten, e.swapScrubbed(ctx, st, path, closeStaged)
}

// swapScrubbed writes the scrubbed content of st with VACUUM INTO (which copies
// only live content: no free pages, no WAL frames) beside target and installs it
// with the journaled swap, deleting the old set.
func (e *Env) swapScrubbed(ctx context.Context, st *store.Store, target string, closeStaged func() error) (err error) {
	dir := filepath.Dir(target)
	f, err := os.CreateTemp(dir, ".tkr-tmp-*.db")
	if err != nil {
		return offlineError(err)
	}
	tmp := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return offlineError(err)
	}
	aside := ""
	swapStarted := false
	defer func() {
		if swapStarted && err != nil {
			if _, jerr := os.Lstat(config.ScrubJournal(e.Paths.BackupDir)); jerr == nil {
				return // the journal owns New and Aside; --resume finishes them
			}
		}
		if err != nil {
			err = errors.Join(err, removeAll(tmp), removeAll(aside))
		}
	}()
	if _, err := st.Writer.ExecContext(ctx, "VACUUM INTO ?", tmp); err != nil {
		return offlineError(err)
	}
	if err := closeStaged(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return offlineError(err)
	}
	if _, err := backup.Verify(ctx, tmp); err != nil {
		return offlineError(err)
	}
	if err := syncFile(tmp); err != nil {
		return err
	}
	aside, err = os.MkdirTemp(dir, ".tkr-tmp-scrub-*")
	if err != nil {
		return offlineError(err)
	}
	swapStarted = true
	return backup.Swap(ctx, config.ScrubJournal(e.Paths.BackupDir), &backup.SwapJournal{
		Kind: backup.SwapScrub, New: tmp, Target: target, Aside: aside, DeleteAside: true,
		StartedAt: e.clock().Now().UTC(),
	}, nil)
}

func removeAll(path string) error {
	if path == "" {
		return nil
	}
	return offlineError(os.RemoveAll(path))
}

func syncFile(path string) error {
	f, err := os.Open(path) //nolint:gosec // G304: a scrub output this package just created.
	if err != nil {
		return offlineError(err)
	}
	return offlineError(errors.Join(f.Sync(), f.Close()))
}
