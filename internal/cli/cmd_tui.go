package cli

import (
	"errors"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/linsomniac/taskronin/internal/client"
	"github.com/linsomniac/taskronin/internal/datespec"
	"github.com/linsomniac/taskronin/internal/tui"
)

func init() { launchTUI = runTUI }

func runTUI(cmd *cobra.Command, req TUIRequest) error {
	e := envFrom(cmd)
	var tab tui.Tab
	var mode tui.Mode
	switch req.Mode {
	case "tabs":
		mode = tui.ModeTabs
		if req.Tab != "" {
			var err error
			tab, err = tui.ParseTab(req.Tab)
			if err != nil {
				return usagef("%v", err)
			}
		}
	case "triage":
		mode = tui.ModeTriage
	case "plan":
		mode = tui.ModePlan
	case "recap":
		mode = tui.ModeRecap
	default:
		return usagef("unknown TUI mode %q", req.Mode)
	}
	if !e.sys.StdinTTY || !e.sys.StdoutTTY {
		return usagef("the TUI needs a terminal")
	}
	cfg, err := e.clientConfig()
	if err != nil {
		return err
	}
	c, err := client.NewFromConfig(cfg, "tui", nil)
	if err != nil {
		return err
	}
	err = tui.Run(cmd.Context(), tui.RunOptions{
		API: c, StartTab: tab, StartMode: mode, RecapSpec: req.RecapDate, Color: e.colorEnabled(),
		Editor: tui.EditorEnv{Setting: cfg.Editor, Getenv: e.sys.Getenv, DraftsDir: e.paths.DraftsDir},
		In:     tuiInput(e.in), Out: e.out,
	})
	if errors.Is(err, tui.ErrUsage) {
		if req.Mode == "recap" && req.RecapDate != "" {
			var input *datespec.InputError
			if errors.As(err, &input) {
				return dateUsage("--date", input)
			}
			return usagef("--date: %s", strings.TrimPrefix(err.Error(), tui.ErrUsage.Error()+": "))
		}
		return usagef("%v", err)
	}
	return err
}

func tuiInput(in io.Reader) io.Reader {
	// AIDEV-NOTE: Bubble Tea v2.1.0 enters raw mode only for a term.File.
	// Main wraps TTY stdin for cancellable CLI reads; Bubble Tea needs the
	// underlying file and handles cancellation itself through WithContext.
	if terminal, ok := in.(terminalInput); ok {
		return terminal.file
	}
	return in
}
