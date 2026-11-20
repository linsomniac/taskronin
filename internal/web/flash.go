package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"taskronin/internal/domain"
	"taskronin/internal/service"
)

const flashCookie = "tr_flash"

func flashLines(res *service.OpResult) []string {
	lines := make([]string, 0, len(res.Effects)+len(res.Warnings)+2)
	for _, effect := range res.Effects {
		lines = append(lines, effectLine(effect))
	}
	lines = append(lines, res.Warnings...)
	if res.NoChange {
		lines = append(lines, "no change")
	}
	if res.Replayed {
		lines = append(lines, "already applied")
	}
	return lines
}

// AIDEV-NOTE: Mirror effectLine in internal/cli/effects.go and effectsLine in
// internal/tui/banner.go; Plan E Task 3 holds the wording table. The CLI adds
// terminal styling to sanitized tokens and uses relative dates. Both render
// block ranges with BlockSpanText; the web keeps absolute dates. This copy uses
// service.Effect because A§1.3 forbids web importing cli or api (DF21).
func effectLine(effect service.Effect) string {
	id := effect.TaskID
	switch effect.Kind {
	case service.EffDeferred:
		return fmt.Sprintf("#%d deferred to %s", id, effect.To)
	case service.EffUndeferred:
		return fmt.Sprintf("#%d defer cleared", id)
	case service.EffDueSet:
		return fmt.Sprintf("#%d due %s", id, effect.To)
	case service.EffDueCleared:
		return fmt.Sprintf("#%d due date cleared", id)
	case service.EffBlocked:
		return fmt.Sprintf("b%d scheduled %s", effect.BlockID, domain.BlockSpanText(effect.To))
	case service.EffBlockMoved:
		return fmt.Sprintf("b%d moved %s → %s", effect.BlockID, domain.BlockSpanText(effect.From), domain.BlockSpanText(effect.To))
	case service.EffBlockCancelled:
		if effect.To == "" {
			return fmt.Sprintf("b%d cancelled", effect.BlockID)
		}
		return fmt.Sprintf("b%d cancelled · %s", effect.BlockID, effect.To)
	case service.EffBlockResolved:
		if effect.To == "" {
			return fmt.Sprintf("b%d resolved", effect.BlockID)
		}
		return fmt.Sprintf("b%d %s", effect.BlockID, effect.To)
	case service.EffOutcomeSet:
		return fmt.Sprintf("b%d marked %s", effect.BlockID, effect.To)
	case service.EffSlipped:
		return fmt.Sprintf("b%d slipped", effect.BlockID)
	case service.EffUnslipped:
		return fmt.Sprintf("b%d no longer counts as slipped", effect.BlockID)
	case service.EffReasonAdded:
		return fmt.Sprintf("#%d needs triage (%s)", id, effect.Reason)
	case service.EffReasonRemoved:
		return fmt.Sprintf("#%d no longer flagged %s", id, effect.Reason)
	case service.EffFocusSet:
		return fmt.Sprintf("focus → #%d", id)
	case service.EffFocusCleared:
		return "focus cleared"
	case service.EffInterrupted:
		return "interrupted #" + effect.From
	case service.EffRetitled:
		return fmt.Sprintf("#%d retitled '%s' → '%s'", id, effect.From, effect.To)
	case service.EffEntryLogged, service.EffEntryUpdated, service.EffEntryRelinked, service.EffEntryDeleted, service.EffEntryRestored:
		return fmt.Sprintf("e%d %s", effect.EntryID, strings.TrimPrefix(string(effect.Kind), "entry_"))
	case service.EffReviewed:
		return "AI changes reviewed through #" + effect.To
	case service.EffReverted:
		return domain.OpKind(effect.To).ReversalText(effect.Via, effect.From)
	default:
		return fmt.Sprintf("#%d %s", id, effect.Kind)
	}
}

func (a *app) setFlash(w http.ResponseWriter, r *http.Request, lines []string) {
	s, ok := sessionFrom(r.Context())
	if !ok || s.CSRF == "" {
		return
	}
	text := strings.Join(lines, "\n")
	// AIDEV-NOTE: 1400 UTF-8 bytes leave room for base64 expansion, the
	// SHA-256 MAC and all cookie attributes within 2 KiB. Never split a rune.
	const maxText = 1400
	if len(text) > maxText {
		end := maxText - len("…")
		for !utf8.RuneStart(text[end]) {
			end--
		}
		text = text[:end] + "…"
	}
	value := base64.RawURLEncoding.EncodeToString([]byte(text)) + "." +
		base64.RawURLEncoding.EncodeToString(flashMAC(s.CSRF, []byte(text)))
	setCookie(w, flashCookie, value, "/ui/", time.Time{}, 60)
}

func (a *app) readFlash(w http.ResponseWriter, r *http.Request) []string {
	cookies := r.CookiesNamed(flashCookie)
	if len(cookies) == 0 {
		return nil
	}
	setCookie(w, flashCookie, "", "/ui/", time.Time{}, -1)
	s, ok := sessionFrom(r.Context())
	if !ok || s.CSRF == "" {
		return nil
	}
	// AIDEV-NOTE: Another loopback port can plant a longer-path cookie
	// before ours. Like authenticateSession, try at most four in wire order
	// and use the first authenticated value; clear only our /ui/ cookie.
	for _, cookie := range cookies[:min(len(cookies), 4)] {
		if len(cookie.Value) > 2048 {
			continue
		}
		body, signature, ok := strings.Cut(cookie.Value, ".")
		if !ok {
			continue
		}
		text, err := base64.RawURLEncoding.DecodeString(body)
		if err != nil || len(text) == 0 || !utf8.Valid(text) {
			continue
		}
		mac, err := base64.RawURLEncoding.DecodeString(signature)
		if err != nil || !hmac.Equal(mac, flashMAC(s.CSRF, text)) {
			continue
		}
		return strings.Split(string(text), "\n")
	}
	return nil
}

func flashMAC(csrf string, text []byte) []byte {
	h := hmac.New(sha256.New, []byte(csrf))
	_, _ = h.Write(text)
	return h.Sum(nil)
}
