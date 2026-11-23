package service

import (
	"context"
	"database/sql"
	"time"

	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/store"
)

// ReportIn selects the work date range, task filters and optional current views.
type ReportIn struct {
	From, To       civil.Date
	Tag, Project   string
	Todo, Upcoming bool
}

// ReportLine is a live work or completion entry within a report day.
type ReportLine struct {
	EntryID  domain.EntryID
	Kind     domain.EntryKind
	Title    string
	Notes    string
	TaskID   sql.Null[domain.TaskID]
	Time     sql.Null[civil.Minute]
	Reopened bool
}

// ReportDay groups entries by work date in display order.
type ReportDay struct {
	Date  civil.Date
	Lines []ReportLine
}

// ReportTask supplies the current task annotation for historical entries.
type ReportTask struct {
	Title   string
	Status  domain.TaskStatus
	Deleted bool
}

// ReportTodo contains current triage and backlog tasks in their view order.
type ReportTodo struct{ Triage, Backlog []domain.TaskView }

// Report contains ascending nonempty days and optional current task sections.
type Report struct {
	From, To     civil.Date
	Tag, Project string
	Days         []ReportDay
	Tasks        map[domain.TaskID]ReportTask
	Todo         *ReportTodo
	Upcoming     []domain.TaskView
}

// Report reads work history, reconciling first only when current views are requested.
func (s *Service) Report(ctx context.Context, a domain.Actor, in ReportIn) (Report, error) {
	if err := validateDateRange(in.From, in.To, false); err != nil {
		return Report{}, err
	}
	if err := validateFilters(in.Tag, in.Project); err != nil {
		return Report{}, err
	}
	r := Report{From: in.From, To: in.To, Tag: in.Tag, Project: in.Project,
		Days: make([]ReportDay, 0), Tasks: make(map[domain.TaskID]ReportTask)}
	err := s.read(ctx, a, domain.ScopeRead, in.Todo || in.Upcoming, func(q store.Q, now time.Time, today civil.Date) error {
		entries, err := store.ReportEntries(ctx, q, in.From, in.To, in.Tag, in.Project)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.TaskID.Valid {
				if _, ok := r.Tasks[e.TaskID.V]; !ok {
					// AIDEV-NOTE: Task tombstones do not hide live history. GetTask
					// includes them and supplies the scrubbed title after a purge.
					t, err := store.GetTask(ctx, q, e.TaskID.V)
					if err != nil {
						return err
					}
					r.Tasks[t.ID] = ReportTask{Title: t.Title, Status: t.Status, Deleted: t.DeletedAt.Valid}
				}
			}
			if len(r.Days) == 0 || r.Days[len(r.Days)-1].Date != e.WorkDate {
				r.Days = append(r.Days, ReportDay{Date: e.WorkDate})
			}
			day := &r.Days[len(r.Days)-1]
			day.Lines = append(day.Lines, ReportLine{EntryID: e.ID, Kind: e.Kind, Title: e.Title,
				Notes: e.Notes, TaskID: e.TaskID, Time: e.OccurredMin, Reopened: e.Reopened})
		}
		// AIDEV-NOTE: Reuse the ListTasks/Upcoming queries and view builders
		// inside this read transaction so every report section shares a snapshot.
		if in.Todo {
			r.Todo = &ReportTodo{}
			r.Todo.Triage, err = s.reportTaskViews(ctx, q, in, domain.ViewTriage, now, today)
			if err != nil {
				return err
			}
			r.Todo.Backlog, err = s.reportTaskViews(ctx, q, in, domain.ViewBacklog, now, today)
			if err != nil {
				return err
			}
		}
		if in.Upcoming {
			r.Upcoming, err = s.reportTaskViews(ctx, q, in, domain.ViewUpcoming, now, today)
			if err != nil {
				return err
			}
			tasks, err := store.ListTasks(ctx, q, store.TaskQuery{View: domain.ViewAll, Tag: in.Tag, Project: in.Project, Limit: -1})
			if err != nil {
				return err
			}
			// dueViews excludes deferred tasks, so this append cannot duplicate them.
			_, soon, err := s.dueViews(ctx, q, tasks, now, today)
			if err != nil {
				return err
			}
			r.Upcoming = append(r.Upcoming, soon...)
		}
		return nil
	})
	if err != nil {
		return Report{}, err
	}
	return r, nil
}

func (s *Service) reportTaskViews(ctx context.Context, q store.Q, in ReportIn, view domain.View, now time.Time, today civil.Date) ([]domain.TaskView, error) {
	tasks, err := store.ListTasks(ctx, q, store.TaskQuery{View: view, Tag: in.Tag, Project: in.Project, Limit: -1})
	if err != nil {
		return nil, err
	}
	return s.taskViews(ctx, q, tasks, now, today)
}
