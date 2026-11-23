package cli

import (
	"github.com/spf13/cobra"

	"github.com/linsomniac/taskronin/internal/config"
	"github.com/linsomniac/taskronin/internal/fsutil"
	"github.com/linsomniac/taskronin/internal/offline"
)

func init() { register(migrateCommand) }

func migrateCommand(e *env) *cobra.Command {
	var status bool
	cmd := &cobra.Command{Use: "migrate", Short: "Migrate the database with the server stopped", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			fsutil.SetUmask(0o077)
			cfg, err := config.LoadServer(e.paths.ServerConfig)
			if err != nil {
				return err
			}
			o := e.offlineEnv()
			o.Server = cfg
			return offline.Migrate(e.ctx, o, status)
		}}
	cmd.Flags().BoolVar(&status, "status", false, "Show schema status without migrating")
	return cmd
}
