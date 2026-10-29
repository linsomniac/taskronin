package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/textsafe"
)

func (e *env) outputOp(resp *api.OpResponse, headline string) error {
	for _, warning := range resp.Warnings {
		e.errorLine("tkr: warning: %s", warning)
	}
	if e.jsonOut {
		return json.NewEncoder(e.out).Encode(resp)
	}
	e.printOp(resp, headline)
	if e.opOutputErr != nil {
		return fmt.Errorf("write operation output: %w", e.opOutputErr)
	}
	return nil
}

// printOp echoes all consequences except those already stated in the headline.
func (e *env) printOp(resp *api.OpResponse, headline string) {
	// AIDEV-NOTE: preserve this void interface while retaining the first
	// write error for outputOp. The mutation has already succeeded.
	if e.opOutputErr != nil {
		return
	}
	if _, e.opOutputErr = fmt.Fprintln(e.out, textsafe.SanitizeLine(headline)); e.opOutputErr != nil {
		return
	}
	for _, effect := range resp.Effects {
		if !effectInHeadline(resp, effect, headline) {
			if _, e.opOutputErr = fmt.Fprintln(e.out, textsafe.SanitizeLine(effectLine(effect))); e.opOutputErr != nil {
				return
			}
		}
	}
}

func effectInHeadline(resp *api.OpResponse, effect api.Effect, headline string) bool {
	if headline == effectLine(effect) {
		return true
	}
	if resp.Task == nil || effect.TaskID != resp.Task.ID {
		return false
	}
	// AIDEV-NOTE: match the task prefix before examining the action, so words
	// inside a user title cannot hide an unrelated effect (especially slips).
	action, ok := strings.CutPrefix(headline, taskHeadline(resp.Task, ""))
	if !ok {
		return false
	}
	added := strings.HasPrefix(action, "added")
	completed := strings.HasPrefix(action, "completed; ✓ logged ")
	switch effect.Kind {
	case "created":
		return added
	case "placed", "moved":
		return (added && action != "added to triage (new)") || completed || action == "dropped"
	case "reason_added":
		return effect.Reason == "new" && action == "added to triage (new)"
	case "deferred":
		return strings.HasPrefix(action, "added, deferred to ")
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

func effectLine(effect api.Effect) string {
	id := effect.TaskID
	switch effect.Kind {
	case "deferred":
		return fmt.Sprintf("#%d deferred to %s", id, effect.To)
	case "undeferred":
		return fmt.Sprintf("#%d no longer deferred", id)
	case "due_set":
		return fmt.Sprintf("#%d due %s", id, effect.To)
	case "due_cleared":
		return fmt.Sprintf("#%d due cleared", id)
	case "blocked":
		return fmt.Sprintf("b%d blocked %s", effect.BlockID, effect.To)
	case "block_moved":
		return fmt.Sprintf("b%d moved %s → %s", effect.BlockID, effect.From, effect.To)
	case "block_cancelled":
		return fmt.Sprintf("b%d cancelled (%s)", effect.BlockID, effect.To)
	case "block_resolved":
		return fmt.Sprintf("b%d resolved %s", effect.BlockID, effect.To)
	case "outcome_set":
		return fmt.Sprintf("b%d outcome %s", effect.BlockID, effect.To)
	case "slipped":
		return fmt.Sprintf("b%d slipped", effect.BlockID)
	case "unslipped":
		return fmt.Sprintf("b%d no longer counts as slipped", effect.BlockID)
	case "reason_added":
		return fmt.Sprintf("#%d +%s", id, effect.Reason)
	case "reason_removed":
		return fmt.Sprintf("#%d −%s", id, effect.Reason)
	case "focus_set":
		return fmt.Sprintf("focus → #%d", id)
	case "focus_cleared":
		return "focus cleared"
	case "interrupted":
		return "interrupted #" + effect.From
	case "retitled":
		return fmt.Sprintf("#%d retitled '%s' → '%s'", id, effect.From, effect.To)
	case "entry_logged", "entry_updated", "entry_relinked", "entry_deleted", "entry_restored":
		return fmt.Sprintf("e%d %s", effect.EntryID, strings.TrimPrefix(effect.Kind, "entry_"))
	case "reverted":
		return "reverted op " + effect.From
	default:
		return fmt.Sprintf("#%d %s", id, effect.Kind)
	}
}

func taskHeadline(task *api.Task, action string) string {
	return fmt.Sprintf("#%d '%s' %s", task.ID, task.Title, action)
}

func formatDate(date civil.Date) string {
	return date.Weekday().String()[:3] + " " + date.String()
}

func formatMoment(moment *api.Moment) string {
	text := formatDate(moment.Date)
	if moment.Time != nil {
		text += " " + moment.Time.String()
	}
	return text
}
