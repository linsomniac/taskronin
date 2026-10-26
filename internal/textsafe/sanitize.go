package textsafe

import (
	"strings"
	"unicode"
)

// Sanitize replaces unsafe runes and each invalid UTF-8 byte with U+FFFD.
// It preserves graphic runes, newlines, tabs, ZWNJ, and ZWJ for terminal/log output.
func Sanitize(s string) string {
	// AIDEV-NOTE: Use an allowlist, not ANSI sequence parsing: replacing ESC
	// and C1 controls also neutralizes incomplete or unfamiliar ANSI/OSC
	// sequences. strings.Map decodes each invalid byte as U+FFFD.
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r == '\u200c' || r == '\u200d' ||
			(unicode.IsGraphic(r) && !isBidiControl(r)) {
			return r
		}
		return unicode.ReplacementChar
	}, s)
}

// SanitizeLine sanitizes s and also replaces newlines and tabs with U+FFFD.
func SanitizeLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return unicode.ReplacementChar
		}
		return r
	}, Sanitize(s))
}
