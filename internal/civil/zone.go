package civil

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// GapPolicy says how Zone.Instant resolves a wall time that a DST gap skips.
type GapPolicy uint8

const (
	// GapForward resolves a nonexistent wall time to the first valid instant
	// after it, i.e. the transition instant (defer and due moments).
	GapForward GapPolicy = iota
	// GapReject answers ErrNonexistent for a nonexistent wall time (blocks).
	GapReject
)

// ErrNonexistent reports a wall time that a DST gap skips in the zone.
var ErrNonexistent = errors.New("civil: nonexistent wall time")

// Zone is a loaded IANA time zone that maps civil dates and minutes to
// instants. The zero value is invalid; use LoadZone.
type Zone struct {
	name string
	loc  *time.Location
}

// LoadZone loads an IANA zone such as "America/Denver", or "UTC". It rejects
// "", "Local" and abbreviation or POSIX-style names such as "MST" or
// "EST5EDT", so the server zone never depends on the host's local setting.
func LoadZone(name string) (Zone, error) {
	if name != "UTC" && !strings.Contains(name, "/") {
		return Zone{}, fmt.Errorf("civil: time zone %q is not an IANA name such as America/Denver", name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return Zone{}, fmt.Errorf("civil: load time zone %q: %w", name, err)
	}
	return Zone{name: name, loc: loc}, nil
}

// Name returns the IANA name the zone was loaded with.
func (z Zone) Name() string { return z.name }

// Location returns the zone's *time.Location, for rendering instants with
// the zone's offset.
func (z Zone) Location() *time.Location { return z.loc }

// DateOf returns the civil date of instant t in the zone ("today").
func (z Zone) DateOf(t time.Time) Date {
	y, m, d := t.In(z.loc).Date()
	return Date{y: int16(y), m: uint8(m), d: uint8(d)} //nolint:gosec // G115: calendar values are small
}

// MinuteOf returns the wall-clock minute of instant t in the zone. Both
// occurrences of a repeated (fall-back) wall time give the same minute.
func (z Zone) MinuteOf(t time.Time) Minute {
	h, m, _ := t.In(z.loc).Clock()
	return Minute(h*60 + m) //nolint:gosec // G115: at most 1439
}

// StartOfDay returns the instant d begins: d 00:00, or the first valid
// instant after it when a DST gap skips midnight.
func (z Zone) StartOfDay(d Date) time.Time {
	return z.forward(d, 0)
}

// Instant returns the instant of wall time m on date d in the zone, in UTC.
// m == EndOfDay (24:00) is StartOfDay(d+1). A repeated (fall-back) wall time
// resolves to the earlier instant. A skipped (spring-forward) wall time
// resolves per p: GapForward gives the transition instant, GapReject an error
// wrapping ErrNonexistent. m outside 0..1440 is an error.
func (z Zone) Instant(d Date, m Minute, p GapPolicy) (time.Time, error) {
	if m < 0 || m > EndOfDay {
		return time.Time{}, fmt.Errorf("civil: minute %d out of range", int(m))
	}
	if m == EndOfDay {
		return z.StartOfDay(d.AddDays(1)), nil
	}

	// AIDEV-NOTE: DST correctness lives here (A§2.2, review ruling P13). The
	// candidates come from the zone's actual transitions, never from
	// time.Date, whose choice in gaps and overlaps is unspecified (it lands
	// before the gap in Denver and after it in Auckland). An instant t has
	// wall time `wall` exactly when t + offset(t) == wall, so the candidates
	// are wall - o for the offsets o of the zone periods before, containing
	// and after the first guess q, and each is kept only if offset() confirms
	// it. Covered by TestInstant*, TestStartOfDay, TestMoments, FuzzInstant.
	// AIDEV-NOTE: ZoneBounds is only a hint. Past a zone's last explicit
	// transition (2037 in Go's tzdata) the stdlib returns synthetic period
	// bounds at Jan 1 00:00 UTC, and in leap years an end of Dec 31 00:00 UTC
	// that can precede t (time/zoneinfo.go tzset: ystart + 365 days). Zone()
	// offsets stay exact, which is why every candidate is checked with
	// offset(). Brute-forced against every zone in Go 1.26.8's tzdata
	// (years 1000-2200 plus spot ranges to 9999): no mismatch.
	wall := d.utc().Add(time.Duration(m) * time.Minute) // (d, m) as if it were UTC
	q := wall.Add(-z.offset(wall))
	start, end := q.In(z.loc).ZoneBounds() // zero values: no transition on that side
	offsets := []time.Duration{z.offset(q)}
	if !start.IsZero() {
		offsets = append(offsets, z.offset(start.Add(-time.Nanosecond)))
	}
	if !end.IsZero() {
		offsets = append(offsets, z.offset(end))
	}

	var best time.Time
	found := false
	for _, o := range offsets {
		c := wall.Add(-o)
		if z.offset(c) == o && (!found || c.Before(best)) {
			best, found = c, true
		}
	}
	if found {
		return best.UTC(), nil
	}

	// No candidate keeps its wall time: (d, m) falls in a DST gap.
	if p == GapReject {
		return time.Time{}, fmt.Errorf("%w: %s %s in %s", ErrNonexistent, d, m, z.name)
	}
	for _, tr := range []time.Time{start, end} {
		if !tr.IsZero() && z.wallOf(tr.Add(-time.Nanosecond)).Before(wall) && wall.Before(z.wallOf(tr)) {
			return tr.UTC(), nil
		}
	}
	// AIDEV-NOTE: unreachable with Go 1.26.8's tzdata (brute-forced, see
	// above): q lies within one gap length of the gap's transition, so that
	// transition is the start or end of q's period. Only a zone with
	// transitions less than about one UTC offset (<= 14h) apart could land
	// here; it is reported as an error rather than guessed.
	return time.Time{}, fmt.Errorf("%w: %s %s in %s: no transition found", ErrNonexistent, d, m, z.name)
}

// DeferMoment returns the instant a defer to (d, m) takes effect: StartOfDay(d)
// when m is null, else wall time m on d with GapForward. m.V must be
// 0..1439; anything else is a caller bug and panics.
func (z Zone) DeferMoment(d Date, m sql.Null[Minute]) time.Time {
	if !m.Valid {
		return z.StartOfDay(d)
	}
	return z.forward(d, m.V)
}

// DueMoment returns the instant a due (d, m) falls due: StartOfDay(d+1)
// ("24:00") when m is null, else wall time m on d with GapForward. m.V must
// be 0..1439; anything else is a caller bug and panics.
func (z Zone) DueMoment(d Date, m sql.Null[Minute]) time.Time {
	if !m.Valid {
		return z.StartOfDay(d.AddDays(1))
	}
	return z.forward(d, m.V)
}

// forward is Instant with GapForward for callers whose minute is already
// validated.
func (z Zone) forward(d Date, m Minute) time.Time {
	t, err := z.Instant(d, m, GapForward)
	if err != nil {
		// AIDEV-NOTE: GapForward fails only for a minute outside 0..1440,
		// which validated inputs (ParseHHMM, Scan, UnmarshalText) never hold,
		// or in the unreachable no-transition case in Instant. Both are bugs,
		// so this panics instead of returning a wrong moment.
		panic(err)
	}
	return t
}

// offset returns the zone's UTC offset in effect at instant t.
func (z Zone) offset(t time.Time) time.Duration {
	_, sec := t.In(z.loc).Zone()
	return time.Duration(sec) * time.Second
}

// wallOf returns t's wall clock in the zone written as if it were UTC.
func (z Zone) wallOf(t time.Time) time.Time {
	return t.Add(z.offset(t)).UTC()
}
