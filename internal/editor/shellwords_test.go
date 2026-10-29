package editor

import (
	"slices"
	"strings"
	"testing"
)

func TestSplitWords(t *testing.T) {
	for _, tt := range []struct {
		input string
		want  []string
	}{
		{`vim`, []string{"vim"}},
		{`code --wait`, []string{"code", "--wait"}},
		{`'my editor' -f`, []string{"my editor", "-f"}},
		{`"a\"b"`, []string{`a"b`}},
		{`a\ b`, []string{"a b"}},
		{`"x$HOME"`, []string{"x$HOME"}},
		{`$(rm -rf /)`, []string{"$(rm", "-rf", "/)"}},
		{" \tvim\n--wait\r ", []string{"vim", "--wait"}},
		{`pre"mid dle"'post'`, []string{"premid dlepost"}},
		{`'' "" a''`, []string{"", "", "a"}},
		{`'a\b' "a\qb"`, []string{`a\b`, `a\qb`}},
		{"\"\\\\ \\\" \\$ \\`\"", []string{"\\ \" $ `"}},
		{"vim ; echo hi | cat >file & `id` ~ *.md #comment", []string{"vim", ";", "echo", "hi", "|", "cat", ">file", "&", "`id`", "~", "*.md", "#comment"}},
	} {
		t.Run(tt.input, func(t *testing.T) {
			got, err := SplitWords(tt.input)
			if err != nil || !slices.Equal(got, tt.want) {
				t.Fatalf("SplitWords(%q) = %q, %v; want %q", tt.input, got, err, tt.want)
			}
		})
	}
	for _, input := range []string{"", "   ", `"unterminated`, `'x`, `vim\`, "vim\x00"} {
		t.Run("invalid/"+input, func(t *testing.T) {
			if got, err := SplitWords(input); err == nil {
				t.Fatalf("SplitWords(%q) = %q, want error", input, got)
			}
		})
	}
}

func FuzzSplitWords(f *testing.F) {
	for _, seed := range []string{"", "vim", "a b\x00c", "'quote'\x00\"double\"\x00\\", "$(rm -rf /)", "\xff\x00\xfe", "\n\t"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		_, _ = SplitWords(input) // Arbitrary input must never panic.
		// NUL separates words here, so each quoted argument is NUL-free.
		words := strings.Split(input, "\x00")
		quoted := make([]string, len(words))
		for i, word := range words {
			quoted[i] = "'" + strings.ReplaceAll(word, "'", "'\\''") + "'"
		}
		got, err := SplitWords(strings.Join(quoted, " "))
		if err != nil || !slices.Equal(got, words) {
			t.Fatalf("quoted round trip = %q, %v; want %q", got, err, words)
		}
	})
}

func TestCommandOrder(t *testing.T) {
	for _, tt := range []struct {
		configured, visual, editor string
		want                       []string
	}{
		{"nano -w", "code", "vim", []string{"nano", "-w"}},
		{"", "code", "vim", []string{"code"}},
		{"", "", "vim", []string{"vim"}},
		{"", "", "", []string{"vi"}},
		{"", "'my editor' --wait", "vim", []string{"my editor", "--wait"}},
	} {
		t.Run(strings.Join(tt.want, " "), func(t *testing.T) {
			env := map[string]string{"VISUAL": tt.visual, "EDITOR": tt.editor}
			got, err := Command(tt.configured, func(key string) string { return env[key] })
			if err != nil || !slices.Equal(got, tt.want) {
				t.Fatalf("Command = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
	for _, setting := range []string{" ", "''", `'unterminated`} {
		t.Run("invalid/"+setting, func(t *testing.T) {
			if _, err := Command(setting, func(string) string { return "vi" }); err == nil {
				t.Fatal("invalid configured editor fell back to the environment")
			}
			if _, err := Command("", func(string) string { return setting }); err == nil {
				t.Fatal("invalid environment editor was accepted")
			}
		})
	}
}
