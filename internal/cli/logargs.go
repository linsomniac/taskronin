package cli

import (
	"strings"

	"github.com/linsomniac/taskronin/internal/domain"
)

// parseLogArgs keeps quoted numeric-leading text distinct from a task ID.
func parseLogArgs(args []string, noTask, editor bool) (id domain.TaskID, hasID bool, text string, err error) {
	if len(args) > 2 {
		return 0, false, "", usagef("quote your text")
	}
	if len(args) == 0 {
		if editor {
			return 0, false, "", nil
		}
		return 0, false, "", usagef("missing text")
	}
	if positiveID.MatchString(strings.TrimPrefix(args[0], "#")) {
		parsed, err := parseTaskID(args[0])
		if err != nil {
			return 0, false, "", err
		}
		if noTask {
			return 0, false, "", usagef("-N conflicts with a task id")
		}
		if len(args) == 2 {
			return domain.TaskID(parsed), true, args[1], nil
		}
		if editor {
			return domain.TaskID(parsed), true, "", nil
		}
		return 0, false, "", usagef("missing text after task id (did you mean: tkr log %d \"…\")", parsed)
	}
	if len(args) == 2 {
		return 0, false, "", usagef("quote your text: tkr log \"…\"")
	}
	return 0, false, args[0], nil
}
