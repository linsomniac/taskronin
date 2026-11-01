package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"taskronin/internal/api"
	"taskronin/internal/backup"
	"taskronin/internal/civil"
	"taskronin/internal/clock"
	"taskronin/internal/config"
	"taskronin/internal/fsutil"
	"taskronin/internal/lock"
	"taskronin/internal/service"
	"taskronin/internal/store"
	"taskronin/internal/systemd"
	"taskronin/internal/textsafe"
)

// Deps supplies the server's environment and optional readiness callback.
type Deps struct {
	Paths   config.Paths
	Clock   clock.Clock
	Logger  *slog.Logger
	Started func(Startup)
}

// Startup exposes the service and actual bound addresses after READY=1.
type Startup struct {
	Svc     *service.Service
	Socket  string
	WebAddr string
}

// AIDEV-TODO: Plan D's internal/server/web.go assigns this in init() to an
// adapter over web.New (architecture §6.2 web chain).
var newWebHandler func(svc *service.Service, clk clock.Clock, zone civil.Zone, weekStart time.Weekday, listen string, log *slog.Logger) (http.Handler, error)

// Run recovers and serves an instance until cancellation, then drains it while
// retaining the database and instance lock until all accepted work returns.
func Run(ctx context.Context, cfg *config.Server, deps Deps) (err error) {
	if cfg == nil {
		return errors.New("server configuration is required")
	}
	if err := config.ValidateServer(cfg); err != nil {
		return err
	}
	fsutil.SetUmask(0o077)
	if deps.Clock == nil {
		deps.Clock = clock.Real{}
	}
	if deps.Logger == nil {
		deps.Logger = serverLogger(cfg.Log)
	}
	paths := deps.Paths.WithServerPaths(cfg.Paths.DataDir, cfg.Paths.Socket, cfg.Backup.Dir)
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, dir := range []string{paths.DataDir, paths.StateDir, paths.RuntimeDir} {
		if dir != "" {
			if err := fsutil.EnsureDir0700(dir); err != nil {
				return err
			}
		}
	}
	held, err := lock.Acquire(paths.LockFile())
	if errors.Is(err, lock.ErrHeld) {
		return fmt.Errorf("already running: %w", err)
	}
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, held.Release()) }()
	for _, journal := range []struct{ path, detail string }{
		{paths.SwapJournal(), "an interrupted restore or import is pending — run `tkr restore --resume` or `tkr restore --rollback`"},
		{config.ScrubJournal(paths.BackupDir), "an interrupted backup scrub is pending — run `tkr scrub-backups --resume`"},
	} {
		if _, err := os.Lstat(journal.path); err == nil {
			return errors.New(journal.detail)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if _, err := backup.CleanupTemps(paths.DataDir, paths.BackupDir); err != nil {
		return err
	}
	if _, err := os.Stat(paths.DB); errors.Is(err, os.ErrNotExist) {
		return errors.New("database not found; run `tkr init`")
	} else if err != nil {
		return err
	}
	st, err := store.Open(ctx, paths.DB)
	if err != nil {
		return err
	}
	var apiListener net.Listener
	var created bool
	defer func() {
		if apiListener != nil {
			_ = apiListener.Close()
		}
		err = errors.Join(err, st.Close())
		if created {
			if removeErr := os.Remove(paths.Socket); !errors.Is(removeErr, os.ErrNotExist) {
				err = errors.Join(err, removeErr)
			}
		}
	}()
	if _, err := st.SchemaStatus(ctx); err != nil {
		return err
	}
	if err := fsutil.EnsureDir0700(paths.BackupDir); err != nil {
		return err
	}
	manager := &backup.Manager{Dir: paths.BackupDir, Clock: deps.Clock, Snap: st.Snap}
	if _, err := st.Migrate(ctx, func(ctx context.Context) error {
		if err := st.CheckpointTruncate(ctx); err != nil {
			return err
		}
		_, err := manager.Snapshot(ctx, backup.KindPremigrate)
		return err
	}); err != nil {
		return err
	}
	if _, err := store.GetAuthState(ctx, st.Reader); errors.Is(err, store.ErrNotFound) {
		return errors.New("database is not initialized; run `tkr init`")
	} else if err != nil {
		return err
	}
	ledger, _, err := backup.OpenLedger(paths.LedgerFile)
	if err != nil {
		return err
	}
	zone, _ := cfg.Zone() // validated before opening any files
	weekStart, _ := cfg.WeekStartDay()
	svc, err := service.New(service.Options{Store: st, Clock: deps.Clock, Zone: zone, WeekStart: weekStart,
		DueSoonDays: cfg.DueSoonDays, Backups: manager, Ledger: ledger, Logger: deps.Logger})
	if err != nil {
		return err
	}
	if err := svc.ReapplyLedger(ctx); err != nil {
		return err
	}
	// AIDEV-NOTE: A replay may be a no-op after a crash following the scrub
	// commit. Confirm WAL truncation unconditionally before serving (PRG-11).
	if err := st.CheckpointTruncate(ctx); err != nil {
		return err
	}
	previous, changed, err := svc.SyncTimezone(ctx, cfg.Timezone)
	if err != nil {
		return err
	}
	if changed {
		deps.Logger.Warn(textsafe.SanitizeLine(fmt.Sprintf("timezone changed from %s to %s; wall-clock times keep their meaning", previous, cfg.Timezone)))
	}
	if err := svc.Reconcile(ctx); err != nil {
		return err
	}
	apiListener, created, err = listenAPI(ctx, paths.Socket)
	if err != nil {
		return err
	}
	if created {
		// net/http.Shutdown closes listeners before the drain. Keep the socket
		// pathname until storage is closed, just like the instance lock.
		apiListener.(*net.UnixListener).SetUnlinkOnClose(false)
	}
	adapter := New(svc, api.Conv{Gen: svc.Generation(), Loc: zone.Location()}, deps.Clock, deps.Logger)
	servers := []*http.Server{httpServer(adapter.APIHandler(), deps.Logger)}
	listeners := []net.Listener{apiListener}
	startup := Startup{Svc: svc, Socket: apiListener.Addr().String()}
	if cfg.Web.Enabled && newWebHandler != nil {
		if gateErr := cfg.WebListenerAllowed(); gateErr != nil {
			deps.Logger.Warn(textsafe.SanitizeLine(gateErr.Error()))
		} else {
			addr, _ := cfg.WebAddr()
			ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr.String())
			if err != nil {
				return err
			}
			defer func() { _ = ln.Close() }()
			h, err := newWebHandler(svc, deps.Clock, zone, weekStart, ln.Addr().String(), deps.Logger)
			if err != nil {
				return err
			}
			servers = append(servers, httpServer(adapter.recoverer(adapter.requestID(adapter.accessLog(h))), deps.Logger))
			listeners = append(listeners, ln)
			startup.WebAddr = ln.Addr().String()
			accounts, err := config.OtherHumanAccounts("/etc/passwd", os.Getuid())
			if err != nil {
				return err
			}
			if len(accounts) != 0 {
				deps.Logger.Warn("plain HTTP web listener enabled on a host with other human accounts", "accounts", textsafe.SanitizeLine(fmt.Sprint(accounts)))
			}
		}
	}
	serveErrs := make(chan error, len(servers))
	for i, srv := range servers {
		go func() { serveErrs <- srv.Serve(listeners[i]) }()
	}
	// Schedulers have their own cancellation lifetime: cancel them at shutdown
	// step 3, after HTTP graceful shutdown, and join even unaccounted FlushUsage.
	schedCtx, cancelSchedulers := context.WithCancel(context.WithoutCancel(ctx))
	joinSchedulers := startSchedulers(schedCtx, svc, cfg, deps.Clock, deps.Logger)
	defer func() {
		// AIDEV-NOTE: Drain order is durability-critical. Never give Drain a
		// deadline or release storage/lock when the HTTP grace period expires.
		// BF08: FlushUsage bypasses service admission; join schedulers as well.
		if notifyErr := systemd.Notify("STOPPING=1"); notifyErr != nil {
			deps.Logger.Warn("notify stopping", "error", textsafe.SanitizeLine(notifyErr.Error()))
		}
		svc.BeginShutdown()
		adapter.BeginShutdown()
		// AIDEV-NOTE: HTTP shutdown is a real I/O deadline, independent of the
		// fake/domain clock, and shared by both servers for a total of 10 s.
		shutdownCtx, cancel := context.WithDeadline(context.Background(), clock.Real{}.Now().Add(10*time.Second))
		var shutdowns sync.WaitGroup
		for _, srv := range servers {
			shutdowns.Go(func() {
				if shutdownErr := srv.Shutdown(shutdownCtx); shutdownErr != nil {
					deps.Logger.Warn("HTTP shutdown grace expired", "error", textsafe.SanitizeLine(shutdownErr.Error()))
					_ = srv.Close()
				}
			})
		}
		shutdowns.Wait()
		cancel()
		cancelSchedulers()
		joinSchedulers()
		err = errors.Join(err, svc.Drain(context.Background()))
		flushCtx, cancelFlush := context.WithTimeout(context.Background(), service.OpBudget)
		err = errors.Join(err, svc.FlushUsage(flushCtx))
		cancelFlush()
		err = errors.Join(err, st.CheckpointTruncate(context.Background()))
	}()
	if err := systemd.Notify("READY=1"); err != nil {
		return err
	}
	if deps.Started != nil {
		deps.Started(startup)
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-serveErrs:
		return fmt.Errorf("HTTP serve: %w", err)
	}
}

func httpServer(h http.Handler, log *slog.Logger) *http.Server {
	return &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 64 << 10,
		ConnContext: ConnContext, ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelError)}
}

func serverLogger(cfg config.LogConfig) *slog.Logger {
	var level slog.Level
	_ = level.UnmarshalText([]byte(cfg.Level))
	opts := &slog.HandlerOptions{Level: level}
	if cfg.Format == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}
