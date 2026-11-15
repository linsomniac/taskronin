package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"taskronin/internal/api"
	"taskronin/internal/client"
)

type aiMsg struct {
	req        int
	ai         *api.AIChanges
	appendPage bool
	err        error
}

type aiScreen struct {
	ops     []api.OpSummary
	next    *string
	list    recordList[api.OpSummary]
	req     int
	pageReq int
	loading bool
	loaded  bool
	loadAll bool
}

func newAIOverlay() screen { return &aiScreen{} }

func (s *aiScreen) load(a *App) tea.Cmd {
	// AIDEV-NOTE: own-op reloads restart at page 1 and invalidate the review
	// watermark immediately, even if the read fails. Retain pageReq until an
	// older append replies so it cannot overlap an append for the new list.
	s.loading, s.loaded, s.loadAll, s.next = true, false, false, nil
	return s.loadPage(a, "", false)
}

func (s *aiScreen) loadPage(a *App, cursor string, appendPage bool) tea.Cmd {
	req, ctx, c := a.nextReq("ai"), a.ctx, a.api
	q := client.PageQuery{Limit: 50, Cursor: cursor}
	s.req = req
	if appendPage {
		s.pageReq = req
	}
	return func() tea.Msg {
		changes, err := c.AIChanges(ctx, false, q)
		return aiMsg{req: req, ai: changes, appendPage: appendPage, err: err}
	}
}

func (s *aiScreen) update(a *App, msg tea.Msg) tea.Cmd {
	m, ok := msg.(aiMsg)
	if !ok {
		return nil
	}
	if m.appendPage && m.req == s.pageReq {
		s.pageReq = 0
	}
	if m.req != s.req || !a.latest("ai", m.req) {
		return nil
	}
	s.loading = false
	if m.err != nil {
		s.loadAll = false
		return a.handleReadErr(m.err)
	}
	ops := slices.Clone(m.ai.Ops)
	if m.appendPage {
		ops = append(s.ops, ops...)
	}
	s.ops, s.next, s.loaded = ops, m.ai.NextCursor, true
	s.list.setItems(s.ops, func(op api.OpSummary) int64 { return op.Seq })
	if s.loadAll {
		if s.next != nil {
			return s.loadPage(a, *s.next, true)
		}
		s.loadAll = false
		a.setBanner(bannerInfo, fmt.Sprintf("loaded all %d unreviewed changes — review them, then press A again", len(s.ops)))
	}
	return nil
}

func (*aiScreen) selection() (int64, string, bool) { return 0, "", false }

func (s *aiScreen) handle(a *App, k tea.KeyPressMsg) (tea.Cmd, bool) {
	switch k.String() {
	case "j", "k":
		delta := 1
		if k.String() == "k" {
			delta = -1
		}
		s.list.move(delta)
		if s.loaded && !s.loading && s.pageReq == 0 && s.next != nil && s.list.cursor == len(s.ops)-1 {
			return s.loadPage(a, *s.next, true), true
		}
		return nil, true
	case "A", "R":
	default:
		return nil, false
	}
	if a.inflight || s.loading || (k.String() == "A" && s.pageReq != 0) {
		a.setBanner(bannerInfo, "busy…")
		return nil, true
	}
	if !s.loaded {
		return s.load(a), true
	}
	if k.String() == "R" {
		if op, ok := s.list.selected(); ok {
			return a.mutate("reverted", func(ctx context.Context, c API) (*api.OpResponse, error) {
				return c.Revert(ctx, client.Mut{}, op.OpID)
			}, nil, nil), true
		}
		return nil, true
	}
	// AIDEV-NOTE: AI-review watermark. ThroughSeq reviews EVERY op <= it,
	// including older pages. Never send it until all pages are in the list;
	// loading the tail only invites another A after the user reviews it.
	// CF02 polls must leave this overlay alone so that second press is possible.
	if s.next != nil {
		s.loadAll = true
		return s.loadPage(a, *s.next, true), true
	}
	if len(s.ops) == 0 {
		a.setBanner(bannerInfo, "no unreviewed AI changes")
		return nil, true
	}
	through := s.ops[0].Seq
	for _, op := range s.ops[1:] {
		through = max(through, op.Seq)
	}
	return a.mutate("AI changes reviewed", func(ctx context.Context, c API) (*api.OpResponse, error) {
		return c.ReviewAIChanges(ctx, client.Mut{}, api.ReviewRequest{ThroughSeq: through})
	}, nil, nil), true
}

func (s *aiScreen) view(a *App, w, h int) string {
	lines := []string{a.line("AI changes · unreviewed", w)}
	if len(s.ops) == 0 {
		text := "no unreviewed AI changes"
		if s.loading {
			text = "loading…"
		} else if !s.loaded {
			text = "AI changes not loaded · A retries"
		}
		return clip(append(lines, a.line(text, w)), w, h)
	}
	hint := "j/k move · R revert · A review · esc back"
	if s.next != nil {
		hint += " · more…"
	}
	lines = append(lines, a.line(hint, w))
	body := s.list.render(a, w, max(0, h-len(lines)), func(op api.OpSummary, width int) string {
		text := fmt.Sprintf("%d %s %s by %s", op.Seq, op.At.Format("15:04"), op.Kind, op.Actor)
		if op.State == "reverted" {
			text += " [reverted]"
		}
		return a.line(text, width)
	})
	return clip(append(lines, strings.Split(body, "\n")...), w, h)
}
