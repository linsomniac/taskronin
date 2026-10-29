package cli

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"taskronin/internal/api"
	"taskronin/internal/domain"
)

func init() { register(settingsCommand) }

func settingsCommand(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use: "settings", Short: "Show instance settings", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			c, err := e.client()
			if err != nil {
				return err
			}
			s, err := c.Settings(e.ctx)
			if err != nil {
				return err
			}
			return e.printSettings(s, "")
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use: "set KEY VALUE", Short: "Change a setting (lowering retention asks for the password)", Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			key := args[0]
			if key != "backup.keep_daily" && key != "backup.keep_weekly" {
				return usagef("unknown setting %q (known: backup.keep_daily, backup.keep_weekly)", key)
			}
			value, err := strconv.Atoi(args[1])
			if err != nil {
				return usagef("%s must be a whole number", key)
			}
			c, err := e.client()
			if err != nil {
				return err
			}
			e.mutates = true
			current, err := c.Settings(e.ctx)
			if err != nil {
				return err
			}
			req := api.SettingsRequest{}
			lowering := false
			if key == "backup.keep_daily" {
				req.BackupKeepDaily, lowering = &value, value < current.BackupKeepDaily
			} else {
				req.BackupKeepWeekly, lowering = &value, value < current.BackupKeepWeekly
			}
			if lowering {
				// Lowering retention can delete backups: re-verify the owner.
				if !e.sys.StdinTTY || e.sys.ReadPassword == nil {
					return domain.Errorf(domain.CodePasswordRequired, "password required; run on a terminal")
				}
				if req.Password, err = e.sys.ReadPassword("Password: "); err != nil {
					return err
				}
			}
			updated, err := c.UpdateSettings(e.ctx, req)
			if err != nil {
				return err
			}
			return e.printSettings(updated, key)
		},
	})
	return cmd
}

// printSettings prints every setting, or only the named one.
func (e *env) printSettings(s *api.Settings, only string) error {
	if e.jsonOut {
		return json.NewEncoder(e.out).Encode(s)
	}
	for _, line := range []struct {
		key   string
		value int
	}{{"backup.keep_daily", s.BackupKeepDaily}, {"backup.keep_weekly", s.BackupKeepWeekly}} {
		if only == "" || only == line.key {
			if _, err := fmt.Fprintf(e.out, "%s = %d\n", line.key, line.value); err != nil {
				return err
			}
		}
	}
	return nil
}
