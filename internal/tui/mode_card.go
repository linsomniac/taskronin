package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/textsafe"
)

type cardDeck struct {
	queue                    []api.Task // Distinct session tasks, in first-served order.
	idx                      int
	seen                     map[int64]bool // Actually presented, independent of rendering.
	placedIDs                map[int64]bool // Includes bulk-accepted tasks never presented.
	skipped, placed, blocked int
	startID                  int64
	state                    loadState
}

func (d *cardDeck) refill(tasks []api.Task) {
	_, hasCurrent := d.current()
	queued := make(map[int64]bool)
	for _, t := range d.queue {
		queued[t.ID] = true
	}
	for _, t := range tasks {
		if !queued[t.ID] {
			d.queue = append(d.queue, t)
			queued[t.ID] = true
		}
	}
	if !hasCurrent {
		start := d.idx
		for i, t := range d.queue {
			if t.ID == d.startID {
				start = i
				break
			}
		}
		d.selectNext(start)
	}
	d.startID = 0
	d.markCurrent()
	d.state = loadLoaded
}

func (d *cardDeck) current() (api.Task, bool) {
	if d.idx < 0 || d.idx >= len(d.queue) {
		return api.Task{}, false
	}
	return d.queue[d.idx], true
}

func (d *cardDeck) markCurrent() {
	t, ok := d.current()
	if !ok {
		return
	}
	if d.seen == nil {
		d.seen = make(map[int64]bool)
	}
	d.seen[t.ID] = true
}

func (d *cardDeck) selectNext(start int) {
	// AIDEV-NOTE: a selected start wraps to earlier unseen tasks once, in
	// server order. Keep their original queue entries; neither wrapping nor
	// refill appends history or duplicate IDs. Bulk-placed tasks are not seen
	// unless they were actually presented, and are never selected again.
	for offset := range len(d.queue) {
		i := (start + offset) % len(d.queue)
		t := d.queue[i]
		if !d.seen[t.ID] && !d.placedIDs[t.ID] {
			d.idx = i
			d.markCurrent()
			return
		}
	}
	d.idx = len(d.queue)
	d.state = loadLoading
}

func (d *cardDeck) advance() {
	if _, ok := d.current(); ok {
		d.selectNext(d.idx + 1)
	}
}

func (d *cardDeck) replace(t api.Task) {
	for i := range d.queue {
		// Retain confirmed placement decisions; skipped/unshown cards
		// still participate in bulk accept and need their latest precondition.
		if d.queue[i].ID == t.ID && !d.placedIDs[t.ID] {
			d.queue[i] = t
		}
	}
	d.markCurrent()
}

func (d *cardDeck) markPlaced(id int64) {
	if d.placedIDs == nil {
		d.placedIDs = make(map[int64]bool)
	}
	d.placedIDs[id] = true
}

func (d *cardDeck) placedOK(blocked bool) func(*App, *api.OpResponse) tea.Cmd {
	return func(_ *App, _ *api.OpResponse) tea.Cmd {
		d.placed++
		if blocked {
			d.blocked++
		}
		if t, ok := d.current(); ok {
			d.markPlaced(t.ID)
		}
		d.advance()
		// App refreshes the visible screen after every successful op, including
		// the final card. The owning mode refills its deck from that read.
		return nil
	}
}

func (d *cardDeck) load(a *App, target, view string) tea.Cmd {
	d.state = loadLoading
	return a.loadTasks(target, view)
}

// update is shared by card-owning screens; target is their own loadTasks target.
func (d *cardDeck) update(a *App, msg tea.Msg, target string) tea.Cmd {
	switch m := msg.(type) {
	case tasksMsg:
		if m.target != target || !a.latest(target, m.req) {
			return nil
		}
		if m.err != nil {
			return d.state.finish(a, m.err, a.modeScreen)
		}
		d.state.finish(a, nil)
		for _, t := range m.tasks {
			d.replace(t)
		}
		d.refill(m.tasks)
		if m.truncated {
			a.setTruncationBanner(&d.state)
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
		for _, t := range d.queue {
			if t.ID == fresh.ID && !d.placedIDs[t.ID] {
				d.replace(fresh)
				// The problem's Current is the card the user must review. Discard
				// outstanding loads (including handleErr's) that could overwrite it;
				// the next explicit load/poll gets a new request sequence as usual.
				// An empty deck still needs its reload to confirm exhaustion.
				if _, ok := d.current(); ok {
					a.nextReq(target)
					d.state = loadLoaded
				}
				break
			}
		}
	}
	return nil
}

type cardMode struct{ deck *cardDeck }

func newCardMode(startID int64) *cardMode {
	return &cardMode{deck: &cardDeck{startID: startID}}
}

func (s *cardMode) load(a *App) tea.Cmd { return s.deck.load(a, "triage-card", "triage") }

func (s *cardMode) update(a *App, msg tea.Msg) tea.Cmd {
	return s.deck.update(a, msg, "triage-card")
}

func (s *cardMode) selection() (int64, string, bool) {
	t, ok := s.deck.current()
	return t.ID, t.Title, ok
}

func (s *cardMode) handle(a *App, k tea.KeyPressMsg) (tea.Cmd, bool) {
	return cardKeys(a, s.deck, k)
}

const cardLegend = "k keep · t top · b bottom · p place · d defer · B block · x done · D drop · s skip · A accept new"

func (s *cardMode) view(a *App, w, h int) string { return s.deck.screenView(a, w, h) }

func (d *cardDeck) screenView(a *App, w, h int) string {
	if _, ok := d.current(); !ok {
		if d.state == loadFailed {
			return a.line(d.state.notice("triage"), w)
		}
		if d.state != loadLoaded {
			return a.line("loading…", w)
		}
		text := "Triage clear ✓"
		if d.skipped > 0 {
			text += fmt.Sprintf(" · %d skipped", d.skipped)
		}
		return a.line(text, w)
	}
	if notice := d.state.retainedNotice("triage", true); notice != "" {
		body := d.view(a, w, max(0, h-1))
		return clip(append([]string{a.line(notice, w)}, strings.Split(body, "\n")...), w, h)
	}
	return d.view(a, w, h)
}

func (d *cardDeck) view(a *App, w, h int) string {
	t, ok := d.current()
	if !ok {
		return ""
	}
	// Position counts distinct presentations, not the backing-array index:
	// starting at #2 of three presents Cards 1/3, 2/3, 3/3 as #2, #3, #1.
	total := len(d.queue)
	for id := range d.placedIDs {
		if !d.seen[id] {
			total--
		}
	}
	heading := fmt.Sprintf("Card %d/%d", len(d.seen), total)
	// AIDEV-NOTE: below 20×6 the plain card keeps the legend and gives all
	// remaining cells to content. Check the available body after any notice.
	if w < 20 || h < 6 {
		return plainCard(a, t, heading, w, h)
	}

	box := a.styles.Box
	inner := w - 4
	label := a.line(" "+heading+" ", w-3)
	top := box.TL + box.H + label + strings.Repeat(box.H, w-3-textsafe.Width(label)) + box.TR
	lines := []string{a.styles.Dim.Render(top)}
	edge := a.styles.Dim.Render(box.V)
	add := func(line string) {
		if len(lines) < h-2 { // Reserve the bottom border and legend.
			lines = append(lines, edge+" "+a.pad(line, inner)+" "+edge)
		}
	}
	id, title, hasTitle := strings.Cut(a.line(fmt.Sprintf("#%d %s", t.ID, t.Title), inner), " ")
	line := a.styles.ID.Render(id)
	if hasTitle {
		line += " " + a.styles.Head.Render(title)
	}
	add(line)
	for _, r := range t.Reasons {
		add(a.styles.Warn.Render(a.line(r.Reason+" · since "+a.reasonSince(r.Since), inner)))
	}
	if slips := slipText(t.Slips, "triage"); slips != "" {
		add(a.styles.Bad.Render(a.line(slips, inner)))
	}
	for _, field := range cardFields(t) {
		add(a.line(field, inner))
	}
	if t.Notes != "" {
		for _, note := range cardNotes(t.Notes) {
			add(a.styles.Dim.Render(a.line(note, inner)))
		}
	}
	lines = append(lines, a.styles.Dim.Render(box.BL+strings.Repeat(box.H, w-2)+box.BR), a.styles.Dim.Render(a.line(cardLegend, w)))
	return strings.Join(lines, "\n")
}

func plainCard(a *App, t api.Task, heading string, w, h int) string {
	lines := []string{a.styles.Header.Render(a.line(heading, w))}
	add := func(text string) { lines = append(lines, a.line(text, w)) }
	add(fmt.Sprintf("#%d %s", t.ID, t.Title))
	for _, r := range t.Reasons {
		add(r.Reason + " since " + r.Since.Format("2006-01-02 15:04"))
	}
	if slips := slipText(t.Slips, "last triage"); slips != "" {
		add(slips)
	}
	for _, field := range cardFields(t) {
		add(field)
	}
	for _, note := range cardNotes(t.Notes) {
		add(note)
	}
	lines = lines[:min(len(lines), max(0, h-1))]
	add(cardLegend)
	return clip(lines, w, h)
}

func cardFields(t api.Task) []string {
	var fields []string
	for _, field := range []struct {
		name  string
		value *api.Moment
	}{{"Due", t.Due}, {"Defer", t.Defer}} {
		if field.value != nil {
			text := field.name + ": " + field.value.Date.String()
			if field.value.Time != nil {
				text += " " + hhmm(int(*field.value.Time))
			}
			fields = append(fields, text)
		}
	}
	if t.Project != nil {
		fields = append(fields, "Project: "+*t.Project)
	}
	if len(t.Tags) > 0 {
		fields = append(fields, "Tags: "+strings.Join(t.Tags, ", "))
	}
	if t.Priority != nil {
		fields = append(fields, "Priority: "+*t.Priority)
	}
	return fields
}

func cardNotes(notes string) []string {
	lines := strings.SplitN(notes, "\n", 6)
	lines = lines[:min(len(lines), 5)]
	for i, line := range lines {
		lines[i] = strings.ReplaceAll(line, "\t", "    ")
	}
	return lines
}

func slipText(s api.Slips, since string) string {
	if s.Lifetime == 0 {
		return ""
	}
	return fmt.Sprintf("slipped ×%d lifetime, ×%d since %s", s.Lifetime, s.SinceTriage, since)
}

func cardKeys(a *App, d *cardDeck, k tea.KeyPressMsg) (tea.Cmd, bool) {
	switch cardActions.command(k) {
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
	switch cardActions.command(k) {
	case "k", "t", "b":
		how := map[string]string{"k": "keep", "t": "top", "b": "bottom"}[k.String()]
		return a.mutate("placed", func(ctx context.Context, c API) (*api.OpResponse, error) {
			return c.PlaceTask(ctx, client.Mut{IfMatch: t.ETag}, t.ID, api.PlaceRequest{How: how})
		}, after, nil), true
	case "p":
		cmd := a.openForm(fmt.Sprintf("Place #%d", t.ID), "before/after N · enter saves · esc cancels", "",
			func(_ *App, value string) (string, sendFunc, error) {
				r, err := parseCardPlace(value)
				if err != nil {
					return "", nil, err
				}
				return "placed", func(ctx context.Context, c API, key string) (*api.OpResponse, error) {
					return c.PlaceTask(ctx, client.Mut{IfMatch: t.ETag, Key: key}, t.ID, r)
				}, nil
			}, after).focus()
		return cmd, true
	case "d":
		return a.openDefer(t, false, after).focus(), true
	case "B":
		return a.openBlock(t, d.placedOK(true)).focus(), true
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
	for _, t := range d.queue {
		if !d.placedIDs[t.ID] && len(t.Reasons) == 1 && t.Reasons[0].Reason == "new" {
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
	return a.confirm(fmt.Sprintf("accept %d new → bottom?", len(r.Items)), func(a *App) tea.Cmd {
		return a.mutate("accepted new tasks", func(ctx context.Context, c API) (*api.OpResponse, error) {
			return c.AcceptNew(ctx, client.Mut{}, r)
		}, func(_ *App, _ *api.OpResponse) tea.Cmd {
			d.placed += len(r.Items)
			for _, item := range r.Items {
				d.markPlaced(item.ID)
			}
			if t, ok := d.current(); ok && d.placedIDs[t.ID] {
				d.advance()
			}
			return nil // App's successful-op refresh reloads the owning mode.
		}, nil)
	})
}
