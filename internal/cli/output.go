package cli

import (
	"fmt"

	"charm.land/lipgloss/v2"

	"taskronin/internal/textsafe"
)

func (e *env) colorEnabled() bool {
	mode := e.color
	if !e.colorSet {
		if cfg, err := e.clientConfig(); err == nil {
			mode = cfg.Color
		}
	}
	if mode == "always" {
		return true
	}
	return mode != "never" && e.sys.StdoutTTY && (e.sys.Getenv == nil || e.sys.Getenv("NO_COLOR") == "")
}

func (e *env) errorLine(format string, args ...any) {
	line := textsafe.SanitizeLine(fmt.Sprintf(format, args...))
	if e.colorEnabled() {
		line = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Render(line)
	}
	_, _ = fmt.Fprintln(e.err, line)
}
