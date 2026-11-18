package tui

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	tea "charm.land/bubbletea/v2"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/textsafe"
)

type bannerKind int

const (
	bannerInfo bannerKind = iota
	bannerError
)

type banner struct {
	kind       bannerKind
	text       string
	readError  bool       // Read failures recover on a matching load or when their screen closes.
	loadTarget *loadState // A successful read of this list or pane clears its notice.
	owner      screen     // Screen whose dismissal clears this banner.
}

func (a *App) setBanner(k bannerKind, text string) {
	a.banner = banner{kind: k, text: textsafe.SanitizeLine(text)}
}

func (a *App) setTruncationBanner(target *loadState) {
	a.setBanner(bannerInfo, "showing the first 2500")
	a.banner.loadTarget = target
}

func (a *App) clearScreenBanner(s screen) {
	if s != nil && a.banner.owner == s {
		a.banner = banner{}
	}
}

func (a *App) appendBanner(s string) {
	if s == "" {
		return
	}
	if a.banner.text != "" {
		a.banner.text += " · "
	}
	a.banner.text += textsafe.SanitizeLine(s)
}

// handleReadErr is the error policy for every screen load, including polling.
func (a *App) handleReadErr(err error, target ...*loadState) tea.Cmd {
	// AIDEV-NOTE: reads cannot have saved a mutation. An unanswered read must
	// not refresh itself forever; the next poll or explicit load can retry.
	var unknown *client.OutcomeUnknownError
	var cmd tea.Cmd
	switch {
	case errors.As(err, &unknown):
		a.setBanner(bannerError, "server did not answer")
	case errors.Is(err, client.ErrNotSent):
		a.setBanner(bannerError, "cannot reach the Taskronin server")
	default:
		cmd = a.handleErr(err)
	}
	a.banner.readError = true
	if len(target) > 0 {
		a.banner.loadTarget = target[0]
	}
	return cmd
}

func (a *App) handleErr(err error) tea.Cmd {
	// AIDEV-NOTE: mutation error policy. Outcome-unknown wins over a wrapped
	// definitive problem or not-sent error: an earlier attempt may have saved.
	// 412/generation/unknown only refetch; this layer never resends a mutation.
	var unknown *client.OutcomeUnknownError
	var problem *client.ProblemError
	switch {
	case errors.Is(err, errRetryStale):
		a.setBanner(bannerError, retryStaleBanner)
		return a.refresh()
	case errors.As(err, &unknown):
		a.setBanner(bannerError, "outcome unknown — refreshed; check before repeating")
		return a.refresh()
	case errors.Is(err, client.ErrNotSent):
		a.setBanner(bannerError, "cannot reach the Taskronin server — nothing was saved")
	case errors.As(err, &problem):
		switch {
		case problem.Problem.Code == "generation_mismatch":
			a.setBanner(bannerError, "database was restored — refreshed")
			return a.refresh()
		case problem.Status == http.StatusPreconditionFailed:
			a.setBanner(bannerError, "changed elsewhere — refreshed")
			return a.refresh()
		case problem.Problem.Code == "nothing_to_undo":
			a.setBanner(bannerError, "nothing to undo")
		case problem.Problem.Code == "nothing_to_redo":
			a.setBanner(bannerError, "nothing to redo")
		case problem.Problem.Code == "rate_limited" || problem.Problem.Code == "busy":
			a.setBanner(bannerError, "server busy — try again")
		default:
			detail := problem.Problem.Detail
			if len(problem.Problem.Errors) > 0 {
				fields := make([]string, 0, len(problem.Problem.Errors))
				for _, e := range problem.Problem.Errors {
					fields = append(fields, e.Field+": "+e.Message)
				}
				detail += " (" + strings.Join(fields, "; ") + ")"
			}
			if detail == "" {
				detail = problem.Problem.Title
			}
			if detail == "" {
				detail = problem.Problem.Code
			}
			if detail == "" {
				detail = fmt.Sprintf("HTTP %d", problem.Status)
			}
			a.setBanner(bannerError, detail)
		}
	default:
		a.setBanner(bannerError, err.Error())
	}
	return nil
}

func effectsLine(effs []api.Effect) string {
	parts := make([]string, 0, len(effs))
	for _, e := range effs {
		var part string
		switch e.Kind {
		case "slipped", "unslipped":
			part = fmt.Sprintf("b%d %s", e.BlockID, e.Kind)
		case "reason_added":
			part = fmt.Sprintf("#%d +%s", e.TaskID, e.Reason)
		case "interrupted":
			part = "interrupted #" + e.From
		case "focus_set":
			part = fmt.Sprintf("focus #%d", e.TaskID)
		case "focus_cleared":
			part = "focus cleared"
		case "block_cancelled":
			part = fmt.Sprintf("b%d cancelled", e.BlockID)
		case "block_resolved":
			to := e.To
			if to == "" {
				to = "resolved"
			}
			part = fmt.Sprintf("b%d %s", e.BlockID, to)
		case "completed":
			part = fmt.Sprintf("#%d completed", e.TaskID)
		case "entry_logged":
			part = fmt.Sprintf("e%d logged", e.EntryID)
		case "reverted":
			part = "reverted"
		default:
			continue
		}
		parts = append(parts, textsafe.SanitizeLine(part))
	}
	return strings.Join(parts, " · ")
}
