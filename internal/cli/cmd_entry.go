package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/client"
	"github.com/linsomniac/taskronin/internal/textsafe"
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
					p := e.paint()
					confirmed, err := e.confirmDeletion(p.entryID(entry.ID) + " " + p.quotedTitle(entry.Title))
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
			return e.outputOp(res, func(p paint) string { return p.entryID(id) + " " + action })
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
			return e.outputOp(res, func(p paint) string {
				if res.Entry.TaskID != nil {
					return p.entryID(id) + " linked to " + p.taskID(*res.Entry.TaskID)
				}
				return p.entryID(id) + " standalone (no task)"
			})
		},
	}
}

func (e *env) showEntry(entry *api.Entry) error {
	if e.jsonOut {
		return json.NewEncoder(e.out).Encode(entry)
	}
	p := e.paint()
	var out strings.Builder
	fmt.Fprintf(&out, "%s %s\n", p.entryID(entry.ID), textsafe.SanitizeLine(entry.Title))
	link := "none (standalone)"
	if entry.TaskID != nil {
		link = p.taskID(*entry.TaskID)
		if entry.TaskTitle != nil {
			link += " " + textsafe.SanitizeLine(*entry.TaskTitle)
		}
	}
	fmt.Fprintf(&out, "Task: %s\n", link)
	detailField(&out, "Kind", entry.Kind)
	fmt.Fprintf(&out, "Work date: %s\n", p.dim(formatDate(entry.WorkDate)))
	if entry.Time != nil {
		fmt.Fprintf(&out, "Time: %s\n", p.dim(entry.Time.String()))
	}
	fmt.Fprintf(&out, "Reopened: %t\n", entry.Reopened)
	p.detailTime(&out, "Created", &entry.CreatedAt)
	p.detailTime(&out, "Updated", &entry.UpdatedAt)
	p.detailTime(&out, "Deleted", entry.DeletedAt)
	fmt.Fprintf(&out, "Version: %d\n\n%s\n%s\n", entry.Version, p.head("Notes"), textsafe.Sanitize(entry.Notes))
	_, err := fmt.Fprint(e.out, out.String())
	return err
}

func (e *env) editEntry(entry *api.Entry) error {
	// AIDEV-NOTE: match task editing: retain the draft on every failure,
	// including a rejected PATCH; discard only unchanged or confirmed edits.
	res, err := e.editTitleNotes("entry", entry.Title, entry.Notes, func(title, notes string) (*api.OpResponse, error) {
		req := api.UpdateEntryRequest{
			Title: api.Opt[string]{Set: title != entry.Title, V: title},
			Notes: api.Opt[string]{Set: notes != entry.Notes, V: notes},
		}
		return e.apiClient.UpdateEntry(e.ctx, client.Mut{IfMatch: entry.ETag}, entry.ID, req)
	})
	if err != nil {
		return err
	}
	if res == nil {
		if e.jsonOut {
			return json.NewEncoder(e.out).Encode(entry)
		}
		_, err := fmt.Fprintf(e.out, "%s unchanged\n", e.paint().entryID(entry.ID))
		return err
	}
	return e.outputOp(res, func(p paint) string { return p.entryID(entry.ID) + " updated" })
}
