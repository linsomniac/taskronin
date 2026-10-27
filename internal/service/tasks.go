package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/store"
	"taskronin/internal/textsafe"
)

// Gap is the default spacing between task ranks.
const Gap int64 = 1 << 16

// AddTaskIn supplies a new task's content and optional placement.
type AddTaskIn struct {
	Title, Notes string
	Due          *domain.Moment
	Tags         []string
	Project      string
	Priority     domain.Priority
	Place        *AddPlacement
}

// AddPlacement describes a placement to apply while creating a task.
type AddPlacement struct {
	How   string // top|bottom|before|after|defer|block|now
	Ref   domain.TaskID
	Defer domain.Moment
	Block domain.BlockSpan
}

// MarkManualIn identifies a task and its expected version.
type MarkManualIn struct {
	ID     domain.TaskID
	Expect domain.Expect
}

// AddTask captures a task in triage with the new reason.
func (s *Service) AddTask(ctx context.Context, m Meta, in AddTaskIn) (*OpResult, error) {
	scopes := domain.ScopeCapture
	if in.Place != nil {
		scopes |= domain.ScopeWrite
	}
	return s.run(ctx, m, opSpec{kind: domain.OpTaskAdd, class: classForward, scopes: scopes, idem: true, revertible: true}, func(o *op) error {
		if in.Place != nil {
			if m.Actor.Kind == domain.ActorToken {
				return domain.Errorf(domain.CodeForbidden, "tokens cannot place new tasks")
			}
			// AIDEV-TODO: Tasks 19/22/26/27 implement AddPlacement's individual modes.
			return validation("place", errors.New("placement is not supported yet"))
		}
		title, err := textsafe.ValidateTitle(in.Title)
		if err != nil {
			return validation("title", err)
		}
		notes, err := textsafe.ValidateNotes(in.Notes)
		if err != nil {
			return validation("notes", err)
		}
		if in.Project != "" {
			if err := textsafe.ValidateName(in.Project, 100); err != nil {
				return validation("project", err)
			}
		}
		if !in.Priority.Valid() {
			return validation("priority", errors.New("must be H, M, L or empty"))
		}
		tags := make([]string, 0, len(in.Tags))
		seen := make(map[string]bool, len(in.Tags))
		for _, tag := range in.Tags {
			if err := textsafe.ValidateName(tag, 64); err != nil {
				return validation("tags", err)
			}
			key := strings.ToLower(tag)
			if !seen[key] {
				tags = append(tags, tag)
				seen[key] = true
			}
		}
		t := domain.Task{Title: title, Notes: notes, Status: domain.StatusOpen, DueAck: domain.DueNone,
			Project: in.Project, Priority: in.Priority, Origin: o.actor.Key(), CreatedAt: o.now, UpdatedAt: o.now}
		if in.Due != nil {
			if _, err := civil.ParseDate(in.Due.Date.String()); err != nil {
				return validation("due", err)
			}
			if in.Due.Min.Valid && (in.Due.Min.V < 0 || in.Due.Min.V > 1439) {
				return validation("due", errors.New("minute must be between 0 and 1439"))
			}
			if !s.zone.DueMoment(in.Due.Date, in.Due.Min).After(o.now) {
				return validation("due", errors.New("must be in the future"))
			}
			t.DueDate, t.DueMin = sql.Null[civil.Date]{V: in.Due.Date, Valid: true}, in.Due.Min
		}
		t.Rank, err = o.rankBottom()
		if err != nil {
			return err
		}
		if err := store.InsertTask(o.ctx, o.tx, &t); err != nil {
			return err
		}
		ref := domain.EntityRef{Entity: domain.EntTask, ID: int64(t.ID)}
		// AIDEV-NOTE: Triage and tags dirty the task in this same op; inserted
		// tasks must retain version 1 when the runner finalizes its dirty set.
		o.markInserted(ref)
		after, err := json.Marshal(taskCols(t))
		if err != nil {
			return err
		}
		if err := o.record(domain.Event{Entity: domain.EntTask, EntityID: int64(t.ID), Action: domain.ActInsert, After: after}); err != nil {
			return err
		}
		if err := o.addReason(t.ID, domain.ReasonNew); err != nil {
			return err
		}
		if len(tags) > 0 {
			if err := store.SetTaskTags(o.ctx, o.tx, t.ID, tags, o.now); err != nil {
				return err
			}
			stored, err := store.TaskTags(o.ctx, o.tx, t.ID)
			if err != nil {
				return err
			}
			after, err := json.Marshal(map[string]any{"tags": stored})
			if err != nil {
				return err
			}
			if err := o.record(domain.Event{Entity: domain.EntTaskTags, EntityID: int64(t.ID), Action: domain.ActSet,
				Before: json.RawMessage(`{"tags":[]}`), After: after}); err != nil {
				return err
			}
			o.markTaskDirty(t.ID)
		}
		o.ref(ref)
		o.created = true
		return nil
	})
}

// MarkManual adds an explicit triage reason, preserving an existing reason.
func (s *Service) MarkManual(ctx context.Context, m Meta, in MarkManualIn) (*OpResult, error) {
	return s.run(ctx, m, opSpec{kind: domain.OpTaskManual, class: classForward, scopes: domain.ScopeWrite, idem: true, revertible: true}, func(o *op) error {
		t, err := o.loadTask(in.ID, &in.Expect, false)
		if err != nil {
			return err
		}
		// AIDEV-TODO: Task 27 clears a future defer here (D4).
		if err := o.addReason(t.ID, domain.ReasonManual); err != nil {
			return err
		}
		o.ref(domain.EntityRef{Entity: domain.EntTask, ID: int64(t.ID)})
		return nil
	})
}

func (o *op) rankBottom() (int64, error) {
	_, hi, err := store.RankBounds(o.ctx, o.tx)
	if err != nil {
		return 0, err
	}
	if !hi.Valid {
		return Gap, nil
	}
	return hi.V + Gap, nil
}

func (o *op) loadTask(id domain.TaskID, exp *domain.Expect, allowDeleted bool) (domain.Task, error) {
	t, err := store.GetTask(o.ctx, o.tx, id)
	if errors.Is(err, store.ErrNotFound) {
		return t, domain.Errorf(domain.CodeNotFound, "task #%d not found", id)
	}
	if err != nil {
		return t, err
	}
	if t.DeletedAt.Valid && !allowDeleted {
		return t, domain.Errorf(domain.CodeDeleted, "task #%d is deleted", id)
	}
	if exp != nil {
		if exp.Gen == "" {
			return t, domain.Errorf(domain.CodePreconditionReq, "task precondition is required")
		}
		if exp.Gen != o.gen || exp.Version != t.Version {
			current, err := o.s.taskView(o.ctx, o.tx, t, o.now, o.today)
			if err != nil {
				return t, err
			}
			return t, &domain.Error{Code: domain.CodePrecondition, Detail: "task precondition failed", Current: current}
		}
	}
	return t, nil
}

func (o *op) loadEntry(id domain.EntryID, exp *domain.Expect, allowDeleted bool) (domain.LogEntry, error) {
	e, err := store.GetEntry(o.ctx, o.tx, id)
	if errors.Is(err, store.ErrNotFound) {
		return e, domain.Errorf(domain.CodeNotFound, "entry #%d not found", id)
	}
	if err != nil {
		return e, err
	}
	if e.DeletedAt.Valid && !allowDeleted {
		return e, domain.Errorf(domain.CodeDeleted, "entry #%d is deleted", id)
	}
	if exp != nil {
		if exp.Gen == "" {
			return e, domain.Errorf(domain.CodePreconditionReq, "entry precondition is required")
		}
		if exp.Gen != o.gen || exp.Version != e.Version {
			return e, &domain.Error{Code: domain.CodePrecondition, Detail: "entry precondition failed", Current: e}
		}
	}
	return e, nil
}

func (o *op) loadBlock(id domain.BlockID, exp *domain.Expect) (domain.TimeBlock, error) {
	b, err := store.GetBlock(o.ctx, o.tx, id)
	if errors.Is(err, store.ErrNotFound) {
		return b, domain.Errorf(domain.CodeNotFound, "block #%d not found", id)
	}
	if err != nil {
		return b, err
	}
	if b.DeletedAt.Valid {
		return b, domain.Errorf(domain.CodeDeleted, "block #%d is deleted", id)
	}
	if exp != nil {
		if exp.Gen == "" {
			return b, domain.Errorf(domain.CodePreconditionReq, "block precondition is required")
		}
		if exp.Gen != o.gen || exp.Version != b.Version {
			current, err := o.s.blockView(o.ctx, o.tx, b, o.now)
			if err != nil {
				return b, err
			}
			return b, &domain.Error{Code: domain.CodePrecondition, Detail: "block precondition failed", Current: current}
		}
	}
	return b, nil
}
