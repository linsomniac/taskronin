package web

import (
	"net/http"

	"github.com/linsomniac/taskronin/internal/datespec"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/service"
)

type recapPage struct {
	service.DayView
	Linked   []domain.EntryView
	LogEntry service.AddEntryIn
}

type reportPage struct {
	service.Report
	FirstEntries    map[domain.TaskID]domain.EntryID
	IncludeUpcoming bool
}

func (a *app) recap(w http.ResponseWriter, r *http.Request) {
	date, err := a.formDate(r.URL.Query(), "date", datespec.Past)
	if err != nil {
		a.renderError(w, r, err)
		return
	}
	if date.IsZero() {
		date = a.ref().Today
	}
	s, _ := sessionFrom(r.Context())
	view, err := a.o.Svc.Day(r.Context(), s.Actor, date)
	if err != nil {
		a.renderError(w, r, err)
		return
	}
	p := recapPage{DayView: view, LogEntry: service.AddEntryIn{WorkDate: view.Date}}
	// DayView.Unlinked is a subset of Entries; render each entry just once.
	for _, entry := range view.Entries {
		if entry.TaskID.Valid {
			p.Linked = append(p.Linked, entry)
		}
	}
	a.renderPage(w, r, http.StatusOK, "recap", a.newPage(r, "Recap", "recap", p))
}

func (a *app) report(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, err := a.formDate(q, "from", datespec.Past)
	if err != nil {
		a.renderError(w, r, err)
		return
	}
	to, err := a.formDate(q, "to", datespec.Past)
	if err != nil {
		a.renderError(w, r, err)
		return
	}
	ref := a.ref()
	switch {
	case from.IsZero() && to.IsZero():
		from, to = datespec.Week(ref)
	case from.IsZero():
		ref.Today = to
		from, _ = datespec.Week(ref)
	case to.IsZero():
		to = from.AddDays(366 - 1)
		if to.After(ref.Today) {
			to = ref.Today
		}
	}
	s, _ := sessionFrom(r.Context())
	view, err := a.o.Svc.Report(r.Context(), s.Actor, service.ReportIn{
		From: from, To: to, Tag: q.Get("tag"), Project: q.Get("project"),
		Todo: q.Get("todo") == "1", Upcoming: q.Get("upcoming") == "1",
	})
	if err != nil {
		a.renderError(w, r, suppliedRangeError(err, q))
		return
	}
	p := reportPage{Report: view, FirstEntries: make(map[domain.TaskID]domain.EntryID), IncludeUpcoming: q.Get("upcoming") == "1"}
	// AIDEV-NOTE: First task annotations span the whole report, across days.
	// Preserve service order and use its task snapshot without further reads.
	for _, day := range view.Days {
		for _, line := range day.Lines {
			if line.TaskID.Valid {
				if _, seen := p.FirstEntries[line.TaskID.V]; !seen {
					p.FirstEntries[line.TaskID.V] = line.EntryID
				}
			}
		}
	}
	a.renderPage(w, r, http.StatusOK, "report", a.newPage(r, "Report", "report", p))
}
