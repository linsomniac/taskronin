package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/client"
	"github.com/linsomniac/taskronin/internal/textsafe"
)

type detailScreen struct {
	state    loadState
	id       int64
	req      int
	detail   *api.TaskDetail
	viewport viewport.Model
}

type detailMsg struct {
	req    int
	detail *api.TaskDetail
	err    error
}

func (a *App) openDetail(id int64) tea.Cmd {
	return a.openOverlay(&detailScreen{id: id, viewport: viewport.New()})
}

func (s *detailScreen) load(a *App) tea.Cmd {
	req, ctx, c, id := a.nextReq("detail"), a.ctx, a.api, s.id
	s.req, s.state = req, loadLoading
	return func() tea.Msg {
		d, err := c.GetTask(ctx, id)
		return detailMsg{req: req, detail: d, err: err}
	}
}

func (s *detailScreen) update(a *App, msg tea.Msg) tea.Cmd {
	m, ok := msg.(detailMsg)
	if !ok || m.req != s.req || !a.latest("detail", m.req) {
		return nil
	}
	if m.err != nil {
		return s.state.finish(a, m.err, s)
	}
	s.state.finish(a, nil)
	s.detail = m.detail
	return nil
}

func (s *detailScreen) selection() (int64, string, bool) {
	if s.detail == nil {
		return 0, "", false
	}
	return s.id, s.detail.Title, true
}

func (s *detailScreen) handle(a *App, k tea.KeyPressMsg) (tea.Cmd, bool) {
	if a.inflight && (k.String() == "t") {
		a.setBanner(bannerInfo, "busy…")
		return nil, true
	}
	switch detailKeys.command(k) {
	case "j":
		s.viewport.ScrollDown(1)
	case "k":
		s.viewport.ScrollUp(1)
	case "e":
		if s.detail != nil {
			return a.editTask(s.id), true
		}
	case "h":
		if s.detail != nil {
			return a.openHistory(s.id), true
		}
	case "t":
		if s.detail != nil {
			return s.tagsForm(a), true
		}
	default:
		return nil, false
	}
	return nil, true
}

func (s *detailScreen) tagsForm(a *App) tea.Cmd {
	id, etag := s.id, s.detail.ETag
	return a.openForm(fmt.Sprintf("Tags for #%d", id), "+tag or tag adds · -tag removes · enter saves · esc cancels", "",
		func(_ *App, value string) (string, sendFunc, error) {
			tokens := strings.Fields(value)
			if len(tokens) == 0 {
				return "", nil, fmt.Errorf("enter at least one tag")
			}
			r := api.UpdateTaskRequest{}
			for _, token := range tokens {
				if token == "+" || token == "-" {
					return "", nil, fmt.Errorf("tag name required after %q", token)
				}
				if strings.HasPrefix(token, "-") {
					r.TagsRemove = append(r.TagsRemove, strings.TrimPrefix(token, "-"))
				} else {
					r.TagsAdd = append(r.TagsAdd, strings.TrimPrefix(token, "+"))
				}
			}
			return "tags updated", func(ctx context.Context, c API, key string) (*api.OpResponse, error) {
				return c.UpdateTask(ctx, client.Mut{IfMatch: etag, Key: key}, id, r)
			}, nil
		}, nil).focus()
}

func (s *detailScreen) view(a *App, w, h int) string {
	if s.detail == nil {
		return a.line(s.state.notice("detail"), w)
	}
	d := s.detail
	var lines []string
	add := func(text string) { lines = append(lines, textsafe.SanitizeLine(text)) }
	heading := func(title string) { lines = append(lines, a.styles.Head.Render(a.line(title, w))) }
	add(fmt.Sprintf("#%d %s", s.id, d.Title))
	add("e edit · t tags · h history · j/k scroll · esc back")
	add("Status: " + d.Status + " · View: " + d.View)
	for _, field := range []struct {
		name  string
		value *string
	}{{"Project", d.Project}, {"Priority", d.Priority}} {
		if field.value != nil {
			add(field.name + ": " + *field.value)
		}
	}
	add("Tags: " + strings.Join(d.Tags, ", "))
	add("Origin: " + d.Origin)
	for _, field := range []struct {
		name  string
		value *api.Moment
	}{{"Defer", d.Defer}, {"Due", d.Due}} {
		if field.value != nil {
			text := field.name + ": " + field.value.Date.String()
			if field.value.Time != nil {
				text += " " + hhmm(int(*field.value.Time))
			}
			add(text)
		}
	}
	add(fmt.Sprintf("Slips: %d lifetime, %d since triage", d.Slips.Lifetime, d.Slips.SinceTriage))
	heading("Reasons")
	for _, r := range d.Reasons {
		add(r.Reason + " since " + r.Since.Format("2006-01-02 15:04"))
	}
	heading("Notes")
	lines = append(lines, strings.Split(textsafe.Sanitize(d.Notes), "\n")...)
	heading("Blocks")
	for _, b := range d.Blocks {
		text := fmt.Sprintf("b%d %s %s %s", b.ID, b.Date, spanText(b.Start, b.End), b.Status)
		if b.Slipped {
			text += " (slipped)"
		}
		if b.NeedsReblock {
			text += " (reblock?)"
		}
		add(text)
	}
	heading("Entries")
	for _, e := range d.Entries {
		text := fmt.Sprintf("e%d %s", e.ID, e.WorkDate)
		if e.Time != nil {
			text += " " + hhmm(int(*e.Time))
		}
		add(text + " " + e.Title + " (" + e.Kind + ")")
	}
	if len(d.Interruptions) > 0 {
		heading("Interruptions")
		for _, i := range d.Interruptions {
			// The formatter emits only a time and styled IDs here. Append directly to preserve their SGR.
			lines = append(lines, interruptionLine(i, nil, nil, a.styles.ID))
		}
	}
	if notice := s.state.retainedNotice("detail", true); notice != "" {
		body := overlayViewport(&s.viewport, lines, w, max(0, h-1))
		return clip(append([]string{a.line(notice, w)}, strings.Split(body, "\n")...), w, h)
	}
	return overlayViewport(&s.viewport, lines, w, h)
}

// overlayViewport receives sanitized lines and clips before viewport styling.
func overlayViewport(v *viewport.Model, lines []string, w, h int) string {
	v.SetWidth(max(0, w))
	v.SetHeight(max(0, h))
	for i, line := range lines {
		// AIDEV-NOTE: ANSI width treats tabs as zero cells, but Lip Gloss
		// expands them. Expand first so clipping and scroll heights agree.
		line = strings.ReplaceAll(line, "\t", "    ")
		lines[i] = textsafe.Truncate(line, max(0, w), "…")
	}
	v.SetContentLines(lines)
	return v.View()
}
