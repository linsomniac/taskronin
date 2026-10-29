// Package report renders work reports without accessing data or the clock.
package report

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/textsafe"
)

// Text writes a plain-text work report to w.
func Text(w io.Writer, r *api.Report) error {
	var out strings.Builder
	fmt.Fprintln(&out, reportHeader(r, textsafe.SanitizeLine))
	width := 0
	for _, day := range r.Days {
		for _, line := range day.Lines {
			// AIDEV-NOTE: Sanitize before measuring, since replacing control
			// bytes changes their display width. Padding uses terminal cells.
			width = max(width, textsafe.Width(textsafe.SanitizeLine(line.Title)))
		}
	}
	width = min(60, width)
	if len(r.Days) > 0 {
		out.WriteByte('\n')
	}
	seen := make(map[int64]bool)
	for _, day := range r.Days {
		fmt.Fprintln(&out, reportDate(day.Date))
		for _, line := range day.Lines {
			marker := "-"
			if line.Kind == "complete" {
				marker = "✓"
			}
			title := textsafe.Truncate(textsafe.SanitizeLine(line.Title), width, "…")
			fmt.Fprintf(&out, "  %s %s", marker, title)
			if line.TaskID != nil {
				out.WriteString(strings.Repeat(" ", width-textsafe.Width(title)))
				fmt.Fprintf(&out, "  [%s]", taskAnnotation(r, *line.TaskID, seen, textsafe.SanitizeLine))
			}
			if line.Reopened {
				out.WriteString(" (reopened)")
			}
			out.WriteByte('\n')
		}
	}
	writeTaskSections(&out, r, "\n%s\n", "  - ", textsafe.SanitizeLine)
	_, err := io.WriteString(w, out.String())
	return err
}

func reportDate(d civil.Date) string {
	return d.Weekday().String()[:3] + " " + d.String()
}

func reportHeader(r *api.Report, clean func(string) string) string {
	header := "Work report " + reportDate(r.From) + " – " + reportDate(r.To)
	if r.Tag != nil {
		header += " · tag " + clean(*r.Tag)
	}
	if r.Project != nil {
		header += " · project " + clean(*r.Project)
	}
	return header
}

func taskAnnotation(r *api.Report, id int64, seen map[int64]bool, clean func(string) string) string {
	key := strconv.FormatInt(id, 10)
	annotation := "#" + key
	if !seen[id] {
		annotation += " " + clean(r.Tasks[key].Title)
		seen[id] = true
	}
	return annotation
}

func writeTaskSections(out *strings.Builder, r *api.Report, heading, bullet string, clean func(string) string) {
	writeSection := func(name string, tasks []api.Task) {
		fmt.Fprintf(out, heading, name)
		for _, task := range tasks {
			fmt.Fprintf(out, "%s#%d %s", bullet, task.ID, clean(task.Title))
			switch name {
			case "Triage":
				if len(task.Reasons) > 0 {
					reasons := make([]string, len(task.Reasons))
					for i, reason := range task.Reasons {
						reasons[i] = clean(reason.Reason)
					}
					fmt.Fprintf(out, " [%s]", strings.Join(reasons, ", "))
				}
			case "Upcoming":
				if task.Deferred && task.Defer != nil {
					fmt.Fprintf(out, " (deferred to %s)", reportMoment(task.Defer))
				} else if task.Due != nil {
					fmt.Fprintf(out, " (due %s)", reportMoment(task.Due))
				}
			}
			out.WriteByte('\n')
		}
	}
	// AIDEV-NOTE: BF20 requires optional sections even when Days is empty.
	if r.Todo != nil {
		writeSection("Triage", r.Todo.Triage)
		writeSection("Backlog", r.Todo.Backlog)
	}
	if len(r.Upcoming) > 0 {
		writeSection("Upcoming", r.Upcoming)
	}
}

func reportMoment(moment *api.Moment) string {
	value := moment.Date.String()
	if moment.Time != nil {
		value += " " + moment.Time.String()
	}
	return value
}
