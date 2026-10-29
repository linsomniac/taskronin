package offline

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/store"
)

// verifyAndSealStage is the shared tail of restore and import staging
// (A§12.2 step 5, A§12.5 step 5) after the ledger replay and Reconcile: CheckAll,
// integrity and FTS checks (plus foreign_key_check when foreignKeys is set),
// close the staged store, assert no sidecar files remain, and fsync the file.
// *closed is set once st is closed so the caller's deferred Close is skipped.
func verifyAndSealStage(ctx context.Context, e *Env, st *store.Store, svc *service.Service, stagedPath string, foreignKeys bool, closed *bool) error {
	if err := service.CheckAll(ctx, st.Writer, e.clock().Now(), svc.Zone()); err != nil {
		return err
	}
	if err := store.IntegrityCheck(ctx, st.Writer); err != nil {
		return offlineError(err)
	}
	if foreignKeys {
		if err := store.ForeignKeyCheck(ctx, st.Writer); err != nil {
			return offlineError(err)
		}
	}
	if err := store.FTSIntegrityCheck(ctx, st.Writer); err != nil {
		return offlineError(err)
	}
	*closed = true
	if err := st.Close(); err != nil {
		return offlineError(err)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(stagedPath + suffix); err == nil {
			return domain.Errorf(domain.CodeInternal, "staged database sidecar remains: %s", filepath.Base(stagedPath+suffix))
		} else if !errors.Is(err, fs.ErrNotExist) {
			return offlineError(err)
		}
	}
	f, err := os.Open(stagedPath) //nolint:gosec // G304: path of the staged copy this command created
	if err != nil {
		return offlineError(err)
	}
	return offlineError(errors.Join(f.Sync(), f.Close()))
}
