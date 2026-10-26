package textsafe

import (
	"fmt"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestValidateTitle(t *testing.T) {
	for _, tc := range []struct {
		name, in, want, problem string
	}{
		{name: "trim", in: "  Set up keycloak  ", want: "Set up keycloak"},
		{name: "unicode spaces", in: "\u00a0é漢\u3000", want: "é漢"},
		{name: "leading BOM", in: "\ufeffx", want: "x"},
		{name: "BOM then spaces", in: "\ufeff  x  ", want: "x"},
		{name: "repeated BOM", in: "\ufeff\ufeffx", problem: "invisible format character U+FEFF"},
		{name: "embedded BOM", in: "a\ufeffb", problem: "invisible format character U+FEFF"},
		{name: "BOM exposed by trim", in: " \ufeffx", problem: "invisible format character U+FEFF"},
		{name: "500 runes", in: strings.Repeat("é", 500), want: strings.Repeat("é", 500)},
		{name: "501 runes", in: strings.Repeat("é", 501), problem: "is longer than 500 characters"},
		{name: "unicode", in: "é漢 👨‍👩 a\u200cb e\u0301 �", want: "é漢 👨‍👩 a\u200cb e\u0301 �"},
		{name: "empty", problem: "must not be empty"},
		{name: "spaces", in: "   ", problem: "must not be empty"},
		{name: "BOM only", in: "\ufeff", problem: "must not be empty"},
		{name: "tab", in: "a\tb", problem: "control character U+0009"},
		{name: "newline", in: "a\nb", problem: "control character U+000A"},
		{name: "DEL", in: "a\x7fb", problem: "control character U+007F"},
		{name: "C1", in: "a\u0085b", problem: "control character U+0085"},
		{name: "bidi override", in: "a\u202eb", problem: "bidirectional control U+202E"},
		{name: "bidi isolate", in: "a\u2066b", problem: "bidirectional control U+2066"},
		{name: "line separator", in: "a\u2028b", problem: "U+2028"},
		{name: "invalid UTF-8", in: "\xff", problem: "is not valid UTF-8"},
		{name: "truncated UTF-8", in: "\xe2\x82", problem: "is not valid UTF-8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateTitle(tc.in)
			if tc.problem != "" {
				if err == nil || !strings.Contains(err.Error(), tc.problem) {
					t.Fatalf("ValidateTitle error = %v, want %q", err, tc.problem)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("ValidateTitle = %q, %v; want %q, nil", got, err, tc.want)
			}
		})
	}
}

func TestValidateNotes(t *testing.T) {
	for _, tc := range []struct {
		name, in, want, problem string
	}{
		{name: "empty"},
		{name: "newlines", in: "a\r\nb\rc", want: "a\nb\nc"},
		{name: "consecutive CR", in: "a\r\r\nb", want: "a\n\nb"},
		{name: "indent", in: "\tindent", want: "\tindent"},
		{name: "preserve spaces", in: "  é漢 👨‍👩 \n", want: "  é漢 👨‍👩 \n"},
		{name: "leading BOM", in: "\ufeffx", want: "x"},
		{name: "BOM only", in: "\ufeff", want: ""},
		{name: "repeated BOM", in: "\ufeff\ufeffx", problem: "invisible format character U+FEFF"},
		{name: "embedded BOM", in: "a\ufeffb", problem: "invisible format character U+FEFF"},
		{name: "BOM after space", in: " \ufeffx", problem: "invisible format character U+FEFF"},
		{name: "65536 bytes", in: strings.Repeat("a", 65536), want: strings.Repeat("a", 65536)},
		{name: "65537 bytes", in: strings.Repeat("a", 65537), problem: "is longer than 65536 bytes"},
		{name: "multibyte limit", in: strings.Repeat("é", 32768), want: strings.Repeat("é", 32768)},
		{name: "multibyte over limit", in: strings.Repeat("é", 32768) + "a", problem: "is longer than 65536 bytes"},
		{name: "limit after CRLF normalization", in: strings.Repeat("\r\n", 65536), want: strings.Repeat("\n", 65536)},
		{name: "limit after BOM stripping", in: "\ufeff" + strings.Repeat("a", 65536), want: strings.Repeat("a", 65536)},
		{name: "bell", in: "\x07", problem: "control character U+0007"},
		{name: "C1", in: "\u0085", problem: "control character U+0085"},
		{name: "bidi override", in: "\u202e", problem: "bidirectional control U+202E"},
		{name: "paragraph separator", in: "\u2029", problem: "U+2029"},
		{name: "invalid UTF-8", in: "\xff", problem: "is not valid UTF-8"},
		{name: "invalid after BOM", in: "\ufeff\xff", problem: "is not valid UTF-8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateNotes(tc.in)
			if tc.problem != "" {
				if err == nil || !strings.Contains(err.Error(), tc.problem) {
					t.Fatalf("ValidateNotes error = %v, want %q", err, tc.problem)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("ValidateNotes returned %d bytes, %v; want %d matching bytes, nil", len(got), err, len(tc.want))
			}
		})
	}
}

func TestValidateName(t *testing.T) {
	for _, tc := range []struct {
		in   string
		max  int
		good bool
	}{
		{"infra.keycloak", 100, true}, {"db_1-x", 100, true},
		{"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._-", 100, true},
		{strings.Repeat("a", 100), 100, true}, {"x", 1, true},
		{"", 100, false}, {"a b", 100, false}, {"é", 100, false},
		{strings.Repeat("a", 101), 100, false}, {" a", 100, false},
		{"a\n", 100, false}, {"a/b", 100, false}, {"\xff", 100, false},
		{"\ufeffx", 100, false}, {"x", 0, false}, {"", 0, false}, {"x", -1, false},
	} {
		t.Run(fmt.Sprintf("%q/max=%d", tc.in, tc.max), func(t *testing.T) {
			err := ValidateName(tc.in, tc.max)
			if (err == nil) != tc.good || (err != nil && err.Error() == "") {
				t.Fatalf("ValidateName = %v; want valid=%v and a nonempty error on rejection", err, tc.good)
			}
		})
	}
}

func TestSanitize(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""}, {"a\x1b[31mb", "a�[31mb"}, {"x\u202ey", "x�y"},
		{"\xff", "�"}, {"\xff\xfe", "��"}, {"\xe2\x82", "��"},
		{"\xc0\xaf", "��"}, {"\xed\xa0\x80", "���"},
		{"l1\nl2\t", "l1\nl2\t"}, {"é漢", "é漢"}, {"👨‍👩", "👨‍👩"},
		{"a\u200cb", "a\u200cb"}, {"e\u0301", "e\u0301"}, {"�", "�"},
		{"\u00a0\u3000", "\u00a0\u3000"},
		{"\ufeffx\u200b\u200e\u200f\u2060", "�x����"},
		{"\x1b]52;c;payload\x07", "�]52;c;payload�"},
		{"\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\", "�]8;;https://example.com�\\link�]8;;�\\"},
		{"\u009d52;c;payload\u009c", "�52;c;payload�"},
		{"a\rb\bc", "a�b�c"}, {"\u2028\u2029", "��"},
	} {
		t.Run(fmt.Sprintf("%q", tc.in), func(t *testing.T) {
			if got := Sanitize(tc.in); got != tc.want {
				t.Fatalf("Sanitize = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestSanitizeLine(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""}, {"a\nb\tc", "a�b�c"},
		{"é漢 👨‍👩 e\u0301 a\u200cb", "é漢 👨‍👩 e\u0301 a\u200cb"},
		{"\x1b[31m\xff\u202e\u2028\r\n\t", "�[31m������"},
	} {
		t.Run(fmt.Sprintf("%q", tc.in), func(t *testing.T) {
			if got := SanitizeLine(tc.in); got != tc.want {
				t.Fatalf("SanitizeLine = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestForbiddenRunes(t *testing.T) {
	// Enumerate every control in the specified ranges, with surrounding text
	// so title whitespace trimming cannot hide a forbidden interior rune.
	for _, span := range [][2]rune{{0, 0x1f}, {0x7f, 0x9f}, {0x2028, 0x202e}, {0x2066, 0x2069}, {0xfeff, 0xfeff}} {
		for r := span[0]; r <= span[1]; r++ {
			t.Run(fmt.Sprintf("%U", r), func(t *testing.T) {
				s := "a" + string(r) + "b"
				if _, err := ValidateTitle(s); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%U", r)) {
					t.Fatalf("ValidateTitle(%q) error = %v; want offending code point", s, err)
				}
				got, err := ValidateNotes(s)
				if r == '\n' || r == '\t' || r == '\r' {
					want := s
					if r == '\r' {
						want = "a\nb"
					}
					if err != nil || got != want {
						t.Fatalf("ValidateNotes(%q) = %q, %v; want %q, nil", s, got, err, want)
					}
				} else if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%U", r)) {
					t.Fatalf("ValidateNotes(%q) error = %v; want offending code point", s, err)
				}
				want := "a�b"
				if r == '\n' || r == '\t' {
					want = s
				}
				if got := Sanitize(s); got != want {
					t.Fatalf("Sanitize(%q) = %q; want %q", s, got, want)
				}
				if got := SanitizeLine(s); got != "a�b" {
					t.Fatalf("SanitizeLine(%q) = %q; want a replacement", s, got)
				}
			})
		}
	}
}

func TestWidthTruncate(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"", 0}, {"abcdef", 6}, {"漢字", 4}, {"e\u0301", 1}, {"👨‍👩", 2},
		{"\x1b[31m漢字\x1b[0m", 4},
	} {
		if got := Width(tc.in); got != tc.want {
			t.Errorf("Width(%q) = %d; want %d", tc.in, got, tc.want)
		}
	}
	for _, tc := range []struct {
		in    string
		width int
		tail  string
		want  string
	}{
		{"abcdef", 4, "…", "abc…"}, {"abcdef", 6, "…", "abcdef"},
		{"abcdef", 0, "", ""}, {"abcdef", 4, "", "abcd"},
		{"漢字語", 5, "…", "漢字…"}, {"e\u0301xyz", 2, "…", "e\u0301…"},
		{"👨‍👩xyz", 3, "…", "👨‍👩…"},
		{"\x1b[31mabcdef\x1b[0m", 4, "…", "\x1b[31mabc…\x1b[0m"},
	} {
		if got := Truncate(tc.in, tc.width, tc.tail); got != tc.want {
			t.Errorf("Truncate(%q, %d, %q) = %q; want %q", tc.in, tc.width, tc.tail, got, tc.want)
		}
	}
}

func FuzzValidateTitle(f *testing.F) {
	for _, s := range []string{"", "  Set up keycloak  ", "é漢 👨‍👩", "\ufeffx", "\ufeff\ufeffx", "a\ufeffb", " \ufeffx", "a\nb", "\xff", "a\u0085b", "a\u202eb", strings.Repeat("é", 500), strings.Repeat("é", 501)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, err := ValidateTitle(s)
		if err != nil {
			return
		}
		assertValidatedText(t, got, false)
		if n := utf8.RuneCountInString(got); n < 1 || n > MaxTitleRunes || MaxTitleRunes != 500 {
			t.Fatalf("accepted title has %d runes; limit = %d", n, MaxTitleRunes)
		}
		if strings.TrimSpace(got) != got {
			t.Fatalf("accepted title is not trimmed: %q", got)
		}
		if again, err := ValidateTitle(got); err != nil || again != got {
			t.Fatalf("accepted title %q revalidates to %q, %v", got, again, err)
		}
	})
}

func FuzzValidateNotes(f *testing.F) {
	for _, s := range []string{"", "a\r\nb\rc", "\tindent", "é漢 👨‍👩", "\ufeffx", "\ufeff\ufeffx", "a\ufeffb", " \ufeffx", "\x07", "\u0085", "\u2029", "\u202e", "\xff", strings.Repeat("é", 32768), strings.Repeat("a", 65537)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, err := ValidateNotes(s)
		if err != nil {
			return
		}
		assertValidatedText(t, got, true)
		if len(got) > MaxNotesBytes || MaxNotesBytes != 65536 {
			t.Fatalf("accepted notes have %d bytes; limit = %d", len(got), MaxNotesBytes)
		}
		if again, err := ValidateNotes(got); err != nil || again != got {
			t.Fatalf("accepted notes revalidate differently: err=%v", err)
		}
	})
}

func assertValidatedText(t *testing.T, s string, notes bool) {
	t.Helper()
	if !utf8.ValidString(s) {
		t.Fatal("accepted invalid UTF-8")
	}
	for _, r := range s {
		if notes && (r == '\n' || r == '\t') {
			continue
		}
		// Independent oracle: Unicode's control category plus the explicit
		// separators, bidi controls, and BOM specified by the task/ruling.
		if unicode.IsControl(r) || strings.ContainsRune("\u2028\u2029\u202a\u202b\u202c\u202d\u202e\u2066\u2067\u2068\u2069\ufeff", r) {
			t.Fatalf("accepted forbidden rune %U", r)
		}
	}
}

func FuzzSanitize(f *testing.F) {
	for _, s := range []string{"", "a\x1b[31mb", "\x1b]52;c;payload\x07", "\xff\xfe", "\xe2\x82", "é漢", "👨‍👩", "a\u200cb", "l1\nl2\t", "\ufeff\u200b\u2028\u2029\u202e\u2066"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, line := range []bool{false, true} {
			got := Sanitize(s)
			if line {
				got = SanitizeLine(s)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("sanitizer emitted invalid UTF-8 (line=%v)", line)
			}
			for _, r := range got {
				if strings.ContainsRune("\u2028\u2029\u202a\u202b\u202c\u202d\u202e\u2066\u2067\u2068\u2069", r) {
					t.Fatalf("sanitizer emitted forbidden rune %U", r)
				}
				if !line && (r == '\n' || r == '\t') {
					continue
				}
				if !unicode.IsGraphic(r) && r != '\u200c' && r != '\u200d' && r != unicode.ReplacementChar {
					t.Fatalf("sanitizer emitted non-graphic rune %U (line=%v)", r, line)
				}
			}
			if Sanitize(got) != got || (line && SanitizeLine(got) != got) {
				t.Fatalf("sanitizer is not idempotent (line=%v)", line)
			}
		}
	})
}
