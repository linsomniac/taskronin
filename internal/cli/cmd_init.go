package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/spf13/cobra"

	"taskronin/internal/domain"
	"taskronin/internal/fsutil"
	"taskronin/internal/offline"
	"taskronin/internal/textsafe"
)

func init() { register(initCommand) }

func initCommand(e *env) *cobra.Command {
	var options offline.InitOptions
	cmd := &cobra.Command{Use: "init", Short: "Initialize a private Taskronin instance", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			fsutil.SetUmask(0o077)
			_, err := offline.Init(e.ctx, e.offlineEnv(), options)
			return err
		}}
	f := cmd.Flags()
	f.StringVar(&options.Timezone, "timezone", "", "IANA timezone (default: /etc/localtime)")
	f.BoolVar(&options.PasswordStdin, "password-stdin", false, "Read one password line from stdin")
	f.BoolVar(&options.NoSystemd, "no-systemd", false, "Skip installing systemd user units")
	f.BoolVarP(&options.Yes, "yes", "y", false, "Accept initialization confirmations")
	f.BoolVar(&options.MCPToken, "mcp-token", false, "Create an MCP token")
	f.BoolVar(&options.UnitsOnly, "units-only", false, "Reinstall units from the existing configuration")
	return cmd
}

func (e *env) offlineEnv() *offline.Env {
	// AIDEV-NOTE: share a buffered reader with offline.Init so password-stdin
	// cannot read ahead and hide a later confirmation or MCP expiry answer.
	reader := bufio.NewReader(e.in)
	return &offline.Env{
		Paths: e.paths, In: reader, Out: e.out, Err: e.err, TTY: e.sys.StdinTTY,
		ReadPassword: e.sys.ReadPassword, Logger: slog.New(slog.NewTextHandler(e.err, nil)),
		Confirm: func(prompt string, defaultYes bool) (bool, error) {
			if !e.sys.StdinTTY {
				return false, domain.Errorf(domain.CodeValidation, "confirmation requires a terminal or --yes")
			}
			if err := e.ctx.Err(); err != nil {
				return false, err
			}
			suffix := " [y/N] "
			if defaultYes {
				suffix = " [Y/n] "
			}
			if _, err := fmt.Fprint(e.err, textsafe.SanitizeLine(prompt)+suffix); err != nil {
				return false, err
			}
			line, err := reader.ReadString('\n')
			if err != nil && (!errors.Is(err, io.EOF) || line == "") {
				return false, err
			}
			switch strings.ToLower(strings.TrimSpace(line)) {
			case "":
				return defaultYes, nil
			case "y", "yes":
				return true, nil
			default:
				return false, nil
			}
		},
	}
}
