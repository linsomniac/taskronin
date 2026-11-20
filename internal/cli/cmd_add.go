package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/datespec"
	"taskronin/internal/editor"
)

func init() { register(addCommand) }

func addCommand(e *env) *cobra.Command {
	var req api.AddTaskRequest
	var top, bottom, now, edit bool
	var after, before, deferDate, block, due string
	cmd := &cobra.Command{
		Use: "add TITLE", Short: "Capture a task",
		Annotations: map[string]string{"mutates": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return usagef("quote your title")
			}
			if len(args) != 1 {
				return usagef("add requires a title")
			}
			req.Title = args[0]
			var placement string
			// AIDEV-NOTE: explicit false booleans are changed flags too, but
			// must neither select placement nor conflict with an active one.
			for _, flag := range []struct {
				name   string
				active bool
			}{
				{"top", top}, {"bottom", bottom},
				{"after", cmd.Flags().Changed("after")},
				{"before", cmd.Flags().Changed("before")},
				{"defer", cmd.Flags().Changed("defer")},
				{"block", cmd.Flags().Changed("block")},
				{"now", now},
			} {
				if flag.active {
					if placement != "" {
						return usagef("at most one placement flag")
					}
					placement = flag.name
				}
			}
			if placement != "" {
				req.Place = &api.AddPlacement{How: placement}
				if placement == "after" || placement == "before" {
					ref := after
					if placement == "before" {
						ref = before
					}
					id, err := parseTaskID(ref)
					if err != nil {
						return err
					}
					req.Place.Ref = id
				}
			}
			var draft *editor.Draft
			if edit {
				var err error
				draft, err = e.editAddNotes(&req)
				if err != nil {
					return err
				}
			}
			// Resolve relative dates after the editor closes, using one server clock.
			if cmd.Flags().Changed("due") || placement == "defer" || placement == "block" {
				ref, err := e.ref(datespec.Future)
				if err != nil {
					return err
				}
				if cmd.Flags().Changed("due") {
					req.Due, err = parseCaptureMoment(due, "due", ref)
					if err != nil {
						return err
					}
				}
				switch placement {
				case "defer":
					req.Place.Defer, err = parseCaptureMoment(deferDate, "defer", ref)
					if err != nil {
						return err
					}
				case "block":
					span, err := datespec.ParseSpan(block, datespec.Future, ref)
					if err != nil {
						return usagef("--block: %s", err)
					}
					req.Place.Block = &api.BlockRequest{Date: span.Date, Start: span.Start, End: span.End}
				}
			}
			c, err := e.client()
			if err != nil {
				return err
			}
			res, err := c.AddTask(e.ctx, client.Mut{}, req)
			if err != nil {
				return err
			}
			if draft != nil {
				if err := draft.Discard(); err != nil {
					return fmt.Errorf("task added, but %w", err)
				}
				e.draftPath = ""
			}
			headline, err := e.addHeadline(res, req.Place)
			if err != nil {
				return err
			}
			return e.outputOp(res, headline)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&top, "top", false, "Place at the top of backlog")
	f.BoolVar(&bottom, "bottom", false, "Place at the bottom of backlog")
	f.StringVar(&after, "after", "", "Place after task ID")
	f.StringVar(&before, "before", "", "Place before task ID")
	f.StringVar(&deferDate, "defer", "", "Defer until a date or time")
	f.StringVar(&block, "block", "", "Schedule [date] HH:MM-HH:MM")
	f.BoolVar(&now, "now", false, "Focus this task now")
	f.StringVar(&due, "due", "", "Due date or time")
	f.StringArrayVarP(&req.Tags, "tag", "t", nil, "Add a tag (repeatable)")
	f.StringVarP(&req.Project, "project", "P", "", "Project name")
	f.StringVarP(&req.Priority, "priority", "p", "", "Priority: H, M or L")
	f.BoolVarP(&edit, "editor", "e", false, "Write notes in your editor")
	return cmd
}

func parseCaptureMoment(value, flag string, ref datespec.Ref) (*api.Moment, error) {
	moment, err := datespec.Parse(value, datespec.Future, ref)
	if err != nil {
		return nil, usagef("--%s: %s", flag, err)
	}
	result := &api.Moment{Date: moment.Date}
	if moment.Min.Valid {
		result.Time = &moment.Min.V
	}
	return result, nil
}

func (e *env) editAddNotes(req *api.AddTaskRequest) (*editor.Draft, error) {
	draft, err := (editor.Drafts{Dir: e.paths.DraftsDir}).New(req.Notes)
	if err != nil {
		return nil, err
	}
	e.draftPath = draft.Path
	cfg, err := e.clientConfig()
	if err != nil {
		return nil, err
	}
	argv, err := editor.Command(cfg.Editor, e.sys.Getenv)
	if err != nil {
		return nil, err
	}
	req.Notes, err = editor.Edit(e.ctx, argv, draft, e.in, e.out, e.err)
	return draft, err
}

func (e *env) addHeadline(res *api.OpResponse, place *api.AddPlacement) (string, error) {
	if res.Task == nil {
		return "", fmt.Errorf("server response is missing the added task")
	}
	action := "added to triage (new)"
	if place != nil {
		switch place.How {
		case "top", "bottom":
			action = fmt.Sprintf("added to backlog (%s)", place.How)
		case "after", "before":
			action = fmt.Sprintf("added to backlog (%s #%d)", place.How, place.Ref)
		case "defer":
			if res.Task.Defer == nil {
				return "", fmt.Errorf("server response is missing the task's deferral")
			}
			action = "added, deferred to " + e.momentWord(res.Task.Defer)
		case "block":
			var err error
			action, err = e.addBlockAction(res)
			if err != nil {
				return "", err
			}
		case "now":
			action = fmt.Sprintf("added, focus → #%d", res.Task.ID)
		}
	}
	var metadata []string
	if res.Task.Project != nil {
		metadata = append(metadata, *res.Task.Project)
	}
	for _, tag := range res.Task.Tags {
		metadata = append(metadata, "+"+tag)
	}
	if res.Task.Priority != nil {
		metadata = append(metadata, "prio "+*res.Task.Priority)
	}
	if len(metadata) > 0 {
		action += " · " + strings.Join(metadata, " ")
	}
	return taskHeadline(res.Task, action), nil
}

func (e *env) addBlockAction(res *api.OpResponse) (string, error) {
	// AddTask returns its task plus effects. The block's resolved span is
	// available even when the block is absent as a top-level response entity.
	for _, effect := range res.Effects {
		if effect.Kind == "blocked" && effect.TaskID == res.Task.ID {
			span, err := datespec.ParseSpan(effect.To, datespec.Future, datespec.Ref{})
			if err != nil {
				return "", fmt.Errorf("invalid block in server response: %w", err)
			}
			return fmt.Sprintf("added, blocked b%d %s %s–%s", effect.BlockID, e.dayWord(span.Date), span.Start, span.End), nil
		}
	}
	return "", fmt.Errorf("server response is missing the added block")
}
