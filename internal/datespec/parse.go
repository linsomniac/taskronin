// Package datespec resolves relative dates and clock spans against an explicit
// civil-date reference, without reading a clock or performing I/O.
package datespec

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/domain"
)

// Mode selects how weekday names are resolved relative to today.
type Mode uint8

const (
	// Past selects the most recent weekday, including today.
	Past Mode = iota + 1
	// Future selects the next weekday, excluding today.
	Future Mode = iota + 1
)

// Ref supplies the current civil date and configured first day of the week.
type Ref struct {
	Today     civil.Date
	WeekStart time.Weekday
}

// InputError keeps historical problem JSON available to the CLI while
// Error describes an input failure without implementation prefixes.
type InputError struct {
	detail, problemDetail string
	// NamesBlock reports whether the diagnostic itself identifies a block span or endpoint.
	NamesBlock bool
	// BareClock reports that the diagnostic starts with the offending clock value.
	BareClock bool
}

func (e *InputError) Error() string { return e.detail }

// ProblemDetail returns the original detail used in machine-readable errors.
// AIDEV-NOTE: CLI --json depends on this detail even when Error changes.
func (e *InputError) ProblemDetail() string { return e.problemDetail }

// Parse resolves a date with an optional clock, or a bare clock on today.
// Keywords are case-insensitive and surrounding whitespace is ignored.
func Parse(s string, m Mode, r Ref) (domain.Moment, error) {
	fields := strings.Fields(strings.ToLower(s))
	var date, clock string
	switch len(fields) {
	case 1:
		date = fields[0]
		if strings.Contains(date, ":") {
			date, clock = "today", date
		}
	case 2:
		date, clock = fields[0], fields[1]
	default:
		if len(fields) > 2 {
			if _, err := parseDate(fields[0], m, r); err != nil {
				return domain.Moment{}, err
			}
		}
		return domain.Moment{}, fmt.Errorf("invalid date specification %q: expected a date, a clock, or a date followed by a clock", s)
	}
	d, err := parseDate(date, m, r)
	if err != nil {
		return domain.Moment{}, err
	}
	result := domain.Moment{Date: d}
	if clock != "" {
		minute, err := ParseClock(clock, false)
		if err != nil {
			return domain.Moment{}, err
		}
		result.Min = sql.Null[civil.Minute]{V: minute, Valid: true}
	}
	return result, nil
}

func invalidSpan(value, reason string) error {
	detail := fmt.Sprintf("invalid block span %q: %s", value, reason)
	return &InputError{detail: detail, problemDetail: detail, NamesBlock: true}
}

// ParseSpan resolves an optional date followed by start-end clocks, defaulting
// to today. The span must satisfy 0 <= start < end <= 1440, with no spaces
// around the hyphen. Only the end clock may be 24:00.
func ParseSpan(s string, m Mode, r Ref) (domain.BlockSpan, error) {
	fields := strings.Fields(strings.ToLower(s))
	date := "today"
	var clocks string
	switch len(fields) {
	case 1:
		clocks = fields[0]
	case 2:
		date, clocks = fields[0], fields[1]
	default:
		if len(fields) > 2 {
			if _, err := parseDate(fields[0], m, r); err != nil {
				return domain.BlockSpan{}, err
			}
		}
		return domain.BlockSpan{}, invalidSpan(s, "expected [date] start-end without spaces around the hyphen")
	}
	d, err := parseDate(date, m, r)
	if err != nil {
		return domain.BlockSpan{}, err
	}
	startText, endText, ok := strings.Cut(clocks, "-")
	if !ok {
		return domain.BlockSpan{}, invalidSpan(s, "expected start-end clocks")
	}
	start, err := parseClock(startText, false, "start")
	if err != nil {
		return domain.BlockSpan{}, err
	}
	end, err := parseClock(endText, true, "end")
	if err != nil {
		return domain.BlockSpan{}, err
	}
	if start >= end {
		return domain.BlockSpan{}, invalidSpan(s, "end must be after start on the same day")
	}
	return domain.BlockSpan{Date: d, Start: start, End: civil.EndMinute(end)}, nil
}

// ParseClock parses H:MM or HH:MM, trimming surrounding whitespace.
// The clock 24:00 is accepted only when allow2400 is true.
func ParseClock(s string, allow2400 bool) (civil.Minute, error) {
	return parseClock(s, allow2400, "")
}

func parseClock(s string, allow2400 bool, blockField string) (civil.Minute, error) {
	minute, err := civil.ParseHHMM(strings.TrimSpace(s), allow2400)
	var clock *civil.ClockError
	if errors.As(err, &clock) {
		detail, wire := fmt.Sprintf("%q: %s", clock.Value, clock.Reason), clock.Error()
		if blockField != "" {
			detail = "block " + blockField + " " + detail
			wire = "invalid block " + blockField + ": " + wire
		}
		return 0, &InputError{detail: detail, problemDetail: wire, NamesBlock: blockField != "", BareClock: blockField == ""}
	}
	return minute, err
}

var isoDatePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func parseDate(s string, m Mode, r Ref) (civil.Date, error) {
	if m != Past && m != Future {
		return civil.Date{}, fmt.Errorf("invalid date direction %d: expected Past or Future", m)
	}
	var offset int
	switch s {
	case "today":
		offset = 0
	case "yesterday":
		offset = -1
	case "tomorrow":
		offset = 1
	default:
		if day, ok := weekday(s); ok {
			// AIDEV-NOTE: Direction applies only to weekday names. Past includes
			// today; Future excludes it. Service rules validate resolved moments.
			offset = (int(day) - int(r.Today.Weekday()) + 7) % 7
			if m == Past && offset > 0 {
				offset -= 7
			} else if m == Future && offset == 0 {
				offset = 7
			}
		} else if days, ok := relativeDays(s); ok {
			offset = days
		} else {
			if !isoDatePattern.MatchString(s) {
				return civil.Date{}, fmt.Errorf("unrecognised date %q: use today, yesterday, tomorrow, a weekday (mon…sun), +Nd/-Nd, +Nw/-Nw, or YYYY-MM-DD", s)
			}
			d, err := civil.ParseDate(s)
			if err != nil {
				return civil.Date{}, fmt.Errorf("invalid date %q: no such calendar day", s)
			}
			return d, nil
		}
	}
	if r.Today.IsZero() {
		return civil.Date{}, fmt.Errorf("cannot resolve a relative date without a reference date")
	}
	// AIDEV-NOTE: AddDays can cross civil's supported year bounds. Revalidate
	// here so every accepted date can round-trip through the ISO grammar.
	d, err := civil.ParseDate(r.Today.AddDays(offset).String())
	if err != nil {
		detail := fmt.Sprintf("date specification %q resolves outside the supported calendar", s)
		return civil.Date{}, &InputError{detail: detail, problemDetail: detail + ": " + err.Error()}
	}
	return d, nil
}

func weekday(s string) (time.Weekday, bool) {
	switch s {
	case "mon", "monday":
		return time.Monday, true
	case "tue", "tues", "tuesday":
		return time.Tuesday, true
	case "wed", "weds", "wednesday":
		return time.Wednesday, true
	case "thu", "thur", "thurs", "thursday":
		return time.Thursday, true
	case "fri", "friday":
		return time.Friday, true
	case "sat", "saturday":
		return time.Saturday, true
	case "sun", "sunday":
		return time.Sunday, true
	default:
		return 0, false
	}
}

func relativeDays(s string) (int, bool) {
	if len(s) < 3 || len(s) > 5 || (s[0] != '+' && s[0] != '-') {
		return 0, false
	}
	unit := s[len(s)-1]
	if unit != 'd' && unit != 'w' {
		return 0, false
	}
	days := 0
	for i := 1; i < len(s)-1; i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		days = days*10 + int(s[i]-'0')
	}
	if unit == 'w' {
		days *= 7
	}
	if s[0] == '-' {
		days = -days
	}
	return days, true
}
