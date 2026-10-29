package offline

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"taskronin/internal/config"
	"taskronin/internal/domain"
	"taskronin/internal/fsutil"
	"taskronin/internal/textsafe"
)

// Recovery-file modes recorded on ledger complete lines (A§12.6 step 8).
const (
	recoveryKept    = "kept"
	recoveryNone    = "none"
	recoveryDeleted = "deleted"
)

// CleanRecoveryFiles handles this host's client recovery files, which may hold
// purged text: saved pending requests and editor drafts (A§12.6 step 8, R4-1).
// keep returns "kept" without touching anything; no files returns "none";
// otherwise the user is asked (default yes, skipped with assumeYes) and the
// files are deleted ("deleted"). Answering no returns ("", nil): the caller must
// then leave the ledger records pending.
//
// AIDEV-NOTE: SECURITY. Every non-directory entry counts, not only *.json/*.md:
// an interrupted atomic write leaves a temp file with the same body. Deletion
// removes the directory entry itself (never follows a symlink).
func CleanRecoveryFiles(ctx context.Context, paths config.Paths, keep, assumeYes bool,
	prompt func(q string, defaultYes bool) (bool, error)) (mode string, err error) {
	if keep {
		return recoveryKept, nil
	}
	pending, err := recoveryEntries(paths.PendingDir)
	if err != nil {
		return "", err
	}
	drafts, err := recoveryEntries(paths.DraftsDir)
	if err != nil {
		return "", err
	}
	if len(pending)+len(drafts) == 0 {
		return recoveryNone, nil
	}
	if !assumeYes {
		if prompt == nil {
			return "", domain.Errorf(domain.CodeInternal, "confirmation reader is not configured")
		}
		ok, err := prompt(fmt.Sprintf("Delete %d pending request(s) and %d draft(s) that may hold purged text?", len(pending), len(drafts)), true)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", nil
		}
	}
	for _, path := range append(pending, drafts...) {
		if err := ctx.Err(); err != nil {
			return "", offlineError(err)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", domain.Errorf(domain.CodeInternal, "delete %s: %s", textsafe.SanitizeLine(path), textsafe.SanitizeLine(err.Error()))
		}
	}
	for _, dir := range []string{paths.PendingDir, paths.DraftsDir} {
		if err := syncDirIfExists(dir); err != nil {
			return "", err
		}
	}
	return recoveryDeleted, nil
}

// recoveryEntries lists the non-directory entries of dir; a missing dir is empty.
func recoveryEntries(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, offlineError(err)
	}
	var out []string
	for _, entry := range entries {
		if !entry.IsDir() {
			out = append(out, filepath.Join(dir, entry.Name()))
		}
	}
	return out, nil
}

func syncDirIfExists(dir string) error {
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return offlineError(fsutil.FsyncDir(dir))
}
