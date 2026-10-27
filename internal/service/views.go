package service

import (
	"context"
	"errors"
	"time"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/store"
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
