package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/client"
	"taskronin/internal/datespec"
	"taskronin/internal/textsafe"
)

func init() { register(entriesCommand) }

func entriesCommand(e *env) *cobra.Command {
	var date, from, to, task string
	cmd := &cobra.Command{
		Use: "entries", Short: "List entries by date or task", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dateSet, fromSet, toSet := cmd.Flags().Changed("date"), cmd.Flags().Changed("from"), cmd.Flags().Changed("to")
			if dateSet && (fromSet || toSet) {
				return usagef("--date and --from/--to are mutually exclusive")
			}
			if fromSet != toSet {
				return usagef("--from and --to must be supplied together")
			}
			var query client.EntryQuery
			if cmd.Flags().Changed("task") {
				if task == "none" {
					query.Standalone = true
				} else {
					id, err := parseTaskID(task)
					if err != nil {
						return err
					}
					query.Task = &id
				}
			}
			if dateSet || fromSet || query.Task == nil {
				ref, err := e.ref(datespec.Past)
				if err != nil {
					return err
				}
				if !fromSet {
					if !dateSet {
						date = "today"
					}
					query.From, err = entryDate(date, "--date", ref)
					query.To = query.From
				} else {
					query.From, err = entryDate(from, "--from", ref)
					if err == nil {
						query.To, err = entryDate(to, "--to", ref)
					}
				}
				if err != nil {
					return err
				}
				if query.From.After(*query.To) {
					return usagef("--from must not be after --to")
				}
			}
			c, err := e.client()
			if err != nil {
				return err
			}
			result := api.Page[api.Entry]{Items: []api.Entry{}}
			for {
				page, err := c.ListEntries(e.ctx, query)
				if err != nil {
					return err
				}
				result.Items = append(result.Items, page.Items...)
				if page.NextCursor == nil || *page.NextCursor == "" {
					break
				}
				query.Cursor = *page.NextCursor
			}
			if e.jsonOut {
				return json.NewEncoder(e.out).Encode(result)
			}
			p := e.paint()
			var out strings.Builder
			for _, entry := range result.Items {
				fmt.Fprintln(&out, entryLine(entry, p))
			}
			_, err = fmt.Fprint(e.out, out.String())
			return err
		},
	}
	f := cmd.Flags()
	f.StringVar(&date, "date", "", "Work date (default today unless --task ID)")
	f.StringVar(&from, "from", "", "First work date, inclusive")
	f.StringVar(&to, "to", "", "Last work date, inclusive")
	f.StringVar(&task, "task", "", "Task ID or none for standalone entries")
	return cmd
}

func entryDate(value, flag string, ref datespec.Ref) (*civil.Date, error) {
	moment, err := datespec.Parse(value, datespec.Past, ref)
	if err != nil {
		return nil, usagef("%s: %s", flag, err)
	}
	if moment.Min.Valid {
		return nil, usagef("%s requires a date without a time", flag)
	}
	return &moment.Date, nil
}

func entryLine(entry api.Entry, p paint) string {
	date := entry.WorkDate.String()
	if entry.Time != nil {
		date += " " + entry.Time.String()
	}
	marker := "-"
	if entry.Kind == "complete" {
		marker = p.ok("✓")
	}
	line := fmt.Sprintf("%s  %s  %s %s", p.entryID(entry.ID), p.dim(date), marker, textsafe.Truncate(textsafe.SanitizeLine(entry.Title), 40, "…"))
	if entry.TaskID != nil {
		line += fmt.Sprintf("  [%s", p.taskID(*entry.TaskID))
		if entry.TaskTitle != nil {
			line += " " + textsafe.Truncate(textsafe.SanitizeLine(*entry.TaskTitle), 40, "…")
		}
		line += "]"
	}
	if entry.Reopened {
		line += "  (reopened)"
	}
	if entry.DeletedAt != nil {
		line += "  (deleted)"
	}
	return line
}
