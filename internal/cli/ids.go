package cli

import (
	"regexp"
	"strconv"
	"strings"
)

var positiveID = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)

func parseTaskID(s string) (int64, error)  { return parseID(s, "#", "task") }
func parseBlockID(s string) (int64, error) { return parseID(s, "b", "block") }
func parseEntryID(s string) (int64, error) { return parseID(s, "e", "entry") }

func parseID(s, prefix, kind string) (int64, error) {
	digits := strings.TrimPrefix(s, prefix)
	if positiveID.MatchString(digits) {
		if id, err := strconv.ParseInt(digits, 10, 64); err == nil {
			return id, nil
		}
	}
	return 0, usagef("invalid %s ID %q", kind, s)
}
