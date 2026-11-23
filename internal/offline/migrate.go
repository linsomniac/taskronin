package offline

import (
	"context"
	"errors"
	"fmt"

	"github.com/linsomniac/taskronin/internal/backup"
	"github.com/linsomniac/taskronin/internal/fsutil"
)

// Migrate reports schema status or migrates and completes live purge recovery.
func Migrate(ctx context.Context, e *Env, statusOnly bool) (err error) {
	release, err := e.begin(ctx, refuseJournals)
	if err != nil {
		return err
	}
	defer release()
	st, err := e.openStore(ctx)
	if err != nil {
		return err
	}
	defer func() { err = offlineError(errors.Join(err, st.Close())) }()
	status, err := st.SchemaStatus(ctx)
	if err != nil {
		return offlineError(err)
	}
	if statusOnly {
		if len(status.Pending) == 0 {
			_, err = fmt.Fprintf(e.Out, "schema %d (binary %d), nothing pending\n", status.Current, status.Latest)
		} else {
			_, err = fmt.Fprintf(e.Out, "schema %d (binary %d), pending %v\n", status.Current, status.Latest, status.Pending)
		}
		return offlineError(err)
	}
	_, err = st.Migrate(ctx, func(ctx context.Context) error {
		if err := checkpoint(ctx, st); err != nil {
			return err
		}
		if err := fsutil.EnsureDir0700(e.Paths.BackupDir); err != nil {
			return err
		}
		manager := backup.Manager{Dir: e.Paths.BackupDir, Clock: e.Clock, Snap: st.Snap}
		_, err := manager.Snapshot(ctx, backup.KindPremigrate)
		return err
	})
	if err != nil {
		return offlineError(err)
	}
	// AIDEV-NOTE: recover after migration so replay uses current-schema columns
	// (A§4.10). Even an already-current schema must replay and confirm WAL
	// truncation; a previous scrub may have committed before a crash (R3-2).
	ledger, _, err := backup.OpenLedger(e.Paths.LedgerFile)
	if err != nil {
		return offlineError(err)
	}
	svc, err := e.newService(st, ledger)
	if err != nil {
		return err
	}
	if err := svc.ReapplyLedger(ctx); err != nil {
		return err
	}
	return checkpoint(ctx, st)
}
