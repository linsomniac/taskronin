package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/client"
)

type entriesMsg struct {
	req        int
	to         civil.Date
	page       *api.Page[api.Entry]
	appendPage bool
	err        error
}

type logScreen struct {
	list       recordList[api.Entry]
	to         civil.Date
	nextCursor string
	pageReq    int
	state      loadState
}

func (s *logScreen) load(a *App) tea.Cmd {
	// AIDEV-NOTE: a full reload invalidates every older page at Cmd creation.
	// Keep pageReq until its reply arrives, even across reloads, so a second
	// append cannot overlap it. Old rows stay visible if this read fails, but
	// their cursor/range advance only after success, so paging and midnight
	// navigation can retry after failure.
	s.state = loadLoading
	return s.loadPage(a, "", false)
}

func (s *logScreen) loadPage(a *App, cursor string, appendPage bool) tea.Cmd {
	req, ctx, c := a.nextReq("entries"), a.ctx, a.api
	from, to := a.clock.Today.AddDays(-13), a.clock.Today
	q := client.EntryQuery{From: &from, To: &to, PageQuery: client.PageQuery{Limit: 100, Cursor: cursor}}
	if appendPage {
		s.pageReq = req
	}
	return func() tea.Msg {
		page, err := c.ListEntries(ctx, q)
		return entriesMsg{req: req, to: to, page: page, appendPage: appendPage, err: err}
	}
}

func (s *logScreen) update(a *App, msg tea.Msg) tea.Cmd {
	m, ok := msg.(entriesMsg)
	if !ok {
		return nil
	}
	if m.appendPage && m.req == s.pageReq {
		s.pageReq = 0
	}
	if !a.latest("entries", m.req) {
		return nil
	}
	if m.err != nil {
		return s.state.finish(a, m.err)
	}
	s.state.finish(a, nil)
	s.to = m.to
	items := slices.Clone(m.page.Items)
	if m.appendPage {
		items = append(s.list.items, items...)
	}
	s.list.setItems(items, func(e api.Entry) int64 { return e.ID })
	s.nextCursor = ""
	if m.page.NextCursor != nil {
		s.nextCursor = *m.page.NextCursor
	}
	return nil
}

// Entries are not selected tasks, even when linked; l uses focus/block/none.
func (*logScreen) selection() (int64, string, bool) { return 0, "", false }

func (s *logScreen) handle(a *App, k tea.KeyPressMsg) (tea.Cmd, bool) {
	switch k.String() {
	case "j", "k":
		delta := 1
		if k.String() == "k" {
			delta = -1
		}
		s.list.move(delta)
		if s.state != loadLoading && s.to != a.clock.Today {
			return s.load(a), true
		}
		if s.list.cursor == len(s.list.items)-1 && s.nextCursor != "" && s.pageReq == 0 && s.state != loadLoading {
			return s.loadPage(a, s.nextCursor, true), true
		}
		return nil, true
	case "l", "e", "L", "X":
	default:
		return nil, false
	}
	if a.inflight {
		a.setBanner(bannerInfo, "busy…")
		return nil, true
	}
	if k.String() == "l" {
		return a.openLog(a.clock.Today, a.logLink()), true
	}
	e, ok := s.list.selected()
	if !ok {
		return nil, true
	}
	switch k.String() {
	case "e":
		return a.editEntry(e), true
	case "L":
		return a.openForm(fmt.Sprintf("Relink e%d", e.ID), "task ID (#N or N) or - to unlink · enter saves · esc cancels", "",
			func(_ *App, value string) (string, sendFunc, error) {
				id, none, err := parseTaskRef(value)
				if err != nil {
					return "", nil, err
				}
				r := api.RelinkRequest{TaskID: api.Opt[int64]{Set: true, Null: none, V: id}}
				return "entry link updated", func(ctx context.Context, c API, key string) (*api.OpResponse, error) {
					return c.RelinkEntry(ctx, client.Mut{IfMatch: e.ETag, Key: key}, e.ID, r)
				}, nil
			}, nil).focus(), true
	case "X":
		return a.confirm(fmt.Sprintf("delete e%d? (y/n)", e.ID), func(a *App) tea.Cmd {
			return a.mutate("entry deleted", func(ctx context.Context, c API) (*api.OpResponse, error) {
				return c.DeleteEntry(ctx, client.Mut{IfMatch: e.ETag}, e.ID)
			}, nil, nil)
		}), true
	}
	return nil, true
}

func (s *logScreen) view(a *App, w, h int) string {
	notice := s.state.notice("log")
	if len(s.list.items) == 0 {
		if notice != "" {
			return a.line(notice, w)
		}
		return a.line("no log entries", w)
	}
	bodyHeight := h
	if notice != "" {
		bodyHeight = max(0, h-1)
	}
	headings := make(map[int]string)
	for i, e := range s.list.items {
		if i == 0 || e.WorkDate != s.list.items[i-1].WorkDate {
			headings[i] = logDay(e.WorkDate)
		}
	}
	body := s.list.renderHeadings(a, w, bodyHeight, func(e api.Entry, width int) string {
		text := fmt.Sprintf("%s  e%d", logDay(e.WorkDate), e.ID)
		if e.Time != nil {
			text += " " + hhmm(int(*e.Time))
		}
		text += " " + e.Title
		if e.TaskID != nil {
			text += fmt.Sprintf("  [#%d]", *e.TaskID)
		}
		if e.Reopened {
			text += " (reopened)"
		}
		return a.line(text, width)
	}, headings)
	if notice != "" {
		return clip(append([]string{a.line(notice+" · previous results", w)}, strings.Split(body, "\n")...), w, h)
	}
	return body
}

func logDay(date civil.Date) string {
	return fmt.Sprintf("%.3s %s", date.Weekday(), date.String()[5:])
}
