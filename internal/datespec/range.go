package datespec

import (
	"time"

	"taskronin/internal/civil"
)

// Week returns the inclusive full week containing r.Today, starting on
// r.WeekStart. The reference must contain a valid date and weekday.
func Week(r Ref) (from, to civil.Date) {
	offset := (int(r.Today.Weekday()) - int(r.WeekStart) + 7) % 7
	from = r.Today.AddDays(-offset)
	return from, from.AddDays(6)
}

// LastWeek returns the inclusive full week immediately before Week(r).
func LastWeek(r Ref) (from, to civil.Date) {
	from, to = Week(r)
	return from.AddDays(-7), to.AddDays(-7)
}

// Month returns the inclusive full calendar month containing r.Today.
// A zero reference date returns zero dates.
func Month(r Ref) (from, to civil.Date) {
	today, err := time.Parse(time.DateOnly, r.Today.String())
	if err != nil {
		return civil.Date{}, civil.Date{}
	}
	from = r.Today.AddDays(1 - today.Day())
	days := time.Date(today.Year(), today.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	return from, from.AddDays(days - 1)
}
