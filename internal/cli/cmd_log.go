package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/datespec"
	"taskronin/internal/editor"
)

func init() { register(logCommand) }

func logCommand(e *env) *cobra.Command {
	var noTask, edit bool
	var date, at, note string
	cmd := &cobra.Command{
		Use: "log [ID] TEXT", Short: "Log work on a task or as a standalone entry",
		Annotations: map[string]string{"mutates": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			id, hasID, text, err := parseLogArgs(args, noTask, edit)
			if err != nil {
				return err
			}
			req := api.AddEntryRequest{
				TaskID: api.Opt[int64]{Set: true, Null: !hasID, V: int64(id)},
				Title:  text, Notes: note,
			}
			if cmd.Flags().Changed("at") {
				minute, err := datespec.ParseClock(at, false)
				if err != nil {
					return dateUsage("--at", err)
				}
				req.Time = &minute
			}
			var draft *editor.Draft
			if edit {
				draft, err = e.editLog(&req)
				if err != nil {
					return err
				}
			}
			// AIDEV-NOTE: acquire the cached server date only after editing;
			// the editor may cross midnight before we resolve implicit links.
			backdated := false
			if cmd.Flags().Changed("date") {
				ref, err := e.ref(datespec.Past)
				if err != nil {
					return err
				}
				moment, err := datespec.Parse(date, datespec.Past, ref)
				if err != nil {
					return dateUsage("--date", err)
				}
				if moment.Min.Valid {
					return usagef("--date requires a date; use --at for the time")
				}
				req.WorkDate = &moment.Date
				backdated = moment.Date != ref.Today
			}
			implicit := !hasID && !noTask
			if implicit {
				if backdated {
					return usagef("backdated entries need a task id or -N")
				}
				if !e.sys.StdinTTY {
					return usagef("a task id or -N is required when stdin is not a terminal")
				}
			}
			c, err := e.client()
			if err != nil {
				return err
			}
			p := e.paint()
			link := ""
			if implicit {
				status, _, _, err := c.Status(e.ctx, "")
				if err != nil {
					return err
				}
				// AIDEV-NOTE: resolve only at the CLI boundary. Even an implicit
				// standalone entry sends task_id:null; the API never infers a link.
				switch {
				case status.Focus != nil:
					req.TaskID = api.Opt[int64]{Set: true, V: status.Focus.ID}
					link = fmt.Sprintf("→ %s (focus)", p.taskID(status.Focus.ID))
				case status.CurrentBlock != nil:
					req.TaskID = api.Opt[int64]{Set: true, V: status.CurrentBlock.TaskID}
					link = fmt.Sprintf("→ %s (current block)", p.taskID(status.CurrentBlock.TaskID))
				default:
					link = "→ no task"
				}
			}
			res, err := c.AddEntry(e.ctx, client.Mut{}, req)
			if err != nil {
				return err
			}
			for _, warning := range res.Warnings {
				e.errorLine("tkr: warning: %s", warning)
			}
			if draft != nil {
				if err := draft.Discard(); err != nil {
					return fmt.Errorf("entry logged, but %w", err)
				}
				e.draftPath = ""
			}
			if e.jsonOut {
				return json.NewEncoder(e.out).Encode(res)
			}
			if link != "" {
				if _, err := fmt.Fprintln(e.out, link); err != nil {
					return err
				}
			}
			entry := res.Entry
			if entry == nil {
				return fmt.Errorf("server response is missing the logged entry")
			}
			day := e.dayWord(entry.WorkDate)
			if entry.TaskID == nil {
				_, err = fmt.Fprintf(e.out, "%s logged %s (no task)\n", p.entryID(entry.ID), day)
			} else {
				title := ""
				if entry.TaskTitle != nil {
					title = *entry.TaskTitle
				}
				_, err = fmt.Fprintf(e.out, "%s logged %s on %s %s\n", p.entryID(entry.ID), day, p.taskID(*entry.TaskID), p.quotedTitle(title))
			}
			return err
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&noTask, "no-task", "N", false, "Log without a task")
	f.StringVar(&date, "date", "", "Work date (default today)")
	f.StringVar(&at, "at", "", "Work time (HH:MM)")
	f.BoolVarP(&edit, "editor", "e", false, "Edit the title and notes in your editor")
	f.StringVarP(&note, "message", "m", "", "Entry notes")
	return cmd
}

func (e *env) editLog(req *api.AddEntryRequest) (*editor.Draft, error) {
	draft, err := (editor.Drafts{Dir: e.paths.DraftsDir}).New(editor.FormatTaskBuffer(req.Title, req.Notes))
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
	buffer, err := e.runEditor(argv, draft)
	if err != nil {
		return nil, err
	}
	req.Title, req.Notes, err = editor.ParseLogBuffer(buffer)
	if err != nil {
		return nil, err
	}
	return draft, nil
}
