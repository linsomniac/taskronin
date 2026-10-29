package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/textsafe"
)

func init() {
	register(func(e *env) *cobra.Command { return closeTaskCommand(e, "drop") })
	register(func(e *env) *cobra.Command { return closeTaskCommand(e, "delete") })
	register(func(e *env) *cobra.Command { return closeTaskCommand(e, "undelete") })
}

func closeTaskCommand(e *env, verb string) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use: verb + " ID", Short: map[string]string{"drop": "Drop a task", "delete": "Delete a task", "undelete": "Restore a deleted task"}[verb],
		Args: cobra.ExactArgs(1), Annotations: map[string]string{"mutates": "true"},
		RunE: func(_ *cobra.Command, args []string) error {
			id, err := parseTaskID(args[0])
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
			task, err := c.GetTask(e.ctx, id)
			if err != nil {
				return err
			}
			if verb == "delete" && !yes {
				confirmed, err := e.confirmDelete(&task.Task)
				if err != nil || !confirmed {
					return err
				}
			}
			m := client.Mut{IfMatch: task.ETag}
			var res *api.OpResponse
			var action string
			switch verb {
			case "drop":
				res, err = c.DropTask(e.ctx, m, id)
				action = "dropped"
			case "delete":
				res, err = c.DeleteTask(e.ctx, m, id)
				action = "deleted"
			case "undelete":
				res, err = c.UndeleteTask(e.ctx, m, id)
				action = "restored"
			}
			if err != nil {
				return err
			}
			if res.Task == nil {
				return fmt.Errorf("server response is missing the %s task", action)
			}
			return e.outputOp(res, taskHeadline(res.Task, action))
		},
	}
	if verb == "delete" || verb == "undelete" {
		cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Confirm deletion without prompting")
	}
	return cmd
}

func (e *env) confirmDelete(task *api.Task) (bool, error) {
	return e.confirmDeletion(fmt.Sprintf("#%d '%s'", task.ID, task.Title))
}

func (e *env) confirmDeletion(label string) (bool, error) {
	if _, err := fmt.Fprintf(e.err, "Delete %s? [y/N] ", textsafe.SanitizeLine(label)); err != nil {
		return false, err
	}
	line, err := bufio.NewReader(e.in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	if err := e.ctx.Err(); err != nil {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}
