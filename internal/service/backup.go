package service

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"taskronin/internal/backup"
	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/store"
)

// BackupStatusView describes the configured snapshots and the latest backup health.
type BackupStatusView struct {
	Dir       string
	Snapshots []backup.SnapshotInfo
	BackupHealth
	Warning string
}

// RunBackup takes a snapshot for an administrator or the system scheduler, then rotates it.
// Backup bookkeeping is maintenance: it creates no events or idempotency records.
func (s *Service) RunBackup(ctx context.Context, a domain.Actor, kind backup.Kind) (backup.SnapshotInfo, error) {
	if a.Kind != domain.ActorSystem && !a.Scopes.Has(domain.ScopeAdmin) {
		return backup.SnapshotInfo{}, domain.Errorf(domain.CodeForbidden, "insufficient scopes")
	}
	leave, err := s.enter()
	if err != nil {
		return backup.SnapshotInfo{}, err
	}
	defer leave()
	if s.backups == nil {
		return backup.SnapshotInfo{}, domain.Errorf(domain.CodeInternal, "backups are not configured")
	}
	ctx, cancel := s.longOpContext(ctx)
	defer cancel()
	fail := func(cause error) (backup.SnapshotInfo, error) {
		err := s.backupMaint(ctx, func(ctx context.Context, tx *sql.Tx, _ time.Time) error {
			return store.SetSetting(ctx, tx, "last_backup_error", cause.Error())
		})
		return backup.SnapshotInfo{}, serviceError(errors.Join(cause, err))
	}
	if err := s.backupMaint(ctx, func(ctx context.Context, tx *sql.Tx, now time.Time) error {
		return store.SetSetting(ctx, tx, "last_backup_attempt_at", strconv.FormatInt(now.UnixMilli(), 10))
	}); err != nil {
		return fail(err)
	}
	if s.backupHook != nil {
		if err := s.backupHook(ctx); err != nil {
			return fail(err)
		}
	}
	info, err := s.backups.Snapshot(ctx, kind)
	if err != nil {
		return fail(err)
	}
	var retention backup.Retention
	if err := s.backupMaint(ctx, func(ctx context.Context, tx *sql.Tx, now time.Time) error {
		daily, err := store.GetSetting(ctx, tx, "backup_keep_daily")
		if err != nil {
			return err
		}
		weekly, err := store.GetSetting(ctx, tx, "backup_keep_weekly")
		if err != nil {
			return err
		}
		retention.Daily, err = strconv.Atoi(daily)
		if err != nil {
			return err
		}
		retention.Weekly, err = strconv.Atoi(weekly)
		if err != nil {
			return err
		}
		if err := store.SetSetting(ctx, tx, "last_backup_at", strconv.FormatInt(now.UnixMilli(), 10)); err != nil {
			return err
		}
		return store.SetSetting(ctx, tx, "last_backup_error", "")
	}); err != nil {
		return fail(err)
	}
	if _, err := s.backups.Rotate(ctx, retention); err != nil {
		return fail(err)
	}
	return info, nil
}

func (s *Service) backupMaint(ctx context.Context, fn func(context.Context, *sql.Tx, time.Time) error) error {
	// AIDEV-NOTE: A canceled backup must still persist its outcome while Drain
	// waits for it. Detach cancellation only for bounded maintenance writes.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), OpBudget)
	defer cancel()
	return s.maintTx(ctx, func(tx *sql.Tx, now time.Time) error { return fn(ctx, tx, now) })
}

// BackupStatus returns the snapshot inventory and the same backup warning used by Today.
func (s *Service) BackupStatus(ctx context.Context, a domain.Actor) (BackupStatusView, error) {
	var view BackupStatusView
	err := s.read(ctx, a, domain.ScopeAdmin, false, func(q store.Q, now time.Time, _ civil.Date) error {
		if s.backups == nil {
			return domain.Errorf(domain.CodeInternal, "backups are not configured")
		}
		view.Dir = s.backups.Dir
		var err error
		view.Snapshots, err = backup.List(view.Dir)
		if err != nil {
			return err
		}
		view.BackupHealth, err = s.backupHealth(ctx, q, now)
		view.Warning = backupWarning(view.BackupHealth)
		return err
	})
	return view, err
}

func backupWarning(health BackupHealth) string {
	if health.LastError != "" {
		return "Backup failed: " + health.LastError
	}
	if !health.Healthy {
		return "No successful backup in the last 48 hours."
	}
	return ""
}
