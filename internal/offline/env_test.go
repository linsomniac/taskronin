package offline_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"taskronin/internal/auth"
	"taskronin/internal/backup"
	"taskronin/internal/clock"
	"taskronin/internal/config"
	"taskronin/internal/domain"
	"taskronin/internal/fsutil"
	"taskronin/internal/lock"
	"taskronin/internal/offline"
	"taskronin/internal/service"
	"taskronin/internal/store"
	"taskronin/internal/testutil"
)

var fixtureHash = sync.OnceValues(func() (string, error) { return auth.HashPassword(testutil.Password) })

func newEnv(t *testing.T) *offline.Env {
	t.Helper()
	oldMask := fsutil.SetUmask(0o077)
	t.Cleanup(func() { fsutil.SetUmask(oldMask) })
	root := t.TempDir()
	paths, err := config.DefaultPaths(func(key string) string { return filepath.Join(root, key) }, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{paths.DataDir, paths.StateDir, paths.BackupDir} {
		if err := fsutil.EnsureDir0700(dir); err != nil {
			t.Fatal(err)
		}
	}
	e := &offline.Env{Paths: paths, Server: config.DefaultServer(paths, "America/Denver"),
		Clock: testutil.Clock(t, testutil.DefaultNow), In: strings.NewReader(""),
		Out: new(bytes.Buffer), Err: new(bytes.Buffer), TTY: true,
		ReadPassword: func(string) (string, error) { return testutil.Password, nil }}
	st, err := store.OpenStaging(t.Context(), paths.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.Migrate(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	hash, err := fixtureHash()
	if err != nil {
		t.Fatal(err)
	}
	key, err := auth.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	secret, err := auth.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	name, err := auth.OwnerTokenName(e.Clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.BootstrapDB(t.Context(), st.Writer, store.Bootstrap{
		Generation: domain.NewGeneration(), TZ: "America/Denver",
		Auth: store.AuthState{PasswordHash: hash, PasswordChangedAt: e.Clock.Now(), IdemHMACKey: key},
		Owner: domain.Token{Name: name, SecretHash: auth.HashSecret(secret), Scopes: domain.AllScopes,
			IsOwner: true, CreatedAt: e.Clock.Now()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return e
}

func openDB(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	return st
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func requireAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s still exists or cannot be inspected: %v", path, err)
	}
}

func requireDetail(t *testing.T, err error, code domain.Code, detail string) {
	t.Helper()
	testutil.RequireCode(t, err, code)
	de, _ := domain.AsError(err)
	if de.Detail != detail {
		t.Fatalf("detail = %q, want %q", de.Detail, detail)
	}
}

func TestBeginRefusesWhenLocked(t *testing.T) {
	e := newEnv(t)
	held, err := lock.Acquire(e.Paths.LockFile())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Release() }()
	temp := filepath.Join(e.Paths.StagingDir, "x")
	writeFile(t, temp, "leftover")
	writeFile(t, e.Paths.SwapJournal(), "interrupted")
	err = offline.Migrate(t.Context(), e, true)
	requireDetail(t, err, domain.CodeBusy, "the server is running — `systemctl --user stop taskronin.socket taskronin.service` first")
	if _, err := os.Stat(temp); err != nil {
		t.Fatalf("cleanup ran before lock acquisition: %v", err)
	}
}

func TestBeginRefusesJournals(t *testing.T) {
	for _, command := range []string{"migrate", "backup"} {
		for _, journal := range []string{"swap", "scrub"} {
			t.Run(command+"/"+journal, func(t *testing.T) {
				e := newEnv(t)
				path := e.Paths.SwapJournal()
				detail := "an interrupted restore or import is pending — run `tkr restore --resume` or `tkr restore --rollback`"
				if journal == "scrub" {
					path = config.ScrubJournal(e.Server.Backup.Dir)
					detail = "an interrupted backup scrub is pending — run `tkr scrub-backups --resume`"
				}
				writeFile(t, path, "interrupted")
				temp := filepath.Join(e.Paths.StagingDir, "x")
				writeFile(t, temp, "leftover")
				var err error
				if command == "migrate" {
					err = offline.Migrate(t.Context(), e, true)
				} else {
					_, err = offline.Backup(t.Context(), e)
				}
				requireDetail(t, err, domain.CodeConflict, detail)
				if _, err := os.Stat(temp); err != nil {
					t.Fatalf("cleanup ran before journal refusal: %v", err)
				}
				held, err := lock.Acquire(e.Paths.LockFile())
				if err != nil {
					t.Fatalf("lock leaked after refusal: %v", err)
				}
				if err := held.Release(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestBeginCleansTemps(t *testing.T) {
	e := newEnv(t)
	paths := []string{filepath.Join(e.Paths.StagingDir, "x"), filepath.Join(e.Paths.DataDir, ".tkr-tmp-y"),
		filepath.Join(e.Server.Backup.Dir, ".tkr-tmp-z")}
	for _, path := range paths {
		writeFile(t, path, "leftover")
	}
	if err := offline.Migrate(t.Context(), e, true); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		requireAbsent(t, path)
	}
}

func TestBeginDefaultsAndPermissions(t *testing.T) {
	e := newEnv(t)
	e.Server, e.Clock, e.Out, e.Err = nil, nil, nil, nil // Init has no server config yet.
	for _, dir := range []string{e.Paths.DataDir, e.Paths.StateDir} {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fsutil.SetUmask(0o022)
	release, err := offline.Begin(e, t.Context(), offline.RefuseJournals)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, ok := e.Clock.(clock.Real); !ok {
		t.Fatalf("default clock = %T", e.Clock)
	}
	for _, dir := range []string{e.Paths.DataDir, e.Paths.StateDir} {
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("directory permissions: %v, %v", info, err)
		}
	}
	path := filepath.Join(e.Paths.DataDir, "umask-probe")
	if err := os.WriteFile(path, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("umask permissions: %v, %v", info, err)
	}
}

func TestBeginAllowsSwapRecovery(t *testing.T) {
	e := newEnv(t)
	staged := filepath.Join(e.Paths.StagingDir, "protected", "new.db")
	leftover := filepath.Join(e.Paths.StagingDir, "leftover")
	writeFile(t, staged, "staged replacement")
	writeFile(t, leftover, "leftover")
	want := errors.New("simulated swap crash")
	ctx := backup.WithStepHook(t.Context(), func(point string) error {
		if point == "prepared" {
			return want
		}
		return nil
	})
	err := backup.Swap(ctx, e.Paths.SwapJournal(), &backup.SwapJournal{
		Version: 1, Kind: backup.SwapRestore, New: staged, Target: e.Paths.DB,
		Aside: e.Paths.DB + ".pre-restore-test", StartedAt: e.Clock.Now(),
	}, nil)
	if !errors.Is(err, want) {
		t.Fatalf("swap crash = %v", err)
	}
	release, err := offline.Begin(e, t.Context(), offline.AllowSwapJournal)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	requireAbsent(t, leftover)
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("recovery preamble removed journal-protected data: %v", err)
	}
}

func TestBeginScrubPolicyDefersCleanup(t *testing.T) {
	e := newEnv(t)
	temp := filepath.Join(e.Paths.StagingDir, "x")
	writeFile(t, temp, "leftover")
	writeFile(t, config.ScrubJournal(e.Server.Backup.Dir), "interrupted")
	release, err := offline.Begin(e, t.Context(), offline.AllowScrubJournal)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := os.Stat(temp); err != nil {
		t.Fatalf("scrub preamble cleaned before password verification: %v", err)
	}
	writeFile(t, e.Paths.SwapJournal(), "interrupted")
	_, err = offline.Begin(e, t.Context(), offline.AllowScrubJournal)
	testutil.RequireCode(t, err, domain.CodeConflict)
	_, err = offline.Begin(e, t.Context(), offline.AllowSwapJournal)
	testutil.RequireCode(t, err, domain.CodeConflict)
}

func TestBeginConfiguredPaths(t *testing.T) {
	e := newEnv(t)
	effective := e.Paths
	defaults, err := config.DefaultPaths(func(string) string { return "" }, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e.Paths = defaults
	e.Server.Backup.Dir = filepath.Join(t.TempDir(), "configured-backups")
	e.Server.Paths.Socket = filepath.Join(effective.DataDir, "custom.sock")
	writeFile(t, filepath.Join(effective.StagingDir, "x"), "leftover")
	held, err := lock.Acquire(effective.LockFile())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Release() }()
	testutil.RequireCode(t, offline.Migrate(t.Context(), e, true), domain.CodeBusy)
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	if err := offline.Migrate(t.Context(), e, true); err != nil {
		t.Fatal(err)
	}
	want := defaults.WithServerPaths(e.Server.Paths.DataDir, e.Server.Paths.Socket, e.Server.Backup.Dir)
	if e.Paths != want {
		t.Fatalf("normalized paths = %+v, want %+v", e.Paths, want)
	}
	requireAbsent(t, filepath.Join(effective.StagingDir, "x"))
	requireAbsent(t, defaults.DataDir)
	info, err := offline.Backup(t.Context(), e)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(info.Path) != e.Server.Backup.Dir {
		t.Fatalf("snapshot used wrong backup directory: %s", info.Path)
	}
}

func TestVerifyPasswordNoTTY(t *testing.T) {
	e := newEnv(t)
	e.TTY = false
	e.In = strings.NewReader(testutil.Password)
	e.ReadPassword = func(string) (string, error) { t.Fatal("prompted without a TTY"); return "", nil }
	err := offline.VerifyPassword(e, t.Context(), nil)
	requireDetail(t, err, domain.CodePasswordRequired, "the password can only be entered on a terminal")
}

type observedClock struct {
	*clock.Fake
	waits chan time.Duration
}

func (c *observedClock) After(d time.Duration) <-chan time.Time {
	ch := c.Fake.After(d)
	c.waits <- d
	return ch
}

func TestVerifyPasswordThreeFailures(t *testing.T) {
	for _, success := range []bool{false, true} {
		t.Run(map[bool]string{false: "three failures", true: "second answer correct"}[success], func(t *testing.T) {
			e := newEnv(t)
			st := openDB(t, e.Paths.DB)
			synctest.Test(t, func(t *testing.T) {
				clk := &observedClock{Fake: clock.NewFake(e.Clock.Now()), waits: make(chan time.Duration, 3)}
				e.Clock = clk
				prompts := 0
				e.ReadPassword = func(string) (string, error) {
					prompts++
					if success && prompts == 2 {
						return testutil.Password, nil
					}
					return "wrong", nil // Deliberately invalid input, not a credential fixture.
				}
				done := make(chan error, 1)
				go func() { done <- offline.VerifyPassword(e, t.Context(), st.Reader) }()
				failures, wantPrompts := 3, 3
				if success {
					failures, wantPrompts = 1, 2
				}
				for i := 0; i < failures; i++ {
					synctest.Wait()
					select {
					case d := <-clk.waits:
						if d != time.Second {
							t.Fatalf("failure delay = %v", d)
						}
					default:
						t.Fatal("missing failure delay")
					}
					clk.Advance(time.Second - time.Nanosecond)
					synctest.Wait()
					if prompts != i+1 || len(done) != 0 {
						t.Fatal("verification continued before the full second elapsed")
					}
					clk.Advance(time.Nanosecond)
				}
				err := <-done
				if success && err != nil {
					t.Fatal(err)
				} else if !success {
					testutil.RequireCode(t, err, domain.CodePasswordInvalid)
				}
				if prompts != wantPrompts || len(clk.waits) != 0 {
					t.Fatalf("prompts = %d, extra waits = %d", prompts, len(clk.waits))
				}
			})
		})
	}
}

func TestVerifyPasswordCancellationAndReadError(t *testing.T) {
	e := newEnv(t)
	st := openDB(t, e.Paths.DB)
	e.ReadPassword = func(string) (string, error) { return "", io.EOF }
	if err := offline.VerifyPassword(e, t.Context(), st.Reader); !errors.Is(err, io.EOF) {
		t.Fatalf("read error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	e.ReadPassword = func(string) (string, error) { cancel(); return "wrong", nil }
	if err := offline.VerifyPassword(e, ctx, st.Reader); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestOpenStoreDefersLedgerAndServiceSharesLedger(t *testing.T) {
	e := newEnv(t)
	writeFile(t, e.Paths.LedgerFile, "torn tail")
	st, err := offline.OpenStore(e, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	data, err := os.ReadFile(e.Paths.LedgerFile)
	if err != nil || string(data) != "torn tail" {
		t.Fatalf("opening store touched ledger before password verification: %q, %v", data, err)
	}
	ledger, _, err := backup.OpenLedger(e.Paths.LedgerFile)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := offline.NewService(e, st, ledger)
	if err != nil {
		t.Fatal(err)
	}
	added, err := svc.AddTask(t.Context(), testutil.Meta(testutil.Owner(), svc.Generation()), service.AddTaskIn{Title: "erase me"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Append(backup.LedgerRecord{V: 1, Rec: domain.NewOpID().String(), At: e.Clock.Now().UnixMilli(),
		State: backup.LedgerPending, Tasks: []domain.TaskID{added.Task.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReapplyLedger(t.Context()); err != nil {
		t.Fatal(err)
	}
	task, err := store.GetTask(t.Context(), st.Reader, added.Task.ID)
	if err != nil || task.Title != "[purged]" {
		t.Fatalf("service did not see caller's ledger append: %+v, %v", task, err)
	}
}

func TestWithCrashHook(t *testing.T) {
	want := errors.New("simulated crash")
	ctx := offline.WithCrashHook(t.Context(), func(point string) error {
		if point != "test:point" {
			t.Fatalf("point = %q", point)
		}
		return want
	})
	if err := offline.CrashPoint(ctx, "test:point"); !errors.Is(err, want) {
		t.Fatalf("hook error = %v", err)
	}
	if err := offline.CrashPoint(t.Context(), "test:point"); err != nil {
		t.Fatalf("hook leaked to parent context: %v", err)
	}
}

// TestOfflineErrorsShowCause is final review B-I1: a non-domain failure on an
// offline path reaches the owner's terminal with its sanitized cause, never as
// a bare "internal".
func TestOfflineErrorsShowCause(t *testing.T) {
	e := newEnv(t)
	writeFile(t, e.Paths.SwapJournal(), "{not json\x1b[2J")
	err := offline.RestoreResume(t.Context(), e)
	testutil.RequireCode(t, err, domain.CodeInternal)
	detail := detailOf(err)
	if !strings.Contains(detail, "decode swap journal") || strings.ContainsRune(detail, '\x1b') {
		t.Fatalf("detail = %q, want the sanitized cause", detail)
	}
	if err.Error() == string(domain.CodeInternal) {
		t.Fatalf("bare %q", err.Error())
	}
}
