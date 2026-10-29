package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/editor"
)

func init() { register(editCommand) }

func editCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use: "edit ID", Short: "Edit a task's title and notes in your editor", Args: cobra.ExactArgs(1),
		Annotations: map[string]string{"mutates": "true"},
		RunE: func(_ *cobra.Command, args []string) error {
			id, err := parseTaskID(args[0])
			if err != nil {
				return err
			}
			c, err := e.client()
			if err != nil {
				return err
			}
			task, err := c.GetTask(e.ctx, id)
			if err != nil {
				return err
			}
			res, err := e.editTitleNotes("task", task.Title, task.Notes, func(title, notes string) (*api.OpResponse, error) {
				req := api.UpdateTaskRequest{
					Title: api.Opt[string]{Set: title != task.Title, V: title},
					Notes: api.Opt[string]{Set: notes != task.Notes, V: notes},
				}
				return c.UpdateTask(e.ctx, client.Mut{IfMatch: task.ETag}, id, req)
			})
			if err != nil {
				return err
			}
			if res == nil {
				if e.jsonOut {
					return json.NewEncoder(e.out).Encode(task)
				}
				_, err := fmt.Fprintf(e.out, "#%d unchanged\n", id)
				return err
			}
			return e.outputOp(res, fmt.Sprintf("#%d updated", id))
		},
	}
}

// editTitleNotes owns the shared task/entry draft lifecycle. It calls update
// only for changed content; a nil response with no error means unchanged.
func (e *env) editTitleNotes(kind, title, notes string, update func(string, string) (*api.OpResponse, error)) (*api.OpResponse, error) {
	draft, err := (editor.Drafts{Dir: e.paths.DraftsDir}).New(editor.FormatTaskBuffer(title, notes))
	if err != nil {
		return nil, err
	}
	// AIDEV-NOTE: every failure after creation keeps the draft. Only an
	// unchanged buffer or a confirmed PATCH permits discarding it.
	defer func() {
		if draft != nil {
			e.errorLine("tkr: draft kept at %s", draft.Path)
		}
	}()
	argv, err := editor.Command(e.ccfg.Editor, e.sys.Getenv)
	if err != nil {
		return nil, err
	}
	buffer, err := editor.Edit(e.ctx, argv, draft, e.in, e.out, e.err)
	if err != nil {
		return nil, err
	}
	newTitle, newNotes, err := editor.ParseTaskBuffer(buffer)
	if err != nil {
		return nil, usagef("%s", err)
	}
	var res *api.OpResponse
	changed := newTitle != title || newNotes != notes
	if changed {
		res, err = update(newTitle, newNotes)
		if err != nil {
			return nil, err
		}
	}
	if err := draft.Discard(); err != nil {
		if changed {
			return nil, fmt.Errorf("%s updated, but %w", kind, err)
		}
		return nil, err
	}
	draft = nil
	return res, nil
}
