package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/client"
)

func init() { register(reopenCommand) }

func reopenCommand(e *env) *cobra.Command {
	var req api.ReopenRequest
	cmd := &cobra.Command{
		Use: "reopen ID", Short: "Reopen a completed or dropped task", Args: cobra.ExactArgs(1),
		Annotations: map[string]string{"mutates": "true"},
		RunE: func(_ *cobra.Command, args []string) error {
			id, err := parseTaskID(args[0])
			if err != nil {
				return err
			}
			c, err := e.client()
			if err != nil {
				return err
			}
			task, err := c.GetTask(e.ctx, id)
			if err != nil {
				return err
			}
			res, err := c.ReopenTask(e.ctx, client.Mut{IfMatch: task.ETag}, id, req)
			if err != nil {
				return err
			}
			if res.Task == nil {
				return fmt.Errorf("server response is missing the reopened task")
			}
			if err := e.outputOp(res, func(p paint) string { return p.taskHeadline(res.Task, "reopened") }); err != nil {
				return err
			}
			if !e.jsonOut {
				_, err = fmt.Fprintln(e.out, "hint: blocks cancelled by the completion stay cancelled; `tkr undo` of the done op restores them exactly")
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&req.RemoveEntry, "remove-entry", false, "Delete the completion entry")
	return cmd
}
