package cli

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"taskronin/internal/civil"
	"taskronin/internal/client"
	"taskronin/internal/datespec"
	"taskronin/internal/report"
)

type reportFlags struct {
	week, lastWeek, month, today bool
	from, to                     string
}

func init() { register(reportCommand) }

func reportCommand(e *env) *cobra.Command {
	var ranges reportFlags
	var query client.ReportQuery
	var format string
	cmd := &cobra.Command{
		Use: "report", Short: "Report work over a calendar range (default this week)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch format {
			case "text", "md", "json":
			default:
				return usagef("--format must be text, md or json")
			}
			if cmd.Flags().Changed("from") && ranges.from == "" || cmd.Flags().Changed("to") && ranges.to == "" {
				return usagef("--from and --to require non-empty dates")
			}
			ref, err := e.ref(datespec.Past)
			if err != nil {
				return err
			}
			query.From, query.To, err = reportRange(ranges, ref)
			if err != nil {
				return err
			}
			c, err := e.client()
			if err != nil {
				return err
			}
			result, err := c.Report(e.ctx, query)
			if err != nil {
				return err
			}
			if e.jsonOut || format == "json" {
				enc := json.NewEncoder(e.out)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}
			if format == "md" {
				return report.Markdown(e.out, result)
			}
			return report.Text(e.out, result)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&ranges.week, "week", false, "Full current week (default)")
	f.BoolVar(&ranges.lastWeek, "last-week", false, "Full previous week")
	f.BoolVar(&ranges.month, "month", false, "Full current calendar month")
	f.BoolVar(&ranges.today, "today", false, "Today only")
	f.StringVar(&ranges.from, "from", "", "First work date, inclusive (requires --to)")
	f.StringVar(&ranges.to, "to", "", "Last work date, inclusive (requires --from)")
	f.StringVarP(&query.Tag, "tag", "t", "", "Filter by tag")
	f.StringVarP(&query.Project, "project", "P", "", "Filter by project and its dotted descendants")
	f.StringVar(&format, "format", "text", "Report format: text, md, json")
	f.BoolVar(&query.Todo, "todo", false, "Append current triage and backlog")
	f.BoolVar(&query.Upcoming, "upcoming", false, "Append upcoming tasks")
	return cmd
}

func reportRange(f reportFlags, r datespec.Ref) (from, to civil.Date, err error) {
	if (f.from == "") != (f.to == "") {
		return from, to, usagef("--from and --to must be supplied together")
	}
	selected := 0
	for _, set := range []bool{f.week, f.lastWeek, f.month, f.today, f.from != ""} {
		if set {
			selected++
		}
	}
	if selected > 1 {
		return from, to, usagef("choose only one of --week, --last-week, --month, --today or --from/--to")
	}
	// AIDEV-NOTE: calendar periods include their future days. Past mode
	// controls weekday parsing only; it must not truncate a week or month.
	switch {
	case f.lastWeek:
		from, to = datespec.LastWeek(r)
	case f.month:
		from, to = datespec.Month(r)
	case f.today:
		from, to = r.Today, r.Today
	case f.from != "":
		start, parseErr := datespec.Parse(f.from, datespec.Past, r)
		if parseErr != nil {
			return from, to, usagef("--from: %s", parseErr)
		}
		if start.Min.Valid {
			return from, to, usagef("--from requires a date without a time")
		}
		end, parseErr := datespec.Parse(f.to, datespec.Past, r)
		if parseErr != nil {
			return from, to, usagef("--to: %s", parseErr)
		}
		if end.Min.Valid {
			return from, to, usagef("--to requires a date without a time")
		}
		from, to = start.Date, end.Date
	default:
		from, to = datespec.Week(r)
	}
	if from.After(to) {
		return from, to, usagef("--from must not be after --to")
	}
	return from, to, nil
}
