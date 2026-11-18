package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"taskronin/internal/testutil"
)

func TestTUIHookInstalled(t *testing.T) {
	if launchTUI == nil {
		t.Fatal("TUI hook is not installed")
	}
	sys, _ := testSys(t)
	sys.StdinTTY, sys.StdoutTTY = true, true
	out, stderr, code := runCLI(t, sys, "", "tui", "--tab", "nope")
	if code != 2 || out != "" || !strings.Contains(stderr, `unknown tab "nope"`) {
		t.Fatalf("invalid tab: code=%d stdout=%q stderr=%q", code, out, stderr)
	}
	for _, args := range [][]string{{"tui"}, {"triage", "-i"}, {"plan"}, {"recap"}} {
		for _, inputTTY := range []bool{false, true} {
			sys.StdinTTY, sys.StdoutTTY = inputTTY, !inputTTY
			out, stderr, code := runCLI(t, sys, "", args...)
			if code != 2 || out != "" || !strings.Contains(stderr, "the TUI needs a terminal") {
				t.Fatalf("%v stdinTTY=%v: code=%d stdout=%q stderr=%q", args, inputTTY, code, out, stderr)
			}
		}
	}
}

func TestTUIHookRecapUsage(t *testing.T) {
	testutil.StartServer(t)
	sys := sysDeps{StdinTTY: true, StdoutTTY: true, Getenv: os.Getenv}
	for _, tc := range []struct{ spec, message string }{
		{"nextweek", `tkr: --date: unrecognised date "nextweek": use today, yesterday, tomorrow, a weekday (mon…sun), +Nd/-Nd, +Nw/-Nw, or YYYY-MM-DD`},
		{"tue 10:00", "tkr: --date: expected a date without a time"},
	} {
		spec := tc.spec
		out, stderr, code := runCLI(t, sys, "", "recap", "--date", spec)
		if code != 2 || out != "" || strings.TrimSpace(stderr) != tc.message {
			t.Errorf("date %q: code=%d stdout=%q stderr=%q", spec, code, out, stderr)
		}
	}
}

func TestTUIHookUnreachable(t *testing.T) {
	srv := testutil.StartServer(t)
	srv.Stop()
	sys := sysDeps{StdinTTY: true, StdoutTTY: true, Getenv: os.Getenv}
	for _, args := range [][]string{
		{"tui"}, {"tui", "--tab", "today"}, {"tui", "--tab", "triage"},
		{"tui", "--tab", "backlog"}, {"tui", "--tab", "upcoming"}, {"tui", "--tab", "log"},
		{"triage", "-i"}, {"plan"}, {"recap"},
	} {
		out, stderr, code := runCLI(t, sys, "", args...)
		if code != 3 || out != "" || !strings.Contains(stderr, "cannot reach the Taskronin server") {
			t.Fatalf("%v: code=%d stdout=%q stderr=%q", args, code, out, stderr)
		}
	}
}

func TestTUIHookUnknownMode(t *testing.T) {
	e := &env{ctx: t.Context()}
	cmd := newRoot(e)
	err := runTUI(cmd, TUIRequest{Mode: "unknown"})
	var usage *usageError
	if !errors.As(err, &usage) {
		t.Fatalf("runTUI = %v, want usage error", err)
	}
}

func TestTUIHookInputFile(t *testing.T) { // CF01: Bubble Tea must receive a term.File to enter raw mode.
	f, err := os.Create(filepath.Join(t.TempDir(), "stdin"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	reader := strings.NewReader("q")
	for _, tc := range []struct {
		name     string
		in, want io.Reader
	}{
		{"terminal wrapper", terminalInput{ctx: t.Context(), file: f}, f},
		{"raw file", f, f},
		{"reader", reader, reader},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tuiInput(tc.in); got != tc.want {
				t.Fatalf("tuiInput = %T %v, want %T %v", got, got, tc.want, tc.want)
			}
		})
	}
}
