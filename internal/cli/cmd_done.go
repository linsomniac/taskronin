package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/datespec"
	"taskronin/internal/domain"
)

func init() { register(doneCommand) }

func doneCommand(e *env) *cobra.Command {
	var req api.CompleteRequest
	var as, date string
	cmd := &cobra.Command{
		Use: "done ID", Short: "Complete a task and log its completion", Args: cobra.ExactArgs(1),
		Annotations: map[string]string{"mutates": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseTaskID(args[0])
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("as") {
				req.As = &as
			}
			if cmd.Flags().Changed("date") {
				ref, err := e.ref(datespec.Past)
				if err != nil {
					return err
				}
				moment, err := datespec.Parse(date, datespec.Past, ref)
				if err != nil {
					return usagef("--date: %s", err)
				}
				if moment.Min.Valid {
					return usagef("--date requires a date without a time")
				}
				if moment.Date.After(ref.Today) {
					return domain.Errorf(domain.CodeValidation, "--date must not be in the future")
				}
				req.Date = &moment.Date
			}
			c, err := e.client()
			if err != nil {
				return err
			}
			task, err := c.GetTask(e.ctx, id)
			if err != nil {
				return err
			}
			res, err := c.CompleteTask(e.ctx, client.Mut{IfMatch: task.ETag}, id, req)
			if err != nil {
				return err
			}
			if res.Task == nil || res.Entry == nil {
				return fmt.Errorf("server response is missing the completed task or entry")
			}
			return e.outputOp(res, taskHeadline(res.Task, "completed; ✓ logged "+formatDate(res.Entry.WorkDate)))
		},
	}
	f := cmd.Flags()
	f.StringVar(&as, "as", "", "Retitle the task")
	f.StringVar(&date, "date", "", "Completion date (default today)")
	f.StringVar(&req.Log, "log", "", "Completion entry title")
	f.StringVarP(&req.Notes, "message", "m", "", "Completion entry notes")
	return cmd
}
