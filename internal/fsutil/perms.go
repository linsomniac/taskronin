package fsutil

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// CheckPrivate requires a regular, non-symlink file owned by the effective UID,
// with no permissions granted to its group or other users.
func CheckPrivate(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat private file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("private file %q: not a regular file", path)
	}
	if !ownedByEUID(info) {
		return fmt.Errorf("private file %q: not owned by the effective UID", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("private file %q: group or other permissions are set", path)
	}
	return nil
}

// EnsureDir0700 creates path and missing parents with mode 0700, or tightens an
// existing directory owned by the effective UID to that mode. Symlinks are refused.
func EnsureDir0700(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create private directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat private directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("private directory %q: not a directory", path)
	}
	if !ownedByEUID(info) {
		return fmt.Errorf("private directory %q: not owned by the effective UID", path)
	}
	// Do not follow a symlink substituted after Lstat when tightening the directory.
	if err := unix.Fchmodat(unix.AT_FDCWD, path, 0o700, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("chmod private directory: %w", err)
	}
	return nil
}

func ownedByEUID(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int64(stat.Uid) == int64(os.Geteuid())
}

// SetUmask sets the process-wide file creation mask and returns the old mask.
// Callers must serialize changes with all file creation in the process.
func SetUmask(mask int) int { return unix.Umask(mask) }
