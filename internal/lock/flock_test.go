package lock_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/linsomniac/taskronin/internal/lock"
)

func TestLockExclusive(t *testing.T) {
	p := filepath.Join(t.TempDir(), "taskronin.lock")
	l1 := acquire(t, p)
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("lock file mode = %v, want regular 0600", info.Mode())
	}
	requireHeld(t, p)
	if err := l1.Release(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(p)
	if err != nil || !os.SameFile(info, after) {
		t.Fatalf("release removed or replaced the lock inode: %v", err)
	}
	l2 := acquire(t, p)
	if err := l1.Release(); err != nil {
		t.Fatalf("second release: %v", err)
	}
	requireHeld(t, p)
	if err := l2.Release(); err != nil {
		t.Fatal(err)
	}
	acquire(t, p)
}

func TestLockOpenErrors(t *testing.T) {
	dir := t.TempDir()
	for _, path := range []string{filepath.Join(dir, "missing", "taskronin.lock"), dir} {
		l, err := lock.Acquire(path)
		if l != nil || err == nil || errors.Is(err, lock.ErrHeld) {
			t.Fatalf("Acquire(%q) = %v, %v; want a non-contention error", path, l, err)
		}
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) {
			t.Fatalf("open error lost its cause: %v", err)
		}
	}
}

func TestLockFailedAcquireClosesFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "taskronin.lock")
	acquire(t, p)
	for range 32 {
		requireHeld(t, p)
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if err == nil && target == p {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("open descriptors for lock = %d, want 1", count)
	}
}

func TestLockAcrossProcesses(t *testing.T) {
	p := filepath.Join(t.TempDir(), "taskronin.lock")
	l := acquire(t, p)
	if out, err := helperCommand(t, p, "held").CombinedOutput(); err != nil {
		t.Fatalf("child contention: %v\n%s", err, out)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}

	cmd := helperCommand(t, p, "hold")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("child readiness = %q, err = %v", line, err)
	}
	requireHeld(t, p)
	// A killed holder cannot call Release: the kernel must release its flock.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	waited = true
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("killed child: %v\n%s", err, stderr.String())
	}
	acquire(t, p)
}

func TestLockProcessHelper(t *testing.T) {
	mode := os.Getenv("TASKRONIN_LOCK_TEST_MODE")
	if mode == "" {
		return
	}
	p := os.Getenv("TASKRONIN_LOCK_TEST_PATH")
	if mode == "held" {
		requireHeld(t, p)
		return
	}
	if mode != "hold" {
		t.Fatalf("unknown helper mode %q", mode)
	}
	l, err := lock.Acquire(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(os.Stdout, "ready\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
	runtime.KeepAlive(l)
}

func helperCommand(t *testing.T, path, mode string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestLockProcessHelper$")
	cmd.Env = append(os.Environ(), "TASKRONIN_LOCK_TEST_MODE="+mode, "TASKRONIN_LOCK_TEST_PATH="+path)
	return cmd
}

func acquire(t *testing.T, path string) *lock.Lock {
	t.Helper()
	l, err := lock.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := l.Release(); err != nil {
			t.Errorf("release lock: %v", err)
		}
	})
	return l
}

func requireHeld(t *testing.T, path string) {
	t.Helper()
	l, err := lock.Acquire(path)
	if l != nil {
		_ = l.Release()
		t.Fatal("acquired an already-held lock")
	}
	want := fmt.Sprintf("instance lock is held by another process: %s", path)
	if !errors.Is(err, lock.ErrHeld) || err.Error() != want {
		t.Fatalf("contention error = %v, want ErrHeld with message %q", err, want)
	}
}
