package cli

import (
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/linsomniac/taskronin/internal/config"
	"github.com/linsomniac/taskronin/internal/fsutil"
	"github.com/linsomniac/taskronin/internal/server"
)

func init() { register(serveCommand) }

func serveCommand(e *env) *cobra.Command {
	return &cobra.Command{Use: "serve", Short: "Run the Taskronin server", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			fsutil.SetUmask(0o077)
			cfg, err := config.LoadServer(e.paths.ServerConfig)
			if err != nil {
				return err
			}
			var level slog.Level
			if err := level.UnmarshalText([]byte(cfg.Log.Level)); err != nil {
				return err
			}
			opts := &slog.HandlerOptions{Level: level}
			var handler slog.Handler = slog.NewTextHandler(e.err, opts)
			if cfg.Log.Format == "json" {
				handler = slog.NewJSONHandler(e.err, opts)
			}
			return server.Run(e.ctx, cfg, server.Deps{Paths: e.paths, Logger: slog.New(handler)})
		}}
}
