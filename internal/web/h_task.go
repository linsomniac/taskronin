package web

import (
	"fmt"
	"net/http"

	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/service"
)

type historyEvent struct {
	service.EventView
	CanRevert bool
}

type historyPage struct {
	TaskID domain.TaskID
	Items  []historyEvent
	More   string
}

func (a *app) taskDetail(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		a.renderError(w, r, err)
		return
	}
	s, _ := sessionFrom(r.Context())
	detail, err := a.o.Svc.GetTask(r.Context(), s.Actor, domain.TaskID(id))
	if err != nil {
		a.renderError(w, r, err)
		return
	}
	a.renderPage(w, r, http.StatusOK, "task", a.newPage(r, fmt.Sprintf("#%d %s", id, detail.Title), "task", detail))
}

func (a *app) taskHistory(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		a.renderError(w, r, err)
		return
	}
	s, _ := sessionFrom(r.Context())
	list, err := a.o.Svc.History(r.Context(), s.Actor, service.HistoryIn{
		TaskID: domain.TaskID(id), Limit: 100, Cursor: r.URL.Query().Get("cursor"),
	})
	if err != nil {
		a.renderError(w, r, err)
		return
	}
	p := historyPage{TaskID: domain.TaskID(id)}
	// AIDEV-NOTE: DF17: History pages events, not operations. Offer one
	// revert per OpID at its first event on this page, preserving service order.
	seen := make(map[domain.OpID]bool)
	for _, event := range list.Items {
		p.Items = append(p.Items, historyEvent{EventView: event,
			CanRevert: !seen[event.Op.OpID] && event.Op.Revertible && event.Op.State == domain.OpActive})
		seen[event.Op.OpID] = true
	}
	if list.NextCursor != "" {
		p.More = moreURL(r.URL, list.NextCursor)
	}
	a.renderPage(w, r, http.StatusOK, "history", a.newPage(r, fmt.Sprintf("History of #%d", id), "task", p))
}
