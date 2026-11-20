package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"taskronin/internal/api"
	"taskronin/internal/domain"
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
		if !e.effectInHeadline(resp, effect, headline) {
			if _, e.opOutputErr = fmt.Fprintln(e.out, textsafe.SanitizeLine(e.effectLine(effect))); e.opOutputErr != nil {
				return
			}
		}
	}
}

func (e *env) effectInHeadline(resp *api.OpResponse, effect api.Effect, headline string) bool {
	if headline == e.effectLine(effect) || schedulingEffectInHeadline(resp, effect, headline) || metadataEffectInHeadline(resp, effect, headline) {
		return true
	}
	if resp.Task == nil || effect.TaskID != resp.Task.ID {
		return false
	}
	// AIDEV-NOTE: compare the rendered effect above, then match the task prefix
	// before examining its action. Words in a title must never hide a slip.
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

func metadataEffectInHeadline(resp *api.OpResponse, effect api.Effect, headline string) bool {
	if task := resp.Task; task != nil && effect.TaskID == task.ID {
		if effect.Kind == "reason_added" && effect.Reason == "manual" {
			return headline == fmt.Sprintf("#%d sent to triage (manual)", task.ID)
		}
		if effect.Kind == "updated" {
			for _, verb := range []string{"tag", "project", "priority"} {
				if headline == metadataHeadline(task, verb) {
					return true
				}
			}
		}
	}
	if len(resp.Tasks) > 0 && headline == fmt.Sprintf("accepted %d new tasks into the backlog", len(resp.Tasks)) {
		for _, task := range resp.Tasks {
			if effect.TaskID == task.ID {
				return effect.Kind == "placed" || effect.Kind == "moved" || (effect.Kind == "reason_removed" && effect.Reason == "new")
			}
		}
	}
	return false
}

func schedulingEffectInHeadline(resp *api.OpResponse, effect api.Effect, headline string) bool {
	if effect.Kind == "focus_set" {
		return strings.HasPrefix(headline, fmt.Sprintf("focus → #%d '", effect.TaskID))
	}
	if block := resp.Block; block != nil {
		scheduled := headline == blockHeadline(block)
		if effect.Kind == "placed" && effect.TaskID == block.TaskID && scheduled {
			return true
		}
		if effect.BlockID == block.ID {
			switch effect.Kind {
			case "blocked", "block_moved":
				return scheduled
			case "block_resolved":
				return headline == fmt.Sprintf("b%d resolved %s", block.ID, effect.To)
			case "outcome_set":
				return headline == fmt.Sprintf("b%d outcome %s", block.ID, effect.To)
			case "block_cancelled":
				return headline == fmt.Sprintf("b%d cancelled", block.ID)
			}
		}
	}
	if resp.Task == nil || effect.TaskID != resp.Task.ID {
		return false
	}
	task := resp.Task
	prefix := fmt.Sprintf("#%d ", task.ID)
	deferred := task.Defer != nil && headline == prefix+"deferred to "+formatMoment(task.Defer)
	undeferred := headline == prefix+"no longer deferred"
	switch effect.Kind {
	case "placed":
		return headline == prefix+"kept" || strings.HasPrefix(headline, prefix+"moved ") || deferred || undeferred
	case "moved":
		return strings.HasPrefix(headline, prefix+"moved ")
	case "deferred":
		return deferred
	case "undeferred":
		// AIDEV-NOTE: changing a deferral places the task (clearing the
		// old defer) before setting the new one. Echo the final moment.
		return deferred || undeferred
	case "due_set":
		return task.Due != nil && headline == prefix+"due "+formatMoment(task.Due)
	case "due_cleared":
		return headline == prefix+"due date cleared"
	default:
		return false
	}
}

func (e *env) effectLine(effect api.Effect) string {
	id := effect.TaskID
	switch effect.Kind {
	case "deferred":
		return fmt.Sprintf("#%d deferred to %s", id, e.relDate(effect.To))
	case "undeferred":
		return fmt.Sprintf("#%d defer cleared", id)
	case "due_set":
		return fmt.Sprintf("#%d due %s", id, e.relDate(effect.To))
	case "due_cleared":
		return fmt.Sprintf("#%d due date cleared", id)
	case "blocked":
		return fmt.Sprintf("b%d scheduled %s", effect.BlockID, e.relDate(effect.To))
	case "block_moved":
		return fmt.Sprintf("b%d moved %s → %s", effect.BlockID, effect.From, effect.To)
	case "block_cancelled":
		if effect.To == "" {
			return fmt.Sprintf("b%d cancelled", effect.BlockID)
		}
		return fmt.Sprintf("b%d cancelled · %s", effect.BlockID, effect.To)
	case "block_resolved":
		if effect.To == "" {
			return fmt.Sprintf("b%d resolved", effect.BlockID)
		}
		return fmt.Sprintf("b%d %s", effect.BlockID, effect.To)
	case "outcome_set":
		return fmt.Sprintf("b%d marked %s", effect.BlockID, effect.To)
	case "slipped":
		return fmt.Sprintf("b%d slipped", effect.BlockID)
	case "unslipped":
		return fmt.Sprintf("b%d no longer counts as slipped", effect.BlockID)
	case "reason_added":
		return fmt.Sprintf("#%d needs triage (%s)", id, effect.Reason)
	case "reason_removed":
		return fmt.Sprintf("#%d no longer flagged %s", id, effect.Reason)
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
	case "reviewed":
		return "AI changes reviewed through #" + effect.To
	case "reverted":
		return domain.OpKind(effect.To).ReversalText(effect.Via, effect.From)
	default:
		return fmt.Sprintf("#%d %s", id, effect.Kind)
	}
}

func taskHeadline(task *api.Task, action string) string {
	return fmt.Sprintf("#%d '%s' %s", task.ID, task.Title, action)
}
