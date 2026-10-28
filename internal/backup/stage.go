package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"taskronin/internal/store"
)

// FileSet names a database and its optional -wal sibling. SHM is not copied.
type FileSet struct{ DB string }

// Staged owns a disposable directory containing a self-contained database.
type Staged struct{ Dir, Path string }

// Stage copies a quiescent file set into an existing staging directory and
// recovers its WAL. Only the copy is opened with SQLite; the source is read-only.
func Stage(ctx context.Context, stagingDir string, set FileSet) (_ *Staged, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stagingDir, err = filepath.Abs(stagingDir)
	if err != nil {
		return nil, fmt.Errorf("backup: resolve staging directory: %w", err)
	}
	dir, err := os.MkdirTemp(stagingDir, "stage-*")
	if err != nil {
		return nil, fmt.Errorf("backup: create staging directory: %w", err)
	}
	sourcePath := set.DB
	s := &Staged{Dir: dir, Path: filepath.Join(dir, filepath.Base(sourcePath))}
	defer func() {
		if err != nil {
			err = errors.Join(err, s.Remove())
		}
	}()
	src, err := os.Open(sourcePath) //nolint:gosec // G304: snapshot path chosen by the operator or listed from the backup dir
	if err != nil {
		return nil, fmt.Errorf("backup: open staging source: %w", err)
	}
	if err := copyStageFile(ctx, s.Path, src); err != nil {
		return nil, err
	}
	wal, err := os.Open(sourcePath + "-wal") //nolint:gosec // G304: optional WAL sibling of the operator-selected snapshot
	if err == nil {
		if err := copyStageFile(ctx, s.Path+"-wal", wal); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("backup: open staging WAL: %w", err)
	}
	// AIDEV-NOTE: normal DELETE-mode open recovers committed WAL frames before
	// checkpointing them away. Opening the source or using immutable=1 here
	// would respectively modify the original or silently lose WAL-only changes.
	st, err := store.OpenStaging(ctx, s.Path)
	if err != nil {
		return nil, fmt.Errorf("backup: open staged database: %w", err)
	}
	if err := st.Close(); err != nil {
		return nil, fmt.Errorf("backup: close staged database: %w", err)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(s.Path + suffix); err == nil {
			return nil, fmt.Errorf("backup: staged sidecar remains: %s", s.Path+suffix)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("backup: check staged sidecar: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s, nil
}

// copyStageFile takes ownership of src, closing both files before returning.
func copyStageFile(ctx context.Context, path string, src *os.File) (err error) {
	defer func() { err = errors.Join(err, src.Close()) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := src.Stat()
	if err != nil {
		return fmt.Errorf("backup: stat staging source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("backup: staging source is not a regular file: %s", src.Name())
	}
	dst, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: exclusive copy inside the fresh staging directory
	if err != nil {
		return fmt.Errorf("backup: create staging copy: %w", err)
	}
	defer func() { err = errors.Join(err, dst.Close()) }()
	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("backup: copy staging source: %w", err)
	}
	return ctx.Err()
}

// Remove deletes the entire disposable directory. It tolerates repeated calls.
func (s *Staged) Remove() error {
	if err := os.RemoveAll(s.Dir); err != nil {
		return fmt.Errorf("backup: remove staging directory: %w", err)
	}
	return nil
}

// CleanupTemps removes interrupted temporary artifacts, preserving journaled
// recovery paths. Callers must hold the instance lock throughout cleanup.
// The returned paths are removed files or whole removed directory trees.
func CleanupTemps(dataDir, backupDir string) (removed []string, err error) {
	dataDir, err = filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("backup: resolve cleanup data directory: %w", err)
	}
	backupDir, err = filepath.Abs(backupDir)
	if err != nil {
		return nil, fmt.Errorf("backup: resolve cleanup backup directory: %w", err)
	}
	// AIDEV-NOTE: validate BOTH journals before removing anything. A malformed
	// journal cannot safely tell us which copies are still needed for recovery.
	var protected []string
	for _, path := range []string{filepath.Join(dataDir, "swap.journal"), filepath.Join(backupDir, "scrub.journal")} {
		j, err := ReadJournal(path)
		if errors.Is(err, ErrNoJournal) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("backup: inspect cleanup journal %s: %w", path, err)
		}
		for _, ref := range []string{j.New, j.Aside, j.Discard} {
			if ref == "" {
				continue
			}
			ref, err = cleanupLocation(ref)
			if err != nil {
				return nil, fmt.Errorf("backup: resolve journal reference: %w", err)
			}
			protected = append(protected, ref)
		}
	}
	if err := cleanupTempDir(filepath.Join(dataDir, "staging"), true, protected, &removed); err != nil {
		return removed, err
	}
	if err := cleanupTempDir(dataDir, false, protected, &removed); err != nil {
		return removed, err
	}
	if backupDir != dataDir {
		err = cleanupTempDir(backupDir, false, protected, &removed)
	}
	return removed, err
}

func cleanupTempDir(dir string, all bool, protected []string, removed *[]string) error {
	if all {
		info, err := os.Lstat(dir)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("backup: inspect staging directory: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("backup: staging path is not a directory: %s", dir)
		}
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("backup: list temporary artifacts: %w", err)
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if all || strings.HasPrefix(entry.Name(), ".tkr-tmp-") {
			if err := removeUnreferencedTemp(path, protected, removed); err != nil {
				return err
			}
		} else if entry.IsDir() && (strings.Contains(entry.Name(), ".pre-restore-") || strings.Contains(entry.Name(), ".pre-import-")) {
			if err := cleanupTempDir(path, false, protected, removed); err != nil {
				return err
			}
		}
	}
	return nil
}

func removeUnreferencedTemp(path string, protected []string, removed *[]string) error {
	location, err := cleanupLocation(path)
	if err != nil {
		return fmt.Errorf("backup: resolve temporary artifact: %w", err)
	}
	containsProtected := false
	for _, ref := range protected {
		if location == ref || strings.HasPrefix(location, ref+string(filepath.Separator)) {
			return nil
		}
		if strings.HasPrefix(ref, location+string(filepath.Separator)) {
			containsProtected = true
		}
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("backup: inspect temporary artifact: %w", err)
	}
	// A referenced DB may be nested inside a stage directory. Keep its parents
	// but still remove unrelated siblings; never follow a directory symlink.
	if containsProtected && info.IsDir() {
		return cleanupTempDir(path, true, protected, removed)
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("backup: remove temporary artifact: %w", err)
	}
	*removed = append(*removed, path)
	return nil
}

// AIDEV-NOTE: compare filesystem locations across directory symlink aliases,
// but resolve only the parent: the final entry may itself be a symlink that
// cleanup must unlink without following. Deletions always use the original path.
// Journal references can already be renamed away or not yet created, so resolve
// the longest existing parent and retain any missing suffix for comparison.
func cleanupLocation(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	parent, suffix := filepath.Dir(path), filepath.Base(path)
	for {
		resolved, err := filepath.EvalSymlinks(parent)
		if err == nil {
			return filepath.Join(resolved, suffix), nil
		}
		next := filepath.Dir(parent)
		if !errors.Is(err, fs.ErrNotExist) || next == parent {
			return "", err
		}
		suffix = filepath.Join(filepath.Base(parent), suffix)
		parent = next
	}
}
