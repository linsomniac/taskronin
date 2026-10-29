package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"taskronin/internal/api"
)

func init() { register(upcomingCommand) }

func upcomingCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use: "upcoming", Short: "Show deferred and soon-due tasks", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			c, err := e.client()
			if err != nil {
				return err
			}
			view, err := c.Upcoming(e.ctx)
			if err != nil {
				return err
			}
			if e.jsonOut {
				return json.NewEncoder(e.out).Encode(view)
			}
			clock, err := e.clock()
			if err != nil {
				return err
			}
			var out strings.Builder
			for i, section := range []struct {
				title string
				tasks []api.Task
			}{{"Deferred", view.Deferred}, {"Due soon", view.DueSoon}} {
				if i > 0 {
					fmt.Fprintln(&out)
				}
				fmt.Fprintln(&out, section.title)
				for _, task := range section.tasks {
					fmt.Fprintf(&out, "  %s", taskLine(task, clock.Today))
					if task.Defer != nil {
						fmt.Fprintf(&out, "  deferred to %s", formatMoment(task.Defer))
					}
					fmt.Fprintln(&out)
				}
			}
			_, err = fmt.Fprint(e.out, out.String())
			return err
		},
	}
}
