package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"taskronin/internal/client"
	"taskronin/internal/textsafe"
)

func init() { register(historyCommand) }

func historyCommand(e *env) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use: "history ID", Short: "Show a task's audit events, newest first", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			id, err := parseTaskID(args[0])
			if err != nil {
				return err
			}
			if limit < 1 || limit > 500 {
				return usagef("--limit must be between 1 and 500")
			}
			c, err := e.client()
			if err != nil {
				return err
			}
			page, err := c.TaskHistory(e.ctx, id, client.PageQuery{Limit: limit})
			if err != nil {
				return err
			}
			if e.jsonOut {
				return json.NewEncoder(e.out).Encode(page)
			}
			var out strings.Builder
			for _, event := range page.Items {
				entity := fmt.Sprintf("%s %d", event.Entity, event.EntityID)
				switch event.Entity {
				case "task":
					entity = fmt.Sprintf("#%d", event.EntityID)
				case "triage", "task_tags":
					entity = fmt.Sprintf("#%d %s", event.EntityID, event.Entity)
				case "entry":
					entity = fmt.Sprintf("e%d", event.EntityID)
				case "block":
					entity = fmt.Sprintf("b%d", event.EntityID)
				}
				op := event.Op
				line := fmt.Sprintf("op %.8s %s by %s at %s (%s %s; %s)", op.OpID, op.Kind, op.Actor, op.At.Format("2006-01-02 15:04:05Z07:00"), entity, event.Action, op.State)
				fmt.Fprintln(&out, textsafe.SanitizeLine(line))
			}
			_, err = fmt.Fprint(e.out, out.String())
			return err
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 100, "Maximum number of events (1–500)")
	return cmd
}
