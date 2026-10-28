// Package offline implements operations performed with the server stopped.
package offline

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"time"

	"taskronin/internal/auth"
	"taskronin/internal/backup"
	"taskronin/internal/clock"
	"taskronin/internal/config"
	"taskronin/internal/domain"
	"taskronin/internal/fsutil"
	"taskronin/internal/lock"
	"taskronin/internal/service"
	"taskronin/internal/store"
	"taskronin/internal/textsafe"
)

// Env supplies configuration, time and terminal interactions to offline commands.
// A nil Server is permitted only during init; a nil Clock uses real time.
type Env struct {
	Paths        config.Paths
	Server       *config.Server
	Clock        clock.Clock
	In           io.Reader
	Out, Err     io.Writer
	TTY          bool
	ReadPassword func(prompt string) (string, error)
	Confirm      func(prompt string, defaultYes bool) (bool, error)
	Logger       *slog.Logger
}

type journalPolicy uint8

const (
	refuseJournals journalPolicy = iota
	allowSwapJournal
	allowScrubJournal
)

// AIDEV-NOTE: every offline command goes through begin: umask, private dirs,
// lock, journal refusal, then temp cleanup. Scrub defers cleanup until after
// password verification and ResumeSwap (R4-8); init --units-only bypasses this
// entire data preamble (BF03). Never move cleanup ahead of these checks.
func (e *Env) begin(ctx context.Context, p journalPolicy) (release func(), err error) {
	fsutil.SetUmask(0o077)
	if e.Server != nil {
		// BF01: resolve every instance path from the same effective configuration.
		e.Paths = e.Paths.WithServerPaths(e.Server.Paths.DataDir, e.Server.Paths.Socket, e.Server.Backup.Dir)
	}
	if e.Clock == nil {
		e.Clock = clock.Real{}
	}
	if e.Out == nil {
		e.Out = io.Discard
	}
	if e.Err == nil {
		e.Err = io.Discard
	}
	if e.Logger == nil {
		e.Logger = slog.New(slog.DiscardHandler)
	}
	if err := ctx.Err(); err != nil {
		return nil, offlineError(err)
	}
	for _, dir := range []string{e.Paths.DataDir, e.Paths.StateDir} {
		if err := fsutil.EnsureDir0700(dir); err != nil {
			return nil, offlineError(err)
		}
	}
	held, err := lock.Acquire(e.Paths.LockFile())
	if errors.Is(err, lock.ErrHeld) {
		return nil, &domain.Error{Code: domain.CodeBusy,
			Detail: "the server is running — `systemctl --user stop taskronin.socket taskronin.service` first", Err: err}
	}
	if err != nil {
		return nil, offlineError(err)
	}
	release = func() {
		if err := held.Release(); err != nil {
			e.Logger.Error("release offline lock", "error", textsafe.SanitizeLine(err.Error()))
		}
	}
	defer func() {
		if err != nil {
			release()
			release = nil
		}
	}()
	for _, journal := range []struct {
		path, detail string
		allowed      bool
	}{
		{e.Paths.SwapJournal(), "an interrupted restore or import is pending — run `tkr restore --resume` or `tkr restore --rollback`", p == allowSwapJournal},
		{config.ScrubJournal(e.Paths.BackupDir), "an interrupted backup scrub is pending — run `tkr scrub-backups --resume`", p == allowScrubJournal},
	} {
		if journal.allowed {
			continue
		}
		if _, err := os.Lstat(journal.path); err == nil {
			return release, domain.Errorf(domain.CodeConflict, "%s", journal.detail)
		} else if !errors.Is(err, os.ErrNotExist) {
			return release, offlineError(err)
		}
	}
	if p != allowScrubJournal {
		if _, err := backup.CleanupTemps(e.Paths.DataDir, e.Paths.BackupDir); err != nil {
			return release, offlineError(err)
		}
	}
	return release, nil
}

// openStore is deliberately independent of the ledger and service: password
// checks and read-only confirmation must precede OpenLedger's torn-tail repair.
func (e *Env) openStore(ctx context.Context) (*store.Store, error) {
	st, err := store.Open(ctx, e.Paths.DB)
	return st, offlineError(err)
}

// newService uses the caller's ledger, including its cached records and health.
// AIDEV-NOTE: BF26 requires one ledger instance throughout replay, purge/scrub
// and completion. Password-gated commands call openStore, verify, OpenLedger,
// then newService; openLive is only the convenience path without a password gate.
func (e *Env) newService(st *store.Store, ledger *backup.Ledger) (*service.Service, error) {
	if e.Server == nil {
		return nil, domain.Errorf(domain.CodeInternal, "server configuration is required")
	}
	zone, err := e.Server.Zone()
	if err != nil {
		return nil, offlineError(err)
	}
	weekStart, err := e.Server.WeekStartDay()
	if err != nil {
		return nil, offlineError(err)
	}
	clk := e.clock()
	svc, err := service.New(service.Options{
		Store: st, Clock: clk, Zone: zone, WeekStart: weekStart, DueSoonDays: e.Server.DueSoonDays,
		Backups: &backup.Manager{Dir: e.Server.Backup.Dir, Clock: clk, Snap: st.Snap},
		Ledger:  ledger, Passwords: auth.NewPasswordGate(clk), Logger: e.Logger,
	})
	return svc, offlineError(err)
}

func (e *Env) openLive(ctx context.Context) (_ *store.Store, _ *service.Service, err error) {
	st, err := e.openStore(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if err != nil {
			err = offlineError(errors.Join(err, st.Close()))
		}
	}()
	ledger, _, err := backup.OpenLedger(e.Paths.LedgerFile)
	if err != nil {
		return nil, nil, offlineError(err)
	}
	svc, err := e.newService(st, ledger)
	if err != nil {
		return nil, nil, err
	}
	return st, svc, nil
}

func (e *Env) verifyPassword(ctx context.Context, q store.Q) error {
	if !e.TTY {
		return domain.Errorf(domain.CodePasswordRequired, "the password can only be entered on a terminal")
	}
	if e.ReadPassword == nil {
		return domain.Errorf(domain.CodeInternal, "password reader is not configured")
	}
	if err := ctx.Err(); err != nil {
		return offlineError(err)
	}
	state, err := store.GetAuthState(ctx, q)
	if err != nil {
		return offlineError(err)
	}
	for range 3 {
		if err := ctx.Err(); err != nil {
			return offlineError(err)
		}
		password, err := e.ReadPassword("Password: ")
		if err != nil {
			return offlineError(err)
		}
		if err := ctx.Err(); err != nil {
			return offlineError(err)
		}
		ok, err := auth.VerifyPassword(state.PasswordHash, password)
		if err != nil {
			return offlineError(err)
		}
		if ok {
			return nil
		}
		select {
		case <-e.clock().After(time.Second):
		case <-ctx.Done():
			return offlineError(ctx.Err())
		}
	}
	return domain.Errorf(domain.CodePasswordInvalid, "incorrect password")
}

func (e *Env) clock() clock.Clock {
	if e.Clock == nil {
		return clock.Real{}
	}
	return e.Clock
}

func checkpoint(ctx context.Context, st *store.Store) error {
	return offlineError(st.CheckpointTruncate(ctx))
}

func offlineError(err error) error {
	if err == nil {
		return nil
	}
	de, _ := domain.AsError(store.MapErr(err))
	return de
}

type crashHookKey struct{}

// WithCrashHook installs a context-scoped test hook for offline crash points.
// A hook error stops the operation at that boundary for recovery testing.
func WithCrashHook(ctx context.Context, h func(point string) error) context.Context {
	return context.WithValue(ctx, crashHookKey{}, h)
}

func crashPoint(ctx context.Context, point string) error {
	if h, _ := ctx.Value(crashHookKey{}).(func(string) error); h != nil {
		if err := h(point); err != nil {
			return err
		}
	}
	return ctx.Err()
}
