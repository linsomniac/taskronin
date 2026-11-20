package cli

import (
	"taskronin/internal/api"
	"taskronin/internal/civil"
)

func formatDate(date civil.Date) string {
	return date.Weekday().String()[:3] + " " + date.String()
}

func formatMoment(moment *api.Moment) string {
	return withTime(formatDate(moment.Date), moment.Time)
}

func withTime(day string, minute *civil.Minute) string {
	if minute == nil {
		return day
	}
	return day + " " + minute.String()
}

func (e *env) dayWord(d civil.Date) string {
	// AIDEV-NOTE: clock caches both success and failure. A write pays for at
	// most one extra local read and can still print the absolute date on error.
	clock, err := e.clock()
	if err == nil {
		switch d.DaysSince(clock.Today) {
		case 0:
			return "today"
		case -1:
			return "yesterday"
		case 1:
			return "tomorrow"
		}
	}
	return formatDate(d)
}

func (e *env) momentWord(m *api.Moment) string {
	return withTime(e.dayWord(m.Date), m.Time)
}

func (e *env) relDate(text string) string {
	const dateLen = len("YYYY-MM-DD")
	if len(text) < dateLen {
		return text
	}
	d, err := civil.ParseDate(text[:dateLen])
	if err != nil {
		return text
	}
	return e.dayWord(d) + text[dateLen:]
}
