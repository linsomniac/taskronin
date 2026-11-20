package cli

import (
	"fmt"
	"strings"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/client"
	"taskronin/internal/textsafe"
)

// taskLine renders the shared compact task row in terminal columns.
func taskLine(t api.Task, today civil.Date, p paint) string {
	title := textsafe.Truncate(textsafe.SanitizeLine(t.Title), 40, "…")
	if len(t.Reasons) > 0 {
		reasons := make([]string, 0, len(t.Reasons))
		for _, reason := range t.Reasons {
			label := textsafe.SanitizeLine(reason.Reason)
			if reason.Reason == "slipped" {
				label += fmt.Sprintf("×%d", t.Slips.SinceTriage)
			}
			reasons = append(reasons, label)
		}
		title = p.warn("["+strings.Join(reasons, ",")+"]") + " " + title
	}
	columns := []string{p.taskID(t.ID), title}
	var metadata []string
	if t.Project != nil && *t.Project != "" {
		metadata = append(metadata, textsafe.SanitizeLine(*t.Project))
	}
	for _, tag := range t.Tags {
		metadata = append(metadata, "+"+textsafe.SanitizeLine(tag))
	}
	if len(metadata) > 0 {
		columns = append(columns, p.dim(strings.Join(metadata, " ")))
	}
	if t.DuePhase == "overdue" {
		columns = append(columns, p.bad("overdue"))
	} else if t.Due != nil {
		date := t.Due.Date.String()
		if !t.Due.Date.Before(today) && !t.Due.Date.After(today.AddDays(6)) {
			date = t.Due.Date.Weekday().String()[:3]
		}
		columns = append(columns, p.warn("due "+date))
	}
	return strings.Join(columns, "  ")
}

func (e *env) listTasks(query client.TaskQuery) (*api.Page[api.Task], error) {
	c, err := e.client()
	if err != nil {
		return nil, err
	}
	result := &api.Page[api.Task]{Items: []api.Task{}}
	for {
		page, err := c.ListTasks(e.ctx, query)
		if err != nil {
			return nil, err
		}
		result.Items = append(result.Items, page.Items...)
		if page.NextCursor == nil || *page.NextCursor == "" {
			return result, nil
		}
		query.Cursor = *page.NextCursor
	}
}
