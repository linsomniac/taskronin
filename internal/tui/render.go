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
		backup, purge, timezone := !a.today.Backup.Healthy, false, false
		var status []string
		for _, b := range a.today.Banners {
			switch b.Kind {
			case "backup_failed", "backup_stale":
				backup = true
			case "tz_changed":
				timezone = true
			case "purge_incomplete", "purge_ledger_damaged":
				purge = true
			}
		}
		if backup {
			status = append(status, a.styles.Warn.Render("⚠ backup"))
		}
		if purge {
			status = append(status, a.styles.Warn.Render("⚠ purge"))
		}
		if timezone {
			status = append(status, a.styles.Warn.Render("⚠ timezone"))
		}
		if a.today.AIChanges > 0 {
			status = append(status, a.styles.Info.Render(fmt.Sprintf("ai %d", a.today.AIChanges)))
		}
		if len(status) > 0 {
			return strings.Join(parts, " ") + "\n" + strings.Join(status, " · ")
		}
	}
	return strings.Join(parts, " ")
}

// AIDEV-NOTE: kept drafts have a separate lifetime from transient banners.
// Sanitize before wrapping, and never truncate the path the user needs to recover.
func (a *App) keepDraft(path string) {
	for _, kept := range a.draftPaths {
		if kept == path {
			return
		}
	}
	a.draftPaths = append(a.draftPaths, path)
}

func (a *App) draftLines(w int) []string {
	if w <= 0 {
		return nil
	}
	var lines []string
	for _, path := range a.draftPaths {
		text := textsafe.SanitizeLine("draft kept at " + path + " · esc dismisses")
		for textsafe.Width(text) > w {
			part := textsafe.Truncate(text, w, "")
			if part == "" {
				// A grapheme wider than the terminal is clipped by the frame.
				break
			}
			lines = append(lines, part)
			text = strings.TrimPrefix(text, part)
		}
		lines = append(lines, text)
	}
	return lines
}
