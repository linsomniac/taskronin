package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"taskronin/internal/api"
	"taskronin/internal/client"
)

type searchMsg struct {
	req  int
	page *api.Page[api.SearchHit]
	err  error
}

type searchScreen struct {
	q     string
	req   int
	page  *api.Page[api.SearchHit]
	list  recordList[api.SearchHit]
	state loadState
}

func newSearchOverlay(q string) screen { return &searchScreen{q: q} }

// Like recapLogPicker, reuse form editing without entering the mutation/retry
// path: submitting a query only opens a screen whose load performs the read.
type searchForm struct{ *form }

func (a *App) openSearch() tea.Cmd {
	f := a.openForm("Search", "enter searches · esc cancels", "", nil, nil)
	a.form = &searchForm{form: f}
	return f.focus()
}

func (f *searchForm) handle(a *App, k tea.KeyPressMsg) (tea.Cmd, bool) {
	if k.String() != "enter" {
		return f.form.handle(a, k)
	}
	q := strings.TrimSpace(f.input.Value())
	if q == "" {
		f.errText = "query required"
		return nil, true
	}
	a.form = nil
	return a.openOverlay(newSearchOverlay(q)), true
}

func (s *searchScreen) load(a *App) tea.Cmd {
	req, ctx, c := a.nextReq("search"), a.ctx, a.api
	q := client.SearchQuery{Q: s.q, Kind: "all", PageQuery: client.PageQuery{Limit: 50}}
	s.req, s.state = req, loadLoading
	return func() tea.Msg {
		page, err := c.Search(ctx, q)
		return searchMsg{req: req, page: page, err: err}
	}
}

func (s *searchScreen) update(a *App, msg tea.Msg) tea.Cmd {
	m, ok := msg.(searchMsg)
	// AIDEV-NOTE: a stacked search has its own latest request. The shared
	// counter only allocates unique IDs; it must not invalidate another instance.
	if !ok || m.req != s.req {
		return nil
	}
	if m.err != nil {
		return s.state.finish(a, m.err)
	}
	s.state.finish(a, nil)
	s.page = m.page
	s.list.setItems(m.page.Items, func(hit api.SearchHit) int64 {
		// Task and entry IDs have separate namespaces; keep selection on the
		// same kind of record when an own-op refresh reorders the results.
		if hit.Kind == "entry" {
			return -hit.ID
		}
		return hit.ID
	})
	return nil
}

func (s *searchScreen) selection() (int64, string, bool) {
	hit, ok := s.list.selected()
	if !ok {
		return 0, "", false
	}
	if hit.Kind == "task" {
		return hit.ID, hit.Title, true
	}
	if hit.TaskID != nil {
		return *hit.TaskID, hit.Title, true
	}
	return 0, "", false
}

func (s *searchScreen) handle(a *App, k tea.KeyPressMsg) (tea.Cmd, bool) {
	switch k.String() {
	case "r":
		if s.state == loadLoading {
			a.setBanner(bannerInfo, "busy…")
			return nil, true
		}
		return s.load(a), true
	case "j":
		s.list.move(1)
	case "k":
		s.list.move(-1)
	case "enter":
		if id, _, ok := s.selection(); ok {
			return a.openDetail(id), true
		}
		if hit, ok := s.list.selected(); ok && hit.Kind == "entry" {
			a.setBanner(bannerInfo, fmt.Sprintf("standalone entry e%d", hit.ID))
		}
	default:
		return nil, false
	}
	return nil, true
}

func (s *searchScreen) view(a *App, w, h int) string {
	lines := []string{a.line("Search: "+s.q, w)}
	if s.page == nil {
		text := "search not loaded · r retries"
		if s.state == loadLoading {
			text = "loading search…"
		}
		return clip(append(lines, a.line(text, w)), w, h)
	}
	if len(s.list.items) == 0 {
		text := "no results · r reload"
		if s.state == loadFailed {
			text = "search not loaded · r retries"
		}
		return clip(append(lines, a.line(text, w)), w, h)
	}
	hint := "j/k move · enter detail · r reload · esc back"
	if s.page.NextCursor != nil {
		hint += " · more results available; refine query"
	}
	if s.state == loadFailed {
		hint = "search not loaded · r retries · previous results"
	}
	lines = append(lines, a.line(hint, w))
	body := s.list.render(a, w, max(0, h-len(lines)), func(hit api.SearchHit, width int) string {
		if hit.Kind == "entry" {
			text := fmt.Sprintf("entry e%d", hit.ID)
			if hit.WorkDate != nil {
				text += " " + logDay(*hit.WorkDate)
			}
			return a.line(text+" "+hit.Title, width)
		}
		text := fmt.Sprintf("task #%d %s", hit.ID, hit.Title)
		if hit.Snippet != "" {
			text += " — " + hit.Snippet
		}
		return a.line(text, width)
	})
	return clip(append(lines, strings.Split(body, "\n")...), w, h)
}
