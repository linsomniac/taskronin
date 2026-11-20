package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/datespec"
)

func init() {
	register(func(e *env) *cobra.Command { return scheduleBlockCommand(e, "block") })
	register(func(e *env) *cobra.Command { return scheduleBlockCommand(e, "reblock") })
	register(unblockCommand)
	register(outcomeCommand)
}

func scheduleBlockCommand(e *env, verb string) *cobra.Command {
	var date string
	idArg := "ID"
	if verb == "reblock" {
		idArg = "BID"
	}
	cmd := &cobra.Command{
		Use: verb + " " + idArg + " SPAN", Short: "Schedule a task's time block", Args: cobra.ExactArgs(2),
		Annotations: map[string]string{"mutates": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			parseID := parseTaskID
			if verb == "reblock" {
				parseID = parseBlockID
			}
			id, err := parseID(args[0])
			if err != nil {
				return err
			}
			ref, err := e.ref(datespec.Future)
			if err != nil {
				return err
			}
			spanText := args[1]
			if cmd.Flags().Changed("date") {
				if strings.TrimSpace(date) == "" {
					return usagef("--date requires a date")
				}
				// Combining the date and span lets ParseSpan reject duplicate
				// dates, clocks in --date, and malformed ranges consistently.
				spanText = date + " " + spanText
			}
			span, err := datespec.ParseSpan(spanText, datespec.Future, ref)
			if err != nil {
				return dateUsage(verb, err)
			}
			req := api.BlockRequest{Date: span.Date, Start: span.Start, End: span.End}
			c, err := e.client()
			if err != nil {
				return err
			}
			var res *api.OpResponse
			if verb == "reblock" {
				block, err := c.GetBlock(e.ctx, id)
				if err != nil {
					return err
				}
				// AIDEV-NOTE: reblock has two preconditions. Read the task
				// separately rather than reusing the block's older task_etag.
				task, err := c.GetTask(e.ctx, block.TaskID)
				if err != nil {
					return err
				}
				res, err = c.Reblock(e.ctx, client.Mut{IfMatch: block.ETag}, id, api.ReblockRequest{
					BlockRequest: req, TaskETag: task.ETag,
				})
				if err != nil {
					return err
				}
			} else {
				task, err := c.GetTask(e.ctx, id)
				if err != nil {
					return err
				}
				res, err = c.BlockTask(e.ctx, client.Mut{IfMatch: task.ETag}, id, req)
				if err != nil {
					return err
				}
			}
			if res.Block == nil {
				return fmt.Errorf("server response is missing the scheduled block")
			}
			return e.outputOp(res, func(p paint) string { return e.blockHeadline(res.Block, p) })
		},
	}
	cmd.Flags().StringVar(&date, "date", "", "Block date (default today)")
	return cmd
}

func (e *env) blockHeadline(block *api.Block, p paint) string {
	return fmt.Sprintf("%s %s %s–%s %s %s", p.blockID(block.ID), e.dayWord(block.Date), block.Start, block.End, p.taskID(block.TaskID), p.quotedTitle(block.TaskTitle))
}

func unblockCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use: "unblock BID", Short: "Cancel or resolve a time block", Args: cobra.ExactArgs(1),
		Annotations: map[string]string{"mutates": "true"},
		RunE: func(_ *cobra.Command, args []string) error {
			id, err := parseBlockID(args[0])
			if err != nil {
				return err
			}
			c, err := e.client()
			if err != nil {
				return err
			}
			block, err := c.GetBlock(e.ctx, id)
			if err != nil {
				return err
			}
			res, err := c.Unblock(e.ctx, client.Mut{IfMatch: block.ETag}, id)
			if err != nil {
				return err
			}
			if res.Block == nil {
				return fmt.Errorf("server response is missing the unblocked block")
			}
			return e.outputOp(res, func(p paint) string {
				return p.blockID(res.Block.ID) + " " + p.status(res.Block.Status)
			})
		},
	}
}

func outcomeCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use: "outcome BID worked|missed|auto", Short: "Set a block's outcome", Args: cobra.ExactArgs(2),
		Annotations: map[string]string{"mutates": "true"},
		RunE: func(_ *cobra.Command, args []string) error {
			id, err := parseBlockID(args[0])
			if err != nil {
				return err
			}
			outcome := args[1]
			switch outcome {
			case "worked", "missed", "auto":
			default:
				return usagef("outcome requires worked, missed or auto")
			}
			c, err := e.client()
			if err != nil {
				return err
			}
			block, err := c.GetBlock(e.ctx, id)
			if err != nil {
				return err
			}
			res, err := c.SetOutcome(e.ctx, client.Mut{IfMatch: block.ETag}, id, api.OutcomeRequest{Outcome: outcome})
			if err != nil {
				return err
			}
			return e.outputOp(res, func(p paint) string { return p.blockID(id) + " marked " + p.status(outcome) })
		},
	}
}
