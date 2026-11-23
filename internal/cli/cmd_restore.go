package cli

import (
	"github.com/spf13/cobra"

	"github.com/linsomniac/taskronin/internal/config"
	"github.com/linsomniac/taskronin/internal/fsutil"
	"github.com/linsomniac/taskronin/internal/offline"
)

func init() { register(restoreCommand) }

func restoreCommand(e *env) *cobra.Command {
	var resume, rollback, yes bool
	cmd := &cobra.Command{Use: "restore [SNAPSHOT]", Short: "Restore a snapshot with the server stopped, or finish an interrupted restore or import",
		Long: "Restore a snapshot by name (see `tkr backup --list`), path, or pre-restore directory. The server must be\n" +
			"stopped. The current database moves to <db>.pre-restore-<time>/.\n" +
			"After an interruption, `--resume` finishes the swap and `--rollback` puts the old database back.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			switch {
			case resume && rollback:
				return usagef("--resume and --rollback are mutually exclusive")
			case (resume || rollback) && (len(args) != 0 || yes):
				return usagef("--resume and --rollback take no SNAPSHOT and no --yes")
			case !resume && !rollback && len(args) != 1:
				return usagef("restore needs a SNAPSHOT (name or path), or --resume / --rollback")
			case !resume && !rollback && !yes && !e.sys.StdinTTY:
				return usagef("restore needs confirmation: run it on a terminal or pass --yes")
			}
			// AIDEV-NOTE: restore never talks to the server (it must be stopped);
			// every path goes through offline, which holds the instance lock.
			fsutil.SetUmask(0o077)
			cfg, err := config.LoadServer(e.paths.ServerConfig)
			if err != nil {
				return err
			}
			o := e.offlineEnv()
			o.Server = cfg
			switch {
			case resume:
				return offline.RestoreResume(e.ctx, o)
			case rollback:
				return offline.RestoreRollback(e.ctx, o)
			}
			_, err = offline.Restore(e.ctx, o, args[0], yes)
			return err
		}}
	f := cmd.Flags()
	f.BoolVar(&resume, "resume", false, "Finish an interrupted restore or import")
	f.BoolVar(&rollback, "rollback", false, "Put the old database back after an interrupted restore or import")
	f.BoolVarP(&yes, "yes", "y", false, "Do not ask for confirmation")
	return cmd
}
