// Package lock provides the process-wide instance lock shared by the server
// and offline commands.
package lock

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// ErrHeld indicates that another open file description holds the instance lock.
var ErrHeld = errors.New("instance lock is held by another process")

// Lock holds an exclusive flock until Release or process exit. A Lock must not
// be copied, and calls to Release must be serialized by the owner.
type Lock struct {
	f *os.File
}

// Acquire takes an exclusive, nonblocking instance lock. The caller must first
// ensure the containing directory is private and owned by the effective UID.
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // G304: caller-resolved lock path in the user's private data directory.
	if err != nil {
		return nil, fmt.Errorf("open instance lock: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrHeld, path)
		}
		return nil, fmt.Errorf("acquire instance lock %s: %w", path, err)
	}
	return &Lock{f: f}, nil
}

// Release unlocks and closes the file. Subsequent calls are no-ops.
func (l *Lock) Release() error {
	if l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	// AIDEV-NOTE: never unlink the lock file. A contender may already have the
	// same inode open; replacing it would let separate inodes be locked at once.
	unlockErr := unix.Flock(int(f.Fd()), unix.LOCK_UN)
	closeErr := f.Close()
	if err := errors.Join(unlockErr, closeErr); err != nil {
		return fmt.Errorf("release instance lock %s: %w", f.Name(), err)
	}
	return nil
}
