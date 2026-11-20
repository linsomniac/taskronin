package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/domain"
)

func init() {
	register(func(e *env) *cobra.Command { return undoCommand(e, "undo") })
	register(func(e *env) *cobra.Command { return undoCommand(e, "redo") })
	register(func(e *env) *cobra.Command { return undoCommand(e, "revert") })
}

func undoCommand(e *env, verb string) *cobra.Command {
	cmd := &cobra.Command{
		Use: verb, Short: map[string]string{"undo": "Undo the last operation", "redo": "Redo the last undone operation", "revert": "Revert an operation"}[verb],
		Args: cobra.NoArgs, Annotations: map[string]string{"mutates": "true"},
		RunE: func(_ *cobra.Command, args []string) error {
			if verb == "revert" && !domain.ValidOpIDPrefix(args[0]) {
				return usagef("invalid operation ID %q: give the full ID or a unique prefix (lowercase hex)", args[0])
			}
			c, err := e.client()
			if err != nil {
				return err
			}
			var res *api.OpResponse
			var action string
			switch verb {
			case "undo":
				res, err = c.Undo(e.ctx, client.Mut{})
				action = "undone"
			case "redo":
				res, err = c.Redo(e.ctx, client.Mut{})
				action = "redone"
			case "revert":
				var id string
				if id, err = e.resolveOpID(c, args[0]); err == nil {
					res, err = c.Revert(e.ctx, client.Mut{}, id)
				}
				action = "reverted"
			}
			if err != nil {
				return err
			}
			return e.outputReversal(res, action)
		},
	}
	if verb == "revert" {
		cmd.Use, cmd.Args = "revert OPID", cobra.ExactArgs(1)
		cmd.Long = "Revert an operation by its full ID or a unique prefix of it, such as the " +
			"8 characters that history and ai-changes print, or the op ID that undo and redo name."
	}
	return cmd
}

func (e *env) outputReversal(res *api.OpResponse, fallback string) error {
	for _, effect := range res.Effects {
		if effect.Kind == "reverted" {
			return e.outputOp(res, func(p paint) string { return e.effectLine(effect, p) })
		}
	}
	if res.OpID == nil {
		return fmt.Errorf("server response is missing the %s operation", fallback)
	}
	return e.outputOp(res, func(p paint) string { return fallback + " (" + p.shortOpID(*res.OpID) + ")" })
}

// maxPrefixMatches bounds the candidates an ambiguous prefix lists.
const maxPrefixMatches = 3

// resolveOpID expands the short op ID that `tkr` prints (history, undo,
// ai-changes) to the full ID through the server's op_prefix filter, refusing
// a prefix that matches no operation or more than one.
func (e *env) resolveOpID(c *client.Client, prefix string) (string, error) {
	if len(prefix) == 32 {
		return prefix, nil
	}
	page, err := c.ListOps(e.ctx, client.OpsQuery{OpPrefix: prefix, PageQuery: client.PageQuery{Limit: maxPrefixMatches + 1}})
	if err != nil {
		return "", err
	}
	switch n := len(page.Items); n {
	case 0:
		return "", domain.Errorf(domain.CodeNotFound, "no operation ID starts with %q", prefix)
	case 1:
		return page.Items[0].OpID, nil
	default:
		count := fmt.Sprint(n)
		if n > maxPrefixMatches {
			count = fmt.Sprintf("more than %d", maxPrefixMatches)
			page.Items = page.Items[:maxPrefixMatches]
		}
		matches := make([]string, len(page.Items))
		for i, op := range page.Items {
			matches[i] = fmt.Sprintf("%s %s at %s", op.OpID, op.Kind, op.At.Format("2006-01-02 15:04"))
		}
		return "", usagef("operation prefix %q matches %s operations (%s); use more characters or the full ID",
			prefix, count, strings.Join(matches, "; "))
	}
}
