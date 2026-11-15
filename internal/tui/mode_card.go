package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/textsafe"
)

type cardDeck struct {
	queue                    []api.Task
	idx                      int
	seen                     map[int64]bool
	skipped, placed, blocked int
}

func (d *cardDeck) refill(tasks []api.Task) {
	if d.seen == nil {
		d.seen = make(map[int64]bool)
	}
	queued := make(map[int64]bool)
	for _, t := range d.queue[d.idx:] {
		queued[t.ID] = true
	}
	for _, t := range tasks {
		if !d.seen[t.ID] && !queued[t.ID] {
			d.queue = append(d.queue, t)
			queued[t.ID] = true
		}
	}
}

func (d *cardDeck) current() (api.Task, bool) {
	if d.idx >= len(d.queue) {
		return api.Task{}, false
	}
	if d.seen == nil {
		d.seen = make(map[int64]bool)
	}
	t := d.queue[d.idx]
	d.seen[t.ID] = true
	return t, true
}

func (d *cardDeck) advance() {
	if _, ok := d.current(); ok {
		d.idx++
	}
}

func (d *cardDeck) replace(t api.Task) {
	for i := range d.queue {
		// Retain confirmed placements in the history; skipped/unshown cards
		// still participate in bulk accept and need their latest precondition.
		if d.queue[i].ID == t.ID && (i >= d.idx || len(d.queue[i].Reasons) > 0) {
			d.queue[i] = t
		}
	}
}

func (d *cardDeck) placedOK(blocked bool) func(*App, *api.OpResponse) tea.Cmd {
	return func(_ *App, _ *api.OpResponse) tea.Cmd {
		d.placed++
		if blocked {
			d.blocked++
		}
		if t, ok := d.current(); ok {
			// Successful placements clear reasons. Keep that local fact even
			// when a delayed read still contains the old task, so bulk accept
			// cannot place it again. Skipped cards retain their reasons.
			for i := range d.queue {
				if d.queue[i].ID == t.ID {
					d.queue[i].Reasons = nil
				}
			}
		}
		d.advance()
		// App refreshes the visible screen after every successful op, including
		// the final card. The owning mode refills its deck from that read.
		return nil
	}
}

// update is shared by card-owning screens; target is their own loadTasks target.
func (d *cardDeck) update(a *App, msg tea.Msg, target string) tea.Cmd {
	switch m := msg.(type) {
	case tasksMsg:
		if m.target != target || !a.latest(target, m.req) {
			return nil
		}
		if m.err != nil {
			return a.handleReadErr(m.err)
		}
		for _, t := range m.tasks {
			d.replace(t)
		}
		d.refill(m.tasks)
		if m.truncated {
			a.setBanner(bannerInfo, "showing the first 2500")
		}
	case opMsg:
		var read *formReadError
		var unknown *client.OutcomeUnknownError
		var problem *client.ProblemError
		// AIDEV-NOTE: match handleErr's CF03 classification before inspecting
		// Current. A wrapped 412 does not make an unknown outcome definitive.
		if errors.As(m.err, &read) || errors.As(m.err, &unknown) || errors.Is(m.err, client.ErrNotSent) ||
			!errors.As(m.err, &problem) || problem.Status != http.StatusPreconditionFailed ||
			problem.Problem.Code == "generation_mismatch" {
			return nil
		}
		var fresh api.Task
		if json.Unmarshal(problem.Problem.Current, &fresh) != nil || fresh.ID <= 0 || fresh.ETag == "" {
			return nil // handleErr already scheduled a reload.
		}
		for i, t := range d.queue {
			if t.ID == fresh.ID && (i >= d.idx || len(t.Reasons) > 0) {
				d.replace(fresh)
				// The problem's Current is the card the user must review. Discard
				// outstanding loads (including handleErr's) that could overwrite it;
				// the next explicit load/poll gets a new request sequence as usual.
				a.nextReq(target)
				break
			}
		}
	}
	return nil
}

type cardMode struct {
	deck    *cardDeck
	startID int64
}

func newCardMode(startID int64) *cardMode {
	return &cardMode{deck: &cardDeck{}, startID: startID}
}

func (*cardMode) load(a *App) tea.Cmd { return a.loadTasks("triage-card", "triage") }

func (s *cardMode) update(a *App, msg tea.Msg) tea.Cmd {
	cmd := s.deck.update(a, msg, "triage-card")
	if m, ok := msg.(tasksMsg); ok && m.target == "triage-card" && a.latest(m.target, m.req) && m.err == nil && s.startID != 0 {
		for i, t := range s.deck.queue {
			if t.ID == s.startID {
				s.deck.idx = i
				break
			}
		}
		s.startID = 0
	}
	return cmd
}

func (s *cardMode) selection() (int64, string, bool) {
	t, ok := s.deck.current()
	return t.ID, t.Title, ok
}

func (s *cardMode) handle(a *App, k tea.KeyPressMsg) (tea.Cmd, bool) {
	return cardKeys(a, s.deck, k)
}

const cardLegend = "k keep · t top · b bottom · p place · d defer · B block · x done · D drop · s skip · A accept new"

func (s *cardMode) view(a *App, w, h int) string {
	if _, ok := s.deck.current(); !ok {
		return a.line(fmt.Sprintf("Triage clear (%d skipped)", s.deck.skipped), w)
	}
	return s.deck.view(a, w, h)
}

func (d *cardDeck) view(a *App, w, h int) string {
	t, ok := d.current()
	if !ok {
		return ""
	}
	lines := []string{a.styles.Header.Render(a.line(fmt.Sprintf("Card %d/%d", d.idx+1, len(d.queue)), w))}
	add := func(text string) { lines = append(lines, a.line(text, w)) }
	add(fmt.Sprintf("#%d %s", t.ID, t.Title))
	for _, r := range t.Reasons {
		add(r.Reason + " since " + r.Since.Format("2006-01-02 15:04"))
	}
	if slips := slipText(t.Slips); slips != "" {
		add(slips)
	}
	for _, field := range []struct {
		name  string
		value *api.Moment
	}{{"Due", t.Due}, {"Defer", t.Defer}} {
		if field.value != nil {
			text := field.name + ": " + field.value.Date.String()
			if field.value.Time != nil {
				text += " " + hhmm(int(*field.value.Time))
			}
			add(text)
		}
	}
	if t.Project != nil {
		add("Project: " + *t.Project)
	}
	add("Tags: " + strings.Join(t.Tags, ", "))
	if t.Priority != nil {
		add("Priority: " + *t.Priority)
	}
	notes := strings.Split(textsafe.Sanitize(t.Notes), "\n")
	for _, line := range notes[:min(5, len(notes))] {
		add(strings.ReplaceAll(line, "\t", "    "))
	}
	add(cardLegend)
	return clip(lines, w, h)
}

func slipText(s api.Slips) string {
	if s.Lifetime == 0 {
		return ""
	}
	return fmt.Sprintf("slipped ×%d lifetime, ×%d since last triage", s.Lifetime, s.SinceTriage)
}

func cardKeys(a *App, d *cardDeck, k tea.KeyPressMsg) (tea.Cmd, bool) {
	switch k.String() {
	case "k", "t", "b", "p", "d", "B", "x", "D", "s", "A":
	default:
		return nil, false
	}
	// Skip and forms also obey the guard: moving the deck while a placement
	// is pending would let its success hook advance past a different card.
	if a.inflight {
		a.setBanner(bannerInfo, "busy…")
		return nil, true
	}
	if k.String() == "A" {
		return d.acceptNew(a), true
	}
	t, ok := d.current()
	if !ok {
		return nil, true
	}
	after := d.placedOK(false)
	switch k.String() {
	case "k", "t", "b":
		how := map[string]string{"k": "keep", "t": "top", "b": "bottom"}[k.String()]
		return a.mutate("placed", func(ctx context.Context, c API) (*api.OpResponse, error) {
			return c.PlaceTask(ctx, client.Mut{IfMatch: t.ETag}, t.ID, api.PlaceRequest{How: how})
		}, after, nil), true
	case "p":
		return a.openForm(fmt.Sprintf("Place #%d", t.ID), "before/after N · enter saves · esc cancels", "",
			func(_ *App, value string) (string, sendFunc, error) {
				r, err := parseCardPlace(value)
				if err != nil {
					return "", nil, err
				}
				return "placed", func(ctx context.Context, c API, key string) (*api.OpResponse, error) {
					return c.PlaceTask(ctx, client.Mut{IfMatch: t.ETag, Key: key}, t.ID, r)
				}, nil
			}, after), true
	case "d":
		return a.openDefer(t, false, after), true
	case "B":
		return a.openBlock(t, d.placedOK(true)), true
	case "x":
		return a.mutate("completed", func(ctx context.Context, c API) (*api.OpResponse, error) {
			return c.CompleteTask(ctx, client.Mut{IfMatch: t.ETag}, t.ID, api.CompleteRequest{})
		}, after, nil), true
	case "D":
		return a.mutate("dropped", func(ctx context.Context, c API) (*api.OpResponse, error) {
			return c.DropTask(ctx, client.Mut{IfMatch: t.ETag}, t.ID)
		}, after, nil), true
	case "s":
		d.skipped++
		d.advance()
		if _, ok := d.current(); !ok {
			return a.current().load(a), true
		}
	}
	return nil, true
}

var cardPlacePattern = regexp.MustCompile(`(?i)^(before|after|b|a)\s+#?([1-9][0-9]*)$`)

func parseCardPlace(value string) (api.PlaceRequest, error) {
	m := cardPlacePattern.FindStringSubmatch(value)
	if m == nil {
		return api.PlaceRequest{}, fmt.Errorf("expected before/after N")
	}
	id, _, err := parseTaskRef(m[2])
	if err != nil {
		return api.PlaceRequest{}, err
	}
	how := "before"
	if strings.HasPrefix(strings.ToLower(m[1]), "a") {
		how = "after"
	}
	return api.PlaceRequest{How: how, Ref: id}, nil
}

func (d *cardDeck) acceptNew(a *App) tea.Cmd {
	r := api.AcceptNewRequest{}
	// Include skipped and not-yet-shown cards in their original served order.
	// Starting at a selection can append earlier unseen cards on a refill;
	// use their latest copy, but send each task only once.
	latest := make(map[int64]api.Task, len(d.queue))
	for _, t := range d.queue {
		latest[t.ID] = t
	}
	for _, queued := range d.queue {
		t, ok := latest[queued.ID]
		if !ok {
			continue
		}
		delete(latest, t.ID)
		if len(t.Reasons) == 1 && t.Reasons[0].Reason == "new" {
			r.Items = append(r.Items, struct {
				ID   int64  `json:"id"`
				ETag string `json:"etag"`
			}{t.ID, t.ETag})
		}
	}
	if len(r.Items) == 0 {
		a.setBanner(bannerInfo, "no new-only tasks")
		return nil
	}
	return a.confirm(fmt.Sprintf("accept %d new → bottom? (y/n)", len(r.Items)), func(a *App) tea.Cmd {
		return a.mutate("accepted new tasks", func(ctx context.Context, c API) (*api.OpResponse, error) {
			return c.AcceptNew(ctx, client.Mut{}, r)
		}, func(_ *App, _ *api.OpResponse) tea.Cmd {
			d.placed += len(r.Items)
			accepted := make(map[int64]bool, len(r.Items))
			for _, item := range r.Items {
				accepted[item.ID], d.seen[item.ID] = true, true
			}
			for i := range d.queue {
				if accepted[d.queue[i].ID] {
					d.queue[i].Reasons = nil
				}
			}
			remaining := slices.DeleteFunc(d.queue[d.idx:], func(t api.Task) bool { return accepted[t.ID] })
			d.queue = d.queue[:d.idx+len(remaining)]
			return nil // App's successful-op refresh reloads the owning mode.
		}, nil)
	})
}
