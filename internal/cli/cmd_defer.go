package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/datespec"
)

func init() {
	register(func(e *env) *cobra.Command { return momentCommand(e, "defer") })
	register(func(e *env) *cobra.Command { return momentCommand(e, "due") })
}

func momentCommand(e *env, verb string) *cobra.Command {
	return &cobra.Command{
		Use: verb + " ID D|none", Short: "Set or clear a task's " + verb + " date", Args: cobra.ExactArgs(2),
		Annotations: map[string]string{"mutates": "true"},
		RunE: func(_ *cobra.Command, args []string) error {
			id, err := parseTaskID(args[0])
			if err != nil {
				return err
			}
			moment := api.Opt[api.Moment]{Set: true, Null: args[1] == "none"}
			if !moment.Null {
				ref, err := e.ref(datespec.Future)
				if err != nil {
					return err
				}
				parsed, err := datespec.Parse(args[1], datespec.Future, ref)
				if err != nil {
					return usagef("%s: %s", verb, err)
				}
				moment.V.Date = parsed.Date
				if parsed.Min.Valid {
					moment.V.Time = &parsed.Min.V
				}
			}
			c, err := e.client()
			if err != nil {
				return err
			}
			task, err := c.GetTask(e.ctx, id)
			if err != nil {
				return err
			}
			var res *api.OpResponse
			if verb == "defer" {
				res, err = c.DeferTask(e.ctx, client.Mut{IfMatch: task.ETag}, id, api.DeferRequest{Until: moment})
			} else {
				res, err = c.SetDue(e.ctx, client.Mut{IfMatch: task.ETag}, id, api.DueRequest{Due: moment})
			}
			if err != nil {
				return err
			}
			if res.Task == nil {
				return fmt.Errorf("server response is missing the task")
			}
			headline := fmt.Sprintf("#%d due date cleared", res.Task.ID)
			if verb == "defer" {
				headline = fmt.Sprintf("#%d defer cleared", res.Task.ID)
				if res.Task.Defer != nil {
					headline = fmt.Sprintf("#%d deferred to %s", res.Task.ID, e.momentWord(res.Task.Defer))
				}
			} else if res.Task.Due != nil {
				headline = fmt.Sprintf("#%d due %s", res.Task.ID, e.momentWord(res.Task.Due))
			}
			return e.outputOp(res, headline)
		},
	}
}
