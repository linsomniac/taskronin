package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"taskronin/internal/api"
	"taskronin/internal/buildinfo"
	"taskronin/internal/civil"
	"taskronin/internal/clock"
	"taskronin/internal/config"
	"taskronin/internal/testutil"
)

func TestVersionWithoutServer(t *testing.T) {
	s, _ := testSys(t)
	out, stderr, code := runCLI(t, s, "", "version")
	want := fmt.Sprintf("tkr %s (%s)\nserver unreachable\n", buildinfo.Version, buildinfo.Commit)
	if code != 0 || out != want || stderr != "" {
		t.Fatalf("%d %q %q", code, out, stderr)
	}
}

func TestVersionWithServerAndHungServer(t *testing.T) {
	for _, hung := range []bool{false, true} {
		t.Run(fmt.Sprint(hung), func(t *testing.T) {
			s, _ := testSys(t)
			socket, token := fakeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if hung {
					<-r.Context().Done()
					return
				}
				writeJSON(t, w, api.Clock{ServerVersion: "server-test", Today: civil.MustDate("2026-10-28")})
			}))
			start := (clock.Real{}).Now()
			out, stderr, code := runCLI(t, s, "", "version", "--socket", socket, "--token-file", token)
			if elapsed := (clock.Real{}).Now().Sub(start); elapsed > 2*time.Second {
				t.Fatalf("version took %v", elapsed)
			}
			line := "server server-test\n"
			if hung {
				line = "server unreachable\n"
			}
			if code != 0 || stderr != "" || !strings.HasSuffix(out, line) {
				t.Fatalf("%d %q %q", code, out, stderr)
			}
		})
	}
}

func TestServeWithoutConfig(t *testing.T) {
	s, _ := testSys(t)
	_, stderr, code := runCLI(t, s, "", "serve")
	if code != 1 || !strings.Contains(stderr, "run `tkr init`") {
		t.Fatalf("%d %q", code, stderr)
	}
}

func TestInitCommand(t *testing.T) {
	s, p := testSys(t)
	out, stderr, code := runCLI(t, s, testutil.Password+"\n", "init", "--password-stdin", "--no-systemd", "--yes", "--timezone", "America/Denver")
	if code != 0 || !strings.Contains(out, "tkr status --short") {
		t.Fatalf("%d %q %q", code, out, stderr)
	}
	if _, err := os.Stat(p.DB); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"migrate", "--status"}, {"migrate"}} {
		out, stderr, code = runCLI(t, s, "", args...)
		if code != 0 || stderr != "" {
			t.Fatalf("%v: %d %q %q", args, code, out, stderr)
		}
		if len(args) > 1 && !strings.Contains(out, "nothing pending") {
			t.Fatal(out)
		}
	}
}

func TestColorPolicy(t *testing.T) {
	for _, tc := range []struct {
		mode, noColor string
		tty, want     bool
	}{
		{"auto", "1", true, false}, {"auto", "", false, false}, {"auto", "", true, true},
		{"never", "", true, false}, {"always", "1", false, true},
	} {
		t.Run(fmt.Sprintf("%s-%s-%t", tc.mode, tc.noColor, tc.tty), func(t *testing.T) {
			s, _ := testSys(t)
			getenv := s.Getenv
			s.Getenv = func(key string) string {
				if key == "NO_COLOR" {
					return tc.noColor
				}
				return getenv(key)
			}
			s.StdoutTTY = tc.tty
			_, stderr, code := runCLI(t, s, "", "--color", tc.mode, "__test-mutate")
			if code != 4 || strings.Contains(stderr, "\x1b[") != tc.want {
				t.Fatalf("%d %q", code, stderr)
			}
		})
	}
}

func TestClientConfigAndClockAreLazy(t *testing.T) {
	s, p := testSys(t)
	var calls atomic.Int32
	socket, token := fakeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeJSON(t, w, api.Clock{Today: civil.MustDate("2026-10-28"), WeekStart: "monday"})
	}))
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultClient(p)
	cfg.Socket, cfg.TokenFile, cfg.Color = socket, token, "never"
	if err := config.WriteClient(p.ClientConfig, cfg); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runCLI(t, s, "", "__test-ref")
	if code != 0 || stderr != "" || calls.Load() != 1 {
		t.Fatalf("%d %q, calls %d", code, stderr, calls.Load())
	}
	if code := canceledRun(t, s); code != 130 {
		t.Fatal(code)
	}
	if err := os.WriteFile(p.ClientConfig, []byte("invalid toml !"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := runCLI(t, s, "", "--help")
	if code != 0 || stderr != "" || !strings.Contains(out, "completion") {
		t.Fatalf("%d %q %q", code, out, stderr)
	}
}

func TestUsageAndCompletion(t *testing.T) {
	for _, args := range [][]string{{"unknown"}, {"version", "extra"}, {"--unknown"}, {"--color", "invalid", "version"}} {
		s, _ := testSys(t)
		_, stderr, code := runCLI(t, s, "", args...)
		if code != 2 || !strings.HasPrefix(stderr, "tkr: ") || strings.Contains(stderr, "Usage:") {
			t.Fatalf("%v: %d %q", args, code, stderr)
		}
	}
	s, _ := testSys(t)
	out, stderr, code := runCLI(t, s, "", "completion", "bash")
	if code != 0 || stderr != "" || !strings.Contains(out, "bash completion") {
		t.Fatalf("%d %q %q", code, out, stderr)
	}
}

func TestClientOverridesBeforeValidation(t *testing.T) {
	for _, source := range []string{"default", "config"} {
		for _, field := range []string{"socket", "token_file"} {
			t.Run(source+"/"+field, func(t *testing.T) {
				s, p := testSys(t)
				socket, token := fakeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					writeJSON(t, w, api.Clock{Today: civil.MustDate("2026-10-28")})
				}))
				bad, target := p.OwnerToken, token
				if field == "socket" {
					bad, target = p.Socket, socket
				}
				if source == "config" {
					bad = filepath.Join(s.Home, "invalid")
					if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(p.ClientConfig, fmt.Appendf(nil, "%s = %q\n", field, bad), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.MkdirAll(filepath.Dir(bad), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, bad); err != nil {
					t.Fatal(err)
				}
				out, stderr, code := runCLI(t, s, "", "--socket", socket, "--token-file", token, "__test-clock")
				if code != 0 || out != "" || stderr != "" {
					t.Fatalf("override: %d %q %q", code, out, stderr)
				}
			})
		}
	}
}

func TestTerminalCancellation(t *testing.T) {
	for _, kind := range []string{"password", "confirmation", "password-ctrl-c"} {
		t.Run(kind, func(t *testing.T) {
			s, p := testSys(t)
			for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR"} {
				t.Setenv(key, s.Getenv(key))
			}
			master, slave := terminalPair(t)
			before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil {
				t.Fatal(err)
			}
			prompt := "New password: "
			args := []string{"init", "--no-systemd", "--yes", "--timezone", "America/Denver"}
			if kind == "confirmation" {
				prompt, args = "Continue?", []string{"__test-confirm"}
			}
			stderr := &promptOutput{prompt: prompt, seen: make(chan struct{})}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan int, 1)
			go func() { defer close(done); done <- Main(ctx, args, slave, io.Discard, stderr) }()
			t.Cleanup(func() {
				cancel()
				_, _ = master.Write([]byte("\n\n"))
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Error("terminal reader did not stop")
				}
			})
			select {
			case <-stderr.seen:
			case <-time.After(2 * time.Second):
				t.Fatal("terminal prompt did not appear")
			}
			if kind == "password-ctrl-c" {
				if _, err := master.Write([]byte{3}); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			select {
			case code := <-done:
				if code != 130 {
					t.Fatalf("exit %d, stderr %q", code, stderr.String())
				}
			case <-time.After(time.Second):
				// Release the old blocking implementation so the RED run leaves
				// neither a reader goroutine nor altered terminal state behind.
				cancel()
				_, _ = master.Write([]byte("\n\n"))
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("could not release terminal reader")
				}
				t.Fatal("cancellation did not unblock terminal input within 1s")
			}
			after, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil || *after != *before {
				t.Fatalf("terminal state not restored: %v", err)
			}
			if _, err := os.Stat(p.DB); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("interrupted init published DB: %v", err)
			}
		})
	}
}

func TestTerminalPasswordDoesNotEchoOrLoseInput(t *testing.T) {
	s, _ := testSys(t)
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(key, s.Getenv(key))
	}
	master, slave := terminalPair(t)
	before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	stderr := &promptOutput{prompt: "New password: ", seen: make(chan struct{})}
	done := make(chan int, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		defer close(done)
		done <- Main(ctx, []string{"init", "--no-systemd", "--yes", "--timezone", "America/Denver"}, slave, io.Discard, stderr)
	}()
	t.Cleanup(func() {
		cancel()
		_, _ = master.Write([]byte("\n\n"))
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("terminal reader did not stop")
		}
	})
	select {
	case <-stderr.seen:
	case <-time.After(2 * time.Second):
		t.Fatal("password prompt did not appear")
	}
	if _, err := master.Write([]byte(testutil.Password + "\n" + testutil.Password + "\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d, stderr %q", code, stderr.String())
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("password reader lost buffered input")
	}
	after, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil || *after != *before {
		t.Fatalf("terminal state not restored: %v", err)
	}
	if strings.Contains(stderr.String(), testutil.Password) {
		t.Fatal("password echoed to output")
	}
	masterFD := int(master.Fd())
	if err := unix.SetNonblock(masterFD, true); err != nil {
		t.Fatal(err)
	}
	var echo [256]byte
	if n, err := unix.Read(masterFD, echo[:]); n > 0 || !errors.Is(err, unix.EAGAIN) {
		t.Fatalf("unexpected terminal echo: %d bytes, %v", n, err)
	}
}

func TestMCPMissingTokenFile(t *testing.T) {
	sys, paths := testSys(t)
	_, stderr, code := runCLI(t, sys, "", "mcp")
	want := "MCP token file " + paths.TokenFile("mcp") + " not found — run `tkr token create mcp --mcp --write-file` (or `tkr init --mcp-token`)"
	if code != 4 || !strings.Contains(stderr, want) {
		t.Fatalf("code=%d stderr=%q, want exit 4 containing %q", code, stderr, want)
	}
	flagged := filepath.Join(privateDir(t), "other.token")
	_, stderr, code = runCLI(t, sys, "", "mcp", "--token-file", flagged)
	if code != 4 || !strings.Contains(stderr, "MCP token file "+flagged+" not found") {
		t.Fatalf("flag: code=%d stderr=%q", code, stderr)
	}
}
