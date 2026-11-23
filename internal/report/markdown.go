package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/textsafe"
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
	s = textsafe.SanitizeLine(s)
	// AIDEV-NOTE: A leading bullet or numbered-list delimiter can turn a
	// title into a nested list. Skip spaces/digits to find that punctuation.
	marker := len(s) - len(strings.TrimLeft(s, " 0123456789"))
	var out strings.Builder
	for i, r := range s {
		if strings.ContainsRune("\\`*_[]<>#|~", r) || i == marker && strings.ContainsRune("-+.)", r) {
			out.WriteByte('\\')
		}
		out.WriteRune(r)
	}
	return out.String()
}
