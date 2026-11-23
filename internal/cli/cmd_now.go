package cli

import (
	"github.com/spf13/cobra"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/client"
)

func init() { register(nowCommand) }

func nowCommand(e *env) *cobra.Command {
	var clearFocus, interrupt bool
	cmd := &cobra.Command{
		Use: "now [ID]", Short: "Set or clear today's focus", Args: cobra.MaximumNArgs(1),
		Annotations: map[string]string{"mutates": "true"},
		RunE: func(_ *cobra.Command, args []string) error {
			if clearFocus && (len(args) != 0 || interrupt) {
				return usagef("--clear cannot be combined with a task ID or --interrupt")
			}
			if !clearFocus && len(args) == 0 {
				return usagef("now requires a task ID or --clear")
			}
			req := api.FocusRequest{TaskID: api.Opt[int64]{Set: true, Null: clearFocus}, Interrupt: interrupt}
			if !clearFocus {
				id, err := parseTaskID(args[0])
				if err != nil {
					return err
				}
				req.TaskID.V = id
			}
			c, err := e.client()
			if err != nil {
				return err
			}
			headline := plainHeadline("focus cleared")
			if !clearFocus {
				task, err := c.GetTask(e.ctx, req.TaskID.V)
				if err != nil {
					return err
				}
				headline = func(p paint) string { return "focus → " + p.taskID(task.ID) + " " + p.quotedTitle(task.Title) }
			}
			// AIDEV-NOTE: focus is the API's explicit precondition exception
			// (A§3.5); a task ETag does not guard the focus singleton.
			res, err := c.SetFocus(e.ctx, client.Mut{}, req)
			if err != nil {
				return err
			}
			return e.outputOp(res, headline)
		},
	}
	cmd.Flags().BoolVar(&clearFocus, "clear", false, "Clear focus")
	cmd.Flags().BoolVar(&interrupt, "interrupt", false, "Record an interruption of the previous focus")
	return cmd
}
