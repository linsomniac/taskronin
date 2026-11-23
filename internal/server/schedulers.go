package server

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/linsomniac/taskronin/internal/backup"
	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/clock"
	"github.com/linsomniac/taskronin/internal/config"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/service"
	"github.com/linsomniac/taskronin/internal/textsafe"
)

// Test observers are configured before Run and restored after it has returned.
var schedulerObserver func(job, event string, at time.Time)

func startSchedulers(ctx context.Context, svc *service.Service, cfg *config.Server, clk clock.Clock, log *slog.Logger) func() {
	observer := schedulerObserver
	report := func(job, event string, at time.Time) {
		if observer != nil {
			observer(job, event, at)
		}
	}
	wait := func(job string, at time.Time) bool {
		if ctx.Err() != nil {
			return false
		}
		ch := clk.After(at.Sub(clk.Now()))
		report(job, "armed", at)
		select {
		case <-ctx.Done():
			return false
		case <-ch:
			return ctx.Err() == nil
		}
	}
	logError := func(job string, err error) {
		if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
			select {
			case <-svc.Stopping():
				return
			default:
			}
			log.Error("scheduler job failed", "job", job, "error", textsafe.SanitizeLine(err.Error()))
		}
	}
	var jobs sync.WaitGroup
	jobs.Go(func() {
		for wait("reconcile", nextMinute(clk.Now(), 500*time.Millisecond)) {
			jobCtx, cancel := context.WithTimeout(ctx, service.OpBudget)
			logError("reconcile", svc.Reconcile(jobCtx))
			cancel()
			report("reconcile", "ran", clk.Now())
		}
	})
	jobs.Go(func() {
		nextHour := clk.Now().Truncate(time.Hour).Add(time.Hour)
		for wait("maintenance", nextMinute(clk.Now(), 0)) {
			jobCtx, cancel := context.WithTimeout(ctx, service.OpBudget)
			logError("maintenance", svc.FlushUsage(jobCtx))
			if !clk.Now().Before(nextHour) {
				_, err := svc.ExpireIdempotency(jobCtx)
				logError("maintenance", err)
				_, err = svc.ExpireSessions(jobCtx)
				logError("maintenance", err)
				nextHour = clk.Now().Truncate(time.Hour).Add(time.Hour)
			}
			cancel()
			report("maintenance", "ran", clk.Now())
		}
	})
	jobs.Go(func() {
		run := func() {
			_, err := svc.RunBackup(ctx, domain.SystemActor(), backup.KindDaily)
			logError("backup", err)
			report("backup", "ran", clk.Now())
		}
		actor := domain.SystemActor()
		actor.Scopes = domain.ScopeAdmin // BackupStatus uses the ordinary read scope check.
		status, err := svc.BackupStatus(ctx, actor)
		logError("backup", err)
		if err == nil && (!status.LastSuccessAt.Valid || clk.Now().Sub(status.LastSuccessAt.V) > 24*time.Hour) && ctx.Err() == nil {
			run()
		}
		zone, _ := cfg.Zone()
		minute, _ := cfg.BackupMinute()
		for {
			next, err := nextBackup(clk.Now(), zone, minute)
			if err != nil {
				logError("backup", err)
				return
			}
			if !wait("backup", next) {
				return
			}
			run()
		}
	})
	return jobs.Wait
}

func nextMinute(now time.Time, offset time.Duration) time.Time {
	return now.Truncate(time.Minute).Add(time.Minute + offset)
}

func nextBackup(now time.Time, zone civil.Zone, minute civil.Minute) (time.Time, error) {
	date := zone.DateOf(now)
	for {
		next, err := zone.Instant(date, minute, civil.GapForward)
		if err != nil {
			return time.Time{}, err
		}
		if next.After(now) {
			return next, nil
		}
		date = date.AddDays(1)
	}
}
