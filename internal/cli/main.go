// Package cli implements the tkr command line interface with injected I/O.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"golang.org/x/term"

	"taskronin/internal/config"
	"taskronin/internal/domain"
	"taskronin/internal/textsafe"
)

type sysDeps struct {
	StdinTTY, StdoutTTY bool
	Getenv              func(string) string
	Home                string
	ReadPassword        func(prompt string) (string, error)
}

// Main supplies real process dependencies and returns an exit code.
func Main(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	home, _ := os.UserHomeDir()
	s := sysDeps{Getenv: os.Getenv, Home: home}
	inFile, isFile := stdin.(*os.File)
	if isFile {
		s.StdinTTY = term.IsTerminal(int(inFile.Fd()))
		if s.StdinTTY {
			stdin = terminalInput{ctx: ctx, file: inFile}
		}
	}
	if outFile, ok := stdout.(*os.File); ok {
		s.StdoutTTY = term.IsTerminal(int(outFile.Fd()))
	}
	s.ReadPassword = func(prompt string) (string, error) {
		if !s.StdinTTY {
			return "", domain.Errorf(domain.CodePasswordRequired, "the password can only be entered on a terminal")
		}
		return readTerminalPassword(ctx, inFile, stdin, stderr, prompt)
	}
	return run(ctx, args, stdin, stdout, stderr, s)
}

type terminalInput struct {
	ctx  context.Context
	file *os.File
}

func (r terminalInput) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	// AIDEV-NOTE: a separate nonblocking open lets os.File use Go's poller.
	// Cancellation closes only this owned handle, unblocking Read without
	// closing the caller's stdin or changing its file-status flags. A raw
	// term.ReadPassword syscall cannot be interrupted by closing another fd.
	path := fmt.Sprintf("/proc/self/fd/%d", r.file.Fd())
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // G304: proc path is constructed only from the caller's open terminal fd.
	if err != nil {
		return 0, err
	}
	defer f.Close()
	stop := context.AfterFunc(r.ctx, func() { _ = f.Close() })
	defer stop()
	n, err := f.Read(p)
	if ctxErr := r.ctx.Err(); ctxErr != nil {
		return 0, ctxErr
	}
	return n, err
}

type passwordInput struct{ io.Reader }

func (r passwordInput) Read(p []byte) (int, error) {
	// Read only one byte so x/term cannot retain a later prompt's answer.
	// Raw mode delivers Ctrl-C as a byte instead of a process signal.
	n, err := r.Reader.Read(p[:min(len(p), 1)])
	if n > 0 && p[0] == 3 {
		return 0, context.Canceled
	}
	return n, err
}

func readTerminalPassword(ctx context.Context, file *os.File, in io.Reader, out io.Writer, prompt string) (password string, err error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	fd := int(file.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return "", err
	}
	defer func() {
		err = errors.Join(err, term.Restore(fd, state))
		if err != nil {
			_, newlineErr := fmt.Fprintln(out)
			err = errors.Join(err, newlineErr)
		}
	}()
	terminal := term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{passwordInput{in}, out}, "")
	return terminal.ReadPassword(textsafe.SanitizeLine(prompt))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, s sysDeps) int {
	if s.Getenv == nil {
		s.Getenv = func(string) string { return "" }
	}
	e := &env{ctx: ctx, argv: append([]string(nil), args...), in: stdin, out: stdout, err: stderr, sys: s}
	root := newRoot(e)
	if e.tryShortStatus(root, args) {
		return 0
	}
	var err error
	e.paths, err = config.DefaultPaths(s.Getenv, s.Home)
	if err != nil {
		return e.fail(err)
	}
	// A nil slice makes Cobra fall back to the process arguments. The injected
	// CLI invocation may intentionally be empty (the dashboard).
	root.SetArgs(append([]string{}, args...))
	if _, err := root.ExecuteContextC(e.ctx); err != nil {
		if !e.executing {
			err = usagef("%s", err)
		}
		return e.fail(err)
	}
	return 0
}
