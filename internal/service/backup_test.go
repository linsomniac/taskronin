package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linsomniac/taskronin/internal/backup"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/service"
	"github.com/linsomniac/taskronin/internal/store"
	"github.com/linsomniac/taskronin/internal/testutil"
)

func backupSetting(t *testing.T, s *testutil.Scenario, key string) string {
	t.Helper()
	v, err := store.GetSetting(s.Ctx(), s.Store.Reader, key)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestIDM04(t *testing.T) {
	var logs bytes.Buffer
	s := testutil.NewScenario(t, func(o *service.Options) {
		o.Logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	m := s.Meta()
	m.Idem.Body = []byte(`{"password":"` + testutil.Password + `"}`)
	if _, err := s.Svc.Login(s.Ctx(), service.LoginIn{RemoteKey: "socket", Password: testutil.Password}); err != nil {
		t.Fatal(err)
	}
	if err := s.Svc.ChangePassword(s.Ctx(), m, service.ChangePasswordIn{Current: testutil.Password, New: testutil.Password}); err != nil {
		t.Fatal(err)
	}
	daily := 3
	if _, err := s.Svc.UpdateSettings(s.Ctx(), m, service.UpdateSettingsIn{KeepDaily: &daily, Password: testutil.Password}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Svc.CreateToken(s.Ctx(), m, service.CreateTokenIn{Name: "exempt", Scopes: domain.ScopeRead}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Svc.RunBackup(s.Ctx(), testutil.Owner(), backup.KindManual); err != nil {
		t.Fatal(err)
	}
	requireSQLCount(t, s, `SELECT COUNT(*) FROM idempotency`, 0)
	for _, table := range []string{"ops", "events"} {
		if err := store.ExportTable(s.Ctx(), s.Store.Reader, table, func(row map[string]any) error {
			data, err := json.Marshal(row)
			if err != nil {
				return err
			}
			if bytes.Contains(data, []byte(testutil.Password)) {
				t.Errorf("password in %s", table)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if bytes.Contains(logs.Bytes(), []byte(testutil.Password)) {
		t.Fatal("password in logs")
	}
}

func TestRunBackup(t *testing.T) {
	for _, actor := range []domain.Actor{testutil.Owner(), domain.SystemActor()} {
		t.Run(actor.Key(), func(t *testing.T) {
			var manager *backup.Manager
			s := testutil.NewScenario(t, func(o *service.Options) { manager = o.Backups })
			daily, weekly := 3, 1
			if _, err := s.Svc.UpdateSettings(s.Ctx(), s.Meta(), service.UpdateSettingsIn{
				KeepDaily: &daily, KeepWeekly: &weekly, Password: testutil.Password,
			}); err != nil {
				t.Fatal(err)
			}
			var old []backup.SnapshotInfo
			for _, date := range []string{"2026-10-01 10:00", "2026-10-08 10:00", "2026-10-15 10:00", "2026-10-22 10:00"} {
				s.At(date)
				info, err := manager.Snapshot(s.Ctx(), backup.KindDaily)
				if err != nil {
					t.Fatal(err)
				}
				old = append(old, info)
			}
			s.At(testutil.DefaultNow)
			before := s.OpCount()
			info, err := s.Svc.RunBackup(s.Ctx(), actor, backup.KindManual)
			if err != nil {
				t.Fatal(err)
			}
			if stat, err := os.Stat(info.Path); err != nil || stat.Size() != info.Size || info.SchemaVersion != 1 {
				t.Fatalf("snapshot = %+v, stat error = %v", info, err)
			}
			for i, snapshot := range old {
				_, err := os.Stat(snapshot.Path)
				if (i < 2 && !errors.Is(err, os.ErrNotExist)) || (i >= 2 && err != nil) {
					t.Fatalf("rotation for %s: %v", snapshot.Name, err)
				}
			}
			want := strconv.FormatInt(s.Clock.Now().UnixMilli(), 10)
			for _, key := range []string{"last_backup_attempt_at", "last_backup_at"} {
				if got := backupSetting(t, s, key); got != want {
					t.Fatalf("%s = %q, want %q", key, got, want)
				}
			}
			if backupSetting(t, s, "last_backup_error") != "" || s.OpCount() != before {
				t.Fatal("backup recorded an error or an operation")
			}
			requireSQLCount(t, s, `SELECT COUNT(*) FROM events`, 2)
			status, err := s.Svc.BackupStatus(s.Ctx(), testutil.Owner())
			if err != nil || status.Dir != manager.Dir || len(status.Snapshots) != 3 || !status.Healthy || status.Warning != "" {
				t.Fatalf("backup status = %+v, %v", status, err)
			}
		})
	}
}

func TestRunBackupFailure(t *testing.T) {
	var manager *backup.Manager
	s := testutil.NewScenario(t, func(o *service.Options) { manager = o.Backups })
	info, err := s.Svc.RunBackup(s.Ctx(), testutil.Owner(), backup.KindManual)
	if err != nil {
		t.Fatal(err)
	}
	last := backupSetting(t, s, "last_backup_at")
	s.At("2026-10-28 11:00")
	if err := os.Chmod(manager.Dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(manager.Dir, 0o700); err != nil {
			t.Error(err)
		}
	})
	_, err = s.Svc.RunBackup(s.Ctx(), testutil.Owner(), backup.KindDaily)
	testutil.RequireCode(t, err, domain.CodeInternal)
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("backup failure lost cause: %v", err)
	}
	if lastError := backupSetting(t, s, "last_backup_error"); !strings.Contains(lastError, "permission denied") {
		t.Fatalf("backup error = %q", lastError)
	}
	if backupSetting(t, s, "last_backup_at") != last ||
		backupSetting(t, s, "last_backup_attempt_at") != strconv.FormatInt(s.Clock.Now().UnixMilli(), 10) {
		t.Fatal("failure did not preserve success and record attempt")
	}
	if _, err := os.Stat(info.Path); err != nil {
		t.Fatal("failure removed an older snapshot:", err)
	}
	status, err := s.Svc.BackupStatus(s.Ctx(), testutil.Owner())
	if err != nil || status.Healthy || status.Warning != "Backup failed: "+status.LastError {
		t.Fatalf("backup status = %+v, %v", status, err)
	}
	if err := os.Chmod(manager.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Svc.RunBackup(s.Ctx(), testutil.Owner(), backup.KindDaily); err != nil {
		t.Fatal(err)
	}
	if backupSetting(t, s, "last_backup_error") != "" {
		t.Fatal("successful retry did not clear the error")
	}
	requireSQLCount(t, s, `SELECT COUNT(*) FROM events`, 0)
}

func TestBackupStatusHealthy(t *testing.T) {
	s := testutil.NewScenario(t)
	status, err := s.Svc.BackupStatus(s.Ctx(), testutil.Owner())
	if err != nil || status.Healthy || status.Warning != "No successful backup in the last 48 hours." {
		t.Fatalf("initial status = %+v, %v", status, err)
	}
	if _, err := s.Svc.RunBackup(s.Ctx(), testutil.Owner(), backup.KindDaily); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		at      string
		healthy bool
	}{
		{"2026-10-28 10:00", true},
		{"2026-10-30 10:00", true},
		{"2026-10-30 10:01", false},
	} {
		s.At(tc.at)
		status, err := s.Svc.BackupStatus(s.Ctx(), testutil.Owner())
		if err != nil || status.Healthy != tc.healthy || (status.Warning == "") != tc.healthy {
			t.Fatalf("status at %s = %+v, %v", tc.at, status, err)
		}
	}
}

func TestBackupAuthorizationAndConfiguration(t *testing.T) {
	s := testutil.NewScenario(t)
	a := testutil.TokenActor("reader", domain.ScopeRead)
	_, err := s.Svc.RunBackup(s.Ctx(), a, backup.KindManual)
	testutil.RequireCode(t, err, domain.CodeForbidden)
	for _, actor := range []domain.Actor{a, domain.SystemActor()} {
		_, err := s.Svc.BackupStatus(s.Ctx(), actor)
		testutil.RequireCode(t, err, domain.CodeForbidden)
	}
	if backupSetting(t, s, "last_backup_attempt_at") != "" {
		t.Fatal("forbidden backup recorded an attempt")
	}
	s = testutil.NewScenario(t, func(o *service.Options) { o.Backups = nil })
	_, err = s.Svc.RunBackup(s.Ctx(), testutil.Owner(), backup.KindManual)
	testutil.RequireCode(t, err, domain.CodeInternal)
	_, err = s.Svc.BackupStatus(s.Ctx(), testutil.Owner())
	testutil.RequireCode(t, err, domain.CodeInternal)
}

func TestRunBackupStopsOnShutdown(t *testing.T) {
	var dir string
	s := testutil.NewScenario(t, func(o *service.Options) { dir = o.Backups.Dir })
	started, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(func() { s.Svc.BeginShutdown(); unblock() })
	service.SetBackupHook(s.Svc, func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		<-release
		return ctx.Err()
	})
	result := make(chan error, 1)
	go func() {
		_, err := s.Svc.RunBackup(s.Ctx(), domain.SystemActor(), backup.KindDaily)
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("backup never started")
	}
	if backupSetting(t, s, "last_backup_attempt_at") == "" {
		t.Fatal("hook ran before attempt was recorded")
	}
	s.Svc.BeginShutdown()
	short, cancel := context.WithCancel(s.Ctx())
	cancel()
	if err := s.Svc.Drain(short); !errors.Is(err, context.Canceled) {
		t.Fatalf("Drain returned before backup unwound: %v", err)
	}
	unblock()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("backup shutdown = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("backup did not stop")
	}
	ctx, cancel := context.WithTimeout(s.Ctx(), 5*time.Second)
	defer cancel()
	if err := s.Svc.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if backupSetting(t, s, "last_backup_error") != context.Canceled.Error() {
		t.Fatal("shutdown cancellation was not recorded")
	}
	temps, err := filepath.Glob(filepath.Join(dir, ".tkr-tmp-*"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("temporary snapshots remain: %v, %v", temps, err)
	}
	_, err = s.Svc.RunBackup(s.Ctx(), testutil.Owner(), backup.KindManual)
	testutil.RequireCode(t, err, domain.CodeBusy)
}

func TestRunBackupCallerCancellation(t *testing.T) {
	s := testutil.NewScenario(t)
	ctx, cancel := context.WithCancel(s.Ctx())
	defer cancel()
	service.SetBackupHook(s.Svc, func(ctx context.Context) error {
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > service.LongOpBudget || time.Until(deadline) < 9*time.Minute {
			t.Error("backup missing ten-minute budget")
		}
		cancel()
		return ctx.Err()
	})
	_, err := s.Svc.RunBackup(ctx, testutil.Owner(), backup.KindManual)
	if !errors.Is(err, context.Canceled) || backupSetting(t, s, "last_backup_error") != context.Canceled.Error() {
		t.Fatalf("caller cancellation = %v", err)
	}
}

func startBackup(ctx context.Context, s *testutil.Scenario) <-chan error {
	result := make(chan error, 1)
	go func() {
		_, err := s.Svc.RunBackup(ctx, testutil.Owner(), backup.KindManual)
		result <- err
	}()
	return result
}

func awaitBackup(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("backup did not return")
		return nil
	}
}

func TestRunBackupOverlappingOutcomes(t *testing.T) {
	s := testutil.NewScenario(t)
	firstEntered, secondEntered, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(func() { s.Svc.BeginShutdown(); unblock() })
	var calls atomic.Int32
	want := errors.New("later backup failed")
	service.SetBackupHook(s.Svc, func(context.Context) error {
		if calls.Add(1) == 1 {
			close(firstEntered)
			<-release
			return nil
		}
		close(secondEntered)
		return want
	})
	first := startBackup(s.Ctx(), s)
	select {
	case <-firstEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("first backup never reached the hook")
	}
	s.Clock.Advance(time.Minute)
	second := startBackup(s.Ctx(), s)
	// Give the overlapping call a chance to fail before the first succeeds.
	// With workflow serialization it stays queued until the first is released.
	var secondErr error
	overlapped := false
	select {
	case <-secondEntered:
		overlapped = true
		secondErr = awaitBackup(t, second)
	case <-time.After(200 * time.Millisecond):
	}
	unblock()
	if err := awaitBackup(t, first); err != nil {
		t.Fatal(err)
	}
	if !overlapped {
		secondErr = awaitBackup(t, second)
	}
	if !errors.Is(secondErr, want) {
		t.Fatalf("second backup = %v, want %v", secondErr, want)
	}
	status, err := s.Svc.BackupStatus(s.Ctx(), testutil.Owner())
	if err != nil || status.Healthy || status.LastError != want.Error() || status.Warning != "Backup failed: "+want.Error() ||
		!status.LastSuccessAt.Valid || len(status.Snapshots) != 1 {
		t.Fatalf("overlapping success erased the later failure: %+v, %v", status, err)
	}
	if backupSetting(t, s, "last_backup_attempt_at") != strconv.FormatInt(s.Clock.Now().UnixMilli(), 10) {
		t.Fatal("latest attempt time was not recorded")
	}
	requireSQLCount(t, s, `SELECT COUNT(*) FROM events`, 0)
}

func TestRunBackupWaitingCancellation(t *testing.T) {
	for _, reason := range []string{"caller", "deadline", "shutdown"} {
		t.Run(reason, func(t *testing.T) {
			s := testutil.NewScenario(t)
			entered, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			t.Cleanup(func() { s.Svc.BeginShutdown(); unblock() })
			var calls atomic.Int32
			service.SetBackupHook(s.Svc, func(context.Context) error {
				if calls.Add(1) == 1 {
					close(entered)
					<-release
				}
				return nil
			})
			first := startBackup(s.Ctx(), s)
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("first backup never reached the hook")
			}
			attempt := backupSetting(t, s, "last_backup_attempt_at")
			s.Clock.Advance(time.Minute)
			ctx, cancel := context.WithCancel(s.Ctx())
			if reason == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(s.Ctx(), 100*time.Millisecond)
			}
			defer cancel()
			second := startBackup(ctx, s)
			want := context.Canceled
			switch reason {
			case "caller":
				cancel()
			case "deadline":
				want = context.DeadlineExceeded
			case "shutdown":
				s.Svc.BeginShutdown()
			}
			secondErr := awaitBackup(t, second)
			if reason == "shutdown" {
				// Shutdown may close lifecycle admission before the waiting call enters.
				if de, ok := domain.AsError(secondErr); ok && de.Code == domain.CodeBusy {
					secondErr = context.Canceled
				}
			}
			if !errors.Is(secondErr, want) {
				t.Errorf("waiting backup = %v, want %v", secondErr, want)
			}
			if calls.Load() != 1 || backupSetting(t, s, "last_backup_attempt_at") != attempt || backupSetting(t, s, "last_backup_error") != "" {
				t.Error("canceled waiter changed the active backup's bookkeeping")
			}
			unblock()
			firstErr := awaitBackup(t, first)
			if reason == "shutdown" {
				if !errors.Is(firstErr, context.Canceled) {
					t.Fatalf("active backup did not stop: %v", firstErr)
				}
			} else if firstErr != nil {
				t.Fatal(firstErr)
			}
			ctx, cancel = context.WithTimeout(s.Ctx(), 5*time.Second)
			defer cancel()
			if err := s.Svc.Drain(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}
