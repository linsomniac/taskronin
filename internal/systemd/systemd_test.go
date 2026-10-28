package systemd_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"taskronin/internal/clock"
	"taskronin/internal/fsutil"
	"taskronin/internal/systemd"
	"taskronin/internal/testutil"
)

func TestListenersNotActivated(t *testing.T) {
	pid := os.Getpid()
	for _, env := range []map[string]string{
		{},
		{"LISTEN_PID": strconv.Itoa(pid + 1), "LISTEN_FDS": "invalid"},
		{"LISTEN_PID": strconv.Itoa(pid)},
		{"LISTEN_PID": strconv.Itoa(pid), "LISTEN_FDS": "0"},
	} {
		listeners, err := systemd.ListenersFrom(func(key string) string { return env[key] }, pid, 3)
		if listeners != nil || err != nil {
			t.Fatalf("ListenersFrom(%v) = %v, %v; want nil, nil", env, listeners, err)
		}
	}
}

func TestListenersNamed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	f, err := ln.File()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	listeners, err := systemd.ListenersFrom(activationEnv("1", "api"), os.Getpid(), int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeListeners(listeners) })
	// FileListener duplicates the inherited descriptor; it must be closed.
	if _, err := f.Stat(); err == nil {
		t.Fatal("inherited descriptor was left open")
	}
	// Retire the test's os.File before another operation can reuse its raw fd.
	_ = f.Close()
	api, ok := listeners["api"]
	if len(listeners) != 1 || !ok {
		t.Fatalf("listeners = %v; want only api", listeners)
	}
	checkCloseOnExec(t, api)
	checkAccept(t, api, path)
}

func TestListenersRejectMalformedEnvironment(t *testing.T) {
	for _, tc := range []struct{ name, pid, fds, names string }{
		{"pid", "invalid", "1", "api"},
		{"count", strconv.Itoa(os.Getpid()), "invalid", "api"},
		{"negative_count", strconv.Itoa(os.Getpid()), "-1", "api"},
		{"overflow", strconv.Itoa(os.Getpid()), "999999999999999999999", "api"},
		{"missing_names", strconv.Itoa(os.Getpid()), "1", ""},
		{"few_names", strconv.Itoa(os.Getpid()), "2", "api"},
		{"extra_names", strconv.Itoa(os.Getpid()), "1", "api:web"},
		{"empty_name", strconv.Itoa(os.Getpid()), "2", "api:"},
		{"duplicate_names", strconv.Itoa(os.Getpid()), "2", "api:api"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"LISTEN_PID": tc.pid, "LISTEN_FDS": tc.fds, "LISTEN_FDNAMES": tc.names}
			listeners, err := systemd.ListenersFrom(func(key string) string { return env[key] }, os.Getpid(), 3)
			if listeners != nil || err == nil {
				t.Fatalf("ListenersFrom = %v, %v; want nil, error", listeners, err)
			}
		})
	}
}

func TestListenersClearsEnvironment(t *testing.T) {
	for _, pid := range []string{"", strconv.Itoa(os.Getpid() + 1), "invalid"} {
		t.Run(pid, func(t *testing.T) {
			t.Setenv("LISTEN_PID", pid)
			t.Setenv("LISTEN_FDS", "1")
			t.Setenv("LISTEN_FDNAMES", "api")
			listeners, err := systemd.Listeners()
			if listeners != nil || (err != nil) != (pid == "invalid") {
				t.Fatalf("Listeners = %v, %v", listeners, err)
			}
			checkActivationUnset(t)
		})
	}
}

func TestListenersInheritedDescriptors(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(strconv.FormatBool(invalid), func(t *testing.T) {
			var files []*os.File
			var paths []string
			for range 2 {
				path := filepath.Join(t.TempDir(), "sock")
				ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = ln.Close() })
				f, err := ln.File()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = f.Close() })
				files = append(files, f)
				paths = append(paths, path)
			}
			mode := "valid"
			if invalid {
				mode = "invalid"
				f, err := os.Open(os.DevNull)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = f.Close() })
				// An invalid fd between listeners exercises failure cleanup.
				files = []*os.File{files[0], f, files[1]}
			}
			cmd := processCommand(t, "TestListenersProcessHelper")
			cmd.ExtraFiles = files
			cmd.Env = append(os.Environ(), "TASKRONIN_ACTIVATION_TEST="+mode,
				"TASKRONIN_ACTIVATION_PATHS="+strings.Join(paths, ":"))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("activation child: %v\n%s", err, out)
			}
		})
	}
}

func TestListenersProcessHelper(t *testing.T) {
	mode := os.Getenv("TASKRONIN_ACTIVATION_TEST")
	if mode == "" {
		return
	}
	names := "api:web"
	count := 2
	if mode == "invalid" {
		names = "api:invalid:web"
		count = 3
	}
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("LISTEN_FDS", strconv.Itoa(count))
	t.Setenv("LISTEN_FDNAMES", names)
	listeners, err := systemd.Listeners()
	t.Cleanup(func() { closeListeners(listeners) })
	checkActivationUnset(t)
	if mode == "invalid" {
		if err == nil || listeners != nil {
			t.Fatalf("invalid fd: got %v, %v; want nil, error", listeners, err)
		}
		// No original or duplicated socket descriptors may survive the error.
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
			if err == nil && strings.HasPrefix(target, "socket:") {
				t.Errorf("socket fd leaked after failure: %s -> %s", entry.Name(), target)
			}
		}
		return
	}
	if err != nil || len(listeners) != count {
		t.Fatalf("Listeners = %v, %v", listeners, err)
	}
	paths := strings.Split(os.Getenv("TASKRONIN_ACTIVATION_PATHS"), ":")
	for i, name := range strings.Split(names, ":") {
		ln := listeners[name]
		if ln == nil {
			t.Fatalf("missing listener %q", name)
		}
		checkCloseOnExec(t, ln)
		checkAccept(t, ln, paths[i])
	}
	if again, err := systemd.Listeners(); again != nil || err != nil {
		t.Fatalf("second Listeners = %v, %v; want nil, nil", again, err)
	}
}

func TestNotify(t *testing.T) {
	for _, abstract := range []bool{false, true} {
		t.Run(strconv.FormatBool(abstract), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "notify.sock")
			if abstract {
				path = "@" + path
			}
			ln, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = ln.Close() })
			// AIDEV-NOTE: real socket deadlines bound test failures, not domain time.
			if err := ln.SetReadDeadline(clock.Real{}.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NOTIFY_SOCKET", path)
			for _, state := range []string{"READY=1", "STOPPING=1\nSTATUS=Draining"} {
				if err := systemd.Notify(state); err != nil {
					t.Fatal(err)
				}
				buf := make([]byte, 256)
				n, _, err := ln.ReadFromUnix(buf)
				if err != nil || string(buf[:n]) != state {
					t.Fatalf("notify datagram = %q, %v; want %q", buf[:n], err, state)
				}
			}
		})
	}
	t.Setenv("NOTIFY_SOCKET", "")
	if err := os.Unsetenv("NOTIFY_SOCKET"); err != nil {
		t.Fatal(err)
	}
	if err := systemd.Notify("READY=1"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NOTIFY_SOCKET", filepath.Join(t.TempDir(), "missing.sock"))
	if err := systemd.Notify("READY=1"); err == nil {
		t.Fatal("missing notify socket: want error")
	}
}

func TestRenderUnits(t *testing.T) {
	service, socket, err := systemd.RenderUnits(unitData())
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "taskronin.service", service)
	testutil.Golden(t, "taskronin.socket", socket)
	for _, line := range []string{
		"ExecStart=/usr/local/bin/tkr serve\n",
		"ReadWritePaths=/d /s /run/user/1000/taskronin /b\n",
		"TimeoutStopSec=45s\n",
	} {
		if !bytes.Contains(service, []byte(line)) {
			t.Errorf("service missing %q", line)
		}
	}
	for _, line := range []string{
		"ListenStream=/run/user/1000/taskronin/sock\n",
		"FileDescriptorName=api\n", "SocketMode=0600\n",
	} {
		if !bytes.Contains(socket, []byte(line)) {
			t.Errorf("socket missing %q", line)
		}
	}
	for _, extra := range [][]string{nil, {"/b", "/another-backup"}} {
		d := unitData()
		d.ExtraRW = extra
		service, _, err := systemd.RenderUnits(d)
		if err != nil {
			t.Fatal(err)
		}
		want := "ReadWritePaths=/d /s /run/user/1000/taskronin"
		for _, path := range extra {
			want += " " + path
		}
		if !bytes.Contains(service, []byte(want+"\n")) {
			t.Errorf("service missing %q", want)
		}
	}
}

func TestRenderUnitsRejectsUnsafePaths(t *testing.T) {
	for _, field := range []string{"Binary", "Socket", "DataDir", "StateDir", "RuntimeDir", "ExtraRW"} {
		t.Run(field, func(t *testing.T) {
			for _, path := range []string{"/my data", "/a%b", "/a\"b", "/home/o'neil/bin/tkr", "/a'b'c", "/a\\b", "/a\tb", "/a\nb", "/a\rb", "/a\x00b", "/a\x1bb", "/a\x7fb", "/a\u0085b", "/a\u00a0b"} {
				d := unitData()
				switch field {
				case "Binary":
					d.Binary = path
				case "Socket":
					d.Socket = path
				case "DataDir":
					d.DataDir = path
				case "StateDir":
					d.StateDir = path
				case "RuntimeDir":
					d.RuntimeDir = path
				case "ExtraRW":
					d.ExtraRW = []string{"/safe", path}
				}
				service, socket, err := systemd.RenderUnits(d)
				if err == nil || service != nil || socket != nil {
					t.Errorf("%s=%q: got %q, %q, %v; want nil, nil, error", field, path, service, socket, err)
				}
			}
		})
	}
}

func TestUnitsVerify(t *testing.T) {
	analyze, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze is not installed")
	}
	// AIDEV-NOTE: verification checks executable availability without running
	// it; the test binary also works on hosts without /bin/true.
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	d := unitData()
	d.Binary = binary
	service, socket, err := systemd.RenderUnits(d)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "taskronin.service"), filepath.Join(dir, "taskronin.socket")}
	for i, body := range [][]byte{service, socket} {
		if err := fsutil.WriteFileAtomic(paths[i], body, 0o644); err != nil {
			t.Fatal(err)
		}
		assertMode(t, paths[i], 0o644)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, analyze, "--user", "verify", paths[0], paths[1])
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("systemd-analyze --user verify: %v\n%s", err, out)
	}
}

func TestUnitsModeUnderPrivateUmask(t *testing.T) {
	cmd := processCommand(t, "TestUnitsModeProcessHelper")
	cmd.Env = append(os.Environ(), "TASKRONIN_UNIT_MODE_TEST=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("unit permissions child: %v\n%s", err, out)
	}
}

func TestUnitsModeProcessHelper(t *testing.T) {
	if os.Getenv("TASKRONIN_UNIT_MODE_TEST") == "" {
		return
	}
	// AIDEV-NOTE: isolate the process-wide umask from the parent test runner.
	fsutil.SetUmask(0o077)
	service, socket, err := systemd.RenderUnits(unitData())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, body := range map[string][]byte{"taskronin.service": service, "taskronin.socket": socket} {
		path := filepath.Join(dir, name)
		if err := fsutil.WriteFileAtomic(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
		assertMode(t, path, 0o644)
	}
}

func unitData() systemd.UnitData {
	return systemd.UnitData{
		Binary: "/usr/local/bin/tkr", Socket: "/run/user/1000/taskronin/sock",
		DataDir: "/d", StateDir: "/s", RuntimeDir: "/run/user/1000/taskronin", ExtraRW: []string{"/b"},
	}
}

func activationEnv(fds, names string) func(string) string {
	env := map[string]string{"LISTEN_PID": strconv.Itoa(os.Getpid()), "LISTEN_FDS": fds, "LISTEN_FDNAMES": names}
	return func(key string) string { return env[key] }
}

func closeListeners(listeners map[string]net.Listener) {
	for _, ln := range listeners {
		_ = ln.Close()
	}
}

func checkActivationUnset(t *testing.T) {
	t.Helper()
	for _, key := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES"} {
		if _, ok := os.LookupEnv(key); ok {
			t.Errorf("%s was not unset", key)
		}
	}
}

func checkCloseOnExec(t *testing.T, ln net.Listener) {
	t.Helper()
	conn, err := ln.(*net.UnixListener).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var flags int
	var flagErr error
	if err := conn.Control(func(fd uintptr) { flags, flagErr = unix.FcntlInt(fd, unix.F_GETFD, 0) }); err != nil {
		t.Fatal(err)
	}
	if flagErr != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("listener flags = %d, %v; want FD_CLOEXEC", flags, flagErr)
	}
}

func checkAccept(t *testing.T, ln net.Listener, path string) {
	t.Helper()
	// AIDEV-NOTE: real I/O deadlines keep a broken activation test bounded.
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
	if err := server.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err := client.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(client, "hello"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(server, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("activated listener read = %q, %v", buf, err)
	}
}

func processCommand(t *testing.T, test string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)
	return exec.CommandContext(ctx, exe, "-test.run=^"+test+"$")
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o; want %04o", path, got, want)
	}
}
