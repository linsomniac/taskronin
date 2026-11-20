package civil

import (
	"errors"
	"testing"
)

func TestParseHHMM(t *testing.T) {
	for _, tc := range []struct {
		in     string
		allow  bool
		want   Minute
		wantOK bool
	}{
		{"9:05", false, 545, true}, {"09:05", false, 545, true}, {"00:00", false, 0, true},
		{"23:59", false, 1439, true}, {"24:00", false, 0, false}, {"24:00", true, 1440, true},
		{"9:5", false, 0, false}, {"23:60", false, 0, false}, {"24:01", true, 0, false},
		{"-1:00", false, 0, false}, {"", false, 0, false}, {"009:00", false, 0, false},
	} {
		got, err := ParseHHMM(tc.in, tc.allow)
		if (err == nil) != tc.wantOK {
			t.Fatalf("ParseHHMM(%q,%v) err = %v, want ok=%v", tc.in, tc.allow, err, tc.wantOK)
		}
		if tc.wantOK && got != tc.want {
			t.Fatalf("ParseHHMM(%q,%v) = %d, want %d", tc.in, tc.allow, got, tc.want)
		}
	}
}

func TestClockError(t *testing.T) {
	for input, reason := range map[string]string{"25:00": "hour out of range", "1:60": "minutes out of range", "9:5": "expected HH:MM"} {
		_, err := ParseHHMM(input, false)
		var clock *ClockError
		if !errors.As(err, &clock) || clock.Value != input || clock.Reason != reason {
			t.Errorf("ParseHHMM(%q) error = %#v", input, err)
		}
	}
}

func TestMinuteText(t *testing.T) {
	if s := Minute(545).String(); s != "09:05" {
		t.Fatalf("String = %q", s)
	}
	b, err := Minute(545).MarshalText()
	if err != nil || string(b) != "09:05" {
		t.Fatalf("MarshalText = %q, %v", b, err)
	}
	var m Minute
	if err := m.UnmarshalText([]byte("9:05")); err != nil || m != 545 {
		t.Fatalf("UnmarshalText = %v, %v", m, err)
	}
	if err := m.UnmarshalText([]byte("24:00")); err == nil {
		t.Fatal("Minute accepted 24:00")
	}

	e := EndMinute(1440)
	if s := e.String(); s != "24:00" {
		t.Fatalf("EndMinute String = %q", s)
	}
	b, err = e.MarshalText()
	if err != nil || string(b) != "24:00" {
		t.Fatalf("EndMinute MarshalText = %q, %v", b, err)
	}
	var e2 EndMinute
	if err := e2.UnmarshalText([]byte("24:00")); err != nil || e2 != 1440 {
		t.Fatalf("EndMinute UnmarshalText = %v, %v", e2, err)
	}
	if err := e2.UnmarshalText([]byte("00:00")); err == nil {
		t.Fatal("EndMinute accepted 00:00")
	}
	if err := e2.UnmarshalText([]byte("10:30")); err != nil || e2 != 630 {
		t.Fatalf("EndMinute 10:30 = %v, %v", e2, err)
	}
}

func TestMinuteSQL(t *testing.T) {
	v, err := Minute(545).Value()
	if err != nil || v != int64(545) {
		t.Fatalf("Value = %v, %v", v, err)
	}
	var m Minute
	if err := m.Scan(int64(545)); err != nil || m != 545 {
		t.Fatalf("Scan = %v, %v", m, err)
	}
	if err := m.Scan(int64(1440)); err == nil {
		t.Fatal("Minute Scan accepted 1440")
	}
	if err := m.Scan("x"); err == nil {
		t.Fatal("Minute Scan accepted string")
	}
	var e EndMinute
	if err := e.Scan(int64(1440)); err != nil || e != 1440 {
		t.Fatalf("EndMinute Scan = %v, %v", e, err)
	}
	if err := e.Scan(int64(0)); err == nil {
		t.Fatal("EndMinute Scan accepted 0")
	}
	ev, err := EndMinute(1440).Value()
	if err != nil || ev != int64(1440) {
		t.Fatalf("EndMinute Value = %v, %v", ev, err)
	}
}

func FuzzParseHHMM(f *testing.F) {
	for _, s := range []string{"9:05", "09:05", "24:00", "00:00", "23:59", "", "9:5"} {
		f.Add(s, true)
		f.Add(s, false)
	}
	f.Fuzz(func(t *testing.T, s string, allow bool) {
		v, err := ParseHHMM(s, allow)
		if err != nil {
			return
		}
		canon := v.String()
		if len(canon) != 5 || canon[2] != ':' {
			t.Fatalf("non-canonical %q", canon)
		}
		v2, err := ParseHHMM(canon, allow)
		if err != nil || v2 != v {
			t.Fatalf("round trip %q → %q → %v, %v", s, canon, v2, err)
		}
	})
}
