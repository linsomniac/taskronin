package cli

import (
	"strconv"

	"github.com/spf13/cobra"

	"taskronin/internal/config"
	"taskronin/internal/fsutil"
	"taskronin/internal/offline"
)

func init() { register(purgeCommand) }

func purgeCommand(e *env) *cobra.Command {
	var o offline.PurgeOptions
	var repair bool
	cmd := &cobra.Command{Use: "purge (task|entry) ID | --repair-ledger", Short: "Permanently scrub a deleted task or entry everywhere, with the server stopped",
		Long: "Scrub a deleted task or entry from the database and every backup, for secrets pasted by mistake.\n" +
			"The server must be stopped and the owner password is required on a terminal. The target must be deleted first.\n" +
			"Without --with-entries, only a task's completion entries are purged; other linked entries are listed and kept.\n" +
			"Exports and log lines made earlier are out of reach. `--repair-ledger` repairs a damaged purge ledger.",
		Args: cobra.MaximumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			if repair {
				if len(args) != 0 || o.WithEntries || o.KeepRecoveryFiles || o.Yes {
					return usagef("--repair-ledger takes no other arguments or flags")
				}
			} else {
				if len(args) != 2 || (args[0] != "task" && args[0] != "entry") {
					return usagef("purge needs `task ID` or `entry ID`, or --repair-ledger")
				}
				id, err := strconv.ParseInt(args[1], 10, 64)
				if err != nil || id < 1 {
					return usagef("invalid ID %q", args[1])
				}
				if args[0] == "entry" && o.WithEntries {
					return usagef("--with-entries applies to `purge task` only")
				}
				o.Target, o.ID = args[0], id
			}
			// AIDEV-NOTE: purge never talks to the server (it must be stopped) and
			// never writes pending or draft files; every path goes through
			// offline, which holds the instance lock. The password is read from
			// the terminal only (Env.ReadPassword), never from flags or stdin.
			fsutil.SetUmask(0o077)
			cfg, err := config.LoadServer(e.paths.ServerConfig)
			if err != nil {
				return err
			}
			env := e.offlineEnv()
			env.Server = cfg
			if repair {
				_, err = offline.RepairLedger(e.ctx, env)
				return err
			}
			_, err = offline.Purge(e.ctx, env, o)
			return err
		}}
	f := cmd.Flags()
	f.BoolVar(&repair, "repair-ledger", false, "Rewrite a damaged purge ledger from its valid records")
	f.BoolVar(&o.WithEntries, "with-entries", false, "Also purge every entry linked to the task")
	f.BoolVar(&o.KeepRecoveryFiles, "keep-recovery-files", false, "Complete without deleting saved requests and drafts")
	f.BoolVarP(&o.Yes, "yes", "y", false, "Do not ask for confirmation (the password is still required)")
	return cmd
}
