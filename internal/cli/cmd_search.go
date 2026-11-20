package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/textsafe"
)

func init() { register(searchCommand) }

func searchCommand(e *env) *cobra.Command {
	var query client.SearchQuery
	cmd := &cobra.Command{
		Use: "search QUERY…", Short: "Search task and entry titles and notes", Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if query.Kind != "task" && query.Kind != "entry" && query.Kind != "all" {
				return usagef("--kind must be task, entry or all")
			}
			query.Q = strings.Join(args, " ")
			c, err := e.client()
			if err != nil {
				return err
			}
			result := api.Page[api.SearchHit]{Items: []api.SearchHit{}}
			for {
				page, err := c.Search(e.ctx, query)
				if err != nil {
					return err
				}
				result.Items = append(result.Items, page.Items...)
				if page.NextCursor == nil || *page.NextCursor == "" {
					break
				}
				query.Cursor = *page.NextCursor
			}
			if e.jsonOut {
				return json.NewEncoder(e.out).Encode(result)
			}
			p := e.paint()
			if len(result.Items) == 0 {
				_, err := fmt.Fprintln(e.out, p.dim("No matches for '"+textsafe.SanitizeLine(query.Q)+"'"))
				return err
			}
			var out strings.Builder
			for _, hit := range result.Items {
				prefix := "#"
				if hit.Kind == "entry" {
					prefix = "e"
				}
				id := p.id(fmt.Sprintf("%s%d", prefix, hit.ID))
				fmt.Fprintf(&out, "%s  %s", id, textsafe.Truncate(textsafe.SanitizeLine(hit.Title), 40, "…"))
				if hit.WorkDate != nil {
					fmt.Fprintf(&out, "  %s", hit.WorkDate)
				}
				if hit.TaskID != nil {
					fmt.Fprintf(&out, "  [%s]", p.taskID(*hit.TaskID))
				}
				if hit.Status != "" {
					fmt.Fprintf(&out, "  %s", p.status(hit.Status))
				}
				fmt.Fprintln(&out)
				if hit.Snippet != "" {
					fmt.Fprintf(&out, "  %s\n", textsafe.SanitizeLine(hit.Snippet))
				}
			}
			_, err = fmt.Fprint(e.out, out.String())
			return err
		},
	}
	cmd.Flags().StringVar(&query.Kind, "kind", "all", "Search kind: task, entry, all")
	return cmd
}
