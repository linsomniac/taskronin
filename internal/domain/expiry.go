package domain

import (
	"database/sql"
	"errors"
	"regexp"
	"strconv"
	"time"

	"github.com/linsomniac/taskronin/internal/civil"
)

// maxTokenExpiry bounds a relative expiry so n*unit cannot overflow time.Duration.
const maxTokenExpiry = 100 * 365 * 24 * time.Hour

var tokenExpiryDuration = regexp.MustCompile(`^([0-9]{1,6})([hd])$`)

// ParseTokenExpiry parses the token expiry grammar shared by `tkr token create
// --expires` and `tkr init --mcp-token`: "never" (no expiry, an invalid
// sql.Null), a whole number of hours or days ("12h", "30d"; 1h to 100 years
// after now), or a date YYYY-MM-DD, which expires at 00:00 of that day in
// zone. The expiry must be after now. Errors are user-facing predicates
// ("must be ...") for the caller to prefix with the flag or prompt name.
//
// AIDEV-NOTE: final review B-I3. One grammar for both commands: init used to
// accept Go durations (720h, 1h30m) but not the "30d" that token create
// suggests. Keep the two call sites on this function.
func ParseTokenExpiry(s string, now time.Time, zone civil.Zone) (sql.Null[time.Time], error) {
	if s == "never" {
		return sql.Null[time.Time]{}, nil
	}
	var at time.Time
	if m := tokenExpiryDuration.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1]) // at most six digits
		unit := time.Hour
		if m[2] == "d" {
			unit = 24 * time.Hour
		}
		if n == 0 || time.Duration(n) > maxTokenExpiry/unit {
			return sql.Null[time.Time]{}, errors.New("duration must be between 1h and 100 years")
		}
		at = now.Add(time.Duration(n) * unit)
	} else if date, err := civil.ParseDate(s); err == nil {
		at = zone.StartOfDay(date)
	} else {
		return sql.Null[time.Time]{}, errors.New("must be a duration like 12h or 30d, a date like 2026-12-31, or never")
	}
	if !at.After(now) {
		return sql.Null[time.Time]{}, errors.New("must be in the future")
	}
	return sql.Null[time.Time]{V: at, Valid: true}, nil
}
