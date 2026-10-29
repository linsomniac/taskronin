// Package cli implements the tkr command line interface with injected I/O.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

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
	}
	if outFile, ok := stdout.(*os.File); ok {
		s.StdoutTTY = term.IsTerminal(int(outFile.Fd()))
	}
	s.ReadPassword = func(prompt string) (string, error) {
		if !s.StdinTTY {
			return "", domain.Errorf(domain.CodePasswordRequired, "the password can only be entered on a terminal")
		}
		if _, err := fmt.Fprint(stderr, textsafe.SanitizeLine(prompt)); err != nil {
			return "", err
		}
		password, err := term.ReadPassword(int(inFile.Fd()))
		_, newlineErr := fmt.Fprintln(stderr)
		return string(password), errors.Join(err, newlineErr)
	}
	return run(ctx, args, stdin, stdout, stderr, s)
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, s sysDeps) int {
	if s.Getenv == nil {
		s.Getenv = func(string) string { return "" }
	}
	e := &env{ctx: ctx, argv: append([]string(nil), args...), in: stdin, out: stdout, err: stderr, sys: s}
	var err error
	e.paths, err = config.DefaultPaths(s.Getenv, s.Home)
	if err != nil {
		return e.fail(err)
	}
	root := newRoot(e)
	root.SetArgs(args)
	if _, err := root.ExecuteContextC(e.ctx); err != nil {
		if !e.executing {
			err = usagef("%s", err)
		}
		return e.fail(err)
	}
	return 0
}
