package server

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"taskronin/internal/clock"
)

func TestListenAPICreates(t *testing.T) {
	clearActivation(t)
	path := filepath.Join(t.TempDir(), "private", "sock")
	ln := listenCreatedAPI(t, path)
	assertMode(t, filepath.Dir(path), os.ModeDir, 0o700)
	assertMode(t, path, os.ModeSocket, 0o600)
	checkListenerAccepts(t, ln, path)
}

func TestListenAPIStale(t *testing.T) {
	clearActivation(t)
	path := filepath.Join(t.TempDir(), "sock")
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("stale socket missing: %v", err)
	}
	ln := listenCreatedAPI(t, path)
	assertMode(t, path, os.ModeSocket, 0o600)
	checkListenerAccepts(t, ln, path)
}

func TestListenAPITightensDir(t *testing.T) {
	clearActivation(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sock")
	listenCreatedAPI(t, path)
	assertMode(t, dir, os.ModeDir, 0o700)
	assertMode(t, path, os.ModeSocket, 0o600)
}

func TestListenAPIPathTooLong(t *testing.T) {
	clearActivation(t)
	prefix := t.TempDir() + "/"
	path := prefix + strings.Repeat("s", 108-len(prefix))
	ln, created, err := listenAPI(path)
	if ln != nil {
		_ = ln.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "socket path too long") || ln != nil || created {
		t.Fatalf("listenAPI = %v, %v, %v; want nil, false, socket path too long", ln, created, err)
	}
}

func TestListenAPIRejectsNonSocket(t *testing.T) {
	for _, kind := range []string{"file", "directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			clearActivation(t)
			dir := t.TempDir()
			path := filepath.Join(dir, "sock")
			switch kind {
			case "file":
				if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(filepath.Join(dir, "missing"), path); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			_ = assertListenError(t, path)
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("existing %s was replaced: %v", kind, err)
			}
		})
	}
}

func TestListenAPIRejectsSymlinkDir(t *testing.T) {
	clearActivation(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	_ = assertListenError(t, filepath.Join(link, "sock"))
	assertMode(t, dir, os.ModeDir, 0o755)
}

func TestListenAPIRejectsForeignDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root owns the fixture directory")
	}
	clearActivation(t)
	// AIDEV-NOTE: / is root-owned, so EnsureDir0700 must refuse it before
	// chmod or socket removal/bind; this exercises the real ownership check.
	err := assertListenError(t, "/taskronin-test.sock")
	if !strings.Contains(err.Error(), "not owned by the effective UID") {
		t.Fatalf("listenAPI: %v; want directory ownership error", err)
	}
}

func TestListenAPIActivationError(t *testing.T) {
	clearActivation(t)
	t.Setenv("LISTEN_PID", "invalid")
	path := filepath.Join(t.TempDir(), "private", "sock")
	if err := assertListenError(t, path); !strings.Contains(err.Error(), "LISTEN_PID") {
		t.Fatalf("listenAPI: %v; want activation error", err)
	}
	if _, err := os.Lstat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fallback directory touched after activation error: %v", err)
	}
}

func TestListenAPIActivation(t *testing.T) {
	for _, name := range []string{"api", "other"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "activated.sock")
			ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = ln.Close() })
			if err := os.Chmod(path, 0o600); err != nil {
				t.Fatal(err)
			}
			f, err := ln.File()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = f.Close() })
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestListenAPIActivationHelper$")
			cmd.ExtraFiles = []*os.File{f}
			cmd.Env = append(os.Environ(), "TASKRONIN_LISTEN_API_TEST="+name, "TASKRONIN_LISTEN_API_PATH="+path)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("activation child: %v\n%s", err, out)
			}
		})
	}
}

func TestListenAPIActivationHelper(t *testing.T) {
	name := os.Getenv("TASKRONIN_LISTEN_API_TEST")
	if name == "" {
		return
	}
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("LISTEN_FDS", "1")
	t.Setenv("LISTEN_FDNAMES", name)
	path := os.Getenv("TASKRONIN_LISTEN_API_PATH")
	fallback := filepath.Join(t.TempDir(), "private", "sock")
	ln, created, err := listenAPI(fallback)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	if created != (name != "api") {
		t.Fatalf("created = %v for activated %q", created, name)
	}
	if name == "api" {
		if ln.Addr().String() != path {
			t.Fatalf("listener address = %s; want activated %s", ln.Addr(), path)
		}
		if _, err := os.Lstat(filepath.Dir(fallback)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("activated listener touched fallback directory: %v", err)
		}
		checkListenerAccepts(t, ln, path)
		if err := ln.Close(); err != nil {
			t.Fatal(err)
		}
		assertMode(t, path, os.ModeSocket, 0o600)
	} else {
		assertMode(t, fallback, os.ModeSocket, 0o600)
		checkListenerAccepts(t, ln, fallback)
	}
}

func clearActivation(t *testing.T) {
	t.Helper()
	for _, key := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES"} {
		t.Setenv(key, "")
	}
}

func listenCreatedAPI(t *testing.T, path string) net.Listener {
	t.Helper()
	ln, created, err := listenAPI(path)
	if ln != nil {
		t.Cleanup(func() { _ = ln.Close() })
	}
	if err != nil || !created || ln == nil {
		t.Fatalf("listenAPI = %v, %v, %v; want listener, true, nil", ln, created, err)
	}
	return ln
}

func assertListenError(t *testing.T, path string) error {
	t.Helper()
	ln, created, err := listenAPI(path)
	if ln != nil {
		_ = ln.Close()
	}
	if err == nil || ln != nil || created {
		t.Fatalf("listenAPI = %v, %v, %v; want nil, false, error", ln, created, err)
	}
	return err
}

func assertMode(t *testing.T, path string, kind, perm os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Type() != kind || info.Mode().Perm() != perm {
		t.Fatalf("%s mode = %v; want %v", path, info.Mode(), kind|perm)
	}
}

func checkListenerAccepts(t *testing.T, ln net.Listener, path string) {
	t.Helper()
	// AIDEV-NOTE: real I/O deadlines bound failures of the socket fixture.
	deadline := clock.Real{}.Now().Add(5 * time.Second)
	if err := ln.(*net.UnixListener).SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	dialer := net.Dialer{Deadline: deadline}
	client, err := dialer.DialContext(t.Context(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	server, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	if err := client.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err := server.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(client, "ok"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(server, buf); err != nil || string(buf) != "ok" {
		t.Fatalf("accepted read = %q, %v; want ok", buf, err)
	}
}
