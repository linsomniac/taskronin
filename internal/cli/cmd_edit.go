package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"

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
				_, err := fmt.Fprintf(e.out, "%s unchanged\n", e.paint().taskID(id))
				return err
			}
			return e.outputOp(res, func(p paint) string { return p.taskID(id) + " updated" })
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
	buffer, err := e.runEditor(argv, draft)
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

func (e *env) runEditor(argv []string, draft *editor.Draft) (string, error) {
	text, err := editor.Edit(e.ctx, argv, draft, e.in, e.out, e.err)
	if !e.jsonOut && errors.Is(err, exec.ErrNotFound) {
		return "", fmt.Errorf("editor %q not found in $PATH", argv[0])
	}
	return text, err
}
