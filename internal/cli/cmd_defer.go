package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/client"
	"github.com/linsomniac/taskronin/internal/datespec"
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
					return dateUsage(verb, err)
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
			return e.outputOp(res, func(p paint) string {
				id := p.taskID(res.Task.ID)
				if verb == "defer" {
					if res.Task.Defer == nil {
						return id + " defer cleared"
					}
					return id + " deferred to " + e.momentWord(res.Task.Defer)
				}
				if res.Task.Due == nil {
					return id + " due date cleared"
				}
				return id + " " + p.warn("due "+e.momentWord(res.Task.Due))
			})
		},
	}
}
