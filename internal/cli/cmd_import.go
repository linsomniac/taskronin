package cli

import (
	"github.com/spf13/cobra"

	"taskronin/internal/config"
	"taskronin/internal/fsutil"
	"taskronin/internal/offline"
)

func init() { register(importCommand) }

func importCommand(e *env) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{Use: "import FILE", Short: "Import a JSON export into a freshly initialized database, with the server stopped",
		Long: "Import a file written by `tkr export` into a database that was just created by `tkr init` and holds no\n" +
			"tasks, entries, blocks, tags or interruptions. The server must be stopped. Ids and history are kept; the\n" +
			"empty database moves to <db>.pre-import-<time>/. After an interruption, `tkr restore --resume` finishes the\n" +
			"swap and `tkr restore --rollback` puts the old database back.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if !yes && !e.sys.StdinTTY {
				return usagef("import needs confirmation: run it on a terminal or pass --yes")
			}
			// AIDEV-NOTE: like restore, import never talks to the server (it must
			// be stopped); offline.Import holds the instance lock throughout.
			fsutil.SetUmask(0o077)
			cfg, err := config.LoadServer(e.paths.ServerConfig)
			if err != nil {
				return err
			}
			o := e.offlineEnv()
			o.Server = cfg
			_, err = offline.Import(e.ctx, o, args[0], yes)
			return err
		}}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Do not ask for confirmation")
	return cmd
}
