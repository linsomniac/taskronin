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
func EnsureDir0700(path string) (err error) {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create private directory: %w", err)
	}
	// AIDEV-NOTE: check and chmod the same directory descriptor without following
	// a final symlink. Fchmod also avoids the newer fchmodat2 syscall required by
	// Fchmodat with AT_SYMLINK_NOFOLLOW on Linux.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open private directory: %w", err)
	}
	defer func() {
		if closeErr := unix.Close(fd); err == nil && closeErr != nil {
			err = fmt.Errorf("close private directory: %w", closeErr)
		}
	}()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("stat private directory: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("private directory %q: not a directory", path)
	}
	if int64(stat.Uid) != int64(os.Geteuid()) {
		return fmt.Errorf("private directory %q: not owned by the effective UID", path)
	}
	if err := unix.Fchmod(fd, 0o700); err != nil {
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
