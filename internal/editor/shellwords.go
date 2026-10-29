package editor

import (
	"errors"
	"strings"
)

// SplitWords splits whitespace-separated arguments, honoring single quotes,
// double quotes and backslash escapes. It never expands shell expressions.
func SplitWords(s string) ([]string, error) {
	if strings.IndexByte(s, 0) >= 0 {
		return nil, errors.New("NUL in editor command")
	}
	var words []string
	var word strings.Builder
	var quote byte
	started := false
	// AIDEV-NOTE: parse bytes so even non-UTF-8 path names survive quoting.
	// Shell operators, variables, substitutions and globs are only literal text.
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch quote {
		case '\'':
			if c == '\'' {
				quote = 0
			} else {
				word.WriteByte(c)
			}
		case '"':
			switch c {
			case '"':
				quote = 0
			case '\\':
				if i+1 < len(s) && strings.ContainsRune("\\\"$`", rune(s[i+1])) {
					i++
					c = s[i]
				}
				word.WriteByte(c)
			default:
				word.WriteByte(c)
			}
		default:
			switch c {
			case ' ', '\t', '\n', '\r':
				if started {
					words = append(words, word.String())
					word.Reset()
					started = false
				}
			case '\'', '"':
				quote = c
				started = true
			case '\\':
				i++
				if i == len(s) {
					return nil, errors.New("trailing escape in editor command")
				}
				word.WriteByte(s[i])
				started = true
			default:
				word.WriteByte(c)
				started = true
			}
		}
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote in editor command")
	}
	if started {
		words = append(words, word.String())
	}
	if len(words) == 0 {
		return nil, errors.New("empty editor command")
	}
	return words, nil
}
