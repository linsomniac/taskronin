// Package editor runs the user's editor without a shell and retains private
// drafts until a caller explicitly discards them after confirmation.
package editor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
)

// Command chooses configured, $VISUAL, $EDITOR, then vi, in that order.
func Command(configured string, getenv func(string) string) ([]string, error) {
	if configured == "" {
		configured = getenv("VISUAL")
	}
	if configured == "" {
		configured = getenv("EDITOR")
	}
	if configured == "" {
		configured = "vi"
	}
	argv, err := SplitWords(configured)
	if err != nil {
		return nil, err
	}
	if argv[0] == "" {
		return nil, errors.New("empty editor command")
	}
	return argv, nil
}

// Edit appends the draft path to argv, runs the editor with the supplied streams,
// and reads the resulting buffer. It never discards the draft, even on success.
func Edit(ctx context.Context, argv []string, d *Draft, stdin io.Reader, stdout, stderr io.Writer) (string, error) {
	if len(argv) == 0 || argv[0] == "" {
		return "", errors.New("empty editor command")
	}
	if d == nil || d.Path == "" {
		return "", errors.New("empty draft path")
	}
	args := append(slices.Clone(argv[1:]), d.Path)
	// AIDEV-NOTE: editor configuration is argv, never input to a shell. The
	// caller keeps the draft through validation and server confirmation.
	cmd := exec.CommandContext(ctx, argv[0], args...) //nolint:gosec // G204: argv is the user's own editor setting, split without a shell (A§10.6)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("run editor: %w", ctx.Err())
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("editor exited with status %d", exitErr.ExitCode())
		}
		return "", fmt.Errorf("run editor: %w", err)
	}
	data, err := os.ReadFile(d.Path)
	if err != nil {
		return "", fmt.Errorf("read editor draft: %w", err)
	}
	return string(data), nil
}
