package tui

import "charm.land/lipgloss/v2"

// Styles contains only trusted presentation; sanitize user text before rendering.
type Styles struct {
	Header, TabOn, TabOff, Selected, Dim, Warn, Err, Info, Current lipgloss.Style
}

func newStyles(color bool) Styles {
	plain := lipgloss.NewStyle()
	s := Styles{Header: plain, TabOn: plain, TabOff: plain, Selected: plain,
		Dim: plain, Warn: plain, Err: plain, Info: plain, Current: plain}
	if color {
		s.Header = plain.Bold(true)
		s.TabOn = plain.Bold(true).Foreground(lipgloss.Color("6"))
		s.TabOff = plain.Foreground(lipgloss.Color("7"))
		s.Selected = plain.Bold(true).Foreground(lipgloss.Color("6"))
		s.Dim = plain.Foreground(lipgloss.Color("8"))
		s.Warn = plain.Foreground(lipgloss.Color("3"))
		s.Err = plain.Foreground(lipgloss.Color("1"))
		s.Info = plain.Foreground(lipgloss.Color("6"))
		s.Current = plain.Bold(true).Foreground(lipgloss.Color("2"))
	}
	return s
}
