// Package textsafe validates input text and sanitizes terminal and log output.
package textsafe

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// MaxTitleRunes is the maximum number of Unicode code points in a title.
	MaxTitleRunes = 500
	// MaxNotesBytes is the maximum UTF-8 byte length of normalized notes.
	MaxNotesBytes = 65536
)

// ValidateTitle strips one leading BOM, trims whitespace, and validates a
// nonempty, single-line title of at most MaxTitleRunes Unicode code points.
func ValidateTitle(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", errors.New("is not valid UTF-8")
	}
	s = strings.TrimSpace(strings.TrimPrefix(s, "\ufeff"))
	if s == "" {
		return "", errors.New("must not be empty")
	}
	if err := validateRunes(s, false); err != nil {
		return "", err
	}
	if utf8.RuneCountInString(s) > MaxTitleRunes {
		return "", fmt.Errorf("is longer than %d characters", MaxTitleRunes)
	}
	return s, nil
}

// ValidateNotes strips one leading BOM, converts CRLF and CR to LF, and
// validates notes of at most MaxNotesBytes bytes. Empty notes are valid.
func ValidateNotes(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", errors.New("is not valid UTF-8")
	}
	s = strings.TrimPrefix(s, "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	if err := validateRunes(s, true); err != nil {
		return "", err
	}
	if len(s) > MaxNotesBytes {
		return "", fmt.Errorf("is longer than %d bytes", MaxNotesBytes)
	}
	return s, nil
}

func validateRunes(s string, allowNewlineAndTab bool) error {
	for _, r := range s {
		if allowNewlineAndTab && (r == '\n' || r == '\t') {
			continue
		}
		switch {
		case isBidiControl(r):
			return fmt.Errorf("contains a bidirectional control %U", r)
		case r == '\ufeff':
			// AIDEV-NOTE: The controller's Task 4 ruling permits only the one
			// leading BOM removed above. Reject all others so normalization
			// stays idempotent, including when title trimming exposes a BOM.
			return fmt.Errorf("contains an invisible format character %U", r)
		case r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == '\u2028' || r == '\u2029':
			return fmt.Errorf("contains a control character %U", r)
		}
	}
	return nil
}

func isBidiControl(r rune) bool {
	return (r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u2069')
}

// ValidateName checks that s contains 1..maxLen ASCII letters, digits, dots,
// underscores, or hyphens, without trimming or otherwise normalizing it.
func ValidateName(s string, maxLen int) error {
	if maxLen < 1 {
		return errors.New("maximum length must be positive")
	}
	if s == "" {
		return errors.New("must not be empty")
	}
	if len(s) > maxLen {
		return fmt.Errorf("is longer than %d characters", maxLen)
	}
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return errors.New("must contain only ASCII letters, digits, '.', '_' or '-'")
	}
	return nil
}
