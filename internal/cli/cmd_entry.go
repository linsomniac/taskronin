package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/editor"
	"taskronin/internal/textsafe"
)

func init() { register(entryCommand) }

func entryCommand(e *env) *cobra.Command {
	cmd := &cobra.Command{Use: "entry", Short: "Show, edit, delete, restore or link an entry"}
	for _, verb := range []string{"show", "edit", "delete", "undelete"} {
		cmd.AddCommand(entryActionCommand(e, verb))
	}
	cmd.AddCommand(entryLinkCommand(e))
	return cmd
}

func entryActionCommand(e *env, verb string) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use: verb + " EID", Short: map[string]string{"show": "Show an entry", "edit": "Edit an entry's title and notes", "delete": "Delete an entry", "undelete": "Restore a deleted entry"}[verb],
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			id, err := parseEntryID(args[0])
			if err != nil {
				return err
			}
			if verb == "delete" && !yes && !e.sys.StdinTTY {
				return usagef("use -y to confirm")
			}
			c, err := e.client()
			if err != nil {
				return err
			}
			entry, err := c.GetEntry(e.ctx, id)
			if err != nil {
				return err
			}
			switch verb {
			case "show":
				return e.showEntry(entry)
			case "edit":
				return e.editEntry(entry)
			case "delete":
				if !yes {
					confirmed, err := e.confirmDeletion(fmt.Sprintf("e%d '%s'", entry.ID, entry.Title))
					if err != nil || !confirmed {
						return err
					}
				}
			}
			m := client.Mut{IfMatch: entry.ETag}
			var res *api.OpResponse
			action := "deleted"
			if verb == "delete" {
				res, err = c.DeleteEntry(e.ctx, m, id)
			} else {
				res, err = c.UndeleteEntry(e.ctx, m, id)
				action = "restored"
			}
			if err != nil {
				return err
			}
			return e.outputOp(res, fmt.Sprintf("e%d %s", id, action))
		},
	}
	if verb != "show" {
		cmd.Annotations = map[string]string{"mutates": "true"}
	}
	if verb == "delete" {
		cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Confirm deletion without prompting")
	}
	return cmd
}

func entryLinkCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use: "link EID ID|none", Short: "Link an entry to a task or make it standalone", Args: cobra.ExactArgs(2),
		Annotations: map[string]string{"mutates": "true"},
		RunE: func(_ *cobra.Command, args []string) error {
			id, err := parseEntryID(args[0])
			if err != nil {
				return err
			}
			req := api.RelinkRequest{TaskID: api.Opt[int64]{Set: true, Null: args[1] == "none"}}
			if !req.TaskID.Null {
				req.TaskID.V, err = parseTaskID(args[1])
				if err != nil {
					return err
				}
			}
			c, err := e.client()
			if err != nil {
				return err
			}
			entry, err := c.GetEntry(e.ctx, id)
			if err != nil {
				return err
			}
			res, err := c.RelinkEntry(e.ctx, client.Mut{IfMatch: entry.ETag}, id, req)
			if err != nil {
				return err
			}
			if res.Entry == nil {
				return fmt.Errorf("server response is missing the relinked entry")
			}
			headline := fmt.Sprintf("e%d standalone (no task)", id)
			if res.Entry.TaskID != nil {
				headline = fmt.Sprintf("e%d linked to #%d", id, *res.Entry.TaskID)
			}
			return e.outputOp(res, headline)
		},
	}
}

func (e *env) showEntry(entry *api.Entry) error {
	if e.jsonOut {
		return json.NewEncoder(e.out).Encode(entry)
	}
	var out strings.Builder
	fmt.Fprintf(&out, "e%d %s\n", entry.ID, textsafe.SanitizeLine(entry.Title))
	link := "none (standalone)"
	if entry.TaskID != nil {
		link = fmt.Sprintf("#%d", *entry.TaskID)
		if entry.TaskTitle != nil {
			link += " " + *entry.TaskTitle
		}
	}
	detailField(&out, "Task", link)
	detailField(&out, "Kind", entry.Kind)
	detailField(&out, "Work date", formatDate(entry.WorkDate))
	if entry.Time != nil {
		detailField(&out, "Time", entry.Time.String())
	}
	fmt.Fprintf(&out, "Reopened: %t\n", entry.Reopened)
	detailTime(&out, "Created", &entry.CreatedAt)
	detailTime(&out, "Updated", &entry.UpdatedAt)
	detailTime(&out, "Deleted", entry.DeletedAt)
	fmt.Fprintf(&out, "Version: %d\n\nNotes\n%s\n", entry.Version, textsafe.Sanitize(entry.Notes))
	_, err := fmt.Fprint(e.out, out.String())
	return err
}

func (e *env) editEntry(entry *api.Entry) error {
	draft, err := (editor.Drafts{Dir: e.paths.DraftsDir}).New(editor.FormatTaskBuffer(entry.Title, entry.Notes))
	if err != nil {
		return err
	}
	// AIDEV-NOTE: match task editing: retain the draft on every failure,
	// including a rejected PATCH; discard only unchanged or confirmed edits.
	defer func() {
		if draft != nil {
			e.errorLine("tkr: draft kept at %s", draft.Path)
		}
	}()
	argv, err := editor.Command(e.ccfg.Editor, e.sys.Getenv)
	if err != nil {
		return err
	}
	buffer, err := editor.Edit(e.ctx, argv, draft, e.in, e.out, e.err)
	if err != nil {
		return err
	}
	title, notes, err := editor.ParseTaskBuffer(buffer)
	if err != nil {
		return usagef("%s", err)
	}
	req := api.UpdateEntryRequest{
		Title: api.Opt[string]{Set: title != entry.Title, V: title},
		Notes: api.Opt[string]{Set: notes != entry.Notes, V: notes},
	}
	if !req.Title.Set && !req.Notes.Set {
		if err := draft.Discard(); err != nil {
			return err
		}
		draft = nil
		if e.jsonOut {
			return json.NewEncoder(e.out).Encode(entry)
		}
		_, err := fmt.Fprintf(e.out, "e%d unchanged\n", entry.ID)
		return err
	}
	res, err := e.apiClient.UpdateEntry(e.ctx, client.Mut{IfMatch: entry.ETag}, entry.ID, req)
	if err != nil {
		return err
	}
	if err := draft.Discard(); err != nil {
		return fmt.Errorf("entry updated, but %w", err)
	}
	draft = nil
	return e.outputOp(res, fmt.Sprintf("e%d updated", entry.ID))
}
