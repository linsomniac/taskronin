package backup

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"taskronin/internal/clock"
	"taskronin/internal/fsutil"
)

// Kind identifies why a snapshot was taken.
type Kind string

const (
	// KindDaily identifies a scheduled daily snapshot.
	KindDaily Kind = "daily"
	// KindManual identifies a user-requested snapshot.
	KindManual Kind = "manual"
	// KindPremigrate identifies a snapshot taken before a schema migration.
	KindPremigrate Kind = "premigrate"
	// KindPrescrub identifies a snapshot taken before scrubbing recovery files.
	KindPrescrub Kind = "prescrub"
)

// SnapshotInfo describes a snapshot file. List leaves SchemaVersion at zero.
type SnapshotInfo struct {
	Name, Path    string
	Kind          Kind
	At            time.Time
	Size          int64
	SchemaVersion int
}

// Manager serializes snapshots and rotations in Dir. Configure its fields before
// use; offline callers must also hold the application's instance lock.
type Manager struct {
	Dir   string
	Clock clock.Clock
	Snap  *sql.DB
	mu    sync.Mutex

	crashHook func(step string) error // installed only by export_test.go
}

const snapshotTimeLayout = "20060102T150405.000Z"

// Snapshot publishes a verified, private, durable copy from the snapshot pool.
// Settings updates and retention scheduling belong to the service layer.
func (m *Manager) Snapshot(ctx context.Context, kind Kind) (_ SnapshotInfo, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return SnapshotInfo{}, err
	}
	switch kind {
	case KindDaily, KindManual, KindPremigrate, KindPrescrub:
	default:
		return SnapshotInfo{}, fmt.Errorf("backup: invalid snapshot kind %q", kind)
	}
	at := m.Clock.Now().UTC().Truncate(time.Millisecond)
	info, err := m.snapshotName(ctx, at, kind)
	if err != nil {
		return SnapshotInfo{}, err
	}
	f, err := os.CreateTemp(m.Dir, ".tkr-tmp-*.db")
	if err != nil {
		return SnapshotInfo{}, fmt.Errorf("backup: create snapshot temp: %w", err)
	}
	temp := f.Name()
	crashed := false
	defer func() {
		_ = f.Close()
		if !crashed {
			// AIDEV-NOTE: ordinary failures remove the entire unpublished file set.
			// Simulated process death leaves it intact for crash-recovery tests.
			for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
				if removeErr := os.Remove(temp + suffix); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
					err = errors.Join(err, fmt.Errorf("backup: remove snapshot temp: %w", removeErr))
				}
			}
		}
	}()
	checkpoint := func(step string) error {
		if m.crashHook != nil {
			if err := m.crashHook(step); err != nil {
				crashed = true
				return err
			}
		}
		return ctx.Err()
	}
	if _, err := m.Snap.ExecContext(ctx, "VACUUM INTO ?", temp); err != nil {
		return SnapshotInfo{}, fmt.Errorf("backup: vacuum snapshot: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		return SnapshotInfo{}, fmt.Errorf("backup: chmod snapshot: %w", err)
	}
	if err := checkpoint("vacuumed"); err != nil {
		return SnapshotInfo{}, err
	}
	info.SchemaVersion, err = Verify(ctx, temp)
	if err != nil {
		return SnapshotInfo{}, err
	}
	if err := checkpoint("verified"); err != nil {
		return SnapshotInfo{}, err
	}
	// AIDEV-NOTE: verification can write (DELETE mode and FTS checks). Sync only
	// after it has closed SQLite, then publish and sync the directory entry.
	if err := f.Sync(); err != nil {
		return SnapshotInfo{}, fmt.Errorf("backup: sync snapshot: %w", err)
	}
	stat, err := f.Stat()
	if err != nil {
		return SnapshotInfo{}, fmt.Errorf("backup: stat snapshot: %w", err)
	}
	info.Size = stat.Size()
	if err := f.Close(); err != nil {
		return SnapshotInfo{}, fmt.Errorf("backup: close snapshot: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return SnapshotInfo{}, err
	}
	if err := os.Rename(temp, info.Path); err != nil {
		return SnapshotInfo{}, fmt.Errorf("backup: publish snapshot: %w", err)
	}
	if err := checkpoint("renamed"); err != nil {
		return SnapshotInfo{}, err
	}
	if err := fsutil.FsyncDir(m.Dir); err != nil {
		return SnapshotInfo{}, fmt.Errorf("backup: sync snapshot directory: %w", err)
	}
	return info, nil
}

func (m *Manager) snapshotName(ctx context.Context, at time.Time, kind Kind) (SnapshotInfo, error) {
	// The timestamp may repeat (concurrent calls or a clock moving backwards).
	// Check the short random suffix under mu so a collision never replaces a backup.
	for {
		if err := ctx.Err(); err != nil {
			return SnapshotInfo{}, err
		}
		var nonce [2]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return SnapshotInfo{}, fmt.Errorf("backup: generate snapshot name: %w", err)
		}
		name := fmt.Sprintf("taskronin-%s-%s-%s.db", at.Format(snapshotTimeLayout), hex.EncodeToString(nonce[:]), kind)
		path := filepath.Join(m.Dir, name)
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			return SnapshotInfo{Name: name, Path: path, Kind: kind, At: at}, nil
		} else if err != nil {
			return SnapshotInfo{}, fmt.Errorf("backup: check snapshot name: %w", err)
		}
	}
}
