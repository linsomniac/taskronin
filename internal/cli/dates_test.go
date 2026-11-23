package cli

import (
	"errors"
	"testing"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/civil"
)

func TestDayWord(t *testing.T) {
	srv, _ := captureServer(t)
	e := &env{ctx: t.Context(), apiClient: srv.Client}
	newRoot(e)
	for date, want := range map[string]string{
		"2026-10-28": "today",
		"2026-10-27": "yesterday",
		"2026-10-29": "tomorrow",
		"2026-10-30": "Fri 2026-10-30",
	} {
		if got := e.dayWord(civil.MustDate(date)); got != want {
			t.Errorf("dayWord(%q) = %q, want %q", date, got, want)
		}
	}
}

func TestDayWordWithoutServer(t *testing.T) {
	e := &env{clockLoaded: true, clockErr: errors.New("down")}
	if got := e.dayWord(civil.MustDate("2026-10-28")); got != "Wed 2026-10-28" {
		t.Fatal(got)
	}
}

func TestDayWordNilClock(t *testing.T) {
	e := &env{clockLoaded: true}
	if got := e.dayWord(civil.MustDate("2026-10-28")); got != "Wed 2026-10-28" {
		t.Fatal(got)
	}
}

func TestMomentWord(t *testing.T) {
	e := &env{clockLoaded: true, serverClock: &api.Clock{Today: civil.MustDate("2026-10-28")}}
	m := &api.Moment{Date: civil.MustDate("2026-10-29")}
	if got := e.momentWord(m); got != "tomorrow" {
		t.Fatal(got)
	}
	nine := civil.Minute(9 * 60)
	m.Time = &nine
	if got := e.momentWord(m); got != "tomorrow 09:00" {
		t.Fatal(got)
	}
}

func TestRelDate(t *testing.T) {
	e := &env{clockLoaded: true, serverClock: &api.Clock{Today: civil.MustDate("2026-10-28")}}
	for input, want := range map[string]string{
		"2026-10-29 09:00":       "tomorrow 09:00",
		"2026-10-28 13:00-15:00": "today 13:00-15:00",
		"2026-10-30":             "Fri 2026-10-30",
		"2026-10-281":            "2026-10-281",
		"task_closed":            "task_closed",
		"13:00 → 14:00":          "13:00 → 14:00",
		"2026-02-30 09:00":       "2026-02-30 09:00",
		"Review 2026-10-28":      "Review 2026-10-28",
		"":                       "",
	} {
		if got := e.relDate(input); got != want {
			t.Errorf("relDate(%q) = %q, want %q", input, got, want)
		}
	}
}
