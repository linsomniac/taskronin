package offline_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"taskronin/internal/api"
	"taskronin/internal/backup"
	"taskronin/internal/client"
	"taskronin/internal/config"
	"taskronin/internal/domain"
	"taskronin/internal/offline"
	"taskronin/internal/server"
	"taskronin/internal/service"
	"taskronin/internal/store"
	"taskronin/internal/testutil"
)

var errRestoreCrash = errors.New("simulated restore crash")

func crashAtSwap(ctx context.Context, point string) context.Context {
	return backup.WithStepHook(ctx, func(p string) error {
		if p == point {
			return errRestoreCrash
		}
		return nil
	})
}

func restoreAdd(t *testing.T, c *client.Client, title string) int64 {
	t.Helper()
	op, err := c.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: title})
	if err != nil {
		t.Fatal(err)
	}
	return op.Task.ID
}

func restoreTitles(t *testing.T, c *client.Client) []string {
	t.Helper()
	page, err := c.ListTasks(t.Context(), client.TaskQuery{View: "all"})
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, task := range page.Items {
		titles = append(titles, task.Title)
	}
	slices.Sort(titles)
	return titles
}

func requireTitles(t *testing.T, c *client.Client, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := restoreTitles(t, c); !slices.Equal(got, want) {
		t.Fatalf("tasks = %q, want %q", got, want)
	}
}

func fileHash(t *testing.T, path string) [32]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(data)
}

// requireNoLeftovers asserts that a refused or finished restore left no journal
// and no staged copies behind.
func requireNoLeftovers(t *testing.T, srv *testutil.Server) {
	t.Helper()
	requireAbsent(t, srv.Paths.SwapJournal())
	entries, err := os.ReadDir(srv.Paths.StagingDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging not empty: %v", entries)
	}
}

func TestRestoreRoundTrip(t *testing.T) {
	srv := testutil.StartServer(t)
	restoreAdd(t, srv.Client, "one")
	restoreAdd(t, srv.Client, "two")
	key := api.NewIdempotencyKey()
	if _, err := srv.Client.AddTask(t.Context(), client.Mut{Key: key}, api.AddTaskRequest{Title: "keyed"}); err != nil {
		t.Fatal(err)
	}
	snap, err := srv.Client.RunBackup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	restoreAdd(t, srv.Client, "three")
	late := srv.TokenClient(t, "late", domain.MCPDefaultScopes, 60, "mcp")
	before, err := srv.Client.WhoAmI(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	srv.Stop()
	e := passwdEnv(t, srv, "")
	res, err := offline.Restore(t.Context(), e, snap.Name, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.OwnerTokenMinted || !strings.HasPrefix(res.Aside, srv.Paths.DB+".pre-restore-") {
		t.Fatalf("result = %+v", res)
	}
	requireNoLeftovers(t, srv)
	info, err := os.Stat(res.Aside)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("aside: %v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(res.Aside, "taskronin.db")); err != nil {
		t.Fatalf("aside lacks the old database: %v", err)
	}

	srv.Start(t)
	requireTitles(t, srv.Client, "one", "two", "keyed")
	after, err := srv.Client.WhoAmI(t.Context())
	if err != nil || after.Generation == before.Generation {
		t.Fatalf("generation %q -> %q, %v", before.Generation, after.Generation, err)
	}
	if _, err := late.WhoAmI(t.Context()); err != nil {
		t.Fatalf("token created after the snapshot was lost: %v", err)
	}
	if id := restoreAdd(t, srv.Client, "four"); id != 5 {
		t.Fatalf("next task id = %d, want 5 (sequence must survive the restore)", id)
	}
	replay, err := srv.Client.AddTask(t.Context(), client.Mut{Key: key}, api.AddTaskRequest{Title: "keyed"})
	if err != nil || replay.Replayed {
		t.Fatalf("earlier idempotency key replayed: %+v, %v", replay, err)
	}
	ops, err := srv.Client.ListOps(t.Context(), client.OpsQuery{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, op := range ops.Items {
		found = found || op.Kind == "system.restore"
	}
	if !found {
		t.Fatalf("no system.restore op in %+v", ops.Items)
	}
}

func TestRestoreSnapshotPathForms(t *testing.T) {
	srv := testutil.StartServer(t)
	restoreAdd(t, srv.Client, "base")
	snap, err := srv.Client.RunBackup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	infos, err := backup.List(srv.Paths.BackupDir)
	if err != nil || !slices.ContainsFunc(infos, func(i backup.SnapshotInfo) bool { return i.Name == snap.Name }) {
		t.Fatalf("List = %+v, %v", infos, err)
	}
	srv.Stop()
	live := fileHash(t, srv.Paths.DB)

	// A text file is refused before the live database is touched.
	text := filepath.Join(t.TempDir(), "notes.db")
	if err := os.WriteFile(text, []byte("this is not a database, just text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = offline.Restore(t.Context(), passwdEnv(t, srv, ""), text, true)
	testutil.RequireCode(t, err, domain.CodeValidation)
	if !strings.Contains(err.Error()+detailOf(err), "not a valid snapshot") {
		t.Fatalf("text file error = %v", err)
	}
	if fileHash(t, srv.Paths.DB) != live {
		t.Fatal("a refused restore changed the live database")
	}
	requireNoLeftovers(t, srv)

	// A missing bare name names where it looked.
	_, err = offline.Restore(t.Context(), passwdEnv(t, srv, ""), "nope.db", true)
	testutil.RequireCode(t, err, domain.CodeNotFound)
	if !strings.Contains(detailOf(err), "snapshot not found: nope.db (looked in . and "+srv.Paths.BackupDir+")") {
		t.Fatalf("missing snapshot error = %q", detailOf(err))
	}

	// A bare name from List(), run from an unrelated working directory.
	t.Chdir(t.TempDir())
	if _, err := offline.Restore(t.Context(), passwdEnv(t, srv, ""), snap.Name, true); err != nil {
		t.Fatalf("bare name: %v", err)
	}
	srv.Start(t)
	requireTitles(t, srv.Client, "base")
	restoreAdd(t, srv.Client, "extra")
	srv.Stop()

	// A relative path. Each restore's aside directory is named by the second.
	srv.Clock.Advance(time.Minute)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(cwd, snap.Path)
	if err != nil || filepath.IsAbs(rel) {
		t.Fatalf("relative path: %q, %v", rel, err)
	}
	if _, err := offline.Restore(t.Context(), passwdEnv(t, srv, ""), rel, true); err != nil {
		t.Fatalf("relative path: %v", err)
	}
	srv.Start(t)
	requireTitles(t, srv.Client, "base")
	srv.Stop()

	srv.Clock.Advance(time.Minute)
	// A pre-restore set whose newest change lives only in its -wal file.
	e := passwdEnv(t, srv, "")
	work := t.TempDir()
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal(err)
	}
	liveCopy := filepath.Join(work, "taskronin.db")
	copyFile(t, srv.Paths.DB, liveCopy)
	walSet := walOnlySet(t, e, liveCopy, "wal-only change")
	if _, err := offline.Restore(t.Context(), passwdEnv(t, srv, ""), walSet, true); err != nil {
		t.Fatalf("pre-restore directory: %v", err)
	}
	srv.Start(t)
	requireTitles(t, srv.Client, "base", "wal-only change")
}

func detailOf(err error) string {
	if de, ok := domain.AsError(err); ok {
		return de.Detail
	}
	return ""
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// walOnlySet opens the database at path (WAL mode), adds a task, and copies the
// db plus its -wal into a pre-restore-style directory while the connection is
// still open, so the new task exists only in the copied -wal.
func walOnlySet(t *testing.T, e *offline.Env, path, title string) string {
	t.Helper()
	st, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	ledger, _, err := backup.OpenLedger(filepath.Join(filepath.Dir(path), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := offline.NewService(e, st, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddTask(t.Context(), testutil.Meta(testutil.Owner(), svc.Generation()), service.AddTaskIn{Title: title}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(filepath.Dir(path), "taskronin.db.pre-restore-20260101T000000Z")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	copyFile(t, path, filepath.Join(dir, "taskronin.db"))
	copyFile(t, path+"-wal", filepath.Join(dir, "taskronin.db-wal"))
	if info, err := os.Stat(filepath.Join(dir, "taskronin.db-wal")); err != nil || info.Size() == 0 {
		t.Fatalf("fixture has no WAL content: %v, %v", info, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRestoreNewerSchemaRefused(t *testing.T) {
	srv := testutil.StartServer(t)
	restoreAdd(t, srv.Client, "base")
	snap, err := srv.Client.RunBackup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	srv.Stop()
	newer := filepath.Join(t.TempDir(), "newer.db")
	copyFile(t, snap.Path, newer)
	st, err := store.OpenStaging(t.Context(), newer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer.ExecContext(t.Context(), `INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (99, 'future', 'x', 0)`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	live := fileHash(t, srv.Paths.DB)
	_, err = offline.Restore(t.Context(), passwdEnv(t, srv, ""), newer, true)
	testutil.RequireCode(t, err, domain.CodeValidation)
	if d := detailOf(err); !strings.HasPrefix(d, "snapshot schema 99 is newer than this binary (") {
		t.Fatalf("detail = %q", d)
	}
	if fileHash(t, srv.Paths.DB) != live {
		t.Fatal("a refused restore changed the live database")
	}
	requireNoLeftovers(t, srv)
}

func TestRestoreConfirmation(t *testing.T) {
	srv := testutil.StartServer(t)
	restoreAdd(t, srv.Client, "base")
	snap, err := srv.Client.RunBackup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	srv.Stop()
	live := fileHash(t, srv.Paths.DB)

	e := passwdEnv(t, srv, "")
	_, err = offline.Restore(t.Context(), e, snap.Name, false) // non-TTY without --yes
	testutil.RequireCode(t, err, domain.CodeValidation)

	var prompt string
	e.TTY = true
	e.Confirm = func(p string, defaultYes bool) (bool, error) {
		prompt = p
		if defaultYes {
			t.Error("the restore prompt must default to no")
		}
		return false, nil
	}
	_, err = offline.Restore(t.Context(), e, snap.Name, false)
	testutil.RequireCode(t, err, domain.CodeValidation)
	if !strings.HasPrefix(prompt, "Restore "+snap.Name+"? The current database moves to "+srv.Paths.DB+".pre-restore-") {
		t.Fatalf("prompt = %q", prompt)
	}
	if fileHash(t, srv.Paths.DB) != live {
		t.Fatal("a declined restore changed the live database")
	}
	requireNoLeftovers(t, srv)
}

// requireRunRefuses starts server.Run directly: StartServer's Start would fail the test.
func requireRunRefuses(t *testing.T, srv *testutil.Server) {
	t.Helper()
	cfg, err := config.LoadServer(srv.Paths.ServerConfig)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	err = server.Run(ctx, cfg, server.Deps{Paths: srv.Paths, Clock: srv.Clock, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err == nil || !strings.Contains(err.Error(), "tkr restore --resume") {
		t.Fatalf("server.Run with a pending swap journal = %v", err)
	}
}

func TestRestoreCrashResumeRollback(t *testing.T) {
	t.Run("resume", func(t *testing.T) {
		srv := testutil.StartServer(t)
		restoreAdd(t, srv.Client, "one")
		snap, err := srv.Client.RunBackup(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		restoreAdd(t, srv.Client, "two")
		srv.Stop()
		_, err = offline.Restore(crashAtSwap(t.Context(), "moved-old"), passwdEnv(t, srv, ""), snap.Name, true)
		if !errors.Is(err, errRestoreCrash) {
			t.Fatalf("crash = %v", err)
		}
		j, err := backup.ReadJournal(srv.Paths.SwapJournal())
		if err != nil || j.Step != "moved-old" || j.Kind != backup.SwapRestore {
			t.Fatalf("journal = %+v, %v", j, err)
		}
		requireAbsent(t, srv.Paths.DB) // the old database is in the aside dir
		requireRunRefuses(t, srv)
		// Offline commands refuse too, naming the recovery commands.
		_, err = offline.Restore(t.Context(), passwdEnv(t, srv, ""), snap.Name, true)
		testutil.RequireCode(t, err, domain.CodeConflict)

		if err := offline.RestoreResume(t.Context(), passwdEnv(t, srv, "")); err != nil {
			t.Fatal(err)
		}
		requireNoLeftovers(t, srv)
		srv.Start(t)
		requireTitles(t, srv.Client, "one")
	})
	t.Run("rollback", func(t *testing.T) {
		srv := testutil.StartServer(t)
		restoreAdd(t, srv.Client, "one")
		snap, err := srv.Client.RunBackup(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		restoreAdd(t, srv.Client, "two")
		srv.Stop()
		_, err = offline.Restore(crashAtSwap(t.Context(), "installed"), passwdEnv(t, srv, ""), snap.Name, true)
		if !errors.Is(err, errRestoreCrash) {
			t.Fatalf("crash = %v", err)
		}
		requireRunRefuses(t, srv)
		if err := offline.RestoreRollback(t.Context(), passwdEnv(t, srv, "")); err != nil {
			t.Fatal(err)
		}
		requireNoLeftovers(t, srv)
		asides, _ := filepath.Glob(srv.Paths.DB + ".pre-restore-*")
		if len(asides) != 0 {
			t.Fatalf("aside remains after rollback: %v", asides)
		}
		srv.Start(t)
		requireTitles(t, srv.Client, "one", "two")
	})
	t.Run("nothing to recover", func(t *testing.T) {
		srv := testutil.StartServer(t)
		srv.Stop()
		testutil.RequireCode(t, offline.RestoreResume(t.Context(), passwdEnv(t, srv, "")), domain.CodeNotFound)
		testutil.RequireCode(t, offline.RestoreRollback(t.Context(), passwdEnv(t, srv, "")), domain.CodeNotFound)
	})
	t.Run("resume after rollback started is refused", func(t *testing.T) {
		srv := testutil.StartServer(t)
		restoreAdd(t, srv.Client, "one")
		snap, err := srv.Client.RunBackup(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		srv.Stop()
		if _, err := offline.Restore(crashAtSwap(t.Context(), "installed"), passwdEnv(t, srv, ""), snap.Name, true); !errors.Is(err, errRestoreCrash) {
			t.Fatalf("crash = %v", err)
		}
		err = offline.RestoreRollback(crashAtSwap(t.Context(), "rollback:discarding"), passwdEnv(t, srv, ""))
		if !errors.Is(err, errRestoreCrash) {
			t.Fatalf("rollback crash = %v", err)
		}
		testutil.RequireCode(t, offline.RestoreResume(t.Context(), passwdEnv(t, srv, "")), domain.CodeConflict)
		if err := offline.RestoreRollback(t.Context(), passwdEnv(t, srv, "")); err != nil {
			t.Fatalf("second rollback: %v", err)
		}
		srv.Start(t)
		requireTitles(t, srv.Client, "one")
	})
}

// corruptLiveDB destroys the SQLite header of the live database.
func corruptLiveDB(t *testing.T, srv *testutil.Server) {
	t.Helper()
	f, err := os.OpenFile(srv.Paths.DB, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(bytes.Repeat([]byte{'x'}, 100), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreUnreadableCurrent(t *testing.T) {
	srv := testutil.StartServer(t)
	for _, title := range []string{"a", "b", "c"} {
		restoreAdd(t, srv.Client, title)
	}
	snap, err := srv.Client.RunBackup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	restoreAdd(t, srv.Client, "d") // id 4 is lost with the live database
	oldOwner, err := os.ReadFile(srv.Paths.OwnerToken)
	if err != nil {
		t.Fatal(err)
	}
	srv.Stop()
	corruptLiveDB(t, srv)

	e := passwdEnv(t, srv, "")
	res, err := offline.Restore(t.Context(), e, snap.Name, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OwnerTokenMinted {
		t.Fatalf("result = %+v", res)
	}
	notice := e.Err.(*bytes.Buffer).String()
	if !strings.Contains(notice, "WARNING") || !strings.Contains(notice, "tkr passwd --reset") {
		t.Fatalf("no loud notice: %q", notice)
	}
	newOwner, err := os.ReadFile(srv.Paths.OwnerToken)
	if err != nil || bytes.Equal(newOwner, oldOwner) {
		t.Fatalf("owner.token not rewritten: %v", err)
	}
	if info, err := os.Stat(srv.Paths.OwnerToken); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("owner.token mode: %v, %v", info, err)
	}
	requireNoLeftovers(t, srv)

	st, err := store.Open(t.Context(), srv.Paths.DB)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.GetAuthState(t.Context(), st.Reader)
	if err != nil || !state.WebLoginDisabled {
		t.Fatalf("auth state = %+v, %v", state, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	srv.Start(t)
	_, err = clientWithToken(t, srv, "oldowner", string(oldOwner)).Settings(t.Context())
	requireUnauthenticated(t, err)
	if _, err := srv.Client.Settings(t.Context()); err != nil {
		t.Fatalf("new owner token rejected: %v", err)
	}
	requireTitles(t, srv.Client, "a", "b", "c")
	// BF24: only the snapshot's marks (3) are recoverable; the live mark (4) is lost.
	if id := restoreAdd(t, srv.Client, "next"); id < 3+10_001 {
		t.Fatalf("next task id = %d, want >= %d", id, 3+10_001)
	}
}

// TestRestoreLiveReadErrorClassification is final review B-I2: only a missing
// or genuinely unusable live DB may take the destructive unreadable path
// (revoke every token, new owner token, web login off, ids +10 000). Any other
// failure to read it must stop the restore before anything changes.
func TestRestoreLiveReadErrorClassification(t *testing.T) {
	setup := func(t *testing.T) (*testutil.Server, string) {
		srv := testutil.StartServer(t)
		restoreAdd(t, srv.Client, "a")
		snap, err := srv.Client.RunBackup(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		restoreAdd(t, srv.Client, "b")
		srv.Stop()
		return srv, snap.Name
	}
	refusals := map[string]func(t *testing.T, srv *testutil.Server) func(){
		"permission denied": func(t *testing.T, srv *testutil.Server) func() {
			if os.Geteuid() == 0 {
				t.Skip("root ignores file permissions")
			}
			if err := os.Chmod(srv.Paths.DB, 0); err != nil {
				t.Fatal(err)
			}
			return func() {
				if err := os.Chmod(srv.Paths.DB, 0o600); err != nil {
					t.Fatal(err)
				}
			}
		},
		"wal is not a file": func(t *testing.T, srv *testutil.Server) func() {
			wal := srv.Paths.DB + "-wal"
			if err := os.RemoveAll(wal); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(wal, 0o700); err != nil {
				t.Fatal(err)
			}
			return func() {
				if err := os.Remove(wal); err != nil {
					t.Fatal(err)
				}
			}
		},
	}
	for name, breakLive := range refusals {
		t.Run("refused/"+name, func(t *testing.T) {
			srv, snap := setup(t)
			owner := fileBytes(t, srv.Paths.OwnerToken)
			dbHash := fileHash(t, srv.Paths.DB)
			undo := breakLive(t, srv)
			e := passwdEnv(t, srv, "")
			_, err := offline.Restore(t.Context(), e, snap, true)
			if err == nil {
				t.Fatal("restore over an unreadable-but-intact live DB succeeded")
			}
			if detail := detailOf(err); !strings.Contains(detail, "nothing was changed") || !strings.Contains(detail, "current database") {
				t.Fatalf("refusal detail = %q", detail)
			} else {
				t.Logf("refusal: %s", detail)
			}
			if strings.Contains(e.Err.(*bytes.Buffer).String(), "WARNING") {
				t.Fatalf("took the unreadable path: %q", e.Err.(*bytes.Buffer).String())
			}
			requireNoLeftovers(t, srv)
			if asides, _ := filepath.Glob(srv.Paths.DB + ".pre-restore-*"); len(asides) != 0 {
				t.Fatalf("aside created: %v", asides)
			}
			undo()
			if fileHash(t, srv.Paths.DB) != dbHash || !bytes.Equal(owner, fileBytes(t, srv.Paths.OwnerToken)) {
				t.Fatal("the live DB or owner.token changed")
			}
			srv.Start(t) // the original owner token still works and nothing was lost
			requireTitles(t, srv.Client, "a", "b")
		})
	}
	unusable := map[string]func(t *testing.T, srv *testutil.Server){
		"missing": func(t *testing.T, srv *testutil.Server) {
			if err := os.Remove(srv.Paths.DB); err != nil {
				t.Fatal(err)
			}
		},
		"not a database": corruptLiveDB,
		"no schema": func(t *testing.T, srv *testutil.Server) {
			if err := os.WriteFile(srv.Paths.DB, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, breakLive := range unusable {
		t.Run("unreadable path/"+name, func(t *testing.T) {
			srv, snap := setup(t)
			breakLive(t, srv)
			e := passwdEnv(t, srv, "")
			res, err := offline.Restore(t.Context(), e, snap, true)
			if err != nil {
				t.Fatal(err)
			}
			if !res.OwnerTokenMinted || !strings.Contains(e.Err.(*bytes.Buffer).String(), "WARNING") {
				t.Fatalf("result = %+v, stderr = %q", res, e.Err.(*bytes.Buffer).String())
			}
			srv.Start(t)
			requireTitles(t, srv.Client, "a")
		})
	}
}

func TestRestoreOwnerTokenPendingResume(t *testing.T) {
	setup := func(t *testing.T) (*testutil.Server, string) {
		srv := testutil.StartServer(t)
		restoreAdd(t, srv.Client, "a")
		snap, err := srv.Client.RunBackup(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		srv.Stop()
		corruptLiveDB(t, srv)
		return srv, snap.Name
	}
	t.Run("crash after installed, before the token is written", func(t *testing.T) {
		srv, name := setup(t)
		oldOwner, err := os.ReadFile(srv.Paths.OwnerToken)
		if err != nil {
			t.Fatal(err)
		}
		_, err = offline.Restore(crashAtSwap(t.Context(), "installed"), passwdEnv(t, srv, ""), name, true)
		if !errors.Is(err, errRestoreCrash) {
			t.Fatalf("crash = %v", err)
		}
		if got, err := os.ReadFile(srv.Paths.OwnerToken); err != nil || !bytes.Equal(got, oldOwner) {
			t.Fatalf("owner.token changed before onInstalled: %v", err)
		}
		e := passwdEnv(t, srv, "")
		if err := offline.RestoreResume(t.Context(), e); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(e.Err.(*bytes.Buffer).String(), "the new owner token was not saved — run `tkr passwd --reset`") {
			t.Fatalf("resume did not report the lost token: %q", e.Err.(*bytes.Buffer).String())
		}
		requireNoLeftovers(t, srv)
		// `tkr passwd --reset` recovers.
		if err := offline.PasswdReset(t.Context(), passwdEnv(t, srv, domain.NewOpID().String()+"\n"), true); err != nil {
			t.Fatal(err)
		}
		srv.Start(t)
		if _, err := srv.Client.Settings(t.Context()); err != nil {
			t.Fatalf("owner token after passwd --reset: %v", err)
		}
	})
	t.Run("crash after the token is written", func(t *testing.T) {
		srv, name := setup(t)
		_, err := offline.Restore(crashAtSwap(t.Context(), "on-installed"), passwdEnv(t, srv, ""), name, true)
		if !errors.Is(err, errRestoreCrash) {
			t.Fatalf("crash = %v", err)
		}
		e := passwdEnv(t, srv, "")
		if err := offline.RestoreResume(t.Context(), e); err != nil {
			t.Fatal(err)
		}
		if got := e.Err.(*bytes.Buffer).String(); got != "" {
			t.Fatalf("resume warned although the token was saved: %q", got)
		}
		srv.Start(t)
		if _, err := srv.Client.Settings(t.Context()); err != nil {
			t.Fatalf("minted owner token rejected: %v", err)
		}
	})
}

func TestRestoreClearsFutureIdempotency(t *testing.T) {
	srv := testutil.StartServer(t)
	start := srv.Clock.Now()
	srv.Clock.Advance(365 * 24 * time.Hour)
	key := api.NewIdempotencyKey()
	if _, err := srv.Client.AddTask(t.Context(), client.Mut{Key: key}, api.AddTaskRequest{Title: "skewed"}); err != nil {
		t.Fatal(err)
	}
	snap, err := srv.Client.RunBackup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	srv.Clock.Set(start)
	srv.Stop()
	if _, err := offline.Restore(t.Context(), passwdEnv(t, srv, ""), snap.Name, true); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.Context(), srv.Paths.DB)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.Reader.QueryRowContext(t.Context(), `SELECT count(*) FROM idempotency`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("idempotency rows = %d, %v", n, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	srv.Start(t)
	replay, err := srv.Client.AddTask(t.Context(), client.Mut{Key: key}, api.AddTaskRequest{Title: "skewed"})
	if err != nil || replay.Replayed {
		t.Fatalf("future-dated key replayed: %+v, %v", replay, err)
	}
}

func TestPendingRefusedAfterRestore(t *testing.T) {
	srv := testutil.StartServer(t)
	gen, err := srv.Client.Generation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	actor, err := srv.Client.Actor(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	pending := &client.PendingStore{Dir: srv.Paths.PendingDir, Clock: srv.Clock}
	req := client.PendingRequest{Key: api.NewIdempotencyKey(), Method: "POST", Path: "/tasks",
		Generation: gen, Actor: actor, Body: []byte(`{"title":"queued"}`)}
	if err := pending.Save(req); err != nil {
		t.Fatal(err)
	}
	snap, err := srv.Client.RunBackup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	srv.Stop()
	if _, err := offline.Restore(t.Context(), passwdEnv(t, srv, ""), snap.Name, true); err != nil {
		t.Fatal(err)
	}
	srv.Start(t)
	list, err := pending.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("pending = %+v, %v", list, err)
	}
	if _, err := srv.Client.Resend(t.Context(), list[0]); !errors.Is(err, client.ErrGenerationMismatch) {
		t.Fatalf("Resend after restore = %v, want ErrGenerationMismatch", err)
	}
	requireTitles(t, srv.Client)
}

// TestRestoreEveryCrashPoint crashes the swap at each journaled boundary and
// proves both recoveries work from there (A§12.2 step 6, R4-10).
func TestRestoreEveryCrashPoint(t *testing.T) {
	points := []string{"prepared", "moved-old:done", "moved-old", "installed:done", "installed", "on-installed"}
	for _, point := range points {
		for _, mode := range []string{"resume", "rollback"} {
			t.Run(point+"/"+mode, func(t *testing.T) {
				srv := testutil.StartServer(t)
				restoreAdd(t, srv.Client, "one")
				snap, err := srv.Client.RunBackup(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				restoreAdd(t, srv.Client, "two")
				srv.Stop()
				_, err = offline.Restore(crashAtSwap(t.Context(), point), passwdEnv(t, srv, ""), snap.Name, true)
				if !errors.Is(err, errRestoreCrash) {
					t.Fatalf("crash = %v", err)
				}
				requireRunRefuses(t, srv)
				want := []string{"one"}
				if mode == "resume" {
					err = offline.RestoreResume(t.Context(), passwdEnv(t, srv, ""))
				} else {
					err = offline.RestoreRollback(t.Context(), passwdEnv(t, srv, ""))
					want = []string{"one", "two"}
				}
				if err != nil {
					t.Fatal(err)
				}
				requireNoLeftovers(t, srv)
				srv.Start(t)
				requireTitles(t, srv.Client, want...)
			})
		}
	}
}

// writeDamagedLedger installs a purge ledger whose middle line fails its
// checksum (A§12.6: damaged; the valid records still replay).
func writeDamagedLedger(t *testing.T, path string) {
	t.Helper()
	scratch := filepath.Join(t.TempDir(), "scratch")
	sl, _, err := backup.OpenLedger(scratch)
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range []string{randomRec(t), randomRec(t)} {
		if err := sl.Append(backup.LedgerRecord{V: 1, Rec: rec, At: 1, State: backup.LedgerPending, Tasks: []domain.TaskID{99}}); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(scratch)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(raw), "\n")
	damaged := lines[0] + "{\"v\":1,\"rec\":\"zz\",\"at\":1,\"state\":\"pending\",\"tasks\":[2],\"sum\":\"bad\"}\n" + lines[1]
	if err := os.WriteFile(path, []byte(damaged), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRestoreSwapFailureHint is final review B-M3: once the swap journal is
// written, an interrupted or failed swap says how to finish or undo it.
func TestRestoreSwapFailureHint(t *testing.T) {
	srv := testutil.StartServer(t)
	restoreAdd(t, srv.Client, "one")
	snap, err := srv.Client.RunBackup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	srv.Stop()
	e := passwdEnv(t, srv, "")
	if _, err := offline.Restore(crashAtSwap(t.Context(), "moved-old"), e, snap.Name, true); !errors.Is(err, errRestoreCrash) {
		t.Fatalf("crash = %v", err)
	}
	for _, want := range []string{"tkr restore --resume", "tkr restore --rollback"} {
		if !strings.Contains(e.Err.(*bytes.Buffer).String(), want) {
			t.Fatalf("stderr %q does not mention %q", e.Err.(*bytes.Buffer).String(), want)
		}
	}
	if err := offline.RestoreRollback(t.Context(), passwdEnv(t, srv, "")); err != nil {
		t.Fatal(err)
	}
	// A refusal before any journal exists must not send the user to --resume.
	junk := filepath.Join(t.TempDir(), "junk.db")
	if err := os.WriteFile(junk, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	e = passwdEnv(t, srv, "")
	_, err = offline.Restore(t.Context(), e, junk, true)
	testutil.RequireCode(t, err, domain.CodeValidation)
	if strings.Contains(e.Err.(*bytes.Buffer).String(), "--resume") {
		t.Fatalf("hint without a journal: %q", e.Err.(*bytes.Buffer).String())
	}
}

// TestRestoreResumeOwnerTokenCheckError is final review B-M12: after a
// completed resume, an I/O error while checking owner.token still finishes
// with the `tkr passwd --reset` hint instead of a bare failure.
func TestRestoreResumeOwnerTokenCheckError(t *testing.T) {
	srv := testutil.StartServer(t)
	restoreAdd(t, srv.Client, "a")
	snap, err := srv.Client.RunBackup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	srv.Stop()
	corruptLiveDB(t, srv)
	if _, err := offline.Restore(crashAtSwap(t.Context(), "installed"), passwdEnv(t, srv, ""), snap.Name, true); !errors.Is(err, errRestoreCrash) {
		t.Fatalf("crash = %v", err)
	}
	// owner.token cannot be read at all (here: a directory, EISDIR).
	if err := os.Remove(srv.Paths.OwnerToken); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(srv.Paths.OwnerToken, 0o700); err != nil {
		t.Fatal(err)
	}
	e := passwdEnv(t, srv, "")
	if err := offline.RestoreResume(t.Context(), e); err != nil {
		t.Fatalf("resume after a completed swap = %v", err)
	}
	if stderr := e.Err.(*bytes.Buffer).String(); !strings.Contains(stderr, "tkr passwd --reset") || !strings.Contains(stderr, "is a directory") {
		t.Fatalf("stderr = %q", stderr)
	}
	if !strings.Contains(e.Out.(*bytes.Buffer).String(), "resumed and completed") {
		t.Fatalf("stdout = %q", e.Out.(*bytes.Buffer).String())
	}
	requireNoLeftovers(t, srv)
}

// TestRestoreWarnsDamagedLedger is final review B-M4: a record lost to a
// damaged ledger line is not replayed into the restored data, so say so.
func TestRestoreWarnsDamagedLedger(t *testing.T) {
	srv := testutil.StartServer(t)
	restoreAdd(t, srv.Client, "a")
	snap, err := srv.Client.RunBackup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	srv.Stop()
	writeDamagedLedger(t, srv.Paths.LedgerFile)
	e := passwdEnv(t, srv, "")
	if _, err := offline.Restore(t.Context(), e, snap.Name, true); err != nil {
		t.Fatal(err)
	}
	if stderr := e.Err.(*bytes.Buffer).String(); !strings.Contains(stderr, "purge ledger is damaged") || !strings.Contains(stderr, "tkr purge --repair-ledger") {
		t.Fatalf("stderr = %q", stderr)
	}
}
