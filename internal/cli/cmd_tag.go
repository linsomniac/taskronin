package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"taskronin/internal/api"
	"taskronin/internal/client"
)

func init() {
	for _, verb := range []string{"tag", "project", "priority"} {
		register(func(e *env) *cobra.Command { return metadataCommand(e, verb) })
	}
}

func metadataCommand(e *env, verb string) *cobra.Command {
	cmd := &cobra.Command{
		Short: "Update a task's " + verb, Args: cobra.ExactArgs(2),
		Annotations: map[string]string{"mutates": "true"},
		RunE: func(_ *cobra.Command, args []string) error {
			id, err := parseTaskID(args[0])
			if err != nil {
				return err
			}
			var req api.UpdateTaskRequest
			switch verb {
			case "tag":
				for _, arg := range args[1:] {
					if len(arg) < 2 || (arg[0] != '+' && arg[0] != '-') {
						return usagef("tag requires +TAG or -TAG arguments")
					}
					if arg[0] == '+' {
						req.TagsAdd = append(req.TagsAdd, arg[1:])
					} else {
						req.TagsRemove = append(req.TagsRemove, arg[1:])
					}
				}
			case "project":
				req.Project = api.Opt[string]{Set: true, Null: args[1] == "none", V: args[1]}
			case "priority":
				switch args[1] {
				case "H", "M", "L", "none":
					req.Priority = api.Opt[string]{Set: true, Null: args[1] == "none", V: args[1]}
				default:
					return usagef("priority requires H, M, L or none")
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
			res, err := c.UpdateTask(e.ctx, client.Mut{IfMatch: task.ETag}, id, req)
			if err != nil {
				return err
			}
			if res.Task == nil {
				return fmt.Errorf("server response is missing the task")
			}
			return e.outputOp(res, metadataHeadline(res.Task, verb))
		},
	}
	switch verb {
	case "tag":
		cmd.Use, cmd.Args = "tag ID +TAG|-TAG…", cobra.MinimumNArgs(2)
		cmd.Long = "Add or remove task tags. Put global flags before ID."
		// AIDEV-NOTE: after ID, -TAG is an operand, not a Cobra flag.
		cmd.Flags().SetInterspersed(false)
	case "project":
		cmd.Use = "project ID NAME|none"
	case "priority":
		cmd.Use = "priority ID H|M|L|none"
	}
	return cmd
}

func metadataHeadline(task *api.Task, verb string) string {
	if verb == "tag" {
		return fmt.Sprintf("#%d tags: %s", task.ID, strings.Join(task.Tags, ", "))
	}
	value := task.Project
	if verb == "priority" {
		value = task.Priority
	}
	if value == nil {
		return fmt.Sprintf("#%d %s cleared", task.ID, verb)
	}
	return fmt.Sprintf("#%d %s %s", task.ID, verb, *value)
}
