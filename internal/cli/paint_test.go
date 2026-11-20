package cli

import "testing"

func TestPaint(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(paint, string) string
		sgr   string
	}{
		{"id", paint.id, "36"},
		{"ok", paint.ok, "32"},
		{"warn", paint.warn, "33"},
		{"bad", paint.bad, "31"},
		{"dim", paint.dim, "90"},
		{"head", paint.head, "1"},
		{"strong", paint.strong, "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const input = "#1"
			if got := tc.apply(paint{}, input); got != input {
				t.Errorf("off = %q, want %q", got, input)
			}
			on := paint{on: true}
			styled := "\x1b[" + tc.sgr + "m" + input
			if got := tc.apply(on, input); got != styled+"\x1b[m" && got != styled+"\x1b[0m" {
				t.Errorf("on = %q, want %q followed by SGR reset", got, styled)
			}
			if tc.apply(on, "") != "" || tc.apply(paint{}, "") != "" {
				t.Error("empty stays empty")
			}
		})
	}
}
