package cli

import (
	"fmt"

	"charm.land/lipgloss/v2"

	"taskronin/internal/textsafe"
)

// AIDEV-NOTE: quotedTitle, reason, status and shortOpID sanitize their input.
// Pass already-sanitized text to id, ok, warn, bad, dim, head and strong.
// With color off those roles preserve the original bytes.
type paint struct{ on bool }

func (e *env) paint() paint { return paint{on: e.colorEnabled()} }

func (p paint) render(s string, style lipgloss.Style) string {
	if !p.on || s == "" {
		return s
	}
	return style.Render(s)
}

func (p paint) foreground(s, color string) string {
	return p.render(s, lipgloss.NewStyle().Foreground(lipgloss.Color(color)))
}

func (p paint) id(s string) string   { return p.foreground(s, "6") }
func (p paint) ok(s string) string   { return p.foreground(s, "2") }
func (p paint) warn(s string) string { return p.foreground(s, "3") }
func (p paint) bad(s string) string  { return p.foreground(s, "1") }
func (p paint) dim(s string) string  { return p.foreground(s, "8") }

func (p paint) head(s string) string   { return p.render(s, lipgloss.NewStyle().Bold(true)) }
func (p paint) strong(s string) string { return p.head(s) }

func (p paint) taskID(id int64) string  { return p.id(fmt.Sprintf("#%d", id)) }
func (p paint) blockID(id int64) string { return p.id(fmt.Sprintf("b%d", id)) }
func (p paint) entryID(id int64) string { return p.id(fmt.Sprintf("e%d", id)) }
func (p paint) shortOpID(id string) string {
	return p.id(textsafe.SanitizeLine(fmt.Sprintf("op %.8s", id)))
}

func (p paint) quotedTitle(title string) string {
	return "'" + p.strong(textsafe.SanitizeLine(title)) + "'"
}

func (p paint) reason(reason string) string {
	if reason == "slipped" || reason == "overdue" {
		return p.bad(reason)
	}
	return p.warn(textsafe.SanitizeLine(reason))
}

func (p paint) status(status string) string {
	switch status {
	case "none":
		return p.dim(status)
	case "done", "worked":
		return p.ok(status)
	case "overdue", "slipped", "missed":
		return p.bad(status)
	default:
		return textsafe.SanitizeLine(status)
	}
}
