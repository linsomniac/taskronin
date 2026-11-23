package offline

import (
	"context"
	"errors"

	"github.com/linsomniac/taskronin/internal/backup"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/fsutil"
)

// Backup replays purge intents, confirms WAL truncation and takes a manual backup.
func Backup(ctx context.Context, e *Env) (_ backup.SnapshotInfo, err error) {
	release, err := e.begin(ctx, refuseJournals)
	if err != nil {
		return backup.SnapshotInfo{}, err
	}
	defer release()
	st, svc, err := e.openLive(ctx)
	if err != nil {
		return backup.SnapshotInfo{}, err
	}
	defer func() { err = offlineError(errors.Join(err, st.Close())) }()
	if err := svc.ReapplyLedger(ctx); err != nil {
		return backup.SnapshotInfo{}, err
	}
	if err := checkpoint(ctx, st); err != nil {
		return backup.SnapshotInfo{}, err
	}
	if err := fsutil.EnsureDir0700(e.Server.Backup.Dir); err != nil {
		return backup.SnapshotInfo{}, offlineError(err)
	}
	return svc.RunBackup(ctx, domain.SystemActor(), backup.KindManual)
}

// ListBackups reads the configured snapshot inventory without taking the lock.
func ListBackups(e *Env) ([]backup.SnapshotInfo, error) {
	if e.Server == nil {
		return nil, domain.Errorf(domain.CodeInternal, "server configuration is required")
	}
	infos, err := backup.List(e.Server.Backup.Dir)
	return infos, offlineError(err)
}
