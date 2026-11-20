package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/textsafe"
)

func init() { register(todayCommand) }

func todayCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use: "today", Short: "Show today's dashboard", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error { return e.today() },
	}
}

func (e *env) today() error {
	c, err := e.client()
	if err != nil {
		return err
	}
	view, _, _, err := c.Today(e.ctx, "")
	if err != nil {
		return err
	}
	if e.jsonOut {
		return json.NewEncoder(e.out).Encode(view)
	}
	z, err := civil.LoadZone(view.Clock.Timezone)
	if err != nil {
		return err
	}
	// AIDEV-NOTE: Today interruptions contain IDs, not titles. Reuse embedded
	// task metadata, fetching only destinations absent from the dashboard (for
	// example a previous focus now in the backlog). Finish reads before output.
	titles := dashboardTitles(view)
	blocks := make(map[int64]api.Block, len(view.Blocks))
	for _, b := range view.Blocks {
		blocks[b.ID] = b
	}
	for _, in := range view.Interruptions {
		if _, ok := titles[in.ToTaskID]; !ok {
			task, err := c.GetTask(e.ctx, in.ToTaskID)
			if err != nil {
				return err
			}
			titles[task.ID] = task.Title
		}
		if in.FromBlockID != nil {
			if _, ok := blocks[*in.FromBlockID]; !ok {
				b, err := c.GetBlock(e.ctx, *in.FromBlockID)
				if err != nil {
					return err
				}
				blocks[b.ID] = *b
			}
		}
	}
	p := e.paint()
	var out strings.Builder
	fmt.Fprintf(&out, "%s · %s\n", view.Clock.Now.In(z.Location()).Format("Mon 2006-01-02 15:04"), textsafe.SanitizeLine(view.Clock.Timezone))
	focus := "no focus"
	b := view.CurrentBlock
	if f := view.Focus; f != nil {
		title := textsafe.SanitizeLine(f.Title)
		if b != nil && b.TaskID == f.ID {
			title += fmt.Sprintf(" (→%s)", b.End)
		}
		focus = p.ok("▶") + " " + p.taskID(f.ID) + " " + p.ok(title)
	} else if b != nil {
		focus = p.ok("▷") + " " + p.taskID(b.TaskID) + " " + p.ok(fmt.Sprintf("%s (→%s)", textsafe.SanitizeLine(b.TaskTitle), b.End))
	}
	empty := len(view.Blocks) == 0 && view.TriageCount == 0 && len(view.Entries) == 0 && len(view.Interruptions) == 0
	if empty {
		fmt.Fprintf(&out, "%s\n\n%s\n", focus, p.dim("Nothing scheduled, nothing to triage — tkr plan shapes the day"))
	}
	for _, banner := range view.Banners {
		fmt.Fprintln(&out, p.warn("⚠ "+textsafe.SanitizeLine(banner.Message)))
	}
	if !empty {
		fmt.Fprintf(&out, "%s\n\n", focus)
	}
	if len(view.Blocks) > 0 {
		fmt.Fprintln(&out, p.head("Blocks"))
		for _, b := range view.Blocks {
			status := p.status(b.Status)
			switch {
			case b.Current:
				status = p.ok("◀ now")
			case b.NeedsReblock:
				status = p.warn("reblock?")
			}
			fmt.Fprintf(&out, "  %s %s–%s  %s %s  %s\n", p.blockID(b.ID), b.Start, b.End, p.taskID(b.TaskID), textsafe.SanitizeLine(b.TaskTitle), status)
		}
		fmt.Fprintln(&out)
	}
	for _, section := range []struct {
		title string
		tasks []api.Task
	}{
		{fmt.Sprintf("Triage %d", view.TriageCount), view.TriageTop[:min(5, len(view.TriageTop))]},
		{"Overdue", view.Overdue},
		{"Due soon", view.DueSoon},
	} {
		if len(section.tasks) == 0 {
			continue
		}
		fmt.Fprintln(&out, p.head(section.title))
		for _, task := range section.tasks {
			fmt.Fprintf(&out, "  %s\n", taskLine(task, view.Clock.Today, p))
		}
		fmt.Fprintln(&out)
	}
	if len(view.Entries) > 0 {
		fmt.Fprintln(&out, p.head("Log"))
		for _, entry := range view.Entries {
			marker := "-"
			if entry.Kind == "complete" {
				marker = p.ok("✓")
			}
			fmt.Fprintf(&out, "  %s %s", marker, textsafe.SanitizeLine(entry.Title))
			if entry.TaskID != nil {
				fmt.Fprintf(&out, "  [%s]", p.taskID(*entry.TaskID))
			}
			fmt.Fprintln(&out)
		}
		fmt.Fprintln(&out)
	}
	if len(view.Interruptions) > 0 {
		fmt.Fprintln(&out, p.head("Interruptions"))
		for _, in := range view.Interruptions {
			fmt.Fprintf(&out, "  %s %s '%s'", in.At.In(z.Location()).Format("15:04"), p.taskID(in.ToTaskID), textsafe.SanitizeLine(titles[in.ToTaskID]))
			if in.FromTaskID != nil {
				fmt.Fprintf(&out, " interrupted %s", p.taskID(*in.FromTaskID))
			}
			if in.FromBlockID != nil {
				b := blocks[*in.FromBlockID]
				fmt.Fprintf(&out, " (block %s–%s)", b.Start, b.End)
			}
			fmt.Fprintln(&out)
		}
		fmt.Fprintln(&out)
	}
	if view.AIChanges > 0 {
		fmt.Fprintf(&out, "AI changes: %d unreviewed — run `tkr ai-changes`\n", view.AIChanges)
	}
	_, err = fmt.Fprint(e.out, out.String())
	return err
}

func dashboardTitles(view *api.Today) map[int64]string {
	titles := make(map[int64]string)
	for _, tasks := range [][]api.Task{view.TriageTop, view.Overdue, view.DueSoon} {
		for _, task := range tasks {
			titles[task.ID] = task.Title
		}
	}
	for _, b := range view.Blocks {
		titles[b.TaskID] = b.TaskTitle
	}
	for _, entry := range view.Entries {
		if entry.TaskID != nil && entry.TaskTitle != nil {
			titles[*entry.TaskID] = *entry.TaskTitle
		}
	}
	if b := view.CurrentBlock; b != nil {
		titles[b.TaskID] = b.TaskTitle
	}
	if f := view.Focus; f != nil {
		titles[f.ID] = f.Title
	}
	return titles
}
