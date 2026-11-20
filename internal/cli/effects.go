package cli

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"taskronin/internal/api"
	"taskronin/internal/domain"
	"taskronin/internal/textsafe"
)

// AIDEV-NOTE: opHeadline closures render the same tokens for effect matching
// and display. Sanitize user text before styling it with paint.
type opHeadline func(paint) string

func plainHeadline(text string) opHeadline {
	return func(paint) string { return textsafe.SanitizeLine(text) }
}

// outputOp requires a response. A nil headline is valid only under --json.
func (e *env) outputOp(resp *api.OpResponse, headline opHeadline) error {
	for _, warning := range resp.Warnings {
		e.errorLine("tkr: warning: %s", warning)
	}
	if e.jsonOut {
		return json.NewEncoder(e.out).Encode(resp)
	}
	e.printOp(resp, headline)
	return e.operationOutputError()
}

func (e *env) operationOutputError() error {
	if e.opOutputErr != nil {
		return fmt.Errorf("write operation output: %w", e.opOutputErr)
	}
	return nil
}

// printOp echoes all consequences except those already stated in the headline.
func (e *env) printOp(resp *api.OpResponse, headline opHeadline) {
	// AIDEV-NOTE: preserve this void interface while retaining the first
	// write error for outputOp. The mutation has already succeeded.
	if e.opOutputErr != nil {
		return
	}
	p := e.paint()
	plain, display := renderSafeLine(p, headline)
	if _, e.opOutputErr = fmt.Fprintln(e.out, display); e.opOutputErr != nil {
		return
	}
	for _, effect := range resp.Effects {
		if !e.effectInHeadline(resp, effect, plain) {
			if _, e.opOutputErr = fmt.Fprintln(e.out, e.effectLine(effect, p)); e.opOutputErr != nil {
				return
			}
		}
	}
}

func (e *env) effectInHeadline(resp *api.OpResponse, effect api.Effect, headline string) bool {
	if headline == e.formatEffect(effect, paint{}) || schedulingEffectInHeadline(resp, effect, headline) || metadataEffectInHeadline(resp, effect, headline) {
		return true
	}
	// AIDEV-NOTE: placement implies clearing reasons and the old deferral
	// on the placed tasks. New reasons and new deferrals are never implied.
	switch effect.Kind {
	case "reason_removed", "undeferred", "placed":
		if placementHeadline(resp, headline) && ((resp.Task != nil && resp.Task.ID == effect.TaskID) || taskInBatch(resp, effect.TaskID)) {
			return true
		}
	case "moved":
		if taskInBatch(resp, effect.TaskID) && placementHeadline(resp, headline) {
			return true
		}
	}
	if resp.Task == nil || effect.TaskID != resp.Task.ID {
		return false
	}
	action := titledAction(resp.Task, headline)
	added := strings.HasPrefix(action, "added")
	if added {
		action, _, _ = strings.Cut(action, " · ")
	}
	completed := strings.HasPrefix(action, "done · logged ")
	switch effect.Kind {
	case "created", "updated":
		return added
	case "moved":
		return (added && action != "added to triage (new)") || completed || action == "dropped" ||
			strings.HasPrefix(headline, fmt.Sprintf("#%d moved ", effect.TaskID))
	case "reason_added":
		return effect.Reason == "new" && action == "added to triage (new)"
	case "deferred":
		return action == "added, deferred to "+e.relDate(effect.To)
	case "blocked":
		return strings.HasPrefix(action, fmt.Sprintf("added, blocked b%d ", effect.BlockID))
	case "focus_set":
		return action == fmt.Sprintf("added, focus → #%d", effect.TaskID)
	case "completed":
		return completed
	case "entry_logged":
		return completed && resp.Entry != nil && effect.EntryID == resp.Entry.ID
	case "reopened", "dropped", "deleted":
		return action == effect.Kind
	case "undeleted":
		return action == "restored"
	default:
		return false
	}
}

// AIDEV-NOTE: match the closure's plain rendering independently of display.
// Strip the completion checkmark, then match the whole ID and title so words
// inside a user title cannot hide an unrelated effect (especially slips).
func titledAction(task *api.Task, headline string) string {
	action, ok := strings.CutPrefix(strings.TrimPrefix(headline, "✓ "), (paint{}).taskHeadline(task, ""))
	if !ok {
		return ""
	}
	return action
}

func taskInBatch(resp *api.OpResponse, id int64) bool {
	return slices.ContainsFunc(resp.Tasks, func(task api.Task) bool { return task.ID == id })
}

func placementHeadline(resp *api.OpResponse, headline string) bool {
	if resp.Block != nil && scheduledBlockHeadline(resp.Block, headline) {
		return true
	}
	if strings.HasPrefix(headline, "accepted ") && headline == fmt.Sprintf("accepted %d new tasks into the backlog", len(resp.Tasks)) {
		return true
	}
	if resp.Task == nil {
		return false
	}
	action := titledAction(resp.Task, headline)
	if strings.HasPrefix(action, "added") || strings.HasPrefix(action, "done · logged ") || action == "dropped" {
		return true
	}
	action, ok := strings.CutPrefix(headline, fmt.Sprintf("#%d ", resp.Task.ID))
	if !ok {
		return false
	}
	// AIDEV-NOTE: changing a deferral places the task before setting the new moment.
	return action == "kept in place" || strings.HasPrefix(action, "moved ") || strings.HasPrefix(action, "deferred to ") || action == "defer cleared"
}

func metadataEffectInHeadline(resp *api.OpResponse, effect api.Effect, headline string) bool {
	if task := resp.Task; task != nil && effect.TaskID == task.ID {
		if effect.Kind == "reason_added" && effect.Reason == "manual" {
			return headline == fmt.Sprintf("#%d sent to triage (manual)", task.ID)
		}
		if effect.Kind == "updated" {
			for _, verb := range []string{"tag", "project", "priority"} {
				if headline == (paint{}).metadataHeadline(task, verb) {
					return true
				}
			}
		}
	}
	return false
}

// Only block and reblock end in the quoted task title after the block ID.
func scheduledBlockHeadline(block *api.Block, headline string) bool {
	return strings.HasPrefix(headline, fmt.Sprintf("b%d ", block.ID)) &&
		strings.HasSuffix(headline, fmt.Sprintf(" #%d '%s'", block.TaskID, textsafe.SanitizeLine(block.TaskTitle)))
}

func schedulingEffectInHeadline(resp *api.OpResponse, effect api.Effect, headline string) bool {
	if effect.Kind == "focus_set" {
		return strings.HasPrefix(headline, fmt.Sprintf("focus → #%d '", effect.TaskID))
	}
	if block := resp.Block; block != nil {
		if effect.Kind == "placed" && effect.TaskID == block.TaskID {
			return scheduledBlockHeadline(block, headline)
		}
		// AIDEV-NOTE: unblock's resolved status and outcome's "marked" now
		// match their effect lines exactly. These remaining matches handle
		// the title in a schedule headline and the omitted cancellation reason.
		if effect.BlockID == block.ID {
			switch effect.Kind {
			case "blocked", "block_moved":
				return scheduledBlockHeadline(block, headline)
			case "block_cancelled":
				return headline == fmt.Sprintf("b%d cancelled", block.ID)
			}
		}
	}
	return false
}

// AIDEV-NOTE: a whole-line sanitizer would destroy paint's own escapes.
// If a renderer missed user input, fall back to its sanitized plain line.
func renderSafeLine(p paint, render func(paint) string) (plain, display string) {
	plain = render(paint{})
	display = textsafe.SanitizeLine(plain)
	if p.on && display == plain {
		display = render(p)
	}
	return plain, display
}

func (e *env) effectLine(effect api.Effect, p paint) string {
	_, display := renderSafeLine(p, func(p paint) string { return e.formatEffect(effect, p) })
	return display
}

func (e *env) formatEffect(effect api.Effect, p paint) string {
	id := p.taskID(effect.TaskID)
	from, to := textsafe.SanitizeLine(effect.From), textsafe.SanitizeLine(effect.To)
	switch effect.Kind {
	case "deferred":
		return fmt.Sprintf("%s deferred to %s", id, e.relDate(to))
	case "undeferred":
		return fmt.Sprintf("%s defer cleared", id)
	case "due_set":
		return id + " " + p.warn("due "+e.relDate(to))
	case "due_cleared":
		return fmt.Sprintf("%s due date cleared", id)
	case "blocked":
		return fmt.Sprintf("%s scheduled %s", p.blockID(effect.BlockID), e.relDate(domain.BlockSpanText(to)))
	case "block_moved":
		return fmt.Sprintf("%s moved %s → %s", p.blockID(effect.BlockID), e.movedSpanText(from), e.movedSpanText(to))
	case "block_cancelled":
		if effect.To == "" {
			return fmt.Sprintf("%s cancelled", p.blockID(effect.BlockID))
		}
		return fmt.Sprintf("%s cancelled · %s", p.blockID(effect.BlockID), to)
	case "block_resolved":
		if effect.To == "" {
			return fmt.Sprintf("%s resolved", p.blockID(effect.BlockID))
		}
		return fmt.Sprintf("%s %s", p.blockID(effect.BlockID), p.status(to))
	case "outcome_set":
		return fmt.Sprintf("%s marked %s", p.blockID(effect.BlockID), p.status(to))
	case "slipped":
		return fmt.Sprintf("%s %s", p.blockID(effect.BlockID), p.bad("slipped"))
	case "unslipped":
		return fmt.Sprintf("%s no longer counts as %s", p.blockID(effect.BlockID), p.bad("slipped"))
	case "reason_added":
		return fmt.Sprintf("%s needs triage (%s)", id, p.reason(effect.Reason))
	case "reason_removed":
		return fmt.Sprintf("%s no longer flagged %s", id, p.reason(effect.Reason))
	case "focus_set":
		return fmt.Sprintf("focus → %s", id)
	case "focus_cleared":
		return "focus cleared"
	case "interrupted":
		return "interrupted " + p.id("#"+from)
	case "retitled":
		return fmt.Sprintf("%s retitled %s → %s", id, p.quotedTitle(from), p.quotedTitle(to))
	case "entry_logged", "entry_updated", "entry_relinked", "entry_deleted", "entry_restored":
		return fmt.Sprintf("%s %s", p.entryID(effect.EntryID), strings.TrimPrefix(effect.Kind, "entry_"))
	case "reviewed":
		return "AI changes reviewed through " + p.id("#"+to)
	case "reverted":
		kind := domain.OpKind(to)
		if from == "" {
			return kind.ReversalText(effect.Via, "")
		}
		return kind.ReversalLabel(effect.Via, p.id("op "+from))
	default:
		return fmt.Sprintf("%s %s", id, textsafe.SanitizeLine(effect.Kind))
	}
}

func (e *env) movedSpanText(payload string) string {
	if formatted := domain.BlockSpanText(payload); formatted != payload {
		return e.relDate(formatted)
	}
	return payload
}

func (p paint) taskHeadline(task *api.Task, action string) string {
	return fmt.Sprintf("%s %s %s", p.taskID(task.ID), p.quotedTitle(task.Title), action)
}
