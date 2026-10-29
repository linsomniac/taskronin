package cli

import (
	"os"
	"strings"
	"testing"

	"taskronin/internal/domain"
	"taskronin/internal/testutil"
)

// scriptedPasswords answers ReadPassword in order and records the prompts.
func scriptedPasswords(answers ...string) (func(string) (string, error), *[]string) {
	var prompts []string
	return func(prompt string) (string, error) {
		prompts = append(prompts, prompt)
		a := answers[0]
		answers = answers[1:]
		return a, nil
	}, &prompts
}

func TestPasswdOnline(t *testing.T) {
	_, sys := captureServer(t)
	next := domain.NewOpID().String() // runtime-generated (BF30)
	tty := sys
	tty.StdinTTY = true

	tty.ReadPassword, _ = scriptedPasswords(testutil.Password, next, next)
	out, stderr, code := runCLI(t, tty, "", "passwd")
	if code != 0 || out != "password changed; web sessions signed out\n" || stderr != "" {
		t.Fatalf("change: %d %q %q", code, out, stderr)
	}
	if strings.Contains(out+stderr, next) {
		t.Fatal("password echoed")
	}

	// The old password is now wrong.
	tty.ReadPassword, _ = scriptedPasswords(testutil.Password, next, next)
	if out, stderr, code := runCLI(t, tty, "", "passwd"); code != 4 || out != "" {
		t.Fatalf("wrong current: %d %q %q", code, out, stderr)
	}

	// Mismatch is a usage error and nothing is sent.
	tty.ReadPassword, _ = scriptedPasswords(next, next+"1", next+"2")
	if out, stderr, code := runCLI(t, tty, "", "passwd"); code != 2 || out != "" || !strings.Contains(stderr, "passwords do not match") {
		t.Fatalf("mismatch: %d %q %q", code, out, stderr)
	}

	// Not a terminal: exit 4, no prompt, passwords never read from stdin.
	if out, stderr, code := runCLI(t, sys, next+"\n"+next+"\n"+next+"\n", "passwd"); code != 4 || out != "" || !strings.Contains(stderr, "terminal") {
		t.Fatalf("non-TTY: %d %q %q", code, out, stderr)
	}
	if _, _, code := runCLI(t, sys, "", "passwd", "--password-stdin"); code != 2 {
		t.Fatalf("--password-stdin without --reset: %d", code)
	}

	// The change took effect: the new password verifies.
	tty.ReadPassword, _ = scriptedPasswords(next, next+"x1234567", next+"x1234567")
	if _, stderr, code := runCLI(t, tty, "", "passwd"); code != 0 {
		t.Fatalf("second change: %d %q", code, stderr)
	}
}

func TestPasswdResetCLI(t *testing.T) {
	srv, sys := captureServer(t)
	srv.Stop()
	old, err := os.ReadFile(srv.Paths.OwnerToken)
	if err != nil {
		t.Fatal(err)
	}
	next := domain.NewOpID().String()
	out, stderr, code := runCLI(t, sys, next+"\n", "passwd", "--reset", "--password-stdin")
	if code != 0 || strings.Contains(out+stderr, next) {
		t.Fatalf("reset: %d %q %q", code, out, stderr)
	}
	if now, err := os.ReadFile(srv.Paths.OwnerToken); err != nil || string(now) == string(old) {
		t.Fatalf("owner.token unchanged: %v", err)
	}
	// Without --password-stdin, a non-terminal is refused (exit 4).
	if _, _, code := runCLI(t, sys, next+"\n", "passwd", "--reset"); code != 4 {
		t.Fatalf("non-TTY reset: %d", code)
	}
	srv.Start(t)
}
