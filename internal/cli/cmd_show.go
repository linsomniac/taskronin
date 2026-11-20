package cli

import (
	"cmp"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"taskronin/internal/api"
	"taskronin/internal/textsafe"
)

func init() { register(showCommand) }

func showCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use: "show ID", Short: "Show a task's fields and related records", Args: cobra.ExactArgs(1),
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
			if e.jsonOut {
				return json.NewEncoder(e.out).Encode(task)
			}
			p := e.paint()
			var out strings.Builder
			fmt.Fprintf(&out, "%s %s\n", p.taskID(task.ID), textsafe.SanitizeLine(task.Title))
			fmt.Fprintf(&out, "Status: %s\n", p.status(task.Status))
			detailField(&out, "View", task.View)
			fmt.Fprintf(&out, "Project: %s\n", p.dim(textsafe.SanitizeLine(detailOptional(task.Project))))
			tags := cmp.Or(strings.Join(task.Tags, ", "), "none")
			fmt.Fprintf(&out, "Tags: %s\n", p.dim(textsafe.SanitizeLine(tags)))
			if task.Priority == nil {
				fmt.Fprintln(&out, "Priority: "+p.dim("none"))
			} else {
				detailField(&out, "Priority", *task.Priority)
			}
			detailField(&out, "Origin", task.Origin)
			if task.Defer != nil {
				fmt.Fprintf(&out, "Defer: %s\n", p.dim(formatMoment(task.Defer)))
			}
			if task.Due != nil {
				fmt.Fprintf(&out, "Due: %s\n", p.warn(formatMoment(task.Due)))
				fmt.Fprintf(&out, "Due phase: %s\n", p.status(task.DuePhase))
			}
			p.detailTime(&out, "Created", &task.CreatedAt)
			p.detailTime(&out, "Updated", &task.UpdatedAt)
			p.detailTime(&out, "Last placed", task.LastPlacedAt)
			p.detailTime(&out, "Completed", task.CompletedAt)
			if task.CompletedDate != nil {
				fmt.Fprintf(&out, "Completion date: %s\n", p.dim(formatDate(*task.CompletedDate)))
			}
			p.detailTime(&out, "Deleted", task.DeletedAt)
			fmt.Fprintf(&out, "Version: %d\nSlips: %d lifetime, %d since triage\n", task.Version, task.Slips.Lifetime, task.Slips.SinceTriage)
			if len(task.Reasons) > 0 {
				fmt.Fprintln(&out, "\n"+p.head("Reasons"))
				for _, reason := range task.Reasons {
					fmt.Fprintf(&out, "  %s since %s\n", p.reason(reason.Reason), p.dim(reason.Since.Format("2006-01-02 15:04:05Z07:00")))
				}
			}
			// AIDEV-NOTE: notes are plain user data. Preserve their whitespace
			// and Markdown literally, while replacing terminal control bytes.
			if task.Notes == "" {
				fmt.Fprintln(&out, "\nNotes: "+p.dim("none"))
			} else {
				fmt.Fprintf(&out, "\n%s\n%s\n", p.head("Notes"), textsafe.Sanitize(task.Notes))
			}
			if len(task.Blocks) > 0 {
				fmt.Fprintln(&out, "\n"+p.head("Blocks"))
				for _, block := range task.Blocks {
					fmt.Fprintf(&out, "  %s %s  %s", p.blockID(block.ID), p.dim(fmt.Sprintf("%s %s–%s", block.Date, block.Start, block.End)), p.status(block.Status))
					if block.Slipped {
						fmt.Fprint(&out, "  ("+p.bad("slipped")+")")
					}
					if block.NeedsReblock {
						fmt.Fprint(&out, "  ("+p.warn("reblock?")+")")
					}
					if block.OutcomeSource != nil {
						fmt.Fprintf(&out, "  outcome: %s", textsafe.SanitizeLine(*block.OutcomeSource))
					}
					if block.CancelReason != nil {
						fmt.Fprintf(&out, "  cancelled: %s", textsafe.SanitizeLine(*block.CancelReason))
					}
					fmt.Fprintln(&out)
				}
			}
			if len(task.Entries) > 0 {
				fmt.Fprintln(&out, "\n"+p.head("Entries"))
				for _, entry := range task.Entries {
					fmt.Fprintf(&out, "  %s\n", entryLine(entry, p))
				}
			}
			if len(task.Interruptions) > 0 {
				fmt.Fprintln(&out, "\n"+p.head("Interruptions"))
				for _, in := range task.Interruptions {
					fmt.Fprintf(&out, "  %s → %s", p.dim(in.At.Format("2006-01-02 15:04:05Z07:00")), p.taskID(in.ToTaskID))
					if in.FromTaskID != nil {
						fmt.Fprintf(&out, " from %s", p.taskID(*in.FromTaskID))
					}
					if in.FromBlockID != nil {
						fmt.Fprintf(&out, " (%s)", p.blockID(*in.FromBlockID))
					}
					fmt.Fprintln(&out)
				}
			}
			_, err = fmt.Fprint(e.out, out.String())
			return err
		},
	}
}

func detailField(out *strings.Builder, label, value string) {
	fmt.Fprintf(out, "%s: %s\n", label, textsafe.SanitizeLine(value))
}

func detailOptional(value *string) string {
	if value == nil {
		return "none"
	}
	return *value
}

func (p paint) detailTime(out *strings.Builder, label string, value *api.Time) {
	if value != nil {
		fmt.Fprintf(out, "%s: %s\n", label, p.dim(value.Format("2006-01-02 15:04:05Z07:00")))
	}
}
