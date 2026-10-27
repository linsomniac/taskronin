// Package fsutil provides durable file writes and private filesystem permissions.
package fsutil

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteFileAtomic replaces path with data using a temporary file in the same
// directory, syncing the file before rename and the directory afterwards.
func WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".tkr-tmp-*")
	if err != nil {
		return fmt.Errorf("create atomic temporary file: %w", err)
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write atomic temporary file: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync atomic temporary file: %w", err)
	}
	if err := f.Chmod(perm); err != nil {
		return fmt.Errorf("chmod atomic temporary file: %w", err)
	}
	// AIDEV-NOTE: sync again after chmod so the requested permissions, as well
	// as the contents, are durable before publishing the replacement name.
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync atomic file permissions: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close atomic temporary file: %w", err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("rename atomic temporary file: %w", err)
	}
	return FsyncDir(dir)
}

// FsyncDir syncs the directory's entries to stable storage.
func FsyncDir(dir string) error {
	f, err := os.Open(dir) //nolint:gosec // G304: a directory handle for fsync.
	if err != nil {
		return fmt.Errorf("open directory for sync: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat directory for sync: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("sync directory %q: not a directory", dir)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close synced directory: %w", err)
	}
	return nil
}
