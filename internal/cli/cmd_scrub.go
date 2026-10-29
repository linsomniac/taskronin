package cli

import (
	"github.com/spf13/cobra"

	"taskronin/internal/config"
	"taskronin/internal/fsutil"
	"taskronin/internal/offline"
)

func init() { register(scrubCommand) }

func scrubCommand(e *env) *cobra.Command {
	var o offline.ScrubOptions
	cmd := &cobra.Command{Use: "scrub-backups", Short: "Remove purged content from every backup, with the server stopped",
		Long: "Apply the purge ledger to every backup snapshot and pre-restore/pre-import set, rewriting each one that\n" +
			"still holds purged content. The server must be stopped and the owner password is required on a terminal.\n" +
			"After an interruption, `--resume` finishes the interrupted rewrite first (a plain run does too).\n" +
			"`--check` only reports which sets still contain purged content. Once every set is clean, leftover client\n" +
			"recovery files (saved requests and editor drafts) are deleted after confirmation, or kept with\n" +
			"`--keep-recovery-files`, and the purge is marked complete.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			// AIDEV-NOTE: scrub-backups never talks to the server (it must be
			// stopped); offline.ScrubBackups holds the instance lock throughout.
			fsutil.SetUmask(0o077)
			cfg, err := config.LoadServer(e.paths.ServerConfig)
			if err != nil {
				return err
			}
			env := e.offlineEnv()
			env.Server = cfg
			_, err = offline.ScrubBackups(e.ctx, env, o)
			return err
		}}
	f := cmd.Flags()
	f.BoolVar(&o.Check, "check", false, "Only report sets that still contain purged content; change nothing")
	f.BoolVar(&o.Resume, "resume", false, "Finish an interrupted rewrite first (a plain run does this too)")
	f.BoolVar(&o.KeepRecoveryFiles, "keep-recovery-files", false, "Complete without deleting saved requests and drafts")
	f.BoolVarP(&o.Yes, "yes", "y", false, "Delete recovery files without asking")
	return cmd
}
