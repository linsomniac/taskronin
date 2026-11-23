package datespec

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/linsomniac/taskronin/internal/civil"
)

var ref = Ref{Today: civil.MustDate("2026-10-28"), WeekStart: time.Monday}

func checkDates(t *testing.T, mode Mode, cases map[string]string) {
	t.Helper()
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			m, err := Parse(in, mode, ref)
			if err != nil || m.Date.String() != want || m.Min.Valid {
				t.Fatalf("Parse(%q) = %v, %v; want %s without a minute", in, m, err, want)
			}
		})
	}
}

func TestDAT01(t *testing.T) {
	checkDates(t, Past, map[string]string{
		"today": "2026-10-28", "yesterday": "2026-10-27", "-2d": "2026-10-26", "2026-10-01": "2026-10-01",
	})
}

func TestDAT02(t *testing.T) {
	checkDates(t, Past, map[string]string{
		"wed": "2026-10-28", "tue": "2026-10-27", "thu": "2026-10-22", "monday": "2026-10-26",
		"Mon": "2026-10-26", "tues": "2026-10-27", "weds": "2026-10-28", "thur": "2026-10-22", "thurs": "2026-10-22",
	})
}

func TestDAT04(t *testing.T) {
	checkDates(t, Future, map[string]string{
		"tomorrow": "2026-10-29", "thu": "2026-10-29", "wed": "2026-11-04", "mon": "2026-11-02",
		"+3d": "2026-10-31", "+2w": "2026-11-11",
	})
}

func TestDAT06(t *testing.T) {
	m, err := Parse("15:00", Future, ref)
	if err != nil || m.Date.String() != "2026-10-28" || !m.Min.Valid || m.Min.V != 900 {
		t.Fatalf("Parse bare clock = %v, %v; want today at minute 900", m, err)
	}
}

func TestDAT07(t *testing.T) {
	for _, tc := range []struct {
		in, date string
		minute   civil.Minute
	}{
		{"mon 09:00", "2026-11-02", 540},
		{"2026-11-03 14:00", "2026-11-03", 840},
		{"+3d 09:00", "2026-10-31", 540},
	} {
		t.Run(tc.in, func(t *testing.T) {
			m, err := Parse(tc.in, Future, ref)
			if err != nil || m.Date.String() != tc.date || !m.Min.Valid || m.Min.V != tc.minute {
				t.Fatalf("Parse(%q) = %v, %v; want %s at minute %d", tc.in, m, err, tc.date, tc.minute)
			}
		})
	}
}

func TestDAT08(t *testing.T) {
	checkDates(t, Future, map[string]string{
		"today": "2026-10-28", "fri": "2026-10-30", "wed": "2026-11-04", "2027-03-14": "2027-03-14",
	})
}

func TestDAT09(t *testing.T) {
	// The service owns rejection of past block dates; resolution preserves them.
	checkDates(t, Future, map[string]string{
		"today": "2026-10-28", "fri": "2026-10-30", "wed": "2026-11-04", "yesterday": "2026-10-27",
	})
}

func TestDAT10(t *testing.T) {
	minute, err := ParseClock("14:30", false)
	if err != nil || minute != 870 {
		t.Fatalf("ParseClock = %v, %v; want minute 870", minute, err)
	}
	for _, in := range []string{"24:00", "9:5"} {
		if _, err := ParseClock(in, false); err == nil {
			t.Errorf("ParseClock(%q) succeeded", in)
		}
	}
}

func TestDAT12(t *testing.T) {
	for _, in := range []string{"2026-13-01", "2026-2-3", "nextweek", "+3x"} {
		for _, mode := range []Mode{Past, Future} {
			if _, err := Parse(in, mode, ref); err == nil {
				t.Errorf("Parse(%q, %d) succeeded", in, mode)
			}
		}
	}
}

func TestUnrecognisedDateMessages(t *testing.T) {
	const help = ": use today, yesterday, tomorrow, a weekday (mon…sun), +Nd/-Nd, +Nw/-Nw, or YYYY-MM-DD"
	for _, tc := range []struct{ in, want string }{
		{"next monday", `unrecognised date "next"` + help},
		{"next monday 13:00", `unrecognised date "next"` + help},
		{"nxt", `unrecognised date "nxt"` + help},
		{"2026-13-45", `invalid date "2026-13-45": no such calendar day`},
		{"2026-02-29", `invalid date "2026-02-29": no such calendar day`},
		{"2026-2-03", `unrecognised date "2026-2-03"` + help},
		{"13:00 tomorrow", `unrecognised date "13:00"` + help},
		{"today 13:00 extra", `invalid date specification "today 13:00 extra": expected a date, a clock, or a date followed by a clock`},
	} {
		for _, mode := range []Mode{Past, Future} {
			t.Run(fmt.Sprintf("%s/%d", tc.in, mode), func(t *testing.T) {
				_, err := Parse(tc.in, mode, ref)
				if err == nil || err.Error() != tc.want {
					t.Fatalf("Parse(%q) error = %v; want %s", tc.in, err, tc.want)
				}
				if strings.Contains(err.Error(), "civil:") {
					t.Fatalf("internal package leaked: %v", err)
				}
			})
		}
	}
}

func TestParseSpanClockErrors(t *testing.T) {
	for input, want := range map[string]string{
		"25:00-26:00": `block start "25:00": hour out of range`,
		"09:00-24:01": `block end "24:01": hour out of range`,
		"09:00-09:60": `block end "09:60": minutes out of range`,
		"9:5-10:00":   `block start "9:5": expected HH:MM`,
	} {
		if _, err := ParseSpan(input, Future, ref); err == nil || err.Error() != want {
			t.Errorf("ParseSpan(%q) error = %v, want %q", input, err, want)
		}
	}
}

func TestUnrecognisedSpanDateMessages(t *testing.T) {
	const help = ": use today, yesterday, tomorrow, a weekday (mon…sun), +Nd/-Nd, +Nw/-Nw, or YYYY-MM-DD"
	for _, tc := range []struct{ in, want string }{
		{"next monday", `unrecognised date "next"` + help},
		{"next monday 13:00-14:00", `unrecognised date "next"` + help},
		{"nxt 13:00-14:00", `unrecognised date "nxt"` + help},
		{"2026-13-45 13:00-14:00", `invalid date "2026-13-45": no such calendar day`},
		{"13:00 tomorrow", `unrecognised date "13:00"` + help},
		{"today 13:00 - 14:00", `invalid block span "today 13:00 - 14:00": expected [date] start-end without spaces around the hyphen`},
	} {
		t.Run(tc.in, func(t *testing.T) {
			_, err := ParseSpan(tc.in, Future, ref)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("ParseSpan(%q) error = %v; want %s", tc.in, err, tc.want)
			}
			if strings.Contains(err.Error(), "civil:") {
				t.Fatalf("internal package leaked: %v", err)
			}
		})
	}
}

func TestDAT13(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fn       func(Ref) (civil.Date, civil.Date)
		start    time.Weekday
		from, to string
	}{
		{"week", Week, time.Monday, "2026-10-26", "2026-11-01"},
		{"last week", LastWeek, time.Monday, "2026-10-19", "2026-10-25"},
		{"month", Month, time.Monday, "2026-10-01", "2026-10-31"},
		{"Sunday week", Week, time.Sunday, "2026-10-25", "2026-10-31"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			from, to := tc.fn(Ref{Today: ref.Today, WeekStart: tc.start})
			if from.String() != tc.from || to.String() != tc.to {
				t.Fatalf("range = %s..%s; want %s..%s", from, to, tc.from, tc.to)
			}
		})
	}
}

func TestParseSpan(t *testing.T) {
	for _, tc := range []struct {
		in, date string
		mode     Mode
		start    civil.Minute
		end      civil.EndMinute
	}{
		{"13:00-15:00", "2026-10-28", Future, 780, 900},
		{"fri 09:00-10:00", "2026-10-30", Future, 540, 600},
		{"23:00-24:00", "2026-10-28", Future, 1380, 1440},
		{"+0d 13:00-15:00", "2026-10-28", Future, 780, 900},
		{"2026-11-03 00:00-24:00", "2026-11-03", Past, 0, 1440},
		{"  WEDS   9:00-10:00  ", "2026-10-28", Past, 540, 600},
		{"wed 9:00-10:00", "2026-11-04", Future, 540, 600},
		{"yesterday 9:00-10:00", "2026-10-27", Future, 540, 600},
		{"-2d 0:00-0:01", "2026-10-26", Past, 0, 1},
	} {
		t.Run(tc.in, func(t *testing.T) {
			b, err := ParseSpan(tc.in, tc.mode, ref)
			if err != nil || b.Date.String() != tc.date || b.Start != tc.start || b.End != tc.end {
				t.Fatalf("ParseSpan(%q) = %v, %v; want %s %d..%d", tc.in, b, err, tc.date, tc.start, tc.end)
			}
		})
	}
	for _, in := range []string{
		"15:00-13:00", "13:00-13:00", "24:00-25:00", "23:00-01:00", "24:00-24:00",
		"13:00 -15:00", "13:00- 15:00", "fri 13:00 - 15:00", "13:00--15:00", "13:00-15:00-16:00",
		"today", "13:00", "today 13:00", "13:00-", "-15:00", "", "today 00:00-24:01",
		"nextweek 13:00-15:00", "2026-13-01 13:00-15:00", "+1000d 13:00-15:00", "today 9:5-10:00",
	} {
		t.Run("invalid/"+in, func(t *testing.T) {
			if _, err := ParseSpan(in, Future, ref); err == nil {
				t.Fatalf("ParseSpan(%q) succeeded", in)
			}
		})
	}
	checkDates(t, Future, map[string]string{"+0d": "2026-10-28"})
}

func TestWeekdaySpellings(t *testing.T) {
	for _, tc := range []struct{ names, past, future string }{
		{"mon monday", "2026-10-26", "2026-11-02"},
		{"tue tues tuesday", "2026-10-27", "2026-11-03"},
		{"wed weds wednesday", "2026-10-28", "2026-11-04"},
		{"thu thur thurs thursday", "2026-10-22", "2026-10-29"},
		{"fri friday", "2026-10-23", "2026-10-30"},
		{"sat saturday", "2026-10-24", "2026-10-31"},
		{"sun sunday", "2026-10-25", "2026-11-01"},
	} {
		for _, name := range strings.Fields(tc.names) {
			checkDates(t, Past, map[string]string{strings.ToUpper(name): tc.past})
			checkDates(t, Future, map[string]string{name: tc.future})
		}
	}
}

func TestParseGrammar(t *testing.T) {
	for _, mode := range []Mode{Past, Future} {
		for _, tc := range []struct{ in, want string }{
			{"  ToDaY  ", "2026-10-28"}, {" TOMORROW ", "2026-10-29"},
			{"today   0:00", "2026-10-28 00:00"}, {" 9:05 ", "2026-10-28 09:05"},
			{"2024-02-29 23:59", "2024-02-29 23:59"}, {"-2W", "2026-10-14"},
			{"+001d", "2026-10-29"}, {"-000w", "2026-10-28"},
			{"+999d", "2029-07-23"}, {"-999d", "2024-02-02"},
			{"+999w", "2045-12-20"}, {"-999w", "2007-09-05"},
		} {
			t.Run(tc.in, func(t *testing.T) {
				m, err := Parse(tc.in, mode, ref)
				if err != nil || m.String() != tc.want {
					t.Fatalf("Parse(%q, %d) = %v, %v; want %s", tc.in, mode, m, err, tc.want)
				}
			})
		}
		for _, in := range []string{
			"", " ", "next monday", "today tomorrow", "today 09:00 extra", "today09:00",
			"+d", "3d", "+1000d", "-1000w", "+1.5d", "++1d", "+-1d", "+ 3d", "+３d",
			"2026-02-29", "0999-12-31", "10000-01-01", "24:00", "today 24:00", "today 9:5",
			"13:00-15:00", "today\x00", "\xff", "today 09:00 10:00",
		} {
			if _, err := Parse(in, mode, ref); err == nil {
				t.Errorf("Parse(%q, %d) succeeded", in, mode)
			}
		}
	}
}

func TestParseClock(t *testing.T) {
	for _, tc := range []struct {
		in    string
		allow bool
		want  civil.Minute
	}{
		{"0:00", false, 0}, {"9:05", false, 545}, {"23:59", false, 1439},
		{"24:00", true, 1440}, {" 14:30 ", false, 870},
	} {
		got, err := ParseClock(tc.in, tc.allow)
		if err != nil || got != tc.want {
			t.Errorf("ParseClock(%q, %t) = %v, %v; want %d", tc.in, tc.allow, got, err, tc.want)
		}
	}
	for _, in := range []string{"", "9:5", "24:01", "25:00", "12:60", "009:00", "-1:00", "12 :00", "12: 00", "１２:00", "today 12:00"} {
		if _, err := ParseClock(in, true); err == nil {
			t.Errorf("ParseClock(%q) succeeded", in)
		}
	}
}

func TestParseDateBounds(t *testing.T) {
	for _, tc := range []struct{ today, in, want string }{
		{"2024-02-28", "+1d", "2024-02-29"},
		{"2026-12-31", "+1d", "2027-01-01"},
		{"1000-01-01", "today", "1000-01-01"},
		{"9999-12-31", "today", "9999-12-31"},
		{"1000-01-01", "yesterday", ""},
		{"9999-12-31", "tomorrow", ""},
		{"1000-01-01", "-999w", ""},
		{"9999-12-31", "+999w", ""},
	} {
		r := Ref{Today: civil.MustDate(tc.today), WeekStart: time.Monday}
		m, err := Parse(tc.in, Future, r)
		if tc.want == "" {
			if err == nil {
				t.Errorf("Parse(%q) from %s accepted out-of-range date %v", tc.in, tc.today, m)
			}
			if _, err := ParseSpan(tc.in+" 09:00-10:00", Future, r); err == nil {
				t.Errorf("ParseSpan from %s accepted out-of-range date", tc.today)
			}
		} else if err != nil || m.Date.String() != tc.want {
			t.Errorf("Parse(%q) from %s = %v, %v; want %s", tc.in, tc.today, m, err, tc.want)
		}
	}
}

func TestReportRangeBoundaries(t *testing.T) {
	for _, tc := range []struct {
		today    string
		start    time.Weekday
		fn       func(Ref) (civil.Date, civil.Date)
		from, to string
	}{
		{"2026-10-26", time.Monday, Week, "2026-10-26", "2026-11-01"},
		{"2026-11-01", time.Monday, Week, "2026-10-26", "2026-11-01"},
		{"2026-10-28", time.Wednesday, Week, "2026-10-28", "2026-11-03"},
		{"2026-10-28", time.Thursday, Week, "2026-10-22", "2026-10-28"},
		{"2027-01-01", time.Monday, LastWeek, "2026-12-21", "2026-12-27"},
		{"2026-10-28", time.Sunday, LastWeek, "2026-10-18", "2026-10-24"},
		{"2024-02-29", time.Monday, Month, "2024-02-01", "2024-02-29"},
		{"2026-02-01", time.Monday, Month, "2026-02-01", "2026-02-28"},
		{"2026-04-30", time.Monday, Month, "2026-04-01", "2026-04-30"},
		{"2026-12-31", time.Monday, Month, "2026-12-01", "2026-12-31"},
		{"1000-01-01", time.Monday, Month, "1000-01-01", "1000-01-31"},
		{"9999-12-31", time.Monday, Month, "9999-12-01", "9999-12-31"},
	} {
		r := Ref{Today: civil.MustDate(tc.today), WeekStart: tc.start}
		from, to := tc.fn(r)
		if from.String() != tc.from || to.String() != tc.to {
			t.Errorf("range for %s = %s..%s; want %s..%s", tc.today, from, to, tc.from, tc.to)
		}
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{"today", "yesterday", "tomorrow", "Mon", "weds", "+0d", "+999w", "-999w", "2026-11-03", "15:00", " mon   9:00 ", "today 24:00", "+1000d", "", "\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, mode := range []Mode{Past, Future} {
			m, err := Parse(s, mode, ref)
			if err != nil {
				continue
			}
			if m.Date.IsZero() || (m.Min.Valid && (m.Min.V < 0 || m.Min.V >= civil.EndOfDay)) {
				t.Fatalf("Parse(%q) accepted invalid moment %v", s, m)
			}
			got, err := Parse(m.String(), mode, ref)
			if err != nil || got != m {
				t.Fatalf("Parse(%q) = %v; round trip = %v, %v", s, m, got, err)
			}
		}
	})
}

func FuzzParseSpan(f *testing.F) {
	for _, s := range []string{"13:00-15:00", "fri 09:00-10:00", "23:00-24:00", "+0d 0:00-24:00", "-2w 9:00-10:00", "2026-11-03 14:00-15:00", "15:00-13:00", "13:00-13:00", "13:00 - 15:00", "", "\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, mode := range []Mode{Past, Future} {
			b, err := ParseSpan(s, mode, ref)
			if err != nil {
				continue
			}
			if b.Date.IsZero() || b.Start < 0 || b.Start >= civil.EndOfDay || b.End <= civil.EndMinute(b.Start) || b.End > civil.EndMinute(civil.EndOfDay) {
				t.Fatalf("ParseSpan(%q) accepted invalid span %v", s, b)
			}
			got, err := ParseSpan(b.String(), mode, ref)
			if err != nil || got != b {
				t.Fatalf("ParseSpan(%q) = %v; round trip = %v, %v", s, b, got, err)
			}
		}
	})
}
