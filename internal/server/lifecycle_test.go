package server_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linsomniac/taskronin/internal/auth"
	"github.com/linsomniac/taskronin/internal/backup"
	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/clock"
	"github.com/linsomniac/taskronin/internal/config"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/lock"
	"github.com/linsomniac/taskronin/internal/server"
	"github.com/linsomniac/taskronin/internal/service"
	"github.com/linsomniac/taskronin/internal/store"
	"github.com/linsomniac/taskronin/internal/testutil"
)

func runFixture(t *testing.T, s *testutil.Server) error {
	t.Helper()
	cfg, err := config.LoadServer(s.Paths.ServerConfig)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	return server.Run(ctx, cfg, server.Deps{Paths: s.Paths, Clock: s.Clock, Logger: slog.New(slog.NewJSONHandler(s.Log, nil))})
}

func TestRunServesHealthz(t *testing.T) {
	s := testutil.StartServer(t, testutil.InitOnly())
	path := filepath.Join(filepath.Dir(s.Paths.RuntimeDir), "notify")
	ln, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	t.Setenv("NOTIFY_SOCKET", path)
	s.Start(t)
	if err := s.Client.Health(t.Context()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.Socket)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode: %v, %v", info, err)
	}
	readNotify := func(want string) {
		t.Helper()
		if err := ln.SetReadDeadline(clock.Real{}.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		var buf [64]byte
		n, _, err := ln.ReadFromUnix(buf[:])
		if err != nil || string(buf[:n]) != want {
			t.Fatalf("notification = %q, %v; want %q", buf[:n], err, want)
		}
	}
	readNotify("READY=1")
	s.Stop()
	readNotify("STOPPING=1")
}

func TestRunRefusesSecondInstance(t *testing.T) {
	s := testutil.StartServer(t)
	if err := runFixture(t, s); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second Run = %v", err)
	}
	if err := s.Client.Health(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRunRefusesJournals(t *testing.T) {
	for _, kind := range []string{"swap", "scrub"} {
		t.Run(kind, func(t *testing.T) {
			s := testutil.StartServer(t, testutil.InitOnly())
			path, want := s.Paths.SwapJournal(), "tkr restore --resume"
			if kind == "scrub" {
				path, want = config.ScrubJournal(s.Paths.BackupDir), "tkr scrub-backups --resume"
			}
			writeLifecycleFile(t, path, "journal")
			temp := filepath.Join(s.Paths.StagingDir, "keep")
			writeLifecycleFile(t, temp, "recovery data")
			if err := runFixture(t, s); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("journal refusal = %v", err)
			}
			if _, err := os.Stat(temp); err != nil {
				t.Fatalf("cleanup ran before journal refusal: %v", err)
			}
		})
	}
}

func TestRunRefusesMissingDB(t *testing.T) {
	s := testutil.StartServer(t, testutil.InitOnly())
	if err := os.Remove(s.Paths.DB); err != nil {
		t.Fatal(err)
	}
	if err := runFixture(t, s); err == nil || !strings.Contains(err.Error(), "database not found; run `tkr init`") {
		t.Fatalf("missing DB = %v", err)
	}
	if _, err := os.Stat(s.Paths.DB); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing database created: %v", err)
	}
}

func pendingTask(t *testing.T, s *testutil.Server) {
	t.Helper()
	r, err := s.Svc.AddTask(t.Context(), testutil.Meta(testutil.Owner(), s.Svc.Generation()), service.AddTaskIn{Title: "secret title"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Svc.Delete(t.Context(), testutil.Meta(testutil.Owner(), s.Svc.Generation()), service.TaskRef{ID: r.Task.ID, Expect: domain.Expect{Gen: s.Svc.Generation(), Version: r.Task.Version}})
	if err != nil {
		t.Fatal(err)
	}
	s.Stop()
	ledger, _, err := backup.OpenLedger(s.Paths.LedgerFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Append(backup.LedgerRecord{V: 1, Rec: domain.NewOpID().String(), At: s.Clock.Now().UnixMilli(), State: backup.LedgerPending, Tasks: []domain.TaskID{r.Task.ID}}); err != nil {
		t.Fatal(err)
	}
}

func TestPRG09_StartupReplaysPendingLedger(t *testing.T) {
	s := testutil.StartServer(t)
	pendingTask(t, s)
	s.Start(t)
	task, err := s.Svc.GetTask(t.Context(), testutil.Owner(), 1)
	if err != nil || task.Title != "[purged]" {
		t.Fatalf("replayed task = %+v, %v", task, err)
	}
	status, err := s.Svc.Status(t.Context(), testutil.Owner())
	if err != nil || !status.PurgeWarning {
		t.Fatalf("purge warning = %+v, %v", status, err)
	}
	requireBanner(t, s, "purge_incomplete")
	s.Stop()
	ledger, _, err := backup.OpenLedger(s.Paths.LedgerFile)
	if err != nil {
		t.Fatal(err)
	}
	records, err := ledger.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].State != backup.LedgerPending {
		t.Fatal("startup completed pending ledger")
	}
}

func TestPRG11_StartupCheckpointsAfterNoopReplay(t *testing.T) {
	s := testutil.StartServer(t)
	pendingTask(t, s)
	s.Start(t)
	before, err := s.Svc.ListOps(t.Context(), testutil.Owner(), service.ListOpsIn{Actor: "system"})
	if err != nil {
		t.Fatal(err)
	}
	s.Stop()
	s.Start(t)
	after, err := s.Svc.ListOps(t.Context(), testutil.Owner(), service.ListOpsIn{Actor: "system"})
	if err != nil || len(before.Items) != len(after.Items) {
		t.Fatalf("noop replay added ops: %v, %v", after.Items, err)
	}
}

func TestRunCleansTemps(t *testing.T) {
	s := testutil.StartServer(t, testutil.InitOnly())
	paths := []string{filepath.Join(s.Paths.StagingDir, "x"), filepath.Join(s.Paths.BackupDir, ".tkr-tmp-y")}
	for _, path := range paths {
		writeLifecycleFile(t, path, "leftover")
	}
	s.Start(t)
	for _, path := range paths {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("temp %s remains: %v", path, err)
		}
	}
}

func TestRunDamagedLedgerBanner(t *testing.T) {
	s := testutil.StartServer(t, testutil.InitOnly())
	writeLifecycleFile(t, s.Paths.LedgerFile, "{\"sum\":\"bad\"}\n")
	s.Start(t)
	requireBanner(t, s, "purge_ledger_damaged")
}

func requireBanner(t *testing.T, s *testutil.Server, kind string) {
	t.Helper()
	v, err := s.Svc.Today(t.Context(), testutil.Owner())
	if err != nil {
		t.Fatal(err)
	}
	for _, banner := range v.Banners {
		if banner.Kind == kind {
			return
		}
	}
	t.Fatalf("missing %s banner: %+v", kind, v.Banners)
}

func TestRunTimezoneChangeWarns(t *testing.T) {
	s := testutil.StartServer(t, testutil.InitOnly())
	st, err := store.Open(t.Context(), s.Paths.DB)
	if err != nil {
		t.Fatal(err)
	}
	// F14: a previous configured timezone is a startup-only persisted fixture.
	if err := store.SetSetting(t.Context(), st.Writer, "tz_last_seen", "America/New_York"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	s.Start(t)
	if !strings.Contains(s.Log.String(), "timezone changed from America/New_York to America/Denver; wall-clock times keep their meaning") {
		t.Fatalf("missing timezone warning: %s", s.Log.String())
	}
}

func TestWebListenerGate(t *testing.T) {
	var listen string
	restore := server.SetWebHandler(func(_ *service.Service, _ clock.Clock, _ civil.Zone, _ time.Weekday, addr string, _ *slog.Logger) (http.Handler, error) {
		listen = addr
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if server.ConnInfoFrom(r.Context()).Transport != server.TransportTCP {
				t.Error("missing TCP ConnContext")
			}
			_, _ = io.WriteString(w, "stub")
		}), nil
	})
	defer restore()
	t.Run("enabled", func(t *testing.T) {
		s := testutil.StartServer(t, testutil.WithWeb())
		if s.WebURL == "" || s.WebURL != "http://"+listen {
			t.Fatalf("bound web address = %q, adapter = %q", s.WebURL, listen)
		}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.WebURL+"/x", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil || string(body) != "stub" || resp.Header.Get("X-Request-ID") == "" {
			t.Fatalf("web response = %q, %v, %v", body, resp.Header, err)
		}
	})
	t.Run("no consent", func(t *testing.T) {
		s := testutil.StartServer(t, testutil.WithWeb(), testutil.WithConfig(func(c *config.Server) { c.Web.AllowPlainLoopback = false }))
		if s.WebURL != "" || !strings.Contains(s.Log.String(), "allow_plain_loopback = true") {
			t.Fatalf("web consent: %q, %s", s.WebURL, s.Log.String())
		}
		if err := s.Client.Health(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("no handler", func(t *testing.T) {
		restore := server.SetWebHandler(nil)
		defer restore()
		s := testutil.StartServer(t, testutil.WithWeb())
		if s.WebURL != "" {
			t.Fatalf("unexpected web URL %q", s.WebURL)
		}
	})
}

type schedulerEvent struct {
	job, event string
	at         time.Time
}

func observeSchedulers(t *testing.T) <-chan schedulerEvent {
	t.Helper()
	events := make(chan schedulerEvent, 256)
	restore := server.SetSchedulerObserver(func(job, event string, at time.Time) { events <- schedulerEvent{job, event, at} })
	t.Cleanup(restore)
	return events
}

func waitScheduler(t *testing.T, events <-chan schedulerEvent, job, event string) time.Time {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case got := <-events:
			if got.job == job && got.event == event {
				return got.at
			}
		case <-timer.C:
			t.Fatalf("waiting for %s %s", job, event)
			return time.Time{}
		}
	}
}

func TestSchedulerReconcilesAtMidnight(t *testing.T) {
	events := observeSchedulers(t)
	s := testutil.StartServer(t)
	_, err := s.Svc.AddTask(t.Context(), testutil.Meta(testutil.Owner(), s.Svc.Generation()), service.AddTaskIn{Title: "scheduled", Place: &service.AddPlacement{How: "block", Block: domain.BlockSpan{Date: testutil.D("10-28"), Start: 13 * 60, End: 15 * 60}}})
	if err != nil {
		t.Fatal(err)
	}
	want := testutil.Local(t, "10-28 10:01").Add(500 * time.Millisecond)
	if at := waitScheduler(t, events, "reconcile", "armed"); !at.Equal(want) {
		t.Fatalf("first deadline = %v, want %v", at, want)
	}
	s.Clock.Set(testutil.Local(t, "10-28 23:59:30"))
	waitScheduler(t, events, "reconcile", "ran")
	want = testutil.Local(t, "10-29 00:00").Add(500 * time.Millisecond)
	if at := waitScheduler(t, events, "reconcile", "armed"); !at.Equal(want) {
		t.Fatalf("midnight deadline = %v, want %v", at, want)
	}
	s.Clock.Set(want)
	waitScheduler(t, events, "reconcile", "ran")
	ops, err := s.Svc.ListOps(t.Context(), testutil.Owner(), service.ListOpsIn{Actor: "system"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, op := range ops.Items {
		if op.Kind == domain.OpSystemReconcile && op.At.Equal(want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing midnight reconcile: %+v", ops.Items)
	}
	task, err := s.Svc.GetTask(t.Context(), testutil.Owner(), 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, reason := range task.Reasons {
		if reason.Reason == domain.ReasonSlipped {
			return
		}
	}
	t.Fatal("scheduled task did not slip")
}

func TestStartupBackupWhenStale(t *testing.T) {
	events := observeSchedulers(t)
	s := testutil.StartServer(t)
	waitScheduler(t, events, "backup", "ran")
	v, err := s.Svc.BackupStatus(t.Context(), testutil.Owner())
	if err != nil || len(v.Snapshots) != 1 || v.Snapshots[0].Kind != backup.KindDaily {
		t.Fatalf("startup backup = %+v, %v", v, err)
	}
}

func TestShutdownDrainsInFlightOp(t *testing.T) {
	events := observeSchedulers(t)
	s := testutil.StartServer(t)
	waitScheduler(t, events, "backup", "ran")
	st, err := store.Open(t.Context(), s.Paths.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	tx, err := st.Writer.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	op := make(chan error, 1)
	go func() {
		_, err := s.Svc.AddTask(context.Background(), testutil.Meta(testutil.Owner(), s.Svc.Generation()), service.AddTaskIn{Title: "draining"})
		op <- err
	}()
	// The external IMMEDIATE transaction blocks the admitted writer in SQLite.
	select {
	case err := <-op:
		t.Fatalf("writer did not block: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	stopped := make(chan struct{})
	go func() { s.Stop(); close(stopped) }()
	select {
	case <-s.Svc.Stopping():
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not begin")
	}
	_, err = s.Svc.AddTask(t.Context(), testutil.Meta(testutil.Owner(), s.Svc.Generation()), service.AddTaskIn{Title: "too late"})
	testutil.RequireCode(t, err, domain.CodeBusy)
	for range 10 {
		held, err := lock.Acquire(s.Paths.LockFile())
		if held != nil {
			_ = held.Release()
		}
		if !errors.Is(err, lock.ErrHeld) {
			t.Fatalf("lock released before drain: %v", err)
		}
		select {
		case <-stopped:
			t.Fatal("Stop returned before writer")
		case <-time.After(30 * time.Millisecond):
		}
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-op:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("accepted writer did not finish")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not finish")
	}
	held, err := lock.Acquire(s.Paths.LockFile())
	if err != nil {
		t.Fatal(err)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket remains: %v", err)
	}
}

func TestRunConfiguredDataDir(t *testing.T) {
	var configured string
	s := testutil.StartServer(t, testutil.WithConfig(func(c *config.Server) {
		configured = c.Paths.DataDir + "-custom"
		c.Paths.DataDir = configured
		c.Backup.Dir = filepath.Join(configured, "custom-backups")
	}))
	if s.DataDir != configured {
		t.Fatalf("data dir = %s, want %s", s.DataDir, configured)
	}
	if err := runFixture(t, s); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("configured lock = %v", err)
	}
	if _, err := os.Stat(filepath.Join(configured, "taskronin.db")); err != nil {
		t.Fatal(err)
	}
	// BF01: callers may still supply the original XDG paths alongside overrides.
	defaults, err := config.DefaultPaths(os.Getenv, configured)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadServer(s.Paths.ServerConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Run(t.Context(), cfg, server.Deps{Paths: defaults, Clock: s.Clock}); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("stale XDG paths bypassed configured lock: %v", err)
	}
	if _, err := os.Stat(defaults.DataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("default data dir was touched: %v", err)
	}
}

func TestRunRefusesInvalidDatabaseState(t *testing.T) {
	for _, fixture := range []struct{ name, query, want string }{
		{"no auth", "DELETE FROM auth_state", "run `tkr init`"},
		{"new schema", "UPDATE schema_migrations SET version = 999", "newer than this binary"},
		{"checksum", "UPDATE schema_migrations SET checksum = 'damaged'", "checksum mismatch"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			s := testutil.StartServer(t, testutil.InitOnly())
			st, err := store.Open(t.Context(), s.Paths.DB)
			if err != nil {
				t.Fatal(err)
			}
			// F14: simulate damaged/uninitialized or future-binary database state.
			if _, err := st.Writer.ExecContext(t.Context(), fixture.query); err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			if err := runFixture(t, s); err == nil || !strings.Contains(err.Error(), fixture.want) {
				t.Fatalf("startup refusal: %v", err)
			}
			if _, err := os.Stat(s.Socket); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("listener created before refusal: %v", err)
			}
			held, err := lock.Acquire(s.Paths.LockFile())
			if err != nil {
				t.Fatal(err)
			}
			if err := held.Release(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPRG11_StartupRefusesUnconfirmedCheckpoint(t *testing.T) {
	s := testutil.StartServer(t)
	pendingTask(t, s)
	s.Start(t)
	s.Stop() // the pending set is already purged: the next replay is a no-op
	st, err := store.Open(t.Context(), s.Paths.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	tx, err := st.Reader.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := store.GetSetting(t.Context(), tx, "tz_last_seen"); err != nil {
		t.Fatal(err)
	}
	// F14: hold an older WAL reader across a new committed frame, forcing
	// TRUNCATE to report busy even though replay itself has no scrub work.
	if err := store.SetSetting(t.Context(), st.Writer, "last_backup_error", "checkpoint fixture"); err != nil {
		t.Fatal(err)
	}
	if err := runFixture(t, s); err == nil || !strings.Contains(err.Error(), "checkpoint") {
		t.Fatalf("unconfirmed checkpoint allowed startup: %v", err)
	}
	if _, err := os.Stat(s.Socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("served despite incomplete checkpoint: %v", err)
	}
}

func TestSchedulerBackupCivilTime(t *testing.T) {
	for _, tc := range []struct{ name, now, at, next, daily string }{
		{"gap", "2027-03-14 00:30", "2027-03-14T09:00:00Z", "2027-03-15T08:30:00Z", "02:30"},
		{"overlap", "2026-11-01 00:30", "2026-11-01T07:30:00Z", "2026-11-02T08:30:00Z", "01:30"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := observeSchedulers(t)
			s := testutil.StartServer(t, testutil.WithClock(tc.now), testutil.WithConfig(func(c *config.Server) { c.Backup.Time = tc.daily }))
			want := testutil.Local(t, tc.at)
			if at := waitScheduler(t, events, "backup", "armed"); !at.Equal(want) {
				t.Fatalf("backup deadline = %v, want %v", at, want)
			}
			s.Clock.Set(want)
			waitScheduler(t, events, "backup", "ran")
			if at := waitScheduler(t, events, "backup", "armed"); !at.Equal(testutil.Local(t, tc.next)) {
				t.Fatalf("next daily deadline = %v", at)
			}
			v, err := s.Svc.BackupStatus(t.Context(), testutil.Owner())
			if err != nil || len(v.Snapshots) != 2 || !v.LastSuccessAt.V.Equal(want) {
				t.Fatalf("daily backup = %+v, %v", v, err)
			}
		})
	}
}

func TestStartupBackupStalenessBoundary(t *testing.T) {
	for _, age := range []time.Duration{24 * time.Hour, 24*time.Hour + time.Millisecond} {
		t.Run(age.String(), func(t *testing.T) {
			events := observeSchedulers(t)
			s := testutil.StartServer(t)
			waitScheduler(t, events, "backup", "armed")
			s.Stop()
			for len(events) > 0 {
				<-events
			}
			s.Clock.Advance(age)
			s.Start(t)
			waitScheduler(t, events, "backup", "armed")
			v, err := s.Svc.BackupStatus(t.Context(), testutil.Owner())
			want := 1
			if age > 24*time.Hour {
				want = 2
			}
			if err != nil || len(v.Snapshots) != want {
				t.Fatalf("backup count at age %v = %d, %v", age, len(v.Snapshots), err)
			}
		})
	}
}

func TestSchedulerMaintenanceAndFinalUsageFlush(t *testing.T) {
	events := observeSchedulers(t)
	s := testutil.StartServer(t, testutil.WithClock("2026-10-20 10:00"), testutil.WithMCPToken())
	m := testutil.Meta(testutil.Owner(), s.Svc.Generation())
	if _, err := s.Svc.AddTask(t.Context(), m, service.AddTaskIn{Title: "old request"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Svc.Login(t.Context(), service.LoginIn{RemoteKey: "fixture", Password: testutil.Password}); err != nil {
		t.Fatal(err)
	}
	s.Stop()
	for len(events) > 0 {
		<-events
	}
	s.Clock.Set(testutil.Local(t, testutil.DefaultNow))
	s.Start(t)
	waitScheduler(t, events, "maintenance", "armed")
	st, err := store.Open(t.Context(), s.Paths.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	s.TokenClient(t, "limited", domain.ScopeRead, 7, "api")
	contents, err := os.ReadFile(s.Paths.TokenFile("limited"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.Paths.TokenFile("limited"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token mode: %v, %v", info, err)
	}
	token := strings.TrimSpace(string(contents))
	id, _, err := auth.ParseToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Svc.AuthenticateToken(t.Context(), token, true, domain.ChanAPI, "socket"); err != nil {
		t.Fatal(err)
	}
	s.Clock.Set(testutil.Local(t, "10-28 10:01"))
	waitScheduler(t, events, "maintenance", "ran")
	waitScheduler(t, events, "maintenance", "armed")
	row, err := store.GetToken(t.Context(), st.Reader, id)
	if err != nil || !row.LastUsedAt.Valid || !row.LastUsedAt.V.Equal(testutil.Local(t, testutil.DefaultNow)) {
		t.Fatalf("minutely usage not flushed: %+v, %v", row.LastUsedAt, err)
	}
	if _, err := store.GetIdem(t.Context(), st.Reader, m.Actor.Key(), m.Idem.Key); err != nil {
		t.Fatalf("idempotency expired before hourly job: %v", err)
	}
	s.Clock.Set(testutil.Local(t, "10-28 11:00"))
	waitScheduler(t, events, "maintenance", "ran")
	if _, err := store.GetIdem(t.Context(), st.Reader, m.Actor.Key(), m.Idem.Key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("hourly idempotency expiry: %v", err)
	}
	var sessions int
	if err := st.Reader.QueryRowContext(t.Context(), "SELECT count(*) FROM web_sessions").Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("hourly session expiry: %d, %v", sessions, err)
	}
	if _, err := s.Svc.AuthenticateToken(t.Context(), token, true, domain.ChanAPI, "socket"); err != nil {
		t.Fatal(err)
	}
	s.Stop()
	row, err = store.GetToken(t.Context(), st.Reader, id)
	if err != nil || !row.LastUsedAt.V.Equal(s.Clock.Now()) {
		t.Fatalf("final usage not flushed: %+v, %v", row.LastUsedAt, err)
	}
}

func TestShutdownJoinsSchedulers(t *testing.T) {
	armed, finished, release := make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
	restore := server.SetSchedulerObserver(func(job, event string, _ time.Time) {
		if job != "maintenance" {
			return
		}
		if event == "armed" {
			armed <- struct{}{}
			return
		}
		close(finished)
		<-release
	})
	t.Cleanup(restore)
	s := testutil.StartServer(t)
	defer close(release) // unblock before StartServer's Stop cleanup on failure
	select {
	case <-armed:
	case <-time.After(5 * time.Second):
		t.Fatal("maintenance not armed")
	}
	s.Clock.Advance(time.Minute)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("maintenance not run")
	}
	stopped := make(chan struct{})
	go func() { s.Stop(); close(stopped) }()
	select {
	case <-s.Svc.Stopping():
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not start")
	}
	select {
	case <-stopped:
		t.Fatal("shutdown skipped scheduler join")
	case <-time.After(100 * time.Millisecond):
	}
	held, err := lock.Acquire(s.Paths.LockFile())
	if held != nil {
		_ = held.Release()
	}
	if !errors.Is(err, lock.ErrHeld) {
		t.Fatalf("scheduler outlived instance lock: %v", err)
	}
	if _, err := os.Stat(s.Socket); err != nil {
		t.Fatalf("socket removed before scheduler joined: %v", err)
	}
}

func TestShutdownHTTPGraceUsesRealTime(t *testing.T) {
	entered, exited := make(chan struct{}), make(chan struct{})
	restore := server.SetWebHandler(func(_ *service.Service, _ clock.Clock, _ civil.Zone, _ time.Weekday, _ string, _ *slog.Logger) (http.Handler, error) {
		return http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			close(entered)
			<-r.Context().Done()
			close(exited)
		}), nil
	})
	t.Cleanup(restore)
	s := testutil.StartServer(t, testutil.WithWeb())
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.WebURL+"/slow", nil)
	if err != nil {
		t.Fatal(err)
	}
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("web request did not start")
	}
	start := clock.Real{}.Now()
	s.Stop() // the fake clock never advances
	elapsed := clock.Real{}.Now().Sub(start)
	if elapsed < 10*time.Second || elapsed > 14*time.Second {
		t.Fatalf("HTTP grace duration = %v", elapsed)
	}
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("expired HTTP request was not canceled")
	}
	<-requestDone
}

func writeLifecycleFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWebFailureKeepsAPI(t *testing.T) {
	for _, failure := range []string{"busy port", "handler"} {
		t.Run(failure, func(t *testing.T) {
			ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			opts := []testutil.ServerOpt{testutil.WithWeb()}
			if failure == "busy port" {
				opts = append(opts, testutil.WithConfig(func(c *config.Server) { c.Web.Listen = ln.Addr().String() }))
			} else {
				restore := server.SetWebHandler(func(_ *service.Service, _ clock.Clock, _ civil.Zone, _ time.Weekday, _ string, _ *slog.Logger) (http.Handler, error) {
					return nil, errors.New("handler failure\n\x1b")
				})
				defer restore()
			}
			s := testutil.StartServer(t, opts...)
			if s.WebURL != "" {
				t.Errorf("web address = %q", s.WebURL)
			}
			if _, err := s.Client.WhoAmI(t.Context()); err != nil {
				t.Fatalf("socket API: %v", err)
			}
			if log := s.Log.String(); !strings.Contains(log, `"level":"WARN"`) || !strings.Contains(log, "web UI disabled") {
				t.Errorf("missing warning: %s", log)
			} else if failure == "handler" && !strings.Contains(log, "handler failure��") {
				t.Errorf("unsanitized diagnostic: %s", log)
			}
		})
	}
}

type failingWebListener struct{ net.Listener }

func (ln failingWebListener) Accept() (net.Conn, error) {
	conn, err := ln.Listener.Accept()
	if errors.Is(err, net.ErrClosed) {
		return nil, errors.New("web accept failed\n\x1b")
	}
	return conn, err
}

func TestRuntimeWebFailureKeepsAPI(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	t.Cleanup(server.SetWebListener(failingWebListener{ln}))
	s := testutil.StartServer(t, testutil.WithWeb())
	resp, _ := newWebBrowser(t, s.WebURL).request(http.MethodGet, "/ui/login", nil)
	assertWebPage(t, resp, http.StatusOK, "/ui/login")
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	timeout := time.NewTimer(time.Minute)
	defer timeout.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for !strings.Contains(s.Log.String(), `"level":"WARN","msg":"web UI disabled","error":"web accept failed��"`) {
		select {
		case <-s.Svc.Stopping():
			t.Fatal("web listener failure stopped the API")
		case <-timeout.C:
			t.Fatalf("missing sanitized Serve warning: %s", s.Log)
		case <-tick.C:
		}
	}
	if _, err := s.Client.WhoAmI(t.Context()); err != nil {
		t.Fatalf("socket API after web listener failure: %v", err)
	}
	s.Stop()
}
