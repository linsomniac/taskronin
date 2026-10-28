package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// OtherHumanAccounts lists other accounts with UID >= 1000 and a login shell.
// An unreadable or malformed passwd file must not imply a single-user host.
func OtherHumanAccounts(passwdPath string, myUID int) ([]string, error) {
	f, err := os.Open(passwdPath) //nolint:gosec // G304: /etc/passwd or a test fixture
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var names []string
	scanner := bufio.NewScanner(f)
	for line := 1; scanner.Scan(); line++ {
		text := scanner.Text()
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Split(text, ":")
		if len(fields) != 7 || fields[0] == "" {
			return nil, fmt.Errorf("invalid passwd entry at line %d", line)
		}
		uid, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid passwd UID at line %d", line)
		}
		shell := filepath.Base(fields[6])
		if uid >= 1000 && int64(uid) != int64(myUID) && fields[6] != "" && shell != "nologin" && shell != "false" {
			names = append(names, fields[0])
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read passwd: %w", err)
	}
	return names, nil
}
