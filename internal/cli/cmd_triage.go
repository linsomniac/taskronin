package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"taskronin/internal/api"
	"taskronin/internal/client"
)

func init() { register(triageCommand) }

func triageCommand(e *env) *cobra.Command {
	var manual string
	var acceptNew, interactive bool
	cmd := &cobra.Command{
		Use: "triage [--manual ID | --accept-new | -i]", Short: "Review tasks needing triage", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			modes := 0
			for _, active := range []bool{cmd.Flags().Changed("manual"), acceptNew, interactive} {
				if active {
					modes++
				}
			}
			if modes > 1 {
				return usagef("at most one of --manual, --accept-new or -i")
			}
			if interactive {
				return dispatchTUI(cmd, TUIRequest{Mode: "triage"})
			}
			if cmd.Flags().Changed("manual") {
				id, err := parseTaskID(manual)
				if err != nil {
					return err
				}
				e.mutates = true
				return e.markManual(id)
			}
			e.mutates = acceptNew
			page, err := e.listTasks(client.TaskQuery{View: "triage"})
			if err != nil {
				return err
			}
			if acceptNew {
				return e.acceptNew(page.Items)
			}
			if e.jsonOut {
				return json.NewEncoder(e.out).Encode(page)
			}
			clock, err := e.clock()
			if err != nil {
				return err
			}
			p := e.paint()
			if len(page.Items) == 0 {
				_, err := fmt.Fprintln(e.out, p.dim("Triage clear"))
				return err
			}
			for _, task := range page.Items {
				if _, err := fmt.Fprintln(e.out, taskLine(task, clock.Today, p)); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&manual, "manual", "", "Send a task to triage")
	cmd.Flags().BoolVar(&acceptNew, "accept-new", false, "Accept tasks whose only reason is new")
	cmd.Flags().BoolVarP(&interactive, "interactive", "i", false, "Open interactive triage")
	return cmd
}

func (e *env) markManual(id int64) error {
	c, err := e.client()
	if err != nil {
		return err
	}
	task, err := c.GetTask(e.ctx, id)
	if err != nil {
		return err
	}
	res, err := c.MarkManual(e.ctx, client.Mut{IfMatch: task.ETag}, id)
	if err != nil {
		return err
	}
	return e.outputOp(res, func(p paint) string { return p.taskID(id) + " sent to triage (" + p.warn("manual") + ")" })
}

func (e *env) acceptNew(tasks []api.Task) error {
	var req api.AcceptNewRequest
	for _, task := range tasks {
		if len(task.Reasons) == 1 && task.Reasons[0].Reason == "new" {
			req.Items = append(req.Items, struct {
				ID   int64  `json:"id"`
				ETag string `json:"etag"`
			}{ID: task.ID, ETag: task.ETag})
		}
	}
	// AIDEV-NOTE: collect every page before this atomic mutation; never accept
	// a mixed-reason task or send an empty batch (the service rejects it).
	res := &api.OpResponse{Effects: []api.Effect{}, Warnings: []string{}}
	if len(req.Items) > 0 {
		c, err := e.client()
		if err != nil {
			return err
		}
		res, err = c.AcceptNew(e.ctx, client.Mut{}, req)
		if err != nil {
			return err
		}
	}
	return e.outputOp(res, plainHeadline(fmt.Sprintf("accepted %d new tasks into the backlog", len(res.Tasks))))
}
