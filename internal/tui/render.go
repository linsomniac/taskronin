package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"taskronin/internal/api"
	"taskronin/internal/textsafe"
)

func (a *App) line(s string, w int) string {
	return textsafe.Truncate(textsafe.SanitizeLine(s), max(0, w), "…")
}

func (a *App) row(s string, w int, selected bool) string {
	return a.styledRow(textsafe.SanitizeLine(s), w, selected)
}

// styledRow accepts sanitized text with optional trusted token styles.
func (a *App) styledRow(s string, w int, selected bool) string {
	if !selected {
		return textsafe.Truncate("  "+s, w, "…")
	}
	line := textsafe.Truncate("› "+s, w, "…")
	if !a.cfg.Color {
		return line
	}
	return renderOver(a.styles.Selected, a.pad(line, w))
}

// AIDEV-NOTE: pad sanitized, already-truncated text before applying the
// selection style so the reverse bar spans the frame without exceeding it.
func (a *App) pad(s string, w int) string {
	return s + strings.Repeat(" ", max(0, w-textsafe.Width(s)))
}

// AIDEV-NOTE: Lip Gloss closes token styles with ESC[m (\x1b[m). That reset
// also clears the selection, so reapply it to later tokens and cursor padding.
func renderOver(style lipgloss.Style, text string) string {
	var out strings.Builder
	for run := range strings.SplitSeq(text, "\x1b[m") {
		if run != "" {
			out.WriteString(style.Render(run))
		}
	}
	return out.String()
}

func (a *App) id(prefix string, id int64) string {
	return a.styles.ID.Render(fmt.Sprintf("%s%d", prefix, id))
}

func (a *App) blockText(b api.Block) string {
	return a.id("b", b.ID) + " " + spanText(b.Start, b.End) + " " + a.id("#", b.TaskID) + " " + textsafe.SanitizeLine(b.TaskTitle)
}

func (a *App) blockStatus(status string) string {
	switch status {
	case "missed", "slipped":
		return a.styles.Bad.Render(status)
	case "worked":
		return a.styles.OK.Render(status)
	case "cancelled":
		return a.styles.Dim.Render(status)
	default:
		return textsafe.SanitizeLine(status)
	}
}

func (a *App) entryText(e api.Entry, linkSeparator string) string {
	text := a.id("e", e.ID)
	if e.Time != nil {
		text += " " + hhmm(int(*e.Time))
	}
	if e.Kind == "complete" {
		text += " " + a.styles.OK.Render("✓")
	}
	text += " " + textsafe.SanitizeLine(e.Title)
	if e.TaskID != nil {
		text += linkSeparator + "[" + a.id("#", *e.TaskID) + "]"
	}
	return text
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
	var tabs []string
	for i, name := range tabNames {
		label := fmt.Sprintf("%d %s", i+1, name)
		style := a.styles.TabOff
		if Tab(i) == a.tab {
			if a.cfg.Color {
				// Keep a standalone reverse-video SGR around the bold tab.
				style = a.styles.TabOnBar
				label = a.styles.Head.Render(" " + label + " ")
			} else {
				label = "[" + label + "]"
			}
		}
		tabs = append(tabs, style.Render(label))
	}
	// AIDEV-NOTE: J§9 owns the give-way order: brand, date, clock, then inactive tab names.
	header := strings.Join(tabs, " ")
	stripWidth := lipgloss.Width(header)
	if stripWidth > w {
		for i := range tabs {
			if Tab(i) != a.tab {
				tabs[i] = a.styles.TabOff.Render(fmt.Sprint(i + 1))
			}
		}
		header = strings.Join(tabs, " ")
	} else {
		clock := a.clock.Now.Format("Mon 2006-01-02 15:04")
		for _, prefix := range []string{"Taskronin · " + clock, clock, a.clock.Now.Format("Mon 15:04")} {
			if lipgloss.Width(prefix)+1+stripWidth <= w {
				header = a.styles.Head.Render(prefix) + " " + header
				break
			}
		}
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
			return header + "\n" + strings.Join(status, " · ")
		}
	}
	return header
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
