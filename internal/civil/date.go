// Package civil holds calendar dates and minutes-of-day, independent of any
// time zone.
package civil

import (
	"database/sql/driver"
	"fmt"
	"time"
)

// Date is a calendar date (year 1000-9999). The zero value is invalid;
// optional dates use sql.Null[Date].
type Date struct {
	y int16
	m uint8
	d uint8
}

const (
	minYear = 1000
	maxYear = 9999
)

// NewDate returns the date y-m-d, rejecting years outside 1000-9999 and
// nonexistent calendar dates.
func NewDate(y int, m time.Month, d int) (Date, error) {
	if y < minYear || y > maxYear {
		return Date{}, fmt.Errorf("civil: year %d out of range", y)
	}
	if m < time.January || m > time.December || d < 1 {
		return Date{}, fmt.Errorf("civil: invalid date %04d-%02d-%02d", y, int(m), d)
	}
	// time.Date normalizes overflow, so compare the round trip.
	t := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	if t.Year() != y || t.Month() != m || t.Day() != d {
		return Date{}, fmt.Errorf("civil: invalid date %04d-%02d-%02d", y, int(m), d)
	}
	return Date{y: int16(y), m: uint8(m), d: uint8(d)}, nil //nolint:gosec // G115: ranges validated above
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ParseDate parses a strict "YYYY-MM-DD" real calendar date.
func ParseDate(s string) (Date, error) {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' ||
		!isDigits(s[0:4]) || !isDigits(s[5:7]) || !isDigits(s[8:10]) {
		return Date{}, fmt.Errorf("civil: %q is not YYYY-MM-DD", s)
	}
	y := int(s[0]-'0')*1000 + int(s[1]-'0')*100 + int(s[2]-'0')*10 + int(s[3]-'0')
	m := int(s[5]-'0')*10 + int(s[6]-'0')
	d := int(s[8]-'0')*10 + int(s[9]-'0')
	return NewDate(y, time.Month(m), d)
}

// MustDate is ParseDate that panics; for tests only.
func MustDate(s string) Date {
	d, err := ParseDate(s)
	if err != nil {
		panic(err)
	}
	return d
}

// String returns "YYYY-MM-DD" (or "0000-00-00" for the zero value).
func (d Date) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", d.y, d.m, d.d)
}

// IsZero reports whether d is the invalid zero value.
func (d Date) IsZero() bool { return d == Date{} }

func (d Date) utc() time.Time {
	return time.Date(int(d.y), time.Month(d.m), int(d.d), 0, 0, 0, 0, time.UTC)
}

// AddDays returns the date n days after d (n may be negative).
func (d Date) AddDays(n int) Date {
	t := d.utc().AddDate(0, 0, n)
	return Date{y: int16(t.Year()), m: uint8(t.Month()), d: uint8(t.Day())} //nolint:gosec // G115: calendar values are small
}

// DaysSince returns the number of days from o to d (positive when d is later).
func (d Date) DaysSince(o Date) int {
	return int(d.utc().Sub(o.utc()) / (24 * time.Hour))
}

// Weekday returns the day of the week.
func (d Date) Weekday() time.Weekday { return d.utc().Weekday() }

// Compare returns -1, 0 or +1 as d is before, equal to, or after o.
func (d Date) Compare(o Date) int {
	switch {
	case d.y != o.y:
		return cmpInt(int(d.y), int(o.y))
	case d.m != o.m:
		return cmpInt(int(d.m), int(o.m))
	default:
		return cmpInt(int(d.d), int(o.d))
	}
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Before reports whether d is earlier than o.
func (d Date) Before(o Date) bool { return d.Compare(o) < 0 }

// After reports whether d is later than o.
func (d Date) After(o Date) bool { return d.Compare(o) > 0 }

// MarshalText encodes d as "YYYY-MM-DD"; the zero value is an error.
func (d Date) MarshalText() ([]byte, error) {
	if d.IsZero() {
		return nil, fmt.Errorf("civil: cannot marshal zero Date")
	}
	return []byte(d.String()), nil
}

// UnmarshalText decodes a strict "YYYY-MM-DD".
func (d *Date) UnmarshalText(b []byte) error {
	v, err := ParseDate(string(b))
	if err != nil {
		return err
	}
	*d = v
	return nil
}

// Value implements driver.Valuer, storing the "YYYY-MM-DD" text.
func (d Date) Value() (driver.Value, error) {
	if d.IsZero() {
		return nil, fmt.Errorf("civil: cannot store zero Date")
	}
	return d.String(), nil
}

// Scan implements sql.Scanner from string or []byte.
func (d *Date) Scan(src any) error {
	switch v := src.(type) {
	case string:
		return d.UnmarshalText([]byte(v))
	case []byte:
		return d.UnmarshalText(v)
	default:
		return fmt.Errorf("civil: cannot scan %T into Date", src)
	}
}
