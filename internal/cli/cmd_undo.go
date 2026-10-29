package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/domain"
)

func init() {
	register(func(e *env) *cobra.Command { return undoCommand(e, "undo") })
	register(func(e *env) *cobra.Command { return undoCommand(e, "redo") })
	register(func(e *env) *cobra.Command { return undoCommand(e, "revert") })
}

func undoCommand(e *env, verb string) *cobra.Command {
	cmd := &cobra.Command{
		Use: verb, Short: map[string]string{"undo": "Undo the last operation", "redo": "Redo the last undone operation", "revert": "Revert an operation"}[verb],
		Args: cobra.NoArgs, Annotations: map[string]string{"mutates": "true"},
		RunE: func(_ *cobra.Command, args []string) error {
			if verb == "revert" {
				if _, err := domain.ParseOpID(args[0]); err != nil {
					return usagef("%s", err)
				}
			}
			c, err := e.client()
			if err != nil {
				return err
			}
			var res *api.OpResponse
			var action string
			switch verb {
			case "undo":
				res, err = c.Undo(e.ctx, client.Mut{})
				action = "undone"
			case "redo":
				res, err = c.Redo(e.ctx, client.Mut{})
				action = "redone"
			case "revert":
				res, err = c.Revert(e.ctx, client.Mut{}, args[0])
				action = "reverted"
			}
			if err != nil {
				return err
			}
			if res.OpID == nil {
				return fmt.Errorf("server response is missing the %s operation", action)
			}
			return e.outputOp(res, fmt.Sprintf("%s (op %.8s)", action, *res.OpID))
		},
	}
	if verb == "revert" {
		cmd.Use, cmd.Args = "revert OPID", cobra.ExactArgs(1)
	}
	return cmd
}
