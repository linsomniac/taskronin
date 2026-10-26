package textsafe

import "github.com/charmbracelet/x/ansi"

// Width returns the terminal cell width of s, ignoring ANSI escape sequences.
// Sanitize user text before applying trusted terminal styles and measuring it.
func Width(s string) int {
	return ansi.StringWidth(s)
}

// Truncate shortens s to width terminal cells, including tail when truncated,
// while preserving grapheme clusters and ANSI sequences. It does not sanitize s.
func Truncate(s string, width int, tail string) string {
	return ansi.Truncate(s, width, tail)
}
