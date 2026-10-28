package api

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"taskronin/internal/domain"
)

func TestETag(t *testing.T) {
	const gen domain.Generation = "0123456789abcdef"
	for _, tc := range []struct {
		version domain.Version
		wire    string
	}{
		{1, `"0123456789abcdef-1"`},
		{7, `"0123456789abcdef-7"`},
		{9223372036854775807, `"0123456789abcdef-9223372036854775807"`},
	} {
		if got := FormatETag(gen, tc.version); got != tc.wire {
			t.Errorf("FormatETag = %q, want %q", got, tc.wire)
		}
		got, err := ParseETag(tc.wire)
		if err != nil || got != (domain.Expect{Gen: gen, Version: tc.version}) {
			t.Errorf("ParseETag(%q) = %+v, %v", tc.wire, got, err)
		}
	}
	for _, input := range []string{
		"", "*", `W/"0123456789abcdef-7"`, `"0123456789abcdef-7", "0123456789abcdef-8"`,
		"0123456789abcdef-7", `"0123456789ABCDEF-7"`, `"0123456789abcdef-0"`,
		`"0123456789abcdef-"`, `"abc-1"`, `"0123456789abcdef--1"`,
		`"0123456789abcdef-+1"`, `"0123456789abcdef-01"`, `"0123456789abcdef-1.0"`,
		`"0123456789abcdef-9223372036854775808"`, ` "0123456789abcdef-7"`,
		`"0123456789abcdef-7" `, `"0123456789abcdeg-7"`, `"a-44136fa355b3678a"`,
	} {
		_, err := ParseETag(input)
		var e *domain.Error
		if !errors.As(err, &e) || e.Code != domain.CodeMalformed {
			t.Errorf("ParseETag(%q) = %v, want domain.CodeMalformed", input, err)
		}
	}
	for _, tc := range []struct {
		body string
		wire string
	}{
		{"{}", `"a-44136fa355b3678a"`},
		{"", `"a-e3b0c44298fc1c14"`},
		{"abc", `"a-ba7816bf8f01cfea"`},
	} {
		if got := AggregateETag([]byte(tc.body)); got != tc.wire {
			t.Errorf("AggregateETag(%q) = %q, want %q", tc.body, got, tc.wire)
		}
	}
}

func TestIdempotencyKey(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want bool
	}{
		{"", false},
		{strings.Repeat("a", 15), false},
		{strings.Repeat("a", 16), true},
		{strings.Repeat("a", 128), true},
		{strings.Repeat("a", 129), false},
		{"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-", true},
		{"aaaaaaaaaaaaaaa+", false},
		{"aaaaaaaaaaaaaaa/", false},
		{"aaaaaaaaaaaaaaa=", false},
		{"aaaaaaaaaaaaaaa ", false},
		{"aaaaaaaaaaaaaaa\n", false},
		{"aaaaaaaaaaaaaaa\x00", false},
		{"aaaaaaaaaaaaaaaé", false},
	} {
		if got := ValidIdempotencyKey(tc.key); got != tc.want {
			t.Errorf("ValidIdempotencyKey(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
	a, b := NewIdempotencyKey(), NewIdempotencyKey()
	pattern := regexp.MustCompile(`^[0-9a-f]{32}$`)
	if !pattern.MatchString(a) || !pattern.MatchString(b) || a == b ||
		!ValidIdempotencyKey(a) || !ValidIdempotencyKey(b) {
		t.Fatalf("invalid generated idempotency keys %q, %q", a, b)
	}
}
