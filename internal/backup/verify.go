package backup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"taskronin/internal/store"
)

// Verify checks a snapshot or staged copy with SQLite and both FTS indexes,
// returning its highest migration version. It opens the copy in DELETE mode;
// never pass a live database. No SQLite sidecars may remain after closing it.
func Verify(ctx context.Context, path string) (schemaVersion int, err error) {
	stat, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("backup: stat snapshot for verification: %w", err)
	}
	if !stat.Mode().IsRegular() {
		return 0, fmt.Errorf("backup: snapshot is not a regular file: %s", path)
	}
	s, err := store.OpenStaging(ctx, path)
	if err != nil {
		return 0, fmt.Errorf("backup: open snapshot for verification: %w", err)
	}
	defer func() {
		err = errors.Join(err, s.Close())
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if _, sidecarErr := os.Lstat(path + suffix); sidecarErr == nil {
				err = errors.Join(err, fmt.Errorf("backup: snapshot sidecar remains: %s", path+suffix))
			} else if !errors.Is(sidecarErr, fs.ErrNotExist) {
				err = errors.Join(err, fmt.Errorf("backup: check snapshot sidecar: %w", sidecarErr))
			}
		}
	}()
	if err := store.IntegrityCheck(ctx, s.Writer); err != nil {
		return 0, fmt.Errorf("backup: verify snapshot integrity: %w", err)
	}
	if err := store.FTSIntegrityCheck(ctx, s.Writer); err != nil {
		return 0, fmt.Errorf("backup: verify snapshot FTS: %w", err)
	}
	if err := s.Reader.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&schemaVersion); err != nil {
		return 0, fmt.Errorf("backup: read snapshot schema version: %w", err)
	}
	return schemaVersion, nil
}
