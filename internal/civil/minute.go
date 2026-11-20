package civil

import (
	"database/sql/driver"
	"fmt"
)

// Minute is minutes since local midnight, 0..1439 (EndOfDay is the 1440
// sentinel used for "24:00").
type Minute int16

// EndOfDay is the exclusive end of a civil day, "24:00".
const EndOfDay Minute = 1440

// EndMinute is a block end, 1..1440. Its text form allows "24:00"; it is a
// distinct type from Minute (0..1439) so wire schemas and validation differ.
type EndMinute Minute

// ClockError identifies the input and the failed HH:MM check. Error preserves
// the existing diagnostic for configuration files and machine-readable errors.
type ClockError struct {
	Value, Reason string
}

func (e *ClockError) Error() string {
	if e.Reason == "expected HH:MM" {
		return fmt.Sprintf("civil: %q is not HH:MM", e.Value)
	}
	return fmt.Sprintf("civil: %q has %s", e.Value, e.Reason)
}

// ParseHHMM parses "H:MM" or "HH:MM". "24:00" is accepted only when
// allow2400 is true.
func ParseHHMM(s string, allow2400 bool) (Minute, error) {
	var h, m int
	switch {
	case len(s) == 4 && s[1] == ':' && isDigits(s[0:1]) && isDigits(s[2:4]):
		h, m = int(s[0]-'0'), int(s[2]-'0')*10+int(s[3]-'0')
	case len(s) == 5 && s[2] == ':' && isDigits(s[0:2]) && isDigits(s[3:5]):
		h, m = int(s[0]-'0')*10+int(s[1]-'0'), int(s[3]-'0')*10+int(s[4]-'0')
	default:
		return 0, &ClockError{Value: s, Reason: "expected HH:MM"}
	}
	if m > 59 {
		return 0, &ClockError{Value: s, Reason: "minutes out of range"}
	}
	if h == 24 && m == 0 && allow2400 {
		return EndOfDay, nil
	}
	if h > 23 {
		return 0, &ClockError{Value: s, Reason: "hour out of range"}
	}
	return Minute(h*60 + m), nil //nolint:gosec // G115: at most 1439
}

func formatHHMM(m int) string { return fmt.Sprintf("%02d:%02d", m/60, m%60) }

// String returns the canonical "HH:MM" form.
func (m Minute) String() string { return formatHHMM(int(m)) }

// MarshalText encodes m as "HH:MM".
func (m Minute) MarshalText() ([]byte, error) {
	if m < 0 || m > 1439 {
		return nil, fmt.Errorf("civil: minute %d out of range", int(m))
	}
	return []byte(m.String()), nil
}

// UnmarshalText decodes "H:MM" / "HH:MM" (00:00-23:59).
func (m *Minute) UnmarshalText(b []byte) error {
	v, err := ParseHHMM(string(b), false)
	if err != nil {
		return err
	}
	*m = v
	return nil
}

// Value implements driver.Valuer as int64 minutes.
func (m Minute) Value() (driver.Value, error) { return int64(m), nil }

// Scan implements sql.Scanner from int64.
func (m *Minute) Scan(src any) error {
	v, ok := src.(int64)
	if !ok {
		return fmt.Errorf("civil: cannot scan %T into Minute", src)
	}
	if v < 0 || v > 1439 {
		return fmt.Errorf("civil: minute %d out of range", v)
	}
	*m = Minute(v)
	return nil
}

// String returns "HH:MM", with 1440 rendered as "24:00".
func (e EndMinute) String() string { return formatHHMM(int(e)) }

// MarshalText encodes e as "HH:MM" ("24:00" for 1440).
func (e EndMinute) MarshalText() ([]byte, error) {
	if e < 1 || e > 1440 {
		return nil, fmt.Errorf("civil: end minute %d out of range", int(e))
	}
	return []byte(e.String()), nil
}

// UnmarshalText decodes "HH:MM" in 00:01-24:00.
func (e *EndMinute) UnmarshalText(b []byte) error {
	v, err := ParseHHMM(string(b), true)
	if err != nil {
		return err
	}
	if v < 1 {
		return fmt.Errorf("civil: end minute %q must be after 00:00", b)
	}
	*e = EndMinute(v)
	return nil
}

// Value implements driver.Valuer as int64 minutes.
func (e EndMinute) Value() (driver.Value, error) { return int64(e), nil }

// Scan implements sql.Scanner from int64 (1..1440).
func (e *EndMinute) Scan(src any) error {
	v, ok := src.(int64)
	if !ok {
		return fmt.Errorf("civil: cannot scan %T into EndMinute", src)
	}
	if v < 1 || v > 1440 {
		return fmt.Errorf("civil: end minute %d out of range", v)
	}
	*e = EndMinute(v)
	return nil
}
