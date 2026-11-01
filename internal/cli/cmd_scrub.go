package cli

import (
	"fmt"

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
			"`--check` reports which sets still contain purged content and exits 8 if any do; it rewrites no backup, but still\n" +
			"finishes an interrupted rewrite and applies the purge ledger to the live database. Once every set is clean, leftover client\n" +
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
			results, err := offline.ScrubBackups(e.ctx, env, o)
			if err != nil || !o.Check {
				return err
			}
			return scrubCheckResult(results)
		}}
	f := cmd.Flags()
	f.BoolVar(&o.Check, "check", false, "Report sets that still contain purged content (exit 8 if any) without rewriting them")
	f.BoolVar(&o.Resume, "resume", false, "Finish an interrupted rewrite first (a plain run does this too)")
	f.BoolVar(&o.KeepRecoveryFiles, "keep-recovery-files", false, "Complete without deleting saved requests and drafts")
	f.BoolVarP(&o.Yes, "yes", "y", false, "Delete recovery files without asking")
	return cmd
}

// scrubCheckResult turns a successful `--check` run into exit 8 when any set
// still holds purged content (Sean's ruling, 2026-10-29: scripts must be able
// to detect it). A run with set errors has already failed with exit 1.
func scrubCheckResult(results []offline.SetResult) error {
	n := 0
	for _, r := range results {
		if r.Status == offline.SetStatusContains {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	return &checkFoundError{detail: fmt.Sprintf("%d backup set(s) still contain purged content; run `tkr scrub-backups` to rewrite them", n)}
}
