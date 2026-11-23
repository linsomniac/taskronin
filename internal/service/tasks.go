package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/store"
	"github.com/linsomniac/taskronin/internal/textsafe"
)

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

// UpdateTaskIn supplies optional task fields and case-insensitive tag changes.
type UpdateTaskIn struct {
	ID                    domain.TaskID
	Expect                domain.Expect
	Title, Notes, Project *string          // Nil leaves unchanged; empty Project clears.
	Priority              *domain.Priority // PriorityNone clears.
	TagsAdd, TagsRemove   []string
}

// UpdateTask edits a task's content and tags without placing it.
func (s *Service) UpdateTask(ctx context.Context, m Meta, in UpdateTaskIn) (*OpResult, error) {
	return s.run(ctx, m, opSpec{kind: domain.OpTaskUpdate, class: classForward, scopes: domain.ScopeWrite, idem: true, revertible: true}, func(o *op) error {
		// AIDEV-NOTE: Include tombstones to distinguish immutable purged
		// content (R3-4 conflict) from ordinary deleted tasks.
		t, err := o.loadTask(in.ID, &in.Expect, true)
		if err != nil {
			return err
		}
		if t.PurgedAt.Valid {
			return domain.Errorf(domain.CodeConflict, "task #%d is purged", t.ID)
		}
		if t.DeletedAt.Valid {
			return domain.Errorf(domain.CodeDeleted, "task #%d is deleted", t.ID)
		}
		before := t
		if in.Title != nil {
			t.Title, err = textsafe.ValidateTitle(*in.Title)
			if err != nil {
				return validation("title", err)
			}
		}
		if in.Notes != nil {
			t.Notes, err = textsafe.ValidateNotes(*in.Notes)
			if err != nil {
				return validation("notes", err)
			}
		}
		if in.Project != nil {
			if *in.Project != "" {
				if err := textsafe.ValidateName(*in.Project, 100); err != nil {
					return validation("project", err)
				}
			}
			t.Project = *in.Project
		}
		if in.Priority != nil {
			if !in.Priority.Valid() {
				return validation("priority", errors.New("must be H, M, L or empty"))
			}
			t.Priority = *in.Priority
		}
		if err := store.UpdateTask(o.ctx, o.tx, t); err != nil {
			return err
		}
		if err := o.recordTaskUpdate(before, t); err != nil {
			return err
		}
		if err := o.updateTaskTags(t.ID, in.TagsAdd, in.TagsRemove); err != nil {
			return err
		}
		o.ref(domain.EntityRef{Entity: domain.EntTask, ID: int64(t.ID)})
		return nil
	})
}

func (o *op) updateTaskTags(id domain.TaskID, add, remove []string) error {
	for _, names := range [][]string{add, remove} {
		for _, name := range names {
			if err := textsafe.ValidateName(name, 64); err != nil {
				return validation("tags", err)
			}
		}
	}
	before, err := store.TaskTags(o.ctx, o.tx, id)
	if err != nil {
		return err
	}
	names := make(map[string]string, len(before)+len(add))
	for _, name := range before {
		names[strings.ToLower(name)] = name
	}
	for _, name := range add {
		key := strings.ToLower(name)
		if _, exists := names[key]; !exists {
			names[key] = name
		}
	}
	for _, name := range remove {
		delete(names, strings.ToLower(name))
	}
	after := make([]string, 0, len(names))
	for _, name := range names {
		after = append(after, name)
	}
	slices.SortFunc(after, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	if slices.Equal(before, after) {
		return nil
	}
	if err := store.SetTaskTags(o.ctx, o.tx, id, after, o.now); err != nil {
		return err
	}
	// AIDEV-NOTE: NOCASE tag identity preserves the first spelling globally.
	// Read it back for sorted, canonical event images; empty sets encode [].
	after, err = store.TaskTags(o.ctx, o.tx, id)
	if err != nil {
		return err
	}
	bj, err := json.Marshal(map[string]any{"tags": append([]string{}, before...)})
	if err != nil {
		return err
	}
	aj, err := json.Marshal(map[string]any{"tags": append([]string{}, after...)})
	if err != nil {
		return err
	}
	if err := o.record(domain.Event{Entity: domain.EntTaskTags, EntityID: int64(id), Action: domain.ActSet, Before: bj, After: aj}); err != nil {
		return err
	}
	o.markTaskDirty(id)
	return nil
}

// MarkManualIn identifies a task and its expected version.
type MarkManualIn struct {
	ID     domain.TaskID
	Expect domain.Expect
}

// SetDueIn identifies a task and its new due moment; nil clears the due.
type SetDueIn struct {
	ID     domain.TaskID
	Expect domain.Expect
	Due    *domain.Moment
}

// SetDue changes a task's due moment and resets its due acknowledgement.
func (s *Service) SetDue(ctx context.Context, m Meta, in SetDueIn) (*OpResult, error) {
	return s.run(ctx, m, opSpec{kind: domain.OpTaskDue, class: classForward, scopes: domain.ScopeWrite, idem: true, revertible: true}, func(o *op) error {
		t, err := o.loadTask(in.ID, &in.Expect, false)
		if err != nil {
			return err
		}
		var deferM *domain.Moment
		if t.DeferDate.Valid {
			deferM = &domain.Moment{Date: t.DeferDate.V, Min: t.DeferMin}
		}
		if err := o.checkDeferDue(deferM, in.Due); err != nil {
			return err
		}
		before := t
		t.DueDate, t.DueMin = sql.Null[civil.Date]{}, sql.Null[civil.Minute]{}
		if in.Due != nil {
			if !s.zone.DueMoment(in.Due.Date, in.Due.Min).After(o.now) {
				return validation("due", errors.New("must be in the future"))
			}
			t.DueDate, t.DueMin = sql.Null[civil.Date]{V: in.Due.Date, Valid: true}, in.Due.Min
		}
		t.DueAck, t.DueAckDate = domain.DueNone, sql.Null[civil.Date]{}
		if err := store.UpdateTask(o.ctx, o.tx, t); err != nil {
			return err
		}
		if err := o.recordTaskUpdate(before, t); err != nil {
			return err
		}
		if err := o.removeReason(t.ID, domain.ReasonDue); err != nil {
			return err
		}
		if err := o.removeReason(t.ID, domain.ReasonOverdue); err != nil {
			return err
		}
		o.ref(domain.EntityRef{Entity: domain.EntTask, ID: int64(t.ID)})
		return nil
	})
}

func (o *op) checkDeferDue(deferM, dueM *domain.Moment) error {
	if err := validateMoment("defer", deferM); err != nil {
		return err
	}
	if err := validateMoment("due", dueM); err != nil {
		return err
	}
	if deferM == nil || dueM == nil {
		return nil
	}
	if deferM.Date.After(dueM.Date) ||
		!o.s.zone.DeferMoment(deferM.Date, deferM.Min).Before(o.s.zone.DueMoment(dueM.Date, dueM.Min)) {
		return validation("due", errors.New("defer must precede due and surface on or before its due date"))
	}
	return nil
}

func validateMoment(field string, m *domain.Moment) error {
	if m == nil {
		return nil
	}
	if _, err := civil.ParseDate(m.Date.String()); err != nil {
		return validation(field, err)
	}
	// AIDEV-NOTE: Validate typed API inputs before civil moment helpers;
	// out-of-range minutes are caller bugs to civil and can panic there.
	if m.Min.Valid && (m.Min.V < 0 || m.Min.V > 1439) {
		return validation(field, errors.New("minute must be between 0 and 1439"))
	}
	return nil
}

// AddTask captures a task in triage, or applies an owner-requested placement.
func (s *Service) AddTask(ctx context.Context, m Meta, in AddTaskIn) (*OpResult, error) {
	scopes := domain.ScopeCapture
	if in.Place != nil {
		scopes |= domain.ScopeWrite
	}
	return s.run(ctx, m, opSpec{kind: domain.OpTaskAdd, class: classForward, scopes: scopes, idem: true, revertible: true, ownerOnly: in.Place != nil}, func(o *op) error {
		if in.Place != nil {
			if err := validateAddPlacement(*in.Place); err != nil {
				return validation("place", err)
			}
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
			t.DueDate, t.DueMin = sql.Null[civil.Date]{V: in.Due.Date, Valid: true}, in.Due.Min
		}
		if in.Place != nil && in.Place.How == "defer" {
			if err := o.checkDefer(t, in.Place.Defer); err != nil {
				return err
			}
		}
		if in.Due != nil && !s.zone.DueMoment(in.Due.Date, in.Due.Min).After(o.now) {
			return validation("due", errors.New("must be in the future"))
		}
		switch {
		case in.Place == nil || in.Place.How == "block" || in.Place.How == "defer":
			t.Rank, err = o.rankBottom()
		case in.Place.How == "now":
			t.Rank, err = o.rankTop()
		default:
			t.Rank, err = o.placementRank(domain.PlaceHow(in.Place.How), in.Place.Ref)
		}
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
		if in.Place == nil {
			if err := o.addReason(t.ID, domain.ReasonNew); err != nil {
				return err
			}
		} else {
			if in.Place.How == "block" {
				if _, err := o.createBlock(t, in.Place.Block); err != nil {
					return err
				}
			}
			if err := o.place(t.ID); err != nil {
				return err
			}
			if in.Place.How == "defer" {
				if err := o.setDefer(t.ID, &in.Place.Defer); err != nil {
					return err
				}
			}
			if in.Place.How == "now" {
				if err := o.interrupt(t.ID, true, false); err != nil {
					return err
				}
				if err := o.setFocus(sql.Null[domain.TaskID]{V: t.ID, Valid: true}); err != nil {
					return err
				}
			}
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
		// AIDEV-NOTE: Manual triage clears defer without acknowledging reasons
		// or changing placement fields, so the task is visible in Triage (D4).
		if t.DeferDate.Valid {
			if err := o.setDefer(t.ID, nil); err != nil {
				return err
			}
		}
		if err := o.addReason(t.ID, domain.ReasonManual); err != nil {
			return err
		}
		o.ref(domain.EntityRef{Entity: domain.EntTask, ID: int64(t.ID)})
		return nil
	})
}

func validateAddPlacement(p AddPlacement) error {
	if p.Ref != 0 && p.How != "before" && p.How != "after" {
		return errors.New("reference only belongs to before/after placement")
	}
	if p.Defer != (domain.Moment{}) && p.How != "defer" {
		return errors.New("defer only belongs to defer placement")
	}
	if p.Block != (domain.BlockSpan{}) && p.How != "block" {
		return errors.New("block only belongs to block placement")
	}
	switch p.How {
	case "top", "bottom", "block", "now", "defer":
		// AIDEV-NOTE: All AddPlacement modes are supported; their shared
		// operation helpers validate the block span or defer moment below.
		return nil
	case "before", "after":
		if p.Ref == 0 {
			return errors.New("placement reference is required")
		}
		return nil
	default:
		return errors.New("unknown placement")
	}
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
			current, err := o.s.entryView(o.ctx, o.tx, e)
			if err != nil {
				return e, err
			}
			return e, &domain.Error{Code: domain.CodePrecondition, Detail: "entry precondition failed", Current: current}
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
