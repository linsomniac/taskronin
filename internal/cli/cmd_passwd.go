package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/config"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/fsutil"
	"github.com/linsomniac/taskronin/internal/offline"
)

func init() { register(passwdCommand) }

func passwdCommand(e *env) *cobra.Command {
	var reset, stdin bool
	cmd := &cobra.Command{Use: "passwd", Short: "Change the owner password, or reset it with the server stopped", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if stdin && !reset {
				return usagef("--password-stdin requires --reset")
			}
			if reset {
				return e.passwdReset(stdin)
			}
			return e.passwdChange()
		}}
	cmd.Flags().BoolVar(&reset, "reset", false, "Reset the password with the server stopped (revokes every token)")
	cmd.Flags().BoolVar(&stdin, "password-stdin", false, "With --reset, read the new password as one stdin line")
	return cmd
}

func (e *env) passwdReset(stdin bool) error {
	fsutil.SetUmask(0o077)
	cfg, err := config.LoadServer(e.paths.ServerConfig)
	if err != nil {
		return err
	}
	o := e.offlineEnv()
	o.Server = cfg
	return offline.PasswdReset(e.ctx, o, stdin)
}

// passwdChange is the online change: passwords are read only from the terminal
// (never argv, the environment or a pipe) and sent once, unsaved and unreplayed.
func (e *env) passwdChange() error {
	// AIDEV-NOTE: SECURITY. Prompt before building the client so a non-terminal
	// fails fast; never echo, log or persist the passwords, and never mark this
	// command "mutates" (that would echo/save the request for retry).
	if !e.sys.StdinTTY || e.sys.ReadPassword == nil {
		return domain.Errorf(domain.CodePasswordRequired, "password required; run on a terminal")
	}
	current, err := e.sys.ReadPassword("Current password: ")
	if err != nil {
		return err
	}
	next, err := e.sys.ReadPassword("New password: ")
	if err != nil {
		return err
	}
	again, err := e.sys.ReadPassword("New password again: ")
	if err != nil {
		return err
	}
	if next != again {
		return usagef("passwords do not match")
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	if err := c.ChangePassword(e.ctx, api.PasswordRequest{Current: current, New: next}); err != nil {
		return err
	}
	_, err = fmt.Fprintln(e.out, "password changed; web sessions signed out")
	return err
}
