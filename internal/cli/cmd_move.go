package cli

import (
	"github.com/spf13/cobra"

	"taskronin/internal/api"
	"taskronin/internal/client"
)

func init() {
	register(moveCommand)
	register(keepCommand)
}

func moveCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use: "move ID top|bottom|before N|after N", Short: "Move a task in the backlog",
		Args: cobra.RangeArgs(2, 3), Annotations: map[string]string{"mutates": "true"},
		RunE: func(_ *cobra.Command, args []string) error {
			id, err := parseTaskID(args[0])
			if err != nil {
				return err
			}
			req := api.PlaceRequest{How: args[1]}
			switch req.How {
			case "top", "bottom":
				if len(args) != 2 {
					return usagef("move %s takes no reference task", req.How)
				}
			case "before", "after":
				if len(args) != 3 {
					return usagef("move %s requires a reference task ID", req.How)
				}
				req.Ref, err = parseTaskID(args[2])
				if err != nil {
					return err
				}
			default:
				return usagef("move requires top, bottom, before N or after N")
			}
			return e.placeTask(id, req)
		},
	}
}

func keepCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use: "keep ID", Short: "Keep a task at its current backlog position", Args: cobra.ExactArgs(1),
		Annotations: map[string]string{"mutates": "true"},
		RunE: func(_ *cobra.Command, args []string) error {
			id, err := parseTaskID(args[0])
			if err != nil {
				return err
			}
			return e.placeTask(id, api.PlaceRequest{How: "keep"})
		},
	}
}

func (e *env) placeTask(id int64, req api.PlaceRequest) error {
	c, err := e.client()
	if err != nil {
		return err
	}
	task, err := c.GetTask(e.ctx, id)
	if err != nil {
		return err
	}
	res, err := c.PlaceTask(e.ctx, client.Mut{IfMatch: task.ETag}, id, req)
	if err != nil {
		return err
	}
	return e.outputOp(res, func(p paint) string {
		if req.How == "keep" {
			return p.taskID(id) + " kept in place"
		}
		line := p.taskID(id) + " moved " + req.How
		if req.How == "after" || req.How == "before" {
			line += " " + p.taskID(req.Ref)
		}
		return line
	})
}
