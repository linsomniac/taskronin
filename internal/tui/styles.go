package tui

import "charm.land/lipgloss/v2"

// Styles contains only trusted presentation; sanitize user text before rendering.
type Styles struct {
	Header, TabOn, TabOnBar, TabOff, Selected, Dim, Warn, Err, Info, Current lipgloss.Style
	ID, OK, Bad, Head                                                        lipgloss.Style
	Box                                                                      boxChars
}

type boxChars struct{ TL, TR, BL, BR, H, V string }

func newStyles(color bool) Styles {
	plain := lipgloss.NewStyle()
	s := Styles{Header: plain, TabOn: plain, TabOnBar: plain, TabOff: plain, Selected: plain,
		Dim: plain, Warn: plain, Err: plain, Info: plain, Current: plain,
		ID: plain, OK: plain, Bad: plain, Head: plain,
		Box: boxChars{TL: "╭", TR: "╮", BL: "╰", BR: "╯", H: "─", V: "│"}}
	if color {
		s.Header = plain.Bold(true)
		s.TabOnBar = plain.Reverse(true)
		s.TabOn = s.TabOnBar.Bold(true)
		s.TabOff = plain.Foreground(lipgloss.Color("7"))
		s.Selected = plain.Reverse(true)
		s.Dim = plain.Foreground(lipgloss.Color("8"))
		s.Warn = plain.Foreground(lipgloss.Color("3"))
		s.Err = plain.Foreground(lipgloss.Color("1"))
		s.Info = plain.Foreground(lipgloss.Color("6"))
		s.Current = plain.Bold(true).Foreground(lipgloss.Color("2"))
		s.ID = s.Info
		s.OK = s.Current
		s.Bad = s.Err
		s.Head = s.Header
	}
	return s
}
