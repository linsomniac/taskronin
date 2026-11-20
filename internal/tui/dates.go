package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/datespec"
)

func (a *App) ref() datespec.Ref {
	return datespec.Ref{Today: a.clock.Today, WeekStart: weekdayOf(a.clock.WeekStart)}
}

func (a *App) reasonSince(since api.Time) string {
	if since.Format(time.DateOnly) == a.clock.Today.String() {
		return since.Format("15:04")
	}
	return since.Format("Mon 2006-01-02")
}

func weekdayOf(s string) time.Weekday {
	switch s {
	case "sunday":
		return time.Sunday
	case "tuesday":
		return time.Tuesday
	case "wednesday":
		return time.Wednesday
	case "thursday":
		return time.Thursday
	case "friday":
		return time.Friday
	case "saturday":
		return time.Saturday
	default:
		return time.Monday
	}
}

func (a *App) parseMoment(s string, mode datespec.Mode) (api.Moment, error) {
	m, err := datespec.Parse(s, mode, a.ref())
	if err != nil {
		return api.Moment{}, err
	}
	r := api.Moment{Date: m.Date}
	if m.Min.Valid {
		r.Time = &m.Min.V
	}
	return r, nil
}

func (a *App) parseSpan(s string) (api.BlockRequest, error) {
	span, err := datespec.ParseSpan(s, datespec.Future, a.ref())
	if err != nil {
		return api.BlockRequest{}, err
	}
	return api.BlockRequest{Date: span.Date, Start: span.Start, End: span.End}, nil
}

func (a *App) parseDate(s string, mode datespec.Mode) (civil.Date, error) {
	m, err := a.parseMoment(s, mode)
	if err != nil {
		return civil.Date{}, err
	}
	if m.Time != nil {
		return civil.Date{}, fmt.Errorf("expected a date without a time")
	}
	return m.Date, nil
}

func parseTaskRef(s string) (id int64, none bool, err error) {
	s = strings.TrimSpace(s)
	if s == "-" {
		return 0, true, nil
	}
	text := strings.TrimPrefix(s, "#")
	for _, r := range text {
		if r < '0' || r > '9' {
			return 0, false, fmt.Errorf("expected a task ID, #ID, or -")
		}
	}
	id, err = strconv.ParseInt(text, 10, 64)
	if err != nil || id <= 0 {
		return 0, false, fmt.Errorf("expected a positive task ID, #ID, or -")
	}
	return id, false, nil
}
