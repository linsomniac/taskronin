package service

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/store"
	"github.com/linsomniac/taskronin/internal/textsafe"
)

// GetTask returns a task, including a tombstone, and its non-deleted related records.
func (s *Service) GetTask(ctx context.Context, a domain.Actor, id domain.TaskID) (domain.TaskDetail, error) {
	var detail domain.TaskDetail
	err := s.read(ctx, a, domain.ScopeRead, false, func(q store.Q, now time.Time, today civil.Date) error {
		t, err := store.GetTask(ctx, q, id)
		if errors.Is(err, store.ErrNotFound) {
			return domain.Errorf(domain.CodeNotFound, "task #%d not found", id)
		}
		if err != nil {
			return err
		}
		detail.TaskView, err = s.taskView(ctx, q, t, now, today)
		if err != nil {
			return err
		}
		entries, err := store.EntriesOfTask(ctx, q, id)
		if err != nil {
			return err
		}
		detail.Entries = make([]domain.EntryView, 0, len(entries))
		for _, e := range entries {
			v, err := s.entryView(ctx, q, e)
			if err != nil {
				return err
			}
			detail.Entries = append(detail.Entries, v)
		}
		blocks, err := store.BlocksOfTask(ctx, q, id)
		if err != nil {
			return err
		}
		detail.Blocks = make([]domain.BlockView, 0, len(blocks))
		for _, b := range blocks {
			v, err := s.blockView(ctx, q, b, now)
			if err != nil {
				return err
			}
			detail.Blocks = append(detail.Blocks, v)
		}
		detail.Interruptions, err = store.InterruptionsOfTask(ctx, q, id)
		return err
	})
	if err != nil {
		return domain.TaskDetail{}, err
	}
	return detail, nil
}

func (s *Service) taskView(ctx context.Context, q store.Q, t domain.Task, now time.Time, today civil.Date) (domain.TaskView, error) {
	v := domain.TaskView{Task: t}
	var err error
	v.Reasons, err = store.Reasons(ctx, q, t.ID)
	if err != nil {
		return v, err
	}
	v.Tags, err = store.TaskTags(ctx, q, t.ID)
	if err != nil {
		return v, err
	}
	v.Deferred = t.DeferDate.Valid && s.zone.DeferMoment(t.DeferDate.V, t.DeferMin).After(now)
	switch {
	case t.DeletedAt.Valid:
		v.View = domain.ViewDeleted
	case t.Status == domain.StatusDone:
		v.View = domain.ViewDone
	case t.Status == domain.StatusDropped:
		v.View = domain.ViewDropped
	case v.Deferred:
		v.View = domain.ViewUpcoming
	case len(v.Reasons) > 0:
		v.View = domain.ViewTriage
	default:
		v.View = domain.ViewBacklog
	}
	v.DuePhase = duePhase(t, now, today, s.zone)
	v.DueSoon = t.DueDate.Valid && !t.DueDate.V.After(today.AddDays(s.dueSoonDays))
	// AIDEV-NOTE: Acknowledgement uses op sequences, never wall-clock times;
	// a clock rollback must not change which slips count since the last placement.
	var placed int64
	if t.LastPlacedSeq.Valid {
		placed = int64(t.LastPlacedSeq.V)
	}
	v.SlipsLifetime, v.SlipsSinceTriage, err = store.SlipCounts(ctx, q, t.ID, placed)
	return v, err
}

func (s *Service) entryView(ctx context.Context, q store.Q, e domain.LogEntry) (domain.EntryView, error) {
	v := domain.EntryView{LogEntry: e}
	if e.TaskID.Valid {
		t, err := store.GetTask(ctx, q, e.TaskID.V)
		if err != nil {
			return v, err
		}
		v.TaskTitle = t.Title
	}
	return v, nil
}

func (s *Service) blockView(ctx context.Context, q store.Q, b domain.TimeBlock, now time.Time) (domain.BlockView, error) {
	v := domain.BlockView{TimeBlock: b}
	t, err := store.GetTask(ctx, q, b.TaskID)
	if err != nil {
		return v, err
	}
	v.TaskTitle, v.TaskVersion = t.Title, t.Version
	// AIDEV-NOTE: Compare instants, including the earlier DST fold and
	// GapForward behavior; wall-minute comparisons misclassify repeated hours.
	start, err := s.zone.Instant(b.Span.Date, b.Span.Start, civil.GapForward)
	if err != nil {
		return v, err
	}
	end, err := s.zone.Instant(b.Span.Date, civil.Minute(b.Span.End), civil.GapForward)
	if err != nil {
		return v, err
	}
	v.Started, v.Ended = !start.After(now), !end.After(now)
	v.Current = b.Status == domain.BlockPlanned && v.Started && !v.Ended
	if b.Status == domain.BlockPlanned && v.Ended {
		work, complete, err := store.CountEntriesOn(ctx, q, b.TaskID, b.Span.Date)
		if err != nil {
			return v, err
		}
		v.NeedsReblock = work == 0 && complete == 0
	}
	return v, nil
}

// ClockView supplies the server's civil-time reference and database generation.
type ClockView struct {
	Now         time.Time
	Today       civil.Date
	Zone        string
	WeekStart   time.Weekday
	DueSoonDays int
	Generation  domain.Generation
}

// WhoAmIView identifies the authenticated caller and current database generation.
type WhoAmIView struct {
	Actor      domain.Actor
	Generation domain.Generation
}

// Banner is a dashboard notice with a stable machine-readable kind.
type Banner struct{ Kind, Message string }

// BackupHealth describes the last backup and whether it is recent and successful.
type BackupHealth struct {
	LastSuccessAt sql.Null[time.Time]
	LastError     string
	Healthy       bool
}

// TodayView collects the current day's planning, work and service notices.
type TodayView struct {
	Clock         ClockView
	Focus         *domain.TaskView
	CurrentBlock  *domain.BlockView
	Blocks        []domain.BlockView
	TriageCount   int
	TriageTop     []domain.TaskView
	Overdue       []domain.TaskView
	DueSoon       []domain.TaskView
	Entries       []domain.EntryView
	Interruptions []domain.Interruption
	AIChanges     int
	Backup        BackupHealth
	Banners       []Banner
}

// UpcomingView contains future deferrals and non-deferred tasks due soon.
type UpcomingView struct{ Deferred, DueSoon []domain.TaskView }

// StatusTask is a compact task reference for the status line.
type StatusTask struct {
	ID    domain.TaskID
	Title string
}

// StatusBlock is a compact reference to the current block and its end time.
type StatusBlock struct {
	ID     domain.BlockID
	TaskID domain.TaskID
	Title  string
	End    civil.EndMinute
}

// StatusView contains the task, counts and warnings shown in a status line.
type StatusView struct {
	Focus                    *StatusTask
	CurrentBlock             *StatusBlock
	Triage, Due, AI          int
	BackupWarn, PurgeWarning bool
}

// DayView contains a day's blocks and live entries, including its standalone subset.
type DayView struct {
	Date              civil.Date
	Blocks            []domain.BlockView
	Entries, Unlinked []domain.EntryView
	Interruptions     []domain.Interruption
}

// ListTasksIn selects a task classification, optional filters and a keyset page.
type ListTasksIn struct {
	View         domain.View
	Tag, Project string
	Limit        int
	Cursor       string
}

// ListEntriesIn selects entries by date and task, with optional tombstones and paging.
type ListEntriesIn struct {
	From, To       civil.Date
	TaskID         *sql.Null[domain.TaskID]
	IncludeDeleted bool
	Limit          int
	Cursor         string
}

// ListBlocksIn selects live blocks by date and optionally by task.
type ListBlocksIn struct {
	From, To civil.Date
	TaskID   sql.Null[domain.TaskID]
}

// TagCount counts the open, non-deleted tasks carrying a tag.
type TagCount struct {
	Name  string
	Count int
}

// SettingsView contains the configurable backup retention counts.
type SettingsView struct{ KeepDaily, KeepWeekly int }

func (s *Service) clockView(now time.Time, today civil.Date) ClockView {
	return ClockView{Now: now.In(s.zone.Location()), Today: today, Zone: s.zone.Name(), WeekStart: s.weekStart, DueSoonDays: s.dueSoonDays, Generation: s.gen}
}

// Clock returns the time reference for any authenticated actor, regardless of scopes.
func (s *Service) Clock(ctx context.Context, a domain.Actor) (ClockView, error) {
	var v ClockView
	err := s.read(ctx, a, 0, false, func(_ store.Q, now time.Time, today civil.Date) error {
		v = s.clockView(now, today)
		return nil
	})
	return v, err
}

// WhoAmI returns the supplied authenticated identity, regardless of scopes.
func (s *Service) WhoAmI(ctx context.Context, a domain.Actor) (WhoAmIView, error) {
	var v WhoAmIView
	err := s.read(ctx, a, 0, false, func(_ store.Q, _ time.Time, _ civil.Date) error {
		v = WhoAmIView{Actor: a, Generation: s.gen}
		return nil
	})
	return v, err
}

// Today reconciles time-dependent state and reads a consistent dashboard snapshot.
func (s *Service) Today(ctx context.Context, a domain.Actor) (TodayView, error) {
	var v TodayView
	err := s.read(ctx, a, domain.ScopeRead, true, func(q store.Q, now time.Time, today civil.Date) error {
		v.Clock = s.clockView(now, today)
		var err error
		v.Focus, err = s.focusView(ctx, q, now, today)
		if err != nil {
			return err
		}
		v.CurrentBlock, err = s.currentBlockView(ctx, q, now)
		if err != nil {
			return err
		}
		triage, err := store.ListTasks(ctx, q, store.TaskQuery{View: domain.ViewTriage, Limit: -1})
		if err != nil {
			return err
		}
		v.TriageCount = len(triage)
		v.TriageTop, err = s.taskViews(ctx, q, triage[:min(5, len(triage))], now, today)
		if err != nil {
			return err
		}
		tasks, err := store.ListTasks(ctx, q, store.TaskQuery{View: domain.ViewAll, Limit: -1})
		if err != nil {
			return err
		}
		v.Overdue, v.DueSoon, err = s.dueViews(ctx, q, tasks, now, today)
		if err != nil {
			return err
		}
		day, err := s.dayView(ctx, q, today, now)
		if err != nil {
			return err
		}
		// AIDEV-NOTE: Deleted tasks leave historical blocks, entries and interruptions intact.
		// LIF-09 hides them from Today; Day and entry lists retain that history.
		live := make(map[domain.TaskID]bool, len(tasks))
		for _, t := range tasks {
			live[t.ID] = true
		}
		v.Blocks = slices.DeleteFunc(day.Blocks, func(b domain.BlockView) bool { return !live[b.TaskID] })
		v.Entries = slices.DeleteFunc(day.Entries, func(e domain.EntryView) bool { return e.TaskID.Valid && !live[e.TaskID.V] })
		v.Interruptions = slices.DeleteFunc(day.Interruptions, func(in domain.Interruption) bool {
			return !live[in.ToTaskID] || (in.FromTaskID.Valid && !live[in.FromTaskID.V])
		})
		v.Backup, v.AIChanges, v.Banners, err = s.readWarnings(ctx, q, now)
		return err
	})
	if err != nil {
		return TodayView{}, err
	}
	return v, nil
}

// Upcoming returns reconciled deferred tasks and non-overdue tasks due soon.
func (s *Service) Upcoming(ctx context.Context, a domain.Actor) (UpcomingView, error) {
	var v UpcomingView
	err := s.read(ctx, a, domain.ScopeRead, true, func(q store.Q, now time.Time, today civil.Date) error {
		tasks, err := store.ListTasks(ctx, q, store.TaskQuery{View: domain.ViewUpcoming, Limit: -1})
		if err != nil {
			return err
		}
		v.Deferred, err = s.taskViews(ctx, q, tasks, now, today)
		if err != nil {
			return err
		}
		tasks, err = store.ListTasks(ctx, q, store.TaskQuery{View: domain.ViewAll, Limit: -1})
		if err != nil {
			return err
		}
		_, v.DueSoon, err = s.dueViews(ctx, q, tasks, now, today)
		return err
	})
	if err != nil {
		return UpcomingView{}, err
	}
	return v, nil
}

// Status reconciles state and reads compact focus, due, triage and warning data.
func (s *Service) Status(ctx context.Context, a domain.Actor) (StatusView, error) {
	var v StatusView
	err := s.read(ctx, a, domain.ScopeRead, true, func(q store.Q, now time.Time, today civil.Date) error {
		focus, err := s.focusView(ctx, q, now, today)
		if err != nil {
			return err
		}
		if focus != nil {
			v.Focus = &StatusTask{ID: focus.ID, Title: focus.Title}
		}
		block, err := s.currentBlockView(ctx, q, now)
		if err != nil {
			return err
		}
		if block != nil {
			v.CurrentBlock = &StatusBlock{ID: block.ID, TaskID: block.TaskID, Title: block.TaskTitle, End: block.Span.End}
		}
		triage, err := store.ListTasks(ctx, q, store.TaskQuery{View: domain.ViewTriage, Limit: -1})
		if err != nil {
			return err
		}
		v.Triage = len(triage)
		tasks, err := store.ListTasks(ctx, q, store.TaskQuery{View: domain.ViewAll, Limit: -1})
		if err != nil {
			return err
		}
		for _, t := range tasks {
			phase := duePhase(t, now, today, s.zone)
			if t.Status == domain.StatusOpen && (phase == domain.DueDue || phase == domain.DueOverdue) {
				v.Due++
			}
		}
		health, ai, banners, err := s.readWarnings(ctx, q, now)
		if err != nil {
			return err
		}
		v.BackupWarn, v.AI = !health.Healthy, ai
		for _, b := range banners {
			if b.Kind == "purge_incomplete" || b.Kind == "purge_ledger_damaged" {
				v.PurgeWarning = true
			}
		}
		return nil
	})
	if err != nil {
		return StatusView{}, err
	}
	return v, nil
}

func (s *Service) focusView(ctx context.Context, q store.Q, now time.Time, today civil.Date) (*domain.TaskView, error) {
	f, err := store.GetFocus(ctx, q)
	if err != nil || !f.TaskID.Valid {
		return nil, err
	}
	t, err := store.GetTask(ctx, q, f.TaskID.V)
	if err != nil {
		return nil, err
	}
	v, err := s.taskView(ctx, q, t, now, today)
	return &v, err
}

func (s *Service) currentBlockView(ctx context.Context, q store.Q, now time.Time) (*domain.BlockView, error) {
	b, err := currentBlockAt(ctx, q, s.zone, now)
	if err != nil || !b.Valid {
		return nil, err
	}
	v, err := s.blockView(ctx, q, b.V, now)
	return &v, err
}

func (s *Service) taskViews(ctx context.Context, q store.Q, tasks []domain.Task, now time.Time, today civil.Date) ([]domain.TaskView, error) {
	vs := make([]domain.TaskView, 0, len(tasks))
	for _, t := range tasks {
		v, err := s.taskView(ctx, q, t, now, today)
		if err != nil {
			return nil, err
		}
		vs = append(vs, v)
	}
	return vs, nil
}

func (s *Service) dueViews(ctx context.Context, q store.Q, tasks []domain.Task, now time.Time, today civil.Date) (overdue, soon []domain.TaskView, err error) {
	overdue, soon = make([]domain.TaskView, 0), make([]domain.TaskView, 0)
	for _, t := range tasks {
		if t.Status != domain.StatusOpen || t.DeletedAt.Valid || !t.DueDate.Valid || t.DueDate.V.After(today.AddDays(s.dueSoonDays)) {
			continue
		}
		v, e := s.taskView(ctx, q, t, now, today)
		if e != nil {
			return nil, nil, e
		}
		if v.Deferred {
			continue
		}
		if v.DuePhase == domain.DueOverdue {
			overdue = append(overdue, v)
		} else if v.DueSoon {
			soon = append(soon, v)
		}
	}
	byDue := func(a, b domain.TaskView) int {
		if c := s.zone.DueMoment(a.DueDate.V, a.DueMin).Compare(s.zone.DueMoment(b.DueDate.V, b.DueMin)); c != 0 {
			return c
		}
		if a.Rank < b.Rank {
			return -1
		}
		if a.Rank > b.Rank {
			return 1
		}
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	}
	slices.SortFunc(overdue, byDue)
	slices.SortFunc(soon, byDue)
	return overdue, soon, nil
}

// Day returns a reconciled recap of the given civil date.
func (s *Service) Day(ctx context.Context, a domain.Actor, d civil.Date) (DayView, error) {
	if _, err := civil.ParseDate(d.String()); err != nil {
		return DayView{}, validation("date", err)
	}
	var v DayView
	err := s.read(ctx, a, domain.ScopeRead, true, func(q store.Q, now time.Time, _ civil.Date) error {
		var err error
		v, err = s.dayView(ctx, q, d, now)
		return err
	})
	if err != nil {
		return DayView{}, err
	}
	return v, nil
}

func (s *Service) dayView(ctx context.Context, q store.Q, d civil.Date, now time.Time) (DayView, error) {
	v := DayView{Date: d, Unlinked: make([]domain.EntryView, 0)}
	blocks, err := store.BlocksBetween(ctx, q, d, d, sql.Null[domain.TaskID]{})
	if err != nil {
		return v, err
	}
	v.Blocks, err = s.blockViews(ctx, q, blocks, now)
	if err != nil {
		return v, err
	}
	entries, err := store.ListEntries(ctx, q, store.EntryQuery{From: d, To: d, Limit: -1})
	if err != nil {
		return v, err
	}
	v.Entries, err = s.entryViews(ctx, q, entries)
	if err != nil {
		return v, err
	}
	for _, e := range v.Entries {
		if !e.TaskID.Valid {
			v.Unlinked = append(v.Unlinked, e)
		}
	}
	v.Interruptions, err = store.InterruptionsOn(ctx, q, d)
	return v, err
}

// ListTasks returns one filtered page, reconciling the time-dependent views first.
func (s *Service) ListTasks(ctx context.Context, a domain.Actor, in ListTasksIn) (Page[domain.TaskView], error) {
	var p Page[domain.TaskView]
	if !in.View.Valid() {
		return p, validation("view", errors.New("unknown task view"))
	}
	if err := validateFilters(in.Tag, in.Project); err != nil {
		return p, err
	}
	limit, err := pageLimit(in.Limit)
	if err != nil {
		return p, err
	}
	cursor, err := decodeKeyset(in.View, in.Cursor)
	if err != nil {
		return p, err
	}
	reconcile := in.View == domain.ViewTriage || in.View == domain.ViewBacklog || in.View == domain.ViewUpcoming
	err = s.read(ctx, a, domain.ScopeRead, reconcile, func(q store.Q, now time.Time, today civil.Date) error {
		tasks, err := store.ListTasks(ctx, q, store.TaskQuery{View: in.View, Tag: in.Tag, Project: in.Project, Limit: limit + 1, AfterKeys: cursor.Keys, AfterID: cursor.ID})
		if err != nil {
			return err
		}
		more := len(tasks) > limit
		if more {
			tasks = tasks[:limit]
		}
		p.Items, err = s.taskViews(ctx, q, tasks, now, today)
		if err != nil {
			return err
		}
		if more {
			p.NextCursor = taskCursor(in.View, p.Items[len(p.Items)-1])
		}
		return nil
	})
	if err != nil {
		return Page[domain.TaskView]{}, err
	}
	return p, nil
}

func taskCursor(view domain.View, t domain.TaskView) string {
	// AIDEV-NOTE: Civil minutes marshal as HH:MM text. Cursor schemas require
	// integer minutes, so both task and entry cursor encoders convert explicitly.
	keys := make([]any, 0)
	switch view {
	case domain.ViewTriage:
		keys = []any{t.Reasons[0].Reason.Rank(), t.Reasons[0].Seq}
	case domain.ViewBacklog:
		keys = []any{t.Rank}
	case domain.ViewUpcoming:
		minute := civil.Minute(0)
		if t.DeferMin.Valid {
			minute = t.DeferMin.V
		}
		keys = []any{t.DeferDate.V.String(), int(minute), t.Rank}
	case domain.ViewDone:
		keys = []any{string(view), t.CompletedAt.V.UnixMilli()}
	case domain.ViewDropped:
		keys = []any{string(view), t.UpdatedAt.UnixMilli()}
	case domain.ViewDeleted:
		keys = []any{string(view), t.DeletedAt.V.UnixMilli()}
	default:
	}
	return domain.EncodeCursor(domain.Cursor{Keys: keys, ID: int64(t.ID)})
}

// ListEntries returns a page of entries, newest days and timed entries first.
func (s *Service) ListEntries(ctx context.Context, a domain.Actor, in ListEntriesIn) (Page[domain.EntryView], error) {
	var p Page[domain.EntryView]
	hasTask := in.TaskID != nil && in.TaskID.Valid
	if hasTask && in.TaskID.V < 1 {
		return p, validation("task_id", errors.New("must be positive"))
	}
	if err := validateDateRange(in.From, in.To, hasTask); err != nil {
		return p, err
	}
	limit, err := pageLimit(in.Limit)
	if err != nil {
		return p, err
	}
	cursor, err := decodeKeyset("entries", in.Cursor)
	if err != nil {
		return p, err
	}
	err = s.read(ctx, a, domain.ScopeRead, false, func(q store.Q, _ time.Time, _ civil.Date) error {
		entries, err := store.ListEntries(ctx, q, store.EntryQuery{From: in.From, To: in.To, Task: in.TaskID, IncludeDeleted: in.IncludeDeleted, Limit: limit + 1, AfterKeys: cursor.Keys, AfterID: cursor.ID})
		if err != nil {
			return err
		}
		more := len(entries) > limit
		if more {
			entries = entries[:limit]
		}
		p.Items, err = s.entryViews(ctx, q, entries)
		if err != nil {
			return err
		}
		if more {
			e := entries[len(entries)-1]
			untimed, minute := 0, civil.Minute(0)
			if e.OccurredMin.Valid {
				minute = e.OccurredMin.V
			} else {
				untimed = 1
			}
			p.NextCursor = domain.EncodeCursor(domain.Cursor{Keys: []any{e.WorkDate.String(), untimed, int(minute), e.CreatedAt.UnixMilli()}, ID: int64(e.ID)})
		}
		return nil
	})
	if err != nil {
		return Page[domain.EntryView]{}, err
	}
	return p, nil
}

func (s *Service) entryViews(ctx context.Context, q store.Q, entries []domain.LogEntry) ([]domain.EntryView, error) {
	vs := make([]domain.EntryView, 0, len(entries))
	for _, e := range entries {
		v, err := s.entryView(ctx, q, e)
		if err != nil {
			return nil, err
		}
		vs = append(vs, v)
	}
	return vs, nil
}

// ListBlocks returns live blocks ordered by date, start minute and ID.
func (s *Service) ListBlocks(ctx context.Context, a domain.Actor, in ListBlocksIn) ([]domain.BlockView, error) {
	if in.TaskID.Valid && in.TaskID.V < 1 {
		return nil, validation("task_id", errors.New("must be positive"))
	}
	if err := validateDateRange(in.From, in.To, in.TaskID.Valid); err != nil {
		return nil, err
	}
	var vs []domain.BlockView
	err := s.read(ctx, a, domain.ScopeRead, false, func(q store.Q, now time.Time, _ civil.Date) error {
		blocks, err := store.BlocksBetween(ctx, q, in.From, in.To, in.TaskID)
		if err != nil {
			return err
		}
		vs, err = s.blockViews(ctx, q, blocks, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	return vs, nil
}

func (s *Service) blockViews(ctx context.Context, q store.Q, blocks []domain.TimeBlock, now time.Time) ([]domain.BlockView, error) {
	vs := make([]domain.BlockView, 0, len(blocks))
	for _, b := range blocks {
		v, err := s.blockView(ctx, q, b, now)
		if err != nil {
			return nil, err
		}
		vs = append(vs, v)
	}
	return vs, nil
}

func validateFilters(tag, project string) error {
	if tag != "" {
		if err := textsafe.ValidateName(tag, 64); err != nil {
			return validation("tag", err)
		}
	}
	if project != "" {
		if err := textsafe.ValidateName(project, 100); err != nil {
			return validation("project", err)
		}
	}
	return nil
}

func validateDateRange(from, to civil.Date, hasTask bool) error {
	for _, field := range []struct {
		name string
		date civil.Date
	}{{"from", from}, {"to", to}} {
		if field.date.IsZero() && hasTask {
			continue
		}
		if _, err := civil.ParseDate(field.date.String()); err != nil {
			return validation(field.name, err)
		}
	}
	if !from.IsZero() && !to.IsZero() {
		if from.After(to) {
			return validation("to", errors.New("must not precede from"))
		}
		if !hasTask && to.DaysSince(from) > 366 {
			return validation("to", errors.New("date span must not exceed 366 days"))
		}
	}
	return nil
}

func pageLimit(limit int) (int, error) {
	if limit < 0 || limit > 500 {
		return 0, validation("limit", errors.New("must be between 0 and 500"))
	}
	if limit == 0 {
		return 100, nil
	}
	return limit, nil
}

type cursorKey struct {
	literal  string
	date     bool
	min, max int64
}

// AIDEV-NOTE: Validate the entire tuple before read(), which may reconcile and
// write. Convert json.Number to int64: handing malformed numeric keys to SQLite
// can silently change keyset comparisons or turn client input into internal errors.
func decodeKeyset(view domain.View, cursor string) (domain.Cursor, error) {
	if cursor == "" {
		return domain.Cursor{}, nil
	}
	c, err := domain.DecodeCursor(cursor)
	if err != nil {
		return c, err
	}
	malformed := func() (domain.Cursor, error) {
		return domain.Cursor{}, domain.Errorf(domain.CodeMalformed, "invalid keyset cursor for %s", view)
	}
	if c.Keys == nil || c.ID < 1 || c.Offset != 0 {
		return malformed()
	}
	integer := cursorKey{min: math.MinInt64, max: math.MaxInt64}
	positive := cursorKey{min: 1, max: math.MaxInt64}
	millis := cursorKey{min: 0, max: math.MaxInt64}
	minute := cursorKey{min: 0, max: 1439}
	date := cursorKey{date: true}
	var schema []cursorKey
	switch view {
	case domain.ViewTriage:
		schema = []cursorKey{{min: 1, max: 8}, positive}
	case domain.ViewBacklog:
		schema = []cursorKey{integer}
	case domain.ViewUpcoming:
		schema = []cursorKey{date, minute, integer}
	case domain.ViewDone, domain.ViewDropped, domain.ViewDeleted:
		// AIDEV-NOTE: These views share one numeric schema, so the cursor carries
		// a view tag (stripped below; the store takes only the numeric key).
		schema = []cursorKey{{literal: string(view)}, millis}
	case domain.ViewAll:
		schema = []cursorKey{}
	case "history", "ops", "ai-changes":
		// AIDEV-NOTE: ID-only audit lists need a list tag to distinguish event
		// IDs, operation sequences, and the empty-key all-tasks cursor.
		schema = []cursorKey{{literal: string(view)}}
	case "entries":
		schema = []cursorKey{date, {min: 0, max: 1}, minute, millis}
	default:
		return malformed()
	}
	if len(c.Keys) != len(schema) {
		return malformed()
	}
	for i, key := range schema {
		switch {
		case key.literal != "":
			value, ok := c.Keys[i].(string)
			if !ok || value != key.literal {
				return malformed()
			}
		case key.date:
			date, ok := c.Keys[i].(string)
			if !ok {
				return malformed()
			}
			if _, err := civil.ParseDate(date); err != nil {
				return malformed()
			}
		default:
			number, ok := c.Keys[i].(json.Number)
			if !ok {
				return malformed()
			}
			n, err := number.Int64()
			if err != nil || n < key.min || n > key.max {
				return malformed()
			}
			c.Keys[i] = n
		}
	}
	if view == domain.ViewDone || view == domain.ViewDropped || view == domain.ViewDeleted {
		c.Keys = c.Keys[1:]
	}
	return c, nil
}

func decodeOffset(cursor string) (domain.Cursor, error) {
	if cursor == "" {
		return domain.Cursor{}, nil
	}
	c, err := domain.DecodeCursor(cursor)
	if err != nil {
		return c, err
	}
	if c.Keys != nil || c.ID != 0 || c.Offset < 0 {
		return domain.Cursor{}, domain.Errorf(domain.CodeMalformed, "invalid offset cursor")
	}
	// AIDEV-NOTE: A keyset envelope with null keys and id 0 also decodes to a
	// zero Cursor. Inspect the envelope so it cannot masquerade as offset zero.
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return domain.Cursor{}, domain.Errorf(domain.CodeMalformed, "invalid offset cursor")
	}
	var envelope struct {
		Offset *int `json:"o"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Offset == nil {
		return domain.Cursor{}, domain.Errorf(domain.CodeMalformed, "invalid offset cursor")
	}
	return c, nil
}

// ListTags returns positive open-task counts in case-insensitive tag order.
func (s *Service) ListTags(ctx context.Context, a domain.Actor) ([]TagCount, error) {
	var vs []TagCount
	err := s.read(ctx, a, domain.ScopeRead, false, func(q store.Q, _ time.Time, _ civil.Date) error {
		counts, err := store.TagCounts(ctx, q)
		if err != nil {
			return err
		}
		vs = make([]TagCount, 0, len(counts))
		for _, c := range counts {
			vs = append(vs, TagCount{Name: c.Name, Count: c.Count})
		}
		return nil
	})
	return vs, err
}

// Settings returns the persisted backup retention settings.
func (s *Service) Settings(ctx context.Context, a domain.Actor) (SettingsView, error) {
	var v SettingsView
	err := s.read(ctx, a, domain.ScopeRead, false, func(q store.Q, _ time.Time, _ civil.Date) error {
		daily, err := store.GetSetting(ctx, q, "backup_keep_daily")
		if err != nil {
			return err
		}
		weekly, err := store.GetSetting(ctx, q, "backup_keep_weekly")
		if err != nil {
			return err
		}
		v.KeepDaily, err = strconv.Atoi(daily)
		if err != nil {
			return err
		}
		v.KeepWeekly, err = strconv.Atoi(weekly)
		return err
	})
	if err != nil {
		return SettingsView{}, err
	}
	return v, nil
}

// SyncTimezone persists the configured zone without an operation or event and
// remembers a change for dashboard notices throughout this service's lifetime.
func (s *Service) SyncTimezone(ctx context.Context, name string) (previous string, changed bool, err error) {
	if _, err := civil.LoadZone(name); err != nil {
		return "", false, validation("timezone", err)
	}
	err = s.writeTx(ctx, func(tx *sql.Tx, _ time.Time) error {
		var err error
		previous, err = store.GetSetting(ctx, tx, "tz_last_seen")
		if err != nil {
			return err
		}
		changed = previous != name
		if !changed {
			return nil
		}
		return store.SetSetting(ctx, tx, "tz_last_seen", name)
	})
	if err != nil {
		return "", false, err
	}
	if changed {
		s.tzChanged.Store(true)
	}
	return previous, changed, nil
}

func (s *Service) aiUnreviewed(ctx context.Context, q store.Q) (int, error) {
	seq, err := aiReviewedThrough(ctx, q)
	if err != nil {
		return 0, err
	}
	return store.CountAIOps(ctx, q, seq)
}

func (s *Service) backupHealth(ctx context.Context, q store.Q, now time.Time) (BackupHealth, error) {
	var v BackupHealth
	value, err := store.GetSetting(ctx, q, "last_backup_at")
	if err != nil {
		return v, err
	}
	if value != "" {
		ms, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return v, err
		}
		v.LastSuccessAt = sql.Null[time.Time]{V: time.UnixMilli(ms).UTC(), Valid: true}
	}
	v.LastError, err = store.GetSetting(ctx, q, "last_backup_error")
	v.Healthy = v.LastSuccessAt.Valid && now.Sub(v.LastSuccessAt.V) <= 48*time.Hour && v.LastError == ""
	return v, err
}

func (s *Service) readWarnings(ctx context.Context, q store.Q, now time.Time) (BackupHealth, int, []Banner, error) {
	health, err := s.backupHealth(ctx, q, now)
	if err != nil {
		return health, 0, nil, err
	}
	ai, err := s.aiUnreviewed(ctx, q)
	if err != nil {
		return health, 0, nil, err
	}
	banners := make([]Banner, 0)
	if health.LastError != "" {
		banners = append(banners, Banner{Kind: "backup_failed", Message: backupWarning(health)})
	} else if !health.Healthy {
		banners = append(banners, Banner{Kind: "backup_stale", Message: backupWarning(health)})
	}
	if ai > 0 {
		banners = append(banners, Banner{Kind: "ai_changes", Message: fmt.Sprintf("%d unreviewed AI changes.", ai)})
	}
	if s.ledger != nil {
		pending, err := s.ledger.Pending()
		if err != nil {
			return health, ai, nil, err
		}
		if len(pending) > 0 {
			banners = append(banners, Banner{Kind: "purge_incomplete", Message: "A purge is incomplete."})
		}
		if s.ledger.Health().Damaged {
			banners = append(banners, Banner{Kind: "purge_ledger_damaged", Message: "The purge ledger is damaged."})
		}
	}
	if s.tzChanged.Load() {
		banners = append(banners, Banner{Kind: "tz_changed", Message: "The configured timezone changed; dates and times follow the configured zone."})
	}
	return health, ai, banners, nil
}
