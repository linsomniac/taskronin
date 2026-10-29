package report

import (
	"fmt"
	"io"
	"strings"

	"taskronin/internal/api"
	"taskronin/internal/textsafe"
)

// Markdown writes a Markdown work report to w.
func Markdown(w io.Writer, r *api.Report) error {
	var out strings.Builder
	fmt.Fprintf(&out, "# %s\n", reportHeader(r, markdownText))
	seen := make(map[int64]bool)
	for _, day := range r.Days {
		fmt.Fprintf(&out, "\n## %s\n\n", reportDate(day.Date))
		for _, line := range day.Lines {
			out.WriteString("- ")
			if line.Kind == "complete" {
				out.WriteString("✓ ")
			}
			out.WriteString(markdownText(line.Title))
			if line.TaskID != nil {
				fmt.Fprintf(&out, " (%s)", taskAnnotation(r, *line.TaskID, seen, markdownText))
			}
			if line.Reopened {
				out.WriteString(" (reopened)")
			}
			out.WriteByte('\n')
		}
	}
	writeTaskSections(&out, r, "\n## %s\n\n", "- ", markdownText)
	_, err := io.WriteString(w, out.String())
	return err
}

func markdownText(s string) string {
	var out strings.Builder
	for _, r := range textsafe.SanitizeLine(s) {
		if strings.ContainsRune("\\`*_[]<>#|~", r) {
			out.WriteByte('\\')
		}
		out.WriteRune(r)
	}
	return out.String()
}
