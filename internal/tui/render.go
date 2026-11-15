package tui

import (
	"fmt"
	"strings"

	"taskronin/internal/textsafe"
)

func (a *App) line(s string, w int) string {
	return textsafe.Truncate(textsafe.SanitizeLine(s), max(0, w), "…")
}

func (a *App) row(s string, w int, selected bool) string {
	if selected {
		return a.styles.Selected.Render(a.line("› "+s, w))
	}
	return a.line("  "+s, w)
}

// clip accepts already-sanitized, possibly styled lines.
func clip(lines []string, w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	out := make([]string, 0, min(len(lines), h))
	for _, line := range lines[:min(len(lines), h)] {
		out = append(out, textsafe.Truncate(line, w, "…"))
	}
	return strings.Join(out, "\n")
}

func fitLines(s string, h int) []string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		return lines[:h]
	}
	return append(lines, make([]string, h-len(lines))...)
}

func (a *App) header(w int) string {
	parts := []string{a.styles.Header.Render(a.line("Taskronin  "+a.clock.Now.Format("Mon 2006-01-02 15:04"), w))}
	for i, name := range tabNames {
		label := fmt.Sprintf("%d %s", i+1, name)
		style := a.styles.TabOff
		if Tab(i) == a.tab {
			label, style = "["+label+"]", a.styles.TabOn
		}
		parts = append(parts, style.Render(label))
	}
	if a.today != nil {
		backup, purge := false, false
		for _, b := range a.today.Banners {
			switch b.Kind {
			case "backup_failed", "backup_stale":
				backup = true
			case "purge_incomplete", "purge_ledger_damaged":
				purge = true
			}
		}
		if backup {
			parts = append(parts, a.styles.Warn.Render("⚠ backup"))
		}
		if purge {
			parts = append(parts, a.styles.Warn.Render("⚠ purge"))
		}
		if a.today.AIChanges > 0 {
			parts = append(parts, a.styles.Info.Render(fmt.Sprintf("ai %d", a.today.AIChanges)))
		}
	}
	return strings.Join(parts, " ")
}
