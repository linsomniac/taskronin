package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"taskronin/internal/clock"
	"taskronin/internal/fsutil"
)

// SwapKind identifies the offline operation replacing a database file set.
type SwapKind string

// Supported journaled swap operations.
const (
	SwapRestore SwapKind = "restore"
	SwapImport  SwapKind = "import"
	SwapScrub   SwapKind = "scrub"
)

var (
	// ErrNoJournal indicates there is no interrupted swap to recover.
	ErrNoJournal = errors.New("backup: no swap journal")
	// ErrNoRollback indicates a scrub or a swap whose old set was deleted.
	ErrNoRollback = errors.New("backup: this swap cannot be rolled back")
	// ErrRollbackInProgress prevents forward recovery once rollback has begun.
	ErrRollbackInProgress = errors.New("backup: rollback in progress; run `tkr restore --rollback`")
)

// SwapJournal records durable progress. New must be a verified, fsynced,
// self-contained DELETE-mode database. Callers hold the instance lock throughout
// Swap, ResumeSwap and RollbackSwap, and keep all database connections closed.
type SwapJournal struct {
	Version           int       `json:"version"`
	Kind              SwapKind  `json:"kind"`
	Step              string    `json:"step"`
	New               string    `json:"new"`
	Target            string    `json:"target"`
	Aside             string    `json:"aside"`
	DeleteAside       bool      `json:"delete_aside"`
	OwnerTokenPending bool      `json:"owner_token_pending"`
	StartedAt         time.Time `json:"started_at"`
	Rollback          string    `json:"rollback"` // "", started, discarding, new-discarded, old-restored
	Discard           string    `json:"discard"`  // journaled before the installed file moves here
}

type swapHookKey struct{}

// WithStepHook installs a crash-test hook scoped to ctx. A hook error returns
// immediately without cleanup, leaving recovery artifacts exactly where they are.
// Rollback member hooks use file basenames, e.g. rollback:moved:taskronin.db-wal.
func WithStepHook(ctx context.Context, h func(point string) error) context.Context {
	return context.WithValue(ctx, swapHookKey{}, h)
}

func swapPoint(ctx context.Context, point string) error {
	if h, _ := ctx.Value(swapHookKey{}).(func(string) error); h != nil {
		if err := h(point); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// Swap starts a journaled replacement. An outstanding journal must be recovered
// first. onInstalled runs once after installation and before journal removal;
// ResumeSwap never replays it, even if it failed or was interrupted.
//
// AIDEV-NOTE: prepared -> moved-old -> installed -> (scrub: aside-deleted).
// Each action and both affected directories are synced before advancing the
// atomic journal. A missing source with a present destination means a rename
// already happened: old members in Aside are skipped individually, and New
// absent with Target present at moved-old means installation finished. Deletion
// tolerates absent members. Rollback journals its discard destination BEFORE
// renaming the installed file, then restores old members individually; replay
// must never mistake an already-restored Target for the new file to discard.
func Swap(ctx context.Context, journalPath string, j *SwapJournal, onInstalled func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if exists, err := swapExists(journalPath); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("backup: swap journal already exists: %s", journalPath)
	}
	if j == nil {
		return errors.New("backup: nil swap journal")
	}
	if j.Step != "" || j.Rollback != "" || j.Discard != "" {
		return errors.New("backup: a new swap must not contain recovery progress")
	}
	if j.Version == 0 {
		j.Version = 1
	}
	j.Step = "prepared"
	if j.StartedAt.IsZero() {
		j.StartedAt = (clock.Real{}).Now().UTC()
	}
	if err := validateSwapJournal(j); err != nil {
		return err
	}
	// A caller may reserve an empty quarantine directory with MkdirTemp.
	entries, err := os.ReadDir(j.Aside)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("backup: inspect swap aside: %w", err)
	}
	if len(entries) != 0 {
		return errors.New("backup: swap aside directory is not empty")
	}
	if err := writeSwapJournal(ctx, journalPath, j, "prepared"); err != nil {
		return err
	}
	return finishSwap(ctx, journalPath, j, onInstalled)
}

// ReadJournal reads recovery metadata without modifying it.
func ReadJournal(path string) (*SwapJournal, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: journal path derived from the configured data/backup dir
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoJournal
	}
	if err != nil {
		return nil, fmt.Errorf("backup: read swap journal: %w", err)
	}
	var j SwapJournal
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fmt.Errorf("backup: decode swap journal: %w", err)
	}
	if err := validateSwapJournal(&j); err != nil {
		return nil, err
	}
	return &j, nil
}

// ResumeSwap completes a forward swap without invoking its original callback.
// Callers inspect OwnerTokenPending before resuming to handle lost owner tokens.
func ResumeSwap(ctx context.Context, journalPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	j, err := ReadJournal(journalPath)
	if err != nil {
		return err
	}
	if j.Rollback != "" {
		return ErrRollbackInProgress
	}
	return finishSwap(ctx, journalPath, j, nil)
}

func finishSwap(ctx context.Context, path string, j *SwapJournal, onInstalled func() error) error {
	if j.Step == "prepared" {
		if err := fsutil.EnsureDir0700(j.Aside); err != nil {
			return err
		}
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if err := moveSwapFile(j.Target+suffix, filepath.Join(j.Aside, filepath.Base(j.Target)+suffix), false); err != nil {
				return err
			}
		}
		if err := syncSwapDirs(j.Aside, swapParent(j.Aside), swapParent(j.Target)); err != nil {
			return err
		}
		if err := swapPoint(ctx, "moved-old:done"); err != nil {
			return err
		}
		j.Step = "moved-old"
		if err := writeSwapJournal(ctx, path, j, j.Step); err != nil {
			return err
		}
	}
	if j.Step == "moved-old" {
		if err := moveSwapFile(j.New, j.Target, true); err != nil {
			return err
		}
		if err := syncSwapDirs(swapParent(j.Target), swapParent(j.New)); err != nil {
			return err
		}
		if err := swapPoint(ctx, "installed:done"); err != nil {
			return err
		}
		j.Step = "installed"
		if err := writeSwapJournal(ctx, path, j, j.Step); err != nil {
			return err
		}
	}
	if j.Step == "installed" {
		if onInstalled != nil {
			if err := onInstalled(); err != nil {
				return err
			}
		}
		if err := swapPoint(ctx, "on-installed"); err != nil {
			return err
		}
		if j.DeleteAside {
			for _, suffix := range []string{"", "-wal", "-shm"} {
				if err := removeSwapPath(filepath.Join(j.Aside, filepath.Base(j.Target)+suffix)); err != nil {
					return err
				}
			}
			if err := syncSwapAside(j); err != nil {
				return err
			}
			if err := removeSwapPath(j.Aside); err != nil {
				return err
			}
			if err := fsutil.FsyncDir(swapParent(j.Aside)); err != nil {
				return err
			}
			if err := swapPoint(ctx, "aside-deleted:done"); err != nil {
				return err
			}
			j.Step = "aside-deleted"
			if err := writeSwapJournal(ctx, path, j, j.Step); err != nil {
				return err
			}
		}
	}
	return removeSwapJournal(path)
}

// RollbackSwap restores a restore/import's old file set. Its own progress is
// journaled so a second rollback safely resumes after any interrupted rename.
func RollbackSwap(ctx context.Context, journalPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	j, err := ReadJournal(journalPath)
	if err != nil {
		return err
	}
	if (j.Kind != SwapRestore && j.Kind != SwapImport) || j.Step == "aside-deleted" {
		return ErrNoRollback
	}
	if j.Rollback == "" {
		j.Rollback = "started"
		if err := writeSwapJournal(ctx, journalPath, j, "rollback:started"); err != nil {
			return err
		}
	}
	if j.Rollback == "started" {
		installed := j.Step == "installed"
		if j.Step == "moved-old" {
			// installed:done leaves moved-old in the journal. The same
			// absence test used by forward recovery detects the new Target.
			newExists, err := swapExists(j.New)
			if err != nil {
				return err
			}
			installed = !newExists
		}
		if installed {
			j.Discard, err = newSwapDiscard(ctx, j.Target)
			if err != nil {
				return err
			}
			j.Rollback = "discarding"
			if err := writeSwapJournal(ctx, journalPath, j, "rollback:discarding"); err != nil {
				return err
			}
		} else {
			j.Rollback = "new-discarded"
			if err := writeSwapJournal(ctx, journalPath, j, "rollback:new-discarded"); err != nil {
				return err
			}
		}
	}
	if j.Rollback == "discarding" {
		if err := moveSwapFile(j.Target, j.Discard, true); err != nil {
			return err
		}
		if err := fsutil.FsyncDir(swapParent(j.Target)); err != nil {
			return err
		}
		if err := swapPoint(ctx, "rollback:new-discarded:done"); err != nil {
			return err
		}
		j.Rollback = "new-discarded"
		if err := writeSwapJournal(ctx, journalPath, j, "rollback:new-discarded"); err != nil {
			return err
		}
	}
	if j.Rollback == "new-discarded" {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			member := filepath.Base(j.Target) + suffix
			if err := moveSwapFile(filepath.Join(j.Aside, member), j.Target+suffix, false); err != nil {
				return err
			}
			if err := syncSwapAside(j); err != nil {
				return err
			}
			if err := fsutil.FsyncDir(swapParent(j.Target)); err != nil {
				return err
			}
			if err := swapPoint(ctx, "rollback:moved:"+member); err != nil {
				return err
			}
		}
		j.Rollback = "old-restored"
		if err := writeSwapJournal(ctx, journalPath, j, "rollback:old-restored"); err != nil {
			return err
		}
	}
	if j.Discard != "" {
		if err := removeSwapPath(j.Discard); err != nil {
			return err
		}
		if err := fsutil.FsyncDir(swapParent(j.Discard)); err != nil {
			return err
		}
	}
	if err := removeSwapPath(j.Aside); err != nil {
		return err
	}
	if err := fsutil.FsyncDir(swapParent(j.Aside)); err != nil {
		return err
	}
	return removeSwapJournal(journalPath)
}

func writeSwapJournal(ctx context.Context, path string, j *SwapJournal, point string) error {
	data, err := json.Marshal(j)
	if err != nil {
		return fmt.Errorf("backup: encode swap journal: %w", err)
	}
	if err := fsutil.WriteFileAtomic(path, data, 0o600); err != nil {
		return err
	}
	return swapPoint(ctx, point)
}

func validateSwapJournal(j *SwapJournal) error {
	if j.Version != 1 {
		return fmt.Errorf("backup: unsupported swap journal version %d", j.Version)
	}
	switch j.Kind {
	case SwapRestore, SwapImport, SwapScrub:
	default:
		return fmt.Errorf("backup: invalid swap kind %q", j.Kind)
	}
	switch j.Step {
	case "prepared", "moved-old", "installed", "aside-deleted":
	default:
		return fmt.Errorf("backup: invalid swap step %q", j.Step)
	}
	switch j.Rollback {
	case "", "started", "discarding", "new-discarded", "old-restored":
	default:
		return fmt.Errorf("backup: invalid rollback step %q", j.Rollback)
	}
	if j.New == "" || j.Target == "" || j.Aside == "" ||
		filepath.Clean(j.New) == filepath.Clean(j.Target) ||
		filepath.Clean(j.Aside) == filepath.Clean(j.Target) ||
		filepath.Clean(j.Aside) == filepath.Clean(j.New) {
		return errors.New("backup: swap paths must be nonempty and distinct")
	}
	if j.Rollback == "discarding" && j.Discard == "" {
		return errors.New("backup: discarding rollback has no discard path")
	}
	if j.Discard != "" {
		name, ok := strings.CutPrefix(filepath.Base(j.Discard), ".tkr-tmp-rollback-")
		_, err := hex.DecodeString(name)
		if !ok || len(name) != 16 || err != nil || swapParent(j.Discard) != swapParent(j.Target) ||
			filepath.Clean(j.Discard) == filepath.Clean(j.Target) {
			return errors.New("backup: invalid rollback discard path")
		}
	}
	return nil
}

// moveSwapFile never overwrites a destination. A missing source is either an
// already-completed rename or an optional file that never existed (e.g. SHM).
func moveSwapFile(from, to string, required bool) error {
	source, err := swapExists(from)
	if err != nil {
		return err
	}
	target, err := swapExists(to)
	if err != nil {
		return err
	}
	if source && target {
		return fmt.Errorf("backup: refusing to overwrite swap destination %s", to)
	}
	if !source {
		if required && !target {
			return fmt.Errorf("backup: both swap source %s and destination %s are missing", from, to)
		}
		return nil
	}
	if err := os.Rename(from, to); err != nil {
		return fmt.Errorf("backup: rename swap member: %w", err)
	}
	return nil
}

func swapExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("backup: inspect swap path: %w", err)
	}
	return true, nil
}

func newSwapDiscard(ctx context.Context, target string) (string, error) {
	// The instance lock excludes other swaps. Do not pre-create this path:
	// its existence during discarding is the evidence that rename completed.
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		var nonce [8]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", fmt.Errorf("backup: generate rollback discard name: %w", err)
		}
		path := filepath.Join(swapParent(target), ".tkr-tmp-rollback-"+hex.EncodeToString(nonce[:]))
		exists, err := swapExists(path)
		if err != nil {
			return "", err
		}
		if !exists {
			return path, nil
		}
	}
}

func swapParent(path string) string { return filepath.Dir(filepath.Clean(path)) }

func syncSwapDirs(dirs ...string) error {
	seen := make(map[string]bool, len(dirs))
	for _, dir := range dirs {
		dir = filepath.Clean(dir)
		if !seen[dir] {
			if err := fsutil.FsyncDir(dir); err != nil {
				return err
			}
			seen[dir] = true
		}
	}
	return nil
}

func syncSwapAside(j *SwapJournal) error {
	exists, err := swapExists(j.Aside)
	if err != nil {
		return err
	}
	if exists {
		return fsutil.FsyncDir(j.Aside)
	}
	return nil
}

func removeSwapPath(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("backup: remove swap path: %w", err)
	}
	return nil
}

func removeSwapJournal(path string) error {
	if err := removeSwapPath(path); err != nil {
		return err
	}
	return fsutil.FsyncDir(swapParent(path))
}
