package civil

import (
	"testing"
	"time"
)

func TestParseDate(t *testing.T) {
	for _, tc := range []struct {
		in string
		ok bool
	}{
		{"2026-10-28", true}, {"2024-02-29", true}, {"2026-02-30", false}, {"2026-2-3", false},
		{"2026-13-01", false}, {"20261028", false}, {" 2026-10-28", false}, {"0999-01-01", false},
	} {
		d, err := ParseDate(tc.in)
		if (err == nil) != tc.ok {
			t.Fatalf("ParseDate(%q) err = %v, want ok=%v", tc.in, err, tc.ok)
		}
		if tc.ok && d.String() != tc.in {
			t.Fatalf("round trip %q → %q", tc.in, d)
		}
	}
}

func TestDateArithmetic(t *testing.T) {
	d := MustDate("2026-10-28")
	for _, tc := range []struct {
		got, want Date
	}{
		{d.AddDays(4), MustDate("2026-11-01")},
		{d.AddDays(-28), MustDate("2026-09-30")},
		{MustDate("2026-12-31").AddDays(1), MustDate("2027-01-01")},
		{d.AddDays(0), d},
	} {
		if tc.got != tc.want {
			t.Fatalf("got %v, want %v", tc.got, tc.want)
		}
	}
	if n := MustDate("2026-11-04").DaysSince(d); n != 7 {
		t.Fatalf("DaysSince = %d, want 7", n)
	}
	if d.Weekday() != time.Wednesday {
		t.Fatalf("Weekday = %v", d.Weekday())
	}
	later, same := d.AddDays(1), d.AddDays(0)
	if d.Compare(later) != -1 || later.Compare(d) != 1 || d.Compare(same) != 0 {
		t.Fatal("Compare wrong")
	}
	if !d.Before(later) || d.After(later) || !later.After(d) || d.Before(d) {
		t.Fatal("Before/After wrong")
	}
	if !(Date{}).IsZero() || d.IsZero() {
		t.Fatal("IsZero wrong")
	}
}

func TestDateTextAndSQL(t *testing.T) {
	d := MustDate("2026-10-28")
	v, err := d.Value()
	if err != nil || v != "2026-10-28" {
		t.Fatalf("Value = %v, %v", v, err)
	}
	for _, src := range []any{"2026-10-28", []byte("2026-10-28")} {
		var got Date
		if err := got.Scan(src); err != nil || got != d {
			t.Fatalf("Scan(%T) = %v, %v", src, got, err)
		}
	}
	var bad Date
	if err := bad.Scan("2026-02-30"); err == nil {
		t.Fatal("Scan of invalid date succeeded")
	}
	if err := bad.Scan(int64(5)); err == nil {
		t.Fatal("Scan of int64 succeeded")
	}
	if _, err := (Date{}).MarshalText(); err == nil {
		t.Fatal("zero Date MarshalText succeeded")
	}
	b, err := d.MarshalText()
	if err != nil || string(b) != "2026-10-28" {
		t.Fatalf("MarshalText = %q, %v", b, err)
	}
	var u Date
	if err := u.UnmarshalText([]byte("2026-10-28")); err != nil || u != d {
		t.Fatalf("UnmarshalText = %v, %v", u, err)
	}
	if err := u.UnmarshalText([]byte("nope")); err == nil {
		t.Fatal("UnmarshalText accepted garbage")
	}
}

func FuzzParseDate(f *testing.F) {
	for _, s := range []string{"2026-10-28", "2024-02-29", "2026-02-30", "", "0999-01-01", "2026-1-1"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		d, err := ParseDate(s)
		if err != nil {
			return
		}
		if d.String() != s {
			t.Fatalf("accepted %q but String() = %q", s, d.String())
		}
	})
}
