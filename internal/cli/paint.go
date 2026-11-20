package cli

import "charm.land/lipgloss/v2"

// AIDEV-NOTE: apply the CLI palette to already-sanitized text. With color
// off, every role preserves the original bytes.
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
