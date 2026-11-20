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

// AIDEV-NOTE: strip the completion checkmark, then match the whole ID and
// title so words inside a user title cannot hide an unrelated effect (especially slips).
func titledAction(task *api.Task, headline string) string {
	action, ok := strings.CutPrefix(strings.TrimPrefix(headline, "✓ "), taskHeadline(task, ""))
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
				if headline == metadataHeadline(task, verb) {
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
		strings.HasSuffix(headline, fmt.Sprintf(" #%d '%s'", block.TaskID, block.TaskTitle))
}

func schedulingEffectInHeadline(resp *api.OpResponse, effect api.Effect, headline string) bool {
	if effect.Kind == "focus_set" {
		return strings.HasPrefix(headline, fmt.Sprintf("focus → #%d '", effect.TaskID))
	}
	if block := resp.Block; block != nil {
		if effect.Kind == "placed" && effect.TaskID == block.TaskID {
			return scheduledBlockHeadline(block, headline)
		}
		// AIDEV-NOTE: structural matches follow the headline strings in cmd_block.go.
		if effect.BlockID == block.ID {
			switch effect.Kind {
			case "blocked", "block_moved":
				return scheduledBlockHeadline(block, headline)
			case "block_resolved":
				return headline == fmt.Sprintf("b%d resolved %s", block.ID, effect.To)
			case "outcome_set":
				return headline == fmt.Sprintf("b%d outcome %s", block.ID, effect.To)
			case "block_cancelled":
				return headline == fmt.Sprintf("b%d cancelled", block.ID)
			}
		}
	}
	return false
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
