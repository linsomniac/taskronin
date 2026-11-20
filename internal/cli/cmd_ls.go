package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"taskronin/internal/api"
	"taskronin/internal/client"
)

func init() { register(lsCommand) }

func lsCommand(e *env) *cobra.Command {
	var query client.TaskQuery
	var all bool
	cmd := &cobra.Command{
		Use: "ls", Short: "List tasks (default triage, then backlog)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			views := []string{"triage", "backlog"}
			if all && cmd.Flags().Changed("view") {
				return usagef("--all and --view are mutually exclusive")
			}
			if all {
				query.View = "all"
			}
			titles := map[string]string{"triage": "Triage", "backlog": "Backlog", "upcoming": "Upcoming", "done": "Done", "dropped": "Dropped", "deleted": "Deleted", "all": "All"}
			if all || cmd.Flags().Changed("view") {
				if _, ok := titles[query.View]; !ok {
					return usagef("--view must be triage, backlog, upcoming, done, dropped, deleted or all")
				}
				views = []string{query.View}
			}
			pages := make([]*api.Page[api.Task], 0, len(views))
			combined := api.Page[api.Task]{Items: []api.Task{}}
			for _, view := range views {
				query.View = view
				page, err := e.listTasks(query)
				if err != nil {
					return err
				}
				pages = append(pages, page)
				combined.Items = append(combined.Items, page.Items...)
			}
			if e.jsonOut {
				return json.NewEncoder(e.out).Encode(combined)
			}
			clock, err := e.clock()
			if err != nil {
				return err
			}
			p := e.paint()
			var out strings.Builder
			for i, page := range pages {
				if i > 0 {
					fmt.Fprintln(&out)
				}
				fmt.Fprintln(&out, p.head(titles[views[i]]))
				for _, task := range page.Items {
					fmt.Fprintf(&out, "  %s\n", taskLine(task, clock.Today, p))
				}
			}
			_, err = fmt.Fprint(e.out, out.String())
			return err
		},
	}
	f := cmd.Flags()
	f.StringVar(&query.View, "view", "", "Task view: triage, backlog, upcoming, done, dropped, deleted, all")
	f.StringVarP(&query.Tag, "tag", "t", "", "Filter by tag")
	f.StringVarP(&query.Project, "project", "P", "", "Filter by project and its dotted descendants")
	f.BoolVar(&all, "all", false, "Show all non-deleted tasks")
	return cmd
}
