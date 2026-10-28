package api

import "testing"

func FuzzParseETag(f *testing.F) {
	for _, input := range []string{
		`"0123456789abcdef-7"`, `"0000000000000000-1"`, `"ffffffffffffffff-9223372036854775807"`,
		"", "*", `W/"0123456789abcdef-7"`, `"0123456789abcdef-7", "0123456789abcdef-8"`,
		"0123456789abcdef-7", `"0123456789ABCDEF-7"`, `"0123456789abcdef-0"`,
		`"0123456789abcdef-"`, `"abc-1"`, `"0123456789abcdef-01"`, `"0123456789abcdef-9223372036854775808"`,
	} {
		f.Add(input)
	}
	f.Fuzz(func(t *testing.T, input string) {
		expect, err := ParseETag(input)
		if err != nil {
			return
		}
		if got := FormatETag(expect.Gen, expect.Version); got != input {
			t.Fatalf("ParseETag(%q) re-formatted to %q", input, got)
		}
	})
}
