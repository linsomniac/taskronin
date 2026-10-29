package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/config"
	"taskronin/internal/fsutil"
	"taskronin/internal/offline"
	"taskronin/internal/textsafe"
)

func init() { register(backupCommand) }

func backupCommand(e *env) *cobra.Command {
	var list bool
	cmd := &cobra.Command{
		Use: "backup", Short: "Take a backup snapshot, or list snapshots", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			c, err := e.client()
			if err != nil {
				return err
			}
			// AIDEV-NOTE: the offline path is taken only when the request was
			// definitely not sent (ErrNotSent). Any other failure, including an
			// unknown outcome, must not race a possibly running server.
			if list {
				status, err := c.Backups(e.ctx)
				if errors.Is(err, client.ErrNotSent) {
					return e.offlineBackupList()
				}
				if err != nil {
					return err
				}
				return e.printBackups(status)
			}
			snap, err := c.RunBackup(e.ctx)
			if errors.Is(err, client.ErrNotSent) {
				return e.offlineBackup()
			}
			if err != nil {
				return err
			}
			if e.jsonOut {
				return json.NewEncoder(e.out).Encode(snap)
			}
			_, err = fmt.Fprintln(e.out, textsafe.SanitizeLine(snap.Path))
			return err
		},
	}
	cmd.Flags().BoolVar(&list, "list", false, "List snapshots instead of taking one")
	return cmd
}

func (e *env) offlineBackupEnv() (*offline.Env, error) {
	fsutil.SetUmask(0o077)
	cfg, err := config.LoadServer(e.paths.ServerConfig)
	if err != nil {
		return nil, err
	}
	o := e.offlineEnv()
	o.Server = cfg
	return o, nil
}

func (e *env) offlineBackup() error {
	o, err := e.offlineBackupEnv()
	if err != nil {
		return err
	}
	info, err := offline.Backup(e.ctx, o)
	if err != nil {
		return err
	}
	if e.jsonOut {
		return json.NewEncoder(e.out).Encode(snapshotView(info.Name, info.Path, string(info.Kind), info.At, info.Size, info.SchemaVersion))
	}
	_, err = fmt.Fprintf(e.out, "server not running; took an offline snapshot\n%s\n", textsafe.SanitizeLine(info.Path))
	return err
}

func (e *env) offlineBackupList() error {
	o, err := e.offlineBackupEnv()
	if err != nil {
		return err
	}
	infos, err := offline.ListBackups(o)
	if err != nil {
		return err
	}
	var rows []api.Snapshot
	for _, info := range infos {
		rows = append(rows, snapshotView(info.Name, info.Path, string(info.Kind), info.At, info.Size, info.SchemaVersion))
	}
	return e.printBackups(&api.BackupStatus{Snapshots: rows})
}

const timeLayout = "2006-01-02 15:04:05Z07:00"

// snapshotView avoids naming backup.SnapshotInfo: cli may import offline, not backup (A§1.3).
func snapshotView(name, path, kind string, at time.Time, size int64, schema int) api.Snapshot {
	return api.Snapshot{Name: name, Path: path, Kind: kind, At: api.Time{Time: at}, Size: size, SchemaVersion: schema}
}

func (e *env) printBackups(status *api.BackupStatus) error {
	if e.jsonOut {
		return json.NewEncoder(e.out).Encode(status)
	}
	var out strings.Builder
	if status.LastSuccessAt != nil {
		fmt.Fprintf(&out, "last successful backup: %s\n", status.LastSuccessAt.Format(timeLayout))
	}
	if status.LastError != "" {
		fmt.Fprintln(&out, textsafe.SanitizeLine("last backup error: "+status.LastError))
	}
	for _, s := range status.Snapshots {
		fmt.Fprintln(&out, textsafe.SanitizeLine(fmt.Sprintf("%s  %s  %s  %d bytes", s.Path, s.Kind, s.At.Format(timeLayout), s.Size)))
	}
	_, err := fmt.Fprint(e.out, out.String())
	return err
}
