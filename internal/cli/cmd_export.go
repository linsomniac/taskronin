package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"taskronin/internal/fsutil"
	"taskronin/internal/textsafe"
)

func init() { register(exportCommand) }

func exportCommand(e *env) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use: "export", Short: "Write a portable JSON export of all data", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			c, err := e.client()
			if err != nil {
				return err
			}
			path := output
			if path == "" {
				clock, err := e.clock()
				if err != nil {
					return err
				}
				path = "./taskronin-export-" + strings.ReplaceAll(clock.Today.String(), "-", "") + ".json"
			}
			// AIDEV-NOTE: stream into a private temp file beside the target and
			// rename only after fsync; every error path removes the temp, so a
			// failed or interrupted export never leaves a partial export file.
			tmp, err := os.CreateTemp(filepath.Dir(path), ".tkr-tmp-*")
			if err != nil {
				return err
			}
			done := false
			defer func() {
				if !done {
					_ = tmp.Close()
					_ = os.Remove(tmp.Name())
				}
			}()
			if err := c.Export(e.ctx, tmp); err != nil {
				return err
			}
			if err := tmp.Sync(); err != nil {
				return err
			}
			if err := tmp.Close(); err != nil {
				return err
			}
			if err := os.Rename(tmp.Name(), path); err != nil {
				return err
			}
			done = true
			if err := fsutil.FsyncDir(filepath.Dir(path)); err != nil {
				return err
			}
			_, err = fmt.Fprintln(e.out, textsafe.SanitizeLine(path))
			return err
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output file (default ./taskronin-export-YYYYMMDD.json)")
	return cmd
}
