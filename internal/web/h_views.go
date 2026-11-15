package web

import (
	"database/sql"
	"net/http"

	"taskronin/internal/civil"
	"taskronin/internal/datespec"
	"taskronin/internal/domain"
	"taskronin/internal/service"
)

type todayPage struct {
	service.TodayView
	LogEntry service.AddEntryIn
}

type taskListRow struct {
	domain.TaskView
	Prev, Next domain.TaskID
}

type taskListPage struct {
	Items     []taskListRow
	AcceptNew []domain.TaskView
	More      string
}

type entryDay struct {
	Date    civil.Date
	Entries []domain.EntryView
}

type logPage struct {
	From, To       civil.Date
	IncludeDeleted bool
	Days           []entryDay
	More           string
	LogEntry       service.AddEntryIn
}

func (a *app) today(w http.ResponseWriter, r *http.Request) {
	s, _ := sessionFrom(r.Context())
	view, err := a.o.Svc.Today(r.Context(), s.Actor)
	if err != nil {
		a.renderError(w, r, err)
		return
	}
	p := todayPage{TodayView: view}
	if view.Focus != nil {
		p.LogEntry.TaskID = sql.Null[domain.TaskID]{V: view.Focus.ID, Valid: true}
	} else if view.CurrentBlock != nil {
		p.LogEntry.TaskID = sql.Null[domain.TaskID]{V: view.CurrentBlock.TaskID, Valid: true}
	}
	a.renderPage(w, r, http.StatusOK, "today", a.newPage(r, "Today", "today", p))
}

func (a *app) triage(w http.ResponseWriter, r *http.Request) {
	a.taskList(w, r, domain.ViewTriage, "Triage")
}

func (a *app) backlog(w http.ResponseWriter, r *http.Request) {
	a.taskList(w, r, domain.ViewBacklog, "Backlog")
}

func (a *app) taskList(w http.ResponseWriter, r *http.Request, view domain.View, title string) {
	s, _ := sessionFrom(r.Context())
	q := r.URL.Query()
	list, err := a.o.Svc.ListTasks(r.Context(), s.Actor, service.ListTasksIn{
		View: view, Tag: q.Get("tag"), Project: q.Get("project"), Limit: 100, Cursor: q.Get("cursor"),
	})
	if err != nil {
		a.renderError(w, r, err)
		return
	}
	p := taskListPage{}
	for i, task := range list.Items {
		row := taskListRow{TaskView: task}
		// Neighbours belong to this rendered page; moving never needs a second read.
		if i > 0 {
			row.Prev = list.Items[i-1].ID
		}
		if i+1 < len(list.Items) {
			row.Next = list.Items[i+1].ID
		}
		p.Items = append(p.Items, row)
		if len(task.Reasons) == 1 && task.Reasons[0].Reason == domain.ReasonNew {
			p.AcceptNew = append(p.AcceptNew, task)
		}
	}
	if list.NextCursor != "" {
		p.More = moreURL(r.URL, list.NextCursor)
	}
	a.renderPage(w, r, http.StatusOK, string(view), a.newPage(r, title, string(view), p))
}

func (a *app) upcoming(w http.ResponseWriter, r *http.Request) {
	s, _ := sessionFrom(r.Context())
	view, err := a.o.Svc.Upcoming(r.Context(), s.Actor)
	if err != nil {
		a.renderError(w, r, err)
		return
	}
	a.renderPage(w, r, http.StatusOK, "upcoming", a.newPage(r, "Upcoming", "upcoming", view))
}

func (a *app) log(w http.ResponseWriter, r *http.Request) {
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
	today := a.o.Zone.DateOf(a.o.Clock.Now())
	if from.IsZero() {
		from = today.AddDays(-13)
	}
	if to.IsZero() {
		to = today
	}
	s, _ := sessionFrom(r.Context())
	p := logPage{From: from, To: to, IncludeDeleted: q.Get("include_deleted") == "1"}
	list, err := a.o.Svc.ListEntries(r.Context(), s.Actor, service.ListEntriesIn{
		From: from, To: to, IncludeDeleted: p.IncludeDeleted, Limit: 100, Cursor: q.Get("cursor"),
	})
	if err != nil {
		a.renderError(w, r, err)
		return
	}
	for _, entry := range list.Items {
		if len(p.Days) == 0 || p.Days[len(p.Days)-1].Date != entry.WorkDate {
			p.Days = append(p.Days, entryDay{Date: entry.WorkDate})
		}
		day := &p.Days[len(p.Days)-1]
		day.Entries = append(day.Entries, entry)
	}
	if list.NextCursor != "" {
		p.More = moreURL(r.URL, list.NextCursor)
	}
	a.renderPage(w, r, http.StatusOK, "log", a.newPage(r, "Log", "log", p))
}
