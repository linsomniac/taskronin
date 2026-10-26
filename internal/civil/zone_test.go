package civil

import (
	"database/sql"
	"errors"
	"strconv"
	"testing"
	"time"
)

func mustZone(t testing.TB, name string) Zone {
	t.Helper()
	z, err := LoadZone(name)
	if err != nil {
		t.Fatalf("LoadZone(%q): %v", name, err)
	}
	return z
}

func mustInstant(t testing.TB, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("time.Parse(%q): %v", s, err)
	}
	return v
}

func someMinute(m Minute) sql.Null[Minute] { return sql.Null[Minute]{V: m, Valid: true} }

var noMinute sql.Null[Minute]

func TestLoadZone(t *testing.T) {
	for _, name := range []string{"America/Denver", "UTC", "Pacific/Auckland"} {
		z, err := LoadZone(name)
		if err != nil {
			t.Fatalf("LoadZone(%q): %v", name, err)
		}
		if z.Name() != name {
			t.Fatalf("Name() = %q, want %q", z.Name(), name)
		}
		if z.Location() == nil || z.Location().String() != name {
			t.Fatalf("Location() = %v, want %q", z.Location(), name)
		}
	}
	for _, name := range []string{"", "Local", "UTC0", "MST", "EST5EDT", "America/Nowhere", "../etc/passwd"} {
		if _, err := LoadZone(name); err == nil {
			t.Fatalf("LoadZone(%q) succeeded, want error", name)
		}
	}
}

func TestInstant(t *testing.T) {
	z := mustZone(t, "America/Denver")
	for _, tc := range []struct {
		d    string
		m    Minute
		p    GapPolicy
		want string // RFC 3339 UTC, or "gap"
	}{
		{"2026-10-28", 600, GapReject, "2026-10-28T16:00:00Z"},
		{"2027-03-14", 150, GapForward, "2027-03-14T09:00:00Z"}, // 02:30 does not exist → 03:00 MDT (DUE-04)
		{"2027-03-14", 150, GapReject, "gap"},
		{"2027-03-14", 120, GapReject, "gap"}, // 02:00 (BLK-12)
		{"2027-03-14", 90, GapReject, "2027-03-14T08:30:00Z"},
		{"2027-03-14", 180, GapReject, "2027-03-14T09:00:00Z"},
		{"2026-11-01", 90, GapReject, "2026-11-01T07:30:00Z"}, // ambiguous → earlier (DUE-03, BLK-14)
		{"2026-11-01", 150, GapReject, "2026-11-01T09:30:00Z"},
		{"2026-11-01", 1440, GapReject, "2026-11-02T07:00:00Z"}, // 25-hour day ends
		{"2027-03-14", 780, GapReject, "2027-03-14T19:00:00Z"},  // BLK-16
		{"2026-11-01", 780, GapReject, "2026-11-01T20:00:00Z"},
	} {
		got, err := z.Instant(MustDate(tc.d), tc.m, tc.p)
		if tc.want == "gap" {
			if !errors.Is(err, ErrNonexistent) {
				t.Fatalf("Instant(%s, %s, %d) = %v, %v; want ErrNonexistent", tc.d, tc.m, tc.p, got, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("Instant(%s, %s, %d): %v", tc.d, tc.m, tc.p, err)
		}
		if want := mustInstant(t, tc.want); !got.Equal(want) {
			t.Fatalf("Instant(%s, %s, %d) = %s, want %s", tc.d, tc.m, tc.p, got.UTC().Format(time.RFC3339), tc.want)
		}
	}
}

func TestInstantRejectsOutOfRangeMinute(t *testing.T) {
	z := mustZone(t, "America/Denver")
	for _, m := range []Minute{-1, 1441} {
		for _, p := range []GapPolicy{GapForward, GapReject} {
			_, err := z.Instant(MustDate("2026-10-28"), m, p)
			if err == nil || errors.Is(err, ErrNonexistent) {
				t.Fatalf("Instant(minute %d, %d) err = %v, want a range error", int(m), p, err)
			}
		}
	}
}

func TestStartOfDay(t *testing.T) {
	z := mustZone(t, "America/Denver")
	for _, tc := range []struct{ d, want string }{ // catalog §0 midnight table
		{"2026-10-29", "2026-10-29T06:00:00Z"},
		{"2026-11-01", "2026-11-01T06:00:00Z"},
		{"2026-11-02", "2026-11-02T07:00:00Z"},
		{"2027-03-14", "2027-03-14T07:00:00Z"},
		{"2027-03-15", "2027-03-15T06:00:00Z"},
	} {
		if got := z.StartOfDay(MustDate(tc.d)); !got.Equal(mustInstant(t, tc.want)) {
			t.Fatalf("StartOfDay(%s) = %s, want %s", tc.d, got.UTC().Format(time.RFC3339), tc.want)
		}
	}
}

func TestDAT16(t *testing.T) {
	z := mustZone(t, "America/Denver")
	for _, tc := range []struct{ at, want string }{
		{"2026-11-02T06:30:00Z", "2026-11-01"},
		{"2027-03-15T06:00:00Z", "2027-03-15"},
	} {
		if got := z.DateOf(mustInstant(t, tc.at)); got != MustDate(tc.want) {
			t.Fatalf("DateOf(%s) = %s, want %s", tc.at, got, tc.want)
		}
	}
}

func TestMinuteOf(t *testing.T) {
	z := mustZone(t, "America/Denver")
	for _, tc := range []struct {
		at   string
		want Minute
	}{
		{"2026-11-01T08:15:00Z", 75}, // 01:15 MST, second occurrence
		{"2026-11-01T07:15:00Z", 75}, // 01:15 MDT, first occurrence
		{"2026-10-28T16:00:00Z", 600},
	} {
		if got := z.MinuteOf(mustInstant(t, tc.at)); got != tc.want {
			t.Fatalf("MinuteOf(%s) = %d, want %d", tc.at, got, tc.want)
		}
	}
}

func TestDAT15(t *testing.T) {
	z := mustZone(t, "America/Denver")
	got := z.DeferMoment(MustDate("2026-11-01"), someMinute(90))
	if want := mustInstant(t, "2026-11-01T07:30:00Z"); !got.Equal(want) {
		t.Fatalf("DeferMoment(2026-11-01, 01:30) = %s, want %s (earlier occurrence)", got.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestMoments(t *testing.T) {
	z := mustZone(t, "America/Denver")
	for _, tc := range []struct {
		name string
		got  time.Time
		want string
	}{
		{"defer date-only", z.DeferMoment(MustDate("2026-10-29"), noMinute), "2026-10-29T06:00:00Z"},
		{"due date-only", z.DueMoment(MustDate("2026-10-28"), noMinute), "2026-10-29T06:00:00Z"},
		{"due 17:00", z.DueMoment(MustDate("2026-10-28"), someMinute(17*60)), "2026-10-28T23:00:00Z"},
		{"due date-only on 25-hour day", z.DueMoment(MustDate("2026-11-01"), noMinute), "2026-11-02T07:00:00Z"},
		{"defer in gap (DUE-04)", z.DeferMoment(MustDate("2027-03-14"), someMinute(150)), "2027-03-14T09:00:00Z"},
		{"due in gap", z.DueMoment(MustDate("2027-03-14"), someMinute(150)), "2027-03-14T09:00:00Z"},
	} {
		if want := mustInstant(t, tc.want); !tc.got.Equal(want) {
			t.Fatalf("%s = %s, want %s", tc.name, tc.got.UTC().Format(time.RFC3339), tc.want)
		}
	}
}

func TestInstantAuckland(t *testing.T) {
	z := mustZone(t, "Pacific/Auckland")
	gapDay, overlapDay := MustDate("2026-09-27"), MustDate("2026-04-05")

	for _, tc := range []struct {
		d    Date
		m    Minute
		p    GapPolicy
		want string // RFC 3339 UTC, or "gap"
	}{
		{gapDay, 150, GapForward, "2026-09-26T14:00:00Z"}, // 02:30 does not exist → 03:00 NZDT
		{gapDay, 150, GapReject, "gap"},
		{gapDay, 90, GapReject, "2026-09-26T13:30:00Z"},      // 01:30 NZST
		{gapDay, 210, GapReject, "2026-09-26T14:30:00Z"},     // 03:30 NZDT
		{overlapDay, 150, GapReject, "2026-04-04T13:30:00Z"}, // 02:30 ambiguous → earlier (NZDT)
	} {
		got, err := z.Instant(tc.d, tc.m, tc.p)
		if tc.want == "gap" {
			if !errors.Is(err, ErrNonexistent) {
				t.Fatalf("Instant(%s, %s, %d) = %v, %v; want ErrNonexistent", tc.d, tc.m, tc.p, got, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("Instant(%s, %s, %d): %v", tc.d, tc.m, tc.p, err)
		}
		if want := mustInstant(t, tc.want); !got.Equal(want) {
			t.Fatalf("Instant(%s, %s, %d) = %s, want %s", tc.d, tc.m, tc.p, got.UTC().Format(time.RFC3339), tc.want)
		}
	}

	for _, tc := range []struct{ d, want string }{
		{"2026-09-27", "2026-09-26T12:00:00Z"},
		{"2026-09-28", "2026-09-27T11:00:00Z"},
		{"2026-04-05", "2026-04-04T11:00:00Z"},
		{"2026-04-06", "2026-04-05T12:00:00Z"},
	} {
		if got := z.StartOfDay(MustDate(tc.d)); !got.Equal(mustInstant(t, tc.want)) {
			t.Fatalf("StartOfDay(%s) = %s, want %s", tc.d, got.UTC().Format(time.RFC3339), tc.want)
		}
	}

	at := mustInstant(t, "2026-04-04T14:30:00Z") // 02:30 NZST, second occurrence
	if got := z.DateOf(at); got != overlapDay {
		t.Fatalf("DateOf(%s) = %s, want %s", at.Format(time.RFC3339), got, overlapDay)
	}
	if got := z.MinuteOf(at); got != 150 {
		t.Fatalf("MinuteOf(%s) = %d, want 150", at.Format(time.RFC3339), got)
	}
}

// FuzzInstant checks A§13.3: DateOf(Instant(d, m)) is stable and nothing
// panics across DST, for Denver, Auckland and UTC in years 2000-2100.
func FuzzInstant(f *testing.F) {
	zones := []Zone{mustZone(f, "America/Denver"), mustZone(f, "Pacific/Auckland"), mustZone(f, "UTC")}
	for _, s := range []struct {
		zone uint8
		date string
		m    uint16
	}{
		{0, "2027-03-14", 150}, {0, "2027-03-14", 120}, {0, "2027-03-14", 180}, {0, "2026-11-01", 90},
		{0, "2026-11-01", 1440}, {1, "2026-09-27", 150}, {1, "2026-04-05", 150}, {1, "2026-04-05", 0},
		{2, "2026-10-28", 600}, {0, "2100-12-31", 1440}, {1, "2000-01-01", 0},
	} {
		d, jan1 := MustDate(s.date), MustDate(s.date[:4]+"-01-01")
		y, err := strconv.Atoi(s.date[:4])
		if err != nil {
			f.Fatal(err)
		}
		year, day := uint16(y-2000), uint16(d.DaysSince(jan1))
		f.Add(s.zone, year, day, s.m, false)
		f.Add(s.zone, year, day, s.m, true)
	}
	f.Fuzz(func(t *testing.T, zi uint8, year, day, minute uint16, reject bool) {
		z := zones[int(zi)%len(zones)]
		y := 2000 + int(year)%101
		jan1, err := NewDate(y, time.January, 1)
		if err != nil {
			t.Fatal(err)
		}
		next, err := NewDate(y+1, time.January, 1)
		if err != nil {
			t.Fatal(err)
		}
		d := jan1.AddDays(int(day) % next.DaysSince(jan1))
		m := Minute(int(minute) % 1441)
		p := GapForward
		if reject {
			p = GapReject
		}

		got, err := z.Instant(d, m, p)
		if err != nil {
			if p == GapForward {
				t.Fatalf("%s Instant(%s, %d, GapForward) errored: %v", z.Name(), d, m, err)
			}
			if !errors.Is(err, ErrNonexistent) {
				t.Fatalf("%s Instant(%s, %d, GapReject) err = %v, want ErrNonexistent", z.Name(), d, m, err)
			}
			// In a gap GapForward lands on the first valid instant after the
			// wall time: still on d, after m, and the minute before is before m.
			fwd, err := z.Instant(d, m, GapForward)
			if err != nil {
				t.Fatalf("%s Instant(%s, %d, GapForward) errored: %v", z.Name(), d, m, err)
			}
			before := fwd.Add(-time.Minute)
			if z.DateOf(fwd) != d || z.MinuteOf(fwd) <= m || z.DateOf(before) != d || z.MinuteOf(before) >= m {
				t.Fatalf("%s gap %s %d: GapForward = %s (wall %s %s), minute before wall %s %s",
					z.Name(), d, m, fwd.UTC().Format(time.RFC3339), z.DateOf(fwd), z.MinuteOf(fwd), z.DateOf(before), z.MinuteOf(before))
			}
			return
		}
		want := d
		if m == EndOfDay {
			want = d.AddDays(1)
		}
		if got := z.DateOf(got); got != want {
			t.Fatalf("%s DateOf(Instant(%s, %d, %d)) = %s, want %s", z.Name(), d, m, p, got, want)
		}
		if m < EndOfDay && p == GapReject {
			if gm := z.MinuteOf(got); gm != m {
				t.Fatalf("%s MinuteOf(Instant(%s, %d)) = %d", z.Name(), d, m, gm)
			}
			if fwd, err := z.Instant(d, m, GapForward); err != nil || !fwd.Equal(got) {
				t.Fatalf("%s Instant(%s, %d): GapForward = %v, %v; GapReject = %v", z.Name(), d, m, fwd, err, got)
			}
		}
	})
}
