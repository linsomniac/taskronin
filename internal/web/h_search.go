package web

import (
	"net/http"
	"strings"

	"taskronin/internal/service"
)

type searchPage struct {
	Query, Kind string
	Searched    bool
	Items       []service.SearchHit
	More        string
}

type aiChangesPage struct {
	service.AIChanges
	IncludeReviewed bool
	More            string
	Review          *page
}

func (a *app) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := searchPage{Query: q.Get("q"), Kind: q.Get("kind")}
	if p.Kind == "" {
		p.Kind = "all"
	}
	p.Searched = len(p.Query) > service.MaxSearchQueryBytes || strings.TrimSpace(p.Query) != ""
	if p.Searched {
		s, _ := sessionFrom(r.Context())
		list, err := a.o.Svc.Search(r.Context(), s.Actor, service.SearchIn{
			Query: p.Query, Kind: p.Kind, Limit: 100, Cursor: q.Get("cursor"),
		})
		if err != nil {
			a.renderError(w, r, err)
			return
		}
		p.Items = list.Items
		if list.NextCursor != "" {
			p.More = moreURL(r.URL, list.NextCursor)
		}
	}
	a.renderPage(w, r, http.StatusOK, "search", a.newPage(r, "Search", "search", p))
}

func (a *app) aiChanges(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s, _ := sessionFrom(r.Context())
	includeReviewed := q.Get("all") == "1"
	changes, err := a.o.Svc.ListAIChanges(r.Context(), s.Actor, service.ListAIChangesIn{
		IncludeReviewed: includeReviewed, Limit: 100, Cursor: q.Get("cursor"),
	})
	if err != nil {
		a.renderError(w, r, err)
		return
	}
	p := aiChangesPage{AIChanges: changes, IncludeReviewed: includeReviewed}
	if changes.NextCursor != "" {
		p.More = moreURL(r.URL, changes.NextCursor)
	}
	// AIDEV-NOTE: Never acknowledge an unseen change. The review watermark
	// may cover only unreviewed ops on screen: offer it on the oldest nonempty
	// unreviewed-only page, through its highest seq (service order is newest first).
	// DF17 returns review to the default view, since this cursor may become empty.
	if !q.Has("all") && changes.NextCursor == "" && len(changes.Ops) > 0 {
		review := a.newPage(r, "", "", changes.Ops[0].Seq)
		review.Return = "/ui/ai-changes"
		p.Review = &review
	}
	a.renderPage(w, r, http.StatusOK, "aichanges", a.newPage(r, "AI changes", "ai", p))
}
