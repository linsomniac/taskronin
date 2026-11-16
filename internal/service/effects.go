package service

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"taskronin/internal/domain"
)

// Effect is a user-visible consequence reconstructed from persisted events.
type Effect struct {
	Kind     EffectKind
	TaskID   domain.TaskID
	BlockID  domain.BlockID
	EntryID  domain.EntryID
	Reason   domain.TriageReason
	From, To string
}

// EffectKind names a user-visible consequence of an operation.
type EffectKind string

const (
	// EffCreated denotes a new task.
	EffCreated EffectKind = "created"
	// EffUpdated denotes a task field or tag change.
	EffUpdated EffectKind = "updated"
	// EffRetitled denotes a task title change.
	EffRetitled EffectKind = "retitled"
	// EffPlaced denotes task placement.
	EffPlaced EffectKind = "placed"
	// EffMoved denotes task reordering.
	EffMoved EffectKind = "moved"
	// EffDeferred denotes a task deferral.
	EffDeferred EffectKind = "deferred"
	// EffUndeferred denotes a cleared deferral.
	EffUndeferred EffectKind = "undeferred"
	// EffDueSet denotes a due moment change.
	EffDueSet EffectKind = "due_set"
	// EffDueCleared denotes a cleared due moment.
	EffDueCleared EffectKind = "due_cleared"
	// EffBlocked denotes a new scheduled block.
	EffBlocked EffectKind = "blocked"
	// EffBlockMoved denotes a rescheduled block.
	EffBlockMoved EffectKind = "block_moved"
	// EffBlockCancelled denotes a cancelled or deleted block.
	EffBlockCancelled EffectKind = "block_cancelled"
	// EffBlockResolved denotes an automatically resolved block.
	EffBlockResolved EffectKind = "block_resolved"
	// EffOutcomeSet denotes an explicit block outcome.
	EffOutcomeSet EffectKind = "outcome_set"
	// EffSlipped denotes a newly slipped block.
	EffSlipped EffectKind = "slipped"
	// EffUnslipped denotes a cleared slip.
	EffUnslipped EffectKind = "unslipped"
	// EffReasonAdded denotes a new triage reason.
	EffReasonAdded EffectKind = "reason_added"
	// EffReasonRemoved denotes a removed triage reason.
	EffReasonRemoved EffectKind = "reason_removed"
	// EffFocusSet denotes a focused task.
	EffFocusSet EffectKind = "focus_set"
	// EffFocusCleared denotes cleared focus.
	EffFocusCleared EffectKind = "focus_cleared"
	// EffInterrupted denotes a focus interruption.
	EffInterrupted EffectKind = "interrupted"
	// EffEntryLogged denotes a new log entry.
	EffEntryLogged EffectKind = "entry_logged"
	// EffEntryUpdated denotes edited log content.
	EffEntryUpdated EffectKind = "entry_updated"
	// EffEntryRelinked denotes a changed task link.
	EffEntryRelinked EffectKind = "entry_relinked"
	// EffEntryDeleted denotes a tombstoned entry.
	EffEntryDeleted EffectKind = "entry_deleted"
	// EffEntryRestored denotes a restored entry.
	EffEntryRestored EffectKind = "entry_restored"
	// EffCompleted denotes a completed task.
	EffCompleted EffectKind = "completed"
	// EffReopened denotes a reopened task.
	EffReopened EffectKind = "reopened"
	// EffDropped denotes a dropped task.
	EffDropped EffectKind = "dropped"
	// EffDeleted denotes a tombstoned task.
	EffDeleted EffectKind = "deleted"
	// EffUndeleted denotes a restored task.
	EffUndeleted EffectKind = "undeleted"
	// EffReverted denotes a revert, undo or redo operation.
	EffReverted EffectKind = "reverted"
	// EffReviewed denotes acknowledged AI changes.
	EffReviewed EffectKind = "reviewed"
	// EffPurged denotes permanently scrubbed content.
	EffPurged EffectKind = "purged"
)

func effectsFromEvents(rec domain.OpRecord, evs []domain.Event) []Effect {
	effects := make([]Effect, 0)
	if len(evs) == 0 {
		return effects
	}
	if rec.Via != "" {
		effects = append(effects, Effect{Kind: EffReverted, From: rec.RevertsOpID.V.String()[:8]})
	}
	for _, ev := range evs {
		b, a := effectImage(ev.Before), effectImage(ev.After)
		base := Effect{}
		if ev.TaskID.Valid {
			base.TaskID = ev.TaskID.V
		}
		if ev.Entity == domain.EntBlock {
			base.BlockID = domain.BlockID(ev.EntityID)
		}
		if ev.Entity == domain.EntEntry {
			base.EntryID = domain.EntryID(ev.EntityID)
		}
		add := func(kind EffectKind, from, to string) {
			e := base
			e.Kind, e.From, e.To = kind, from, to
			effects = append(effects, e)
		}
		if ev.Action == domain.ActPurge {
			add(EffPurged, "", "")
			continue
		}
		switch ev.Entity {
		case domain.EntTask:
			switch ev.Action {
			case domain.ActInsert:
				add(EffCreated, "", "")
			case domain.ActMove:
				add(EffMoved, "", "")
			case domain.ActTombstone:
				add(EffDeleted, "", "")
			case domain.ActUntombstone:
				add(EffUndeleted, "", "")
			case domain.ActUpdate:
				taskEffects(b, a, add)
			default:
			}
		case domain.EntTaskTags:
			if ev.Action == domain.ActSet {
				add(EffUpdated, "", "")
			}
		case domain.EntTriage:
			switch ev.Action {
			case domain.ActAdd:
				base.Reason = domain.TriageReason(a.text("reason"))
				add(EffReasonAdded, "", "")
			case domain.ActRemove:
				base.Reason = domain.TriageReason(b.text("reason"))
				add(EffReasonRemoved, "", "")
			default:
			}
		case domain.EntBlock:
			switch ev.Action {
			case domain.ActInsert:
				span := fmt.Sprintf("%s %s-%s", a.text("date"), minuteText(a.number("start_min")), minuteText(a.number("end_min")))
				add(EffBlocked, "", span)
			case domain.ActUpdate:
				blockEffects(b, a, add)
			default:
			}
		case domain.EntEntry:
			switch ev.Action {
			case domain.ActInsert:
				add(EffEntryLogged, "", "")
			case domain.ActTombstone:
				add(EffEntryDeleted, "", "")
			case domain.ActUntombstone:
				add(EffEntryRestored, "", "")
			case domain.ActUpdate:
				if a.changed(b, "task_id") {
					add(EffEntryRelinked, "", "")
				} else {
					add(EffEntryUpdated, "", "")
				}
			default:
			}
		case domain.EntFocus:
			if ev.Action == domain.ActSet {
				if a.nonnull("task_id") {
					base.TaskID = domain.TaskID(a.number("task_id"))
					add(EffFocusSet, "", "")
				} else {
					add(EffFocusCleared, "", "")
				}
			}
		case domain.EntInterruption:
			if ev.Action == domain.ActInsert {
				from := ""
				if a.nonnull("from_task_id") {
					from = strconv.FormatInt(a.number("from_task_id"), 10)
				}
				add(EffInterrupted, from, "")
			}
		default:
		}
	}
	if rec.Kind == domain.OpAIReview {
		// AIDEV-NOTE: The acknowledged watermark is the persisted setting,
		// not this review operation's sequence. Preserve it on replay too.
		for _, ev := range evs {
			after := effectImage(ev.After)
			if ev.Entity == domain.EntSetting && after.text("key") == "ai_reviewed_through" {
				effects = append(effects, Effect{Kind: EffReviewed, To: after.text("value")})
				break
			}
		}
	}
	return effects
}

func taskEffects(b, a effectColumns, add func(EffectKind, string, string)) {
	if a.changed(b, "title") {
		add(EffRetitled, b.text("title"), a.text("title"))
	}
	if a.changed(b, "status") {
		switch domain.TaskStatus(a.text("status")) {
		case domain.StatusDone:
			add(EffCompleted, "", "")
		case domain.StatusDropped:
			add(EffDropped, "", "")
		case domain.StatusOpen:
			add(EffReopened, "", "")
		default:
		}
	}
	if a.changed(b, "last_placed_seq") {
		add(EffPlaced, "", "")
	}
	if a.changed(b, "defer_date") || a.changed(b, "defer_min") {
		if a.has("defer_date") && !a.nonnull("defer_date") {
			add(EffUndeferred, "", "")
		} else {
			add(EffDeferred, "", a.moment("defer", b))
		}
	}
	if a.changed(b, "due_date") || a.changed(b, "due_min") {
		if a.has("due_date") && !a.nonnull("due_date") {
			add(EffDueCleared, "", "")
		} else {
			add(EffDueSet, "", a.moment("due", b))
		}
	}
	// AIDEV-NOTE: F9 absorbs bookkeeping columns into their primary effects.
	// Iterating only to decide whether to emit one updated keeps effect order stable.
	for k := range a {
		switch k {
		case "title", "status", "last_placed_at", "last_placed_seq", "due_ack", "due_ack_date",
			"completed_at", "completed_date", "defer_date", "defer_min", "due_date", "due_min", "version", "updated_at":
			continue
		default:
			if a.changed(b, k) {
				add(EffUpdated, "", "")
				return
			}
		}
	}
}

func blockEffects(b, a effectColumns, add func(EffectKind, string, string)) {
	if a.changed(b, "date") || a.changed(b, "start_min") || a.changed(b, "end_min") {
		add(EffBlockMoved, "", "")
	}
	if (a.changed(b, "status") && a.text("status") == string(domain.BlockCancelled)) || (a.changed(b, "deleted_at") && a.nonnull("deleted_at")) {
		add(EffBlockCancelled, "", "")
	} else if a.changed(b, "status") {
		switch domain.BlockStatus(a.text("status")) {
		case domain.BlockDone, domain.BlockWorked, domain.BlockMissed:
			switch domain.OutcomeSource(a.text("outcome_source")) {
			case domain.OutcomeUser:
				add(EffOutcomeSet, "", a.text("status"))
			case domain.OutcomeAuto:
				add(EffBlockResolved, "", a.text("status"))
			default:
			}
		default:
		}
	}
	if a.changed(b, "slipped") {
		if a.number("slipped") == 1 && b.number("slipped") == 0 {
			add(EffSlipped, "", "")
		}
		if a.number("slipped") == 0 && b.number("slipped") == 1 {
			add(EffUnslipped, "", "")
		}
	}
}

type effectColumns map[string]json.RawMessage

func effectImage(raw json.RawMessage) effectColumns {
	var cols effectColumns
	// Persisted events are produced by typed column encoders; malformed synthetic
	// payloads have no fields rather than making effect reconstruction panic.
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cols); err != nil {
			return nil
		}
	}
	return cols
}

func (c effectColumns) has(k string) bool { _, ok := c[k]; return ok }
func (c effectColumns) nonnull(k string) bool {
	return c.has(k) && strings.TrimSpace(string(c[k])) != "null"
}
func (c effectColumns) text(k string) string  { var s string; _ = json.Unmarshal(c[k], &s); return s }
func (c effectColumns) number(k string) int64 { var n int64; _ = json.Unmarshal(c[k], &n); return n }
func (c effectColumns) changed(b effectColumns, k string) bool {
	return c.has(k) && strings.TrimSpace(string(c[k])) != strings.TrimSpace(string(b[k]))
}
func (c effectColumns) moment(prefix string, before effectColumns) string {
	d, m := prefix+"_date", prefix+"_min"
	date := c.text(d)
	if !c.has(d) {
		date = before.text(d)
	}
	minutes := c
	if !c.has(m) {
		minutes = before
	}
	if minutes.nonnull(m) {
		return strings.TrimSpace(date + " " + minuteText(minutes.number(m)))
	}
	return date
}
func minuteText(m int64) string { return fmt.Sprintf("%02d:%02d", m/60, m%60) }
