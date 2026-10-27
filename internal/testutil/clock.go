// Package testutil supplies deterministic fixtures for Taskronin tests.
// Production packages must not import it.
package testutil

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"taskronin/internal/civil"
	"taskronin/internal/clock"
	"taskronin/internal/domain"
)

// DefaultNow is the catalog's default local instant.
const DefaultNow = "2026-10-28 10:00"

// Zone is the catalog's America/Denver civil time zone.
var Zone = mustZone()

func mustZone() civil.Zone {
	z, err := civil.LoadZone("America/Denver")
	if err != nil {
		panic(err)
	}
	return z
}

// Local parses a catalog wall time or UTC ISO instant; wall-time gaps move forward.
func Local(t testing.TB, s string) time.Time {
	t.Helper()
	if strings.HasSuffix(s, "Z") {
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04Z"} {
			if instant, err := time.Parse(layout, s); err == nil {
				return instant
			}
		}
		t.Fatalf("invalid UTC instant %q", s)
	}
	date, wall, ok := strings.Cut(s, " ")
	if !ok {
		t.Fatalf("invalid local time %q", s)
	}
	if len(date) == 5 {
		date = "2026-" + date
	}
	d, err := civil.ParseDate(date)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := time.Parse("15:04:05", wall)
	if err != nil {
		parsed, err = time.Parse("15:04", wall)
	}
	if err != nil {
		t.Fatal(err)
	}
	m, err := civil.ParseHHMM(parsed.Format("15:04"), false)
	if err != nil {
		t.Fatal(err)
	}
	instant, err := Zone.Instant(d, m, civil.GapForward)
	if err != nil {
		t.Fatal(err)
	}
	return instant.Add(time.Duration(parsed.Second()) * time.Second)
}

// D parses an ISO date or a month-day shorthand in 2026, panicking on bad fixtures.
func D(s string) civil.Date {
	if len(s) == 5 {
		s = "2026-" + s
	}
	return civil.MustDate(s)
}

// Moment parses a catalog date with an optional HH:MM minute.
func Moment(s string) domain.Moment {
	s = strings.ReplaceAll(s, "T", " ")
	d, minute, hasMinute := strings.Cut(s, " ")
	m := domain.Moment{Date: D(d)}
	if hasMinute {
		v, err := civil.ParseHHMM(minute, false)
		if err != nil {
			panic(fmt.Errorf("testutil: moment %q: %w", s, err))
		}
		m.Min = sql.Null[civil.Minute]{V: v, Valid: true}
	}
	return m
}

// Clock constructs a fake clock at a catalog instant.
func Clock(t testing.TB, local string) *clock.Fake { t.Helper(); return clock.NewFake(Local(t, local)) }
