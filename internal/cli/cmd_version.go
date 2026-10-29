package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"taskronin/internal/buildinfo"
	"taskronin/internal/textsafe"
)

func init() { register(versionCommand) }

func versionCommand(e *env) *cobra.Command {
	return &cobra.Command{Use: "version", Short: "Print client and server versions", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if _, err := fmt.Fprintf(e.out, "tkr %s (%s)\n", textsafe.SanitizeLine(buildinfo.Version), textsafe.SanitizeLine(buildinfo.Commit)); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(e.ctx, time.Second)
			defer cancel()
			version := "unreachable"
			if c, err := e.client(); err == nil {
				if clock, err := c.Clock(ctx); err == nil {
					version = textsafe.SanitizeLine(clock.ServerVersion)
				}
			}
			_, err := fmt.Fprintf(e.out, "server %s\n", version)
			return err
		}}
}
