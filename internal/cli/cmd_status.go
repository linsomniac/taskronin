package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/client"
	"github.com/linsomniac/taskronin/internal/config"
	"github.com/linsomniac/taskronin/internal/textsafe"
)

func init() { register(statusCommand) }

func statusCommand(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use: "status", Short: "Show focus, counts and service health", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			c, err := e.client()
			if err != nil {
				return err
			}
			view, _, _, err := c.Status(e.ctx, "")
			if err != nil {
				return err
			}
			if e.jsonOut {
				return json.NewEncoder(e.out).Encode(view)
			}
			p := e.paint()
			focus, block := "focus: none", "none"
			if view.Focus != nil {
				focus = p.ok("focus:") + " " + p.taskID(view.Focus.ID) + " " + p.ok(textsafe.SanitizeLine(view.Focus.Title))
			}
			if b := view.CurrentBlock; b != nil {
				block = fmt.Sprintf("%s %s %s (→%s)", p.blockID(b.ID), p.taskID(b.TaskID), textsafe.SanitizeLine(b.Title), b.End)
			}
			health := func(warning bool) string {
				if warning {
					return "warning"
				}
				return "ok"
			}
			_, err = fmt.Fprintf(e.out, "%s\ncurrent block: %s\ntriage: %d\ndue: %d\nai: %d\nbackup: %s\npurge: %s\n",
				focus, block, view.Triage, view.Due, view.AI, health(view.BackupWarn), health(view.PurgeWarning))
			return err
		},
	}
	cmd.Flags().Bool("short", false, "Print a quiet, prompt-safe status line")
	cmd.Flags().Bool("ascii", false, "Use ASCII status markers and separators")
	return cmd
}

// tryShortStatus handles the prompt contract before path/config errors can reach
// normal CLI error reporting. Cobra still validates commands and flags.
func (e *env) tryShortStatus(root *cobra.Command, args []string) bool {
	cmd, flags, err := root.Find(args)
	if err != nil || cmd.Name() != "status" {
		return false
	}
	if !shortStatusRequested(cmd, flags) {
		return false
	}
	cmd.InitDefaultHelpFlag()
	if err := cmd.ParseFlags(flags); err != nil {
		return true
	}
	if err := cmd.ValidateArgs(cmd.Flags().Args()); err != nil {
		return true
	}
	switch e.color {
	case "auto", "always", "never":
	default:
		return true
	}
	e.colorSet = cmd.Flags().Changed("color")
	ascii, _ := cmd.Flags().GetBool("ascii")
	e.shortStatus(ascii)
	return true
}

func shortStatusRequested(cmd *cobra.Command, args []string) bool {
	// AIDEV-NOTE: discover the quiet contract before Cobra can stop at a bad
	// flag. Respect flag operands and -- so a token filename or positional
	// argument named --short cannot silence an ordinary status invocation.
	short := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "--") {
			continue
		}
		name, value, equals := strings.Cut(arg[2:], "=")
		flag := cmd.Flag(name)
		if flag == nil {
			continue
		}
		if name == "short" {
			parsed, err := strconv.ParseBool(value)
			short = !equals || err != nil || parsed
		}
		if !equals && flag.NoOptDefVal == "" {
			i++
		}
	}
	return short
}

func (e *env) shortStatus(ascii bool) {
	// AIDEV-NOTE: BF22 includes setup in the total real-time budget. Context
	// timers work independently of the server's civil clock. Until config is
	// available, bound its I/O by the default budget; anchor a configured budget
	// to that same start instead of granting a fresh timeout after setup.
	budget := config.DefaultClient(config.Paths{}).StatusTimeout
	// Keep the start anchor independent of an earlier caller deadline; observe
	// caller cancellation in the select and apply it to the request below.
	setupCtx, cancelSetup := context.WithTimeout(context.WithoutCancel(e.ctx), budget)
	defer cancelSetup()
	deadline, _ := setupCtx.Deadline()
	configured := make(chan *config.Client, 1)
	go func() {
		paths, err := config.DefaultPaths(e.sys.Getenv, e.sys.Home)
		if err != nil {
			configured <- nil
			return
		}
		overrides := config.ClientOverrides{Socket: e.socket, TokenFile: e.tokenFile}
		if e.colorSet {
			overrides.Color = e.color
		}
		cfg, err := config.LoadClientWithOverrides(paths.ClientConfig, paths, overrides)
		if err != nil {
			configured <- nil
			return
		}
		configured <- cfg
	}()
	var cfg *config.Client
	select {
	case <-e.ctx.Done():
		return
	case <-setupCtx.Done():
		return
	case cfg = <-configured:
		if cfg == nil {
			return
		}
	}
	ctx, cancel := context.WithDeadline(e.ctx, deadline.Add(cfg.StatusTimeout-budget))
	defer cancel()
	result := make(chan *api.Status, 1)
	go func() {
		if ctx.Err() != nil {
			result <- nil
			return
		}
		c, err := client.New(client.Options{Socket: cfg.Socket, TokenFile: cfg.TokenFile, Channel: "cli", Timeout: cfg.StatusTimeout, NoRetry: true})
		if err != nil {
			result <- nil
			return
		}
		view, _, _, err := c.Status(ctx, "")
		if err != nil {
			result <- nil
			return
		}
		result <- view
	}()
	select {
	case <-ctx.Done():
		return
	case view := <-result:
		if view == nil || ctx.Err() != nil {
			return
		}
		if line := statusLine(view, ascii); line != "" {
			_, _ = fmt.Fprintln(e.out, line)
		}
	}
}

func statusLine(view *api.Status, ascii bool) string {
	filled, hollow, separator, backup, purge := "▶", "▷", " · ", "⚠ backup", "⚠ purge"
	if ascii {
		filled, hollow, separator, backup, purge = ">", ">.", " | ", "!backup", "!purge"
	}
	title := func(s string) string {
		s = textsafe.SanitizeLine(s)
		if textsafe.Width(s) > 24 {
			// The contract retains 24 columns BEFORE appending the ellipsis.
			return textsafe.Truncate(s, 24, "") + "…"
		}
		return s
	}
	var segments []string
	b := view.CurrentBlock
	if f := view.Focus; f != nil {
		focus := fmt.Sprintf("%s #%d %s", filled, f.ID, title(f.Title))
		if b != nil && b.TaskID == f.ID {
			focus += fmt.Sprintf(" (→%s)", b.End)
		}
		segments = append(segments, focus)
	} else if b != nil {
		segments = append(segments, fmt.Sprintf("%s #%d %s (→%s)", hollow, b.TaskID, title(b.Title), b.End))
	}
	for _, count := range []struct {
		label string
		n     int
	}{{"triage", view.Triage}, {"due", view.Due}, {"ai", view.AI}} {
		if count.n > 0 {
			segments = append(segments, fmt.Sprintf("%s %d", count.label, count.n))
		}
	}
	if view.BackupWarn {
		segments = append(segments, backup)
	}
	if view.PurgeWarning {
		segments = append(segments, purge)
	}
	return strings.Join(segments, separator)
}
