package cli

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/linsomniac/taskronin/internal/client"
	"github.com/linsomniac/taskronin/internal/mcpserver"
)

func init() { register(mcpCommand) }

// mcpTokenMissingError reports an absent MCP token file with the recovery
// command. It is a token-file failure, so the exit code is 4 (A§9.6).
type mcpTokenMissingError struct{ path string }

func (e *mcpTokenMissingError) Error() string {
	return fmt.Sprintf("MCP token file %s not found — run `tkr token create mcp --mcp --write-file` (or `tkr init --mcp-token`)", e.path)
}

func (e *mcpTokenMissingError) Is(target error) bool { return target == client.ErrTokenFile }

func mcpCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use: "mcp", Short: "Run the MCP server on stdio (needs its own token)", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := e.clientConfig()
			if err != nil {
				return err
			}
			// AIDEV-NOTE: the global --token-file flag names the MCP token here;
			// otherwise [mcp].token_file. The owner token is never used: mcpserver.New
			// refuses it (SCP-13). No pending store: a failed MCP call is reported
			// to the model, never replayed later.
			path := e.tokenFile
			if path == "" {
				path = cfg.MCP.TokenFile
			}
			if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
				return &mcpTokenMissingError{path: path}
			}
			c, err := client.New(client.Options{Socket: cfg.Socket, TokenFile: path, Channel: "mcp", Timeout: cfg.Timeout})
			if err != nil {
				return err
			}
			// Stdout carries the protocol; diagnostics go to stderr.
			logger := slog.New(slog.NewTextHandler(e.err, nil))
			return mcpserver.Run(e.ctx, mcpserver.Options{Client: c, Logger: logger})
		},
	}
}
