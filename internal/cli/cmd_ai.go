package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/domain"
	"taskronin/internal/textsafe"
)

func init() { register(aiChangesCommand) }

func aiChangesCommand(e *env) *cobra.Command {
	var all, ack bool
	var revert string
	cmd := &cobra.Command{
		Use: "ai-changes", Short: "Review changes made by non-owner tokens", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if revert != "" && (ack || all) {
				return usagef("--revert cannot be combined with --ack or --all")
			}
			if revert != "" && !domain.ValidOpIDPrefix(revert) {
				return usagef("invalid operation ID %q", revert)
			}
			c, err := e.client()
			if err != nil {
				return err
			}
			if revert != "" {
				e.mutates = true
				return e.revertAIChange(c, revert)
			}
			changes, err := allAIChanges(e, c, all)
			if err != nil {
				return err
			}
			var newest int64
			for _, op := range changes.Ops {
				newest = max(newest, op.Seq)
			}
			if e.jsonOut && !ack {
				return json.NewEncoder(e.out).Encode(changes)
			}
			if !e.jsonOut {
				if err := e.printAIChanges(changes); err != nil {
					return err
				}
			}
			if !ack {
				return nil
			}
			if len(changes.Ops) == 0 {
				if e.jsonOut {
					return json.NewEncoder(e.out).Encode(changes)
				}
				_, err := fmt.Fprintln(e.out, "nothing to acknowledge")
				return err
			}
			e.mutates = true
			res, err := c.ReviewAIChanges(e.ctx, client.Mut{}, api.ReviewRequest{ThroughSeq: newest})
			if err != nil {
				return err
			}
			return e.outputOp(res, fmt.Sprintf("acknowledged AI changes through #%d", newest))
		},
	}
	f := cmd.Flags()
	f.BoolVar(&all, "all", false, "Include already reviewed operations")
	f.BoolVar(&ack, "ack", false, "Mark the shown operations as reviewed")
	f.StringVar(&revert, "revert", "", "Revert an operation by ID (full or unique prefix)")
	return cmd
}

// allAIChanges follows every page so --ack covers the newest operation.
func allAIChanges(e *env, c *client.Client, all bool) (*api.AIChanges, error) {
	var out *api.AIChanges
	cursor := ""
	for {
		page, err := c.AIChanges(e.ctx, all, client.PageQuery{Limit: 200, Cursor: cursor})
		if err != nil {
			return nil, err
		}
		if out == nil {
			out = page
		} else {
			out.Ops = append(out.Ops, page.Ops...)
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			out.NextCursor = nil
			return out, nil
		}
		cursor = *page.NextCursor
	}
}

func (e *env) printAIChanges(changes *api.AIChanges) error {
	var out strings.Builder
	for _, op := range changes.Ops {
		line := fmt.Sprintf("op %.8s #%d %s by %s at %s", op.OpID, op.Seq, op.Kind, op.Actor, op.At.Format("2006-01-02 15:04:05Z07:00"))
		if op.State == string(domain.OpReverted) {
			line += " (reverted)"
		}
		fmt.Fprintln(&out, textsafe.SanitizeLine(line))
		for _, effect := range op.Effects {
			fmt.Fprintln(&out, "  "+textsafe.SanitizeLine(e.effectLine(effect)))
		}
	}
	_, err := fmt.Fprint(e.out, out.String())
	return err
}

// revertAIChange accepts the 8-hex form printed in the listing: expand a short
// prefix against the full history, refusing ambiguous matches.
// AIDEV-NOTE: unlike `tkr revert` (resolveOpID, server-side op_prefix over
// every op), this resolves only among AI changes, so a prefix can never pick
// an owner op here; keep the two resolvers' wording in step.
func (e *env) revertAIChange(c *client.Client, id string) error {
	if len(id) != 32 {
		changes, err := allAIChanges(e, c, true)
		if err != nil {
			return err
		}
		var matches []string
		for _, op := range changes.Ops {
			if strings.HasPrefix(op.OpID, id) {
				matches = append(matches, op.OpID)
			}
		}
		if len(matches) != 1 {
			return usagef("%d AI changes match operation prefix %q; use more characters or the full ID", len(matches), id)
		}
		id = matches[0]
	}
	res, err := c.Revert(e.ctx, client.Mut{}, id)
	if err != nil {
		return err
	}
	if res.OpID == nil {
		return fmt.Errorf("server response is missing the reverted operation")
	}
	return e.outputOp(res, fmt.Sprintf("reverted (op %.8s)", *res.OpID))
}
