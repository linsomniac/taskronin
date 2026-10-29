package cli

import (
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
			var out strings.Builder
			fmt.Fprintf(&out, "#%d %s\n", task.ID, textsafe.SanitizeLine(task.Title))
			detailField(&out, "Status", task.Status)
			detailField(&out, "View", task.View)
			detailField(&out, "Project", detailOptional(task.Project))
			detailField(&out, "Tags", strings.Join(task.Tags, ", "))
			detailField(&out, "Priority", detailOptional(task.Priority))
			detailField(&out, "Origin", task.Origin)
			if task.Defer != nil {
				detailField(&out, "Defer", formatMoment(task.Defer))
			}
			if task.Due != nil {
				detailField(&out, "Due", formatMoment(task.Due))
				detailField(&out, "Due phase", task.DuePhase)
			}
			detailTime(&out, "Created", &task.CreatedAt)
			detailTime(&out, "Updated", &task.UpdatedAt)
			detailTime(&out, "Last placed", task.LastPlacedAt)
			detailTime(&out, "Completed", task.CompletedAt)
			if task.CompletedDate != nil {
				detailField(&out, "Completion date", formatDate(*task.CompletedDate))
			}
			detailTime(&out, "Deleted", task.DeletedAt)
			fmt.Fprintf(&out, "Version: %d\nSlips: %d lifetime, %d since triage\n", task.Version, task.Slips.Lifetime, task.Slips.SinceTriage)
			fmt.Fprintln(&out, "\nReasons")
			for _, reason := range task.Reasons {
				fmt.Fprintf(&out, "  %s since %s\n", textsafe.SanitizeLine(reason.Reason), reason.Since.Format("2006-01-02 15:04:05Z07:00"))
			}
			// AIDEV-NOTE: notes are plain user data. Preserve their whitespace
			// and Markdown literally, while replacing terminal control bytes.
			fmt.Fprintf(&out, "\nNotes\n%s\n", textsafe.Sanitize(task.Notes))
			fmt.Fprintln(&out, "\nBlocks")
			for _, block := range task.Blocks {
				fmt.Fprintf(&out, "  b%d %s %s–%s  %s", block.ID, block.Date, block.Start, block.End, textsafe.SanitizeLine(block.Status))
				if block.Slipped {
					fmt.Fprint(&out, "  (slipped)")
				}
				if block.NeedsReblock {
					fmt.Fprint(&out, "  (reblock?)")
				}
				if block.OutcomeSource != nil {
					fmt.Fprintf(&out, "  outcome: %s", textsafe.SanitizeLine(*block.OutcomeSource))
				}
				if block.CancelReason != nil {
					fmt.Fprintf(&out, "  cancelled: %s", textsafe.SanitizeLine(*block.CancelReason))
				}
				fmt.Fprintln(&out)
			}
			fmt.Fprintln(&out, "\nEntries")
			for _, entry := range task.Entries {
				fmt.Fprintf(&out, "  %s\n", entryLine(entry))
			}
			if len(task.Interruptions) > 0 {
				fmt.Fprintln(&out, "\nInterruptions")
				for _, in := range task.Interruptions {
					fmt.Fprintf(&out, "  %s → #%d", in.At.Format("2006-01-02 15:04:05Z07:00"), in.ToTaskID)
					if in.FromTaskID != nil {
						fmt.Fprintf(&out, " from #%d", *in.FromTaskID)
					}
					if in.FromBlockID != nil {
						fmt.Fprintf(&out, " (b%d)", *in.FromBlockID)
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

func detailTime(out *strings.Builder, label string, value *api.Time) {
	if value != nil {
		detailField(out, label, value.Format("2006-01-02 15:04:05Z07:00"))
	}
}
