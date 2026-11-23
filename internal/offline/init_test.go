package offline_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/linsomniac/taskronin/internal/auth"
	"github.com/linsomniac/taskronin/internal/config"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/fsutil"
	"github.com/linsomniac/taskronin/internal/lock"
	"github.com/linsomniac/taskronin/internal/offline"
	"github.com/linsomniac/taskronin/internal/store"
	"github.com/linsomniac/taskronin/internal/testutil"
)

func initEnv(t *testing.T) (*offline.Env, offline.InitOptions) {
	t.Helper()
	oldMask := fsutil.SetUmask(0o077)
	t.Cleanup(func() { fsutil.SetUmask(oldMask) })
	// Config validation rejects world-writable ancestors, including /tmp.
	root, err := os.MkdirTemp(".", ".init-*")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	for key, dir := range map[string]string{"XDG_CONFIG_HOME": "c", "XDG_DATA_HOME": "d", "XDG_STATE_HOME": "s", "XDG_RUNTIME_DIR": "r"} {
		t.Setenv(key, filepath.Join(root, dir))
	}
	p, err := config.DefaultPaths(os.Getenv, root)
	if err != nil {
		t.Fatal(err)
	}
	e := &offline.Env{Paths: p, Clock: testutil.Clock(t, testutil.DefaultNow),
		In: strings.NewReader(testutil.Password + "\n"), Out: new(bytes.Buffer), Err: new(bytes.Buffer)}
	passwd := filepath.Join(root, "passwd")
	writeFile(t, passwd, fmt.Sprintf("me:x:%d:1000::/home/me:/bin/zsh\n", os.Getuid()))
	return e, offline.InitOptions{Timezone: "America/Denver", PasswordStdin: true, NoSystemd: true, Yes: true, PasswdPath: passwd}
}

func fileBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func requireMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode {
		t.Fatalf("%s mode = %o, want %o", path, info.Mode().Perm(), mode)
	}
}

func requireToken(t *testing.T, st *store.Store, path string) domain.Token {
	t.Helper()
	b := fileBytes(t, path)
	if !regexp.MustCompile(`^tkr_[1-9][0-9]*_[A-Za-z0-9_-]{43}\n$`).Match(b) {
		t.Fatal("noncanonical token file")
	}
	id, secret, err := auth.ParseToken(strings.TrimSuffix(string(b), "\n"))
	if err != nil {
		t.Fatal(err)
	}
	token, err := store.GetToken(t.Context(), st.Reader, id)
	if err != nil {
		t.Fatal(err)
	}
	if token.SecretHash != auth.HashSecret(secret) {
		t.Fatal("token file does not authenticate against its row")
	}
	requireMode(t, path, 0o600)
	return token
}

func TestInitCreatesEverything(t *testing.T) {
	e, o := initEnv(t)
	result, err := offline.Init(t.Context(), e, o)
	if err != nil {
		t.Fatal(err)
	}
	if result.DB != e.Paths.DB || result.OwnerToken != e.Paths.OwnerToken || result.MCPToken != "" || !result.AllowPlainLoopback {
		t.Fatalf("result = %+v", result)
	}
	for _, path := range []string{e.Paths.DB, e.Paths.ServerConfig, e.Paths.ClientConfig} {
		requireMode(t, path, 0o600)
	}
	for _, dir := range []string{e.Paths.DataDir, e.Paths.StateDir, e.Paths.ConfigDir, e.Paths.StagingDir, e.Paths.BackupDir, e.Paths.TokensDir, e.Paths.DraftsDir, e.Paths.PendingDir, e.Paths.RuntimeDir} {
		requireMode(t, dir, 0o700)
	}
	st := openDB(t, e.Paths.DB)
	token := requireToken(t, st, e.Paths.OwnerToken)
	if !token.IsOwner || token.Scopes != domain.AllScopes || token.ExpiresAt.Valid || token.MutationsPerHour != 0 || !token.CreatedAt.Equal(e.Clock.Now()) || !strings.HasPrefix(token.Name, "owner."+e.Clock.Now().UTC().Format("20060102T150405.000Z")+"-") {
		t.Fatalf("owner metadata = %+v", token)
	}
	a, err := store.GetAuthState(t.Context(), st.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := auth.VerifyPassword(a.PasswordHash, testutil.Password)
	if err != nil || !ok || a.IdemHMACKey == [32]byte{} || a.WebLoginDisabled || !a.PasswordChangedAt.Equal(e.Clock.Now()) {
		t.Fatal("invalid initial auth state", err)
	}
	gen, err := store.GetSetting(t.Context(), st.Reader, "db_generation")
	if err != nil || gen == "" {
		t.Fatalf("generation missing: %v", err)
	}
	tz, err := store.GetSetting(t.Context(), st.Reader, "tz_last_seen")
	if err != nil || tz != "America/Denver" {
		t.Fatalf("timezone = %q, %v", tz, err)
	}
	var ops, events int
	if err := st.Reader.QueryRowContext(t.Context(), "SELECT (SELECT count(*) FROM ops), (SELECT count(*) FROM events)").Scan(&ops, &events); err != nil || ops != 0 || events != 0 {
		t.Fatalf("bootstrap audit counts = %d/%d, %v", ops, events, err)
	}
	s, err := config.LoadServer(e.Paths.ServerConfig)
	if err != nil || s.Timezone != "America/Denver" || s.Paths.DataDir != e.Paths.DataDir || s.Paths.Socket != e.Paths.Socket || s.Backup.Dir != e.Paths.BackupDir {
		t.Fatalf("server config = %+v, %v", s, err)
	}
	c, err := config.LoadClient(e.Paths.ClientConfig, e.Paths)
	if err != nil || c.TokenFile != e.Paths.OwnerToken || c.Socket != e.Paths.Socket {
		t.Fatalf("client config = %+v, %v", c, err)
	}
	entries, err := os.ReadDir(e.Paths.StagingDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("staging not empty: %v, %v", entries, err)
	}
	out := e.Out.(*bytes.Buffer).String()
	if !strings.Contains(out, `tkr add "`) || !strings.Contains(out, "tkr status --short") || strings.Contains(out, strings.TrimSpace(string(fileBytes(t, e.Paths.OwnerToken)))) {
		t.Fatal("next steps missing or token leaked")
	}
	requireAbsent(t, e.Paths.UnitsDir())
}

func TestInitRefusesExisting(t *testing.T) {
	e, o := initEnv(t)
	if _, err := offline.Init(t.Context(), e, o); err != nil {
		t.Fatal(err)
	}
	before := fileBytes(t, e.Paths.OwnerToken)
	_, err := offline.Init(t.Context(), e, o)
	testutil.RequireCode(t, err, domain.CodeConflict)
	if !strings.Contains(err.Error(), "already initialized") || !bytes.Equal(before, fileBytes(t, e.Paths.OwnerToken)) {
		t.Fatal("existing initialization changed", err)
	}
}

// reinitEnv models `tkr init --password-stdin --yes` run again over an existing
// instance whose server.toml is kept.
func reinitEnv(srv *testutil.Server) (*offline.Env, offline.InitOptions) {
	return &offline.Env{Paths: srv.Paths, Clock: srv.Clock, In: strings.NewReader(testutil.Password + "\n"),
			Out: new(bytes.Buffer), Err: new(bytes.Buffer)},
		offline.InitOptions{Timezone: "America/Denver", PasswordStdin: true, NoSystemd: true, Yes: true}
}

// TestInitRefusesLeftovers is final-review probe B-C1: a purge ledger left by
// an earlier database replays with delete semantics against whatever database
// lives in the data dir next, so a fresh init must not silently arm it.
func TestInitRefusesLeftovers(t *testing.T) {
	t.Run("purge ledger after the database was deleted", func(t *testing.T) {
		f := purgeFixture(t)
		if _, err := offline.Purge(t.Context(), f.e, purgeTask1()); err != nil {
			t.Fatal(err)
		}
		srv := f.srv
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if err := os.Remove(srv.Paths.DB + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
		}
		ownerBefore := fileBytes(t, srv.Paths.OwnerToken)
		e, o := reinitEnv(srv)
		_, err := offline.Init(t.Context(), e, o)
		testutil.RequireCode(t, err, domain.CodeConflict)
		for _, want := range []string{srv.Paths.LedgerFile, "tkr restore", "move"} {
			if !strings.Contains(detailOf(err), want) {
				t.Errorf("refusal %q does not mention %q", detailOf(err), want)
			}
		}
		requireAbsent(t, srv.Paths.DB)
		if !bytes.Equal(ownerBefore, fileBytes(t, srv.Paths.OwnerToken)) {
			t.Fatal("a refused init rewrote owner.token")
		}

		// Deliberately moving the ledger aside is how the owner starts fresh.
		if err := os.Rename(srv.Paths.LedgerFile, srv.Paths.LedgerFile+".old"); err != nil {
			t.Fatal(err)
		}
		e, o = reinitEnv(srv)
		if _, err := offline.Init(t.Context(), e, o); err != nil {
			t.Fatal(err)
		}
		srv.Start(t)
		if id := restoreAdd(t, srv.Client, "fresh task"); id != 1 {
			t.Fatalf("first task of the new database = #%d, want #1 (the purged id)", id)
		}
		srv.Stop()
		srv.Start(t) // startup replays whatever ledger the data dir holds
		task, err := srv.Client.GetTask(t.Context(), 1)
		if err != nil {
			t.Fatal(err)
		}
		if task.Title != "fresh task" || task.DeletedAt != nil {
			t.Fatalf("new task #1 after a restart = %q (deleted %v)", task.Title, task.DeletedAt != nil)
		}
	})
	for _, name := range []string{"taskronin.db-wal", "taskronin.db-shm"} {
		t.Run("stray "+name, func(t *testing.T) {
			e, o := initEnv(t)
			if err := os.MkdirAll(e.Paths.DataDir, 0o700); err != nil {
				t.Fatal(err)
			}
			stray := filepath.Join(e.Paths.DataDir, name)
			writeFile(t, stray, "frames of an earlier database")
			_, err := offline.Init(t.Context(), e, o)
			testutil.RequireCode(t, err, domain.CodeConflict)
			if !strings.Contains(detailOf(err), stray) || !strings.Contains(detailOf(err), "tkr restore") {
				t.Fatalf("refusal = %q", detailOf(err))
			}
			requireAbsent(t, e.Paths.DB)
			requireAbsent(t, e.Paths.OwnerToken)
			requireAbsent(t, e.Paths.ServerConfig)
		})
	}
	// begin already refuses both journals (naming --resume/--rollback); keep
	// that guarded here so a fresh init can never run over a pending swap.
	t.Run("journals", func(t *testing.T) {
		for _, journal := range []func(config.Paths) string{config.Paths.SwapJournal, func(p config.Paths) string { return config.ScrubJournal(p.BackupDir) }} {
			e, o := initEnv(t)
			path := journal(e.Paths)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			writeFile(t, path, "{}")
			_, err := offline.Init(t.Context(), e, o)
			testutil.RequireCode(t, err, domain.CodeConflict)
			requireAbsent(t, e.Paths.DB)
			requireAbsent(t, e.Paths.OwnerToken)
		}
	})
}

func TestInitInterruptedBeforePublish(t *testing.T) {
	for _, point := range []string{"init:staged", "init:token-written"} {
		t.Run(point, func(t *testing.T) {
			e, o := initEnv(t)
			crash := errors.New("simulated interruption")
			var before []byte
			ctx := offline.WithCrashHook(t.Context(), func(got string) error {
				requireAbsent(t, e.Paths.DB)
				if got == "init:staged" {
					requireAbsent(t, e.Paths.OwnerToken)
					files, err := filepath.Glob(filepath.Join(e.Paths.StagingDir, "*", "*.db"))
					if err != nil || len(files) != 1 {
						t.Fatalf("staged DB: %v, %v", files, err)
					}
					st, err := store.OpenStaging(t.Context(), files[0])
					if err != nil {
						t.Fatal(err)
					}
					if _, err := store.GetAuthState(t.Context(), st.Reader); err != nil {
						t.Fatal(err)
					}
					if err := st.Close(); err != nil {
						t.Fatal(err)
					}
				}
				if got == "init:token-written" {
					before = fileBytes(t, e.Paths.OwnerToken)
				}
				if got == point {
					return crash
				}
				return nil
			})
			if _, err := offline.Init(ctx, e, o); !errors.Is(err, crash) {
				t.Fatalf("crash = %v", err)
			}
			requireAbsent(t, e.Paths.DB)
			// A fresh Env models restarting the process and loading saved config.
			e.Server = nil
			e.In = strings.NewReader(testutil.Password + "\n")
			if _, err := offline.Init(t.Context(), e, o); err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(before, fileBytes(t, e.Paths.OwnerToken)) {
				t.Fatal("retry reused owner secret")
			}
			requireToken(t, openDB(t, e.Paths.DB), e.Paths.OwnerToken)
		})
	}
}

func TestInitWeakPassword(t *testing.T) {
	e, o := initEnv(t)
	e.In = strings.NewReader("short\n")
	_, err := offline.Init(t.Context(), e, o)
	testutil.RequireCode(t, err, domain.CodeValidation)
	entries, err := os.ReadDir(e.Paths.DataDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "taskronin.lock" {
		t.Fatalf("weak password left data: %v, %v", entries, err)
	}
	requireAbsent(t, e.Paths.OwnerToken)
	requireAbsent(t, e.Paths.ServerConfig)
}

func TestInitTimezoneFromLocaltime(t *testing.T) {
	for _, variant := range []string{"direct", "symlink-alias", "hardlink-alias"} {
		t.Run(variant, func(t *testing.T) {
			e, o := initEnv(t)
			root := filepath.Dir(o.PasswdPath)
			zoneRoot := filepath.Join(root, "zoneinfo")
			zone := filepath.Join(zoneRoot, "America", "Denver")
			writeFile(t, zone, "fixture: the IANA name is loaded through civil.LoadZone")
			if variant != "direct" {
				alias := filepath.Join(zoneRoot, "Navajo")
				var err error
				if variant == "symlink-alias" {
					err = os.Symlink("America/Denver", alias)
				} else {
					err = os.Link(zone, alias)
					writeFile(t, filepath.Join(zoneRoot, "tzdata.zi"), "L America/Denver Navajo\n")
				}
				if err != nil {
					t.Fatal(err)
				}
				zone = alias
			}
			o.Timezone, o.LocaltimePath = "", filepath.Join(root, "localtime")
			if err := os.Symlink(zone, o.LocaltimePath); err != nil {
				t.Fatal(err)
			}
			if _, err := offline.Init(t.Context(), e, o); err != nil {
				t.Fatal(err)
			}
			s, err := config.LoadServer(e.Paths.ServerConfig)
			if err != nil || s.Timezone != "America/Denver" {
				t.Fatalf("detected timezone: %+v, %v", s, err)
			}
		})
	}
}

func TestInitSingleUser(t *testing.T) {
	for _, other := range []bool{false, true} {
		t.Run(fmt.Sprint(other), func(t *testing.T) {
			e, o := initEnv(t)
			if other {
				uid := 1001
				if uid == os.Getuid() {
					uid++
				}
				writeFile(t, o.PasswdPath, string(fileBytes(t, o.PasswdPath))+fmt.Sprintf("bob:x:%d:1001::/home/bob:/bin/bash\n", uid))
			}
			r, err := offline.Init(t.Context(), e, o)
			if err != nil {
				t.Fatal(err)
			}
			if r.AllowPlainLoopback == other || e.Server.Web.AllowPlainLoopback == other {
				t.Fatal("incorrect plain loopback consent")
			}
			if other && !strings.Contains(e.Err.(*bytes.Buffer).String(), "other user accounts") {
				t.Fatal("missing shared-host warning")
			}
		})
	}
}

func TestInitMCPToken(t *testing.T) {
	for _, expiry := range []string{"non-TTY", "never", "2027-01-01", "24h", "30d"} {
		t.Run(expiry, func(t *testing.T) {
			e, o := initEnv(t)
			o.MCPToken = true
			if expiry != "non-TTY" {
				e.TTY = true
				e.In = strings.NewReader(testutil.Password + "\n" + expiry + "\n")
			}
			r, err := offline.Init(t.Context(), e, o)
			if err != nil {
				t.Fatal(err)
			}
			if r.MCPToken != e.Paths.TokenFile("mcp") {
				t.Fatalf("MCP path = %q", r.MCPToken)
			}
			st := openDB(t, e.Paths.DB)
			token := requireToken(t, st, r.MCPToken)
			if token.Name != "mcp" || token.IsOwner || token.Scopes.String() != "read,capture,write" || token.MutationsPerHour != 60 {
				t.Fatalf("MCP metadata = %+v", token)
			}
			if token.ExpiresAt.Valid != (expiry == "2027-01-01" || expiry == "24h" || expiry == "30d") {
				t.Fatal("wrong expiry choice")
			}
			if expiry == "24h" && !token.ExpiresAt.V.Equal(e.Clock.Now().Add(24*time.Hour)) {
				t.Fatal("wrong duration expiry")
			}
			// B-I3: the same grammar as `tkr token create`, whose prompt suggests 30d.
			if expiry == "30d" && !token.ExpiresAt.V.Equal(e.Clock.Now().Add(30*24*time.Hour)) {
				t.Fatalf("30d expiry = %v", token.ExpiresAt.V)
			}
			var count int
			if err := st.Reader.QueryRowContext(t.Context(), "SELECT count(*) FROM ops WHERE kind = ?", domain.OpTokenCreate).Scan(&count); err != nil || count != 1 {
				t.Fatalf("MCP creation audit count = %d, %v", count, err)
			}
			if !strings.Contains(e.Out.(*bytes.Buffer).String(), `{"command": "tkr", "args": ["mcp"]}`) {
				t.Fatal("missing MCP snippet")
			}
		})
	}
}

// TestInitMCPExpiryValidatedFirst is final review B-I3: an invalid MCP expiry
// answer is a usage error found before anything is created, never a failure
// after the DB is published (which left a half-initialized instance).
func TestInitMCPExpiryValidatedFirst(t *testing.T) {
	for _, answer := range []string{"soon", "720", "1h30m", "0d", "999999d", "2020-01-01", ""} {
		t.Run(answer, func(t *testing.T) {
			e, o := initEnv(t)
			o.MCPToken, e.TTY = true, true
			e.In = strings.NewReader(testutil.Password + "\n" + answer + "\n")
			_, err := offline.Init(t.Context(), e, o)
			if !errors.Is(err, offline.ErrUsage) {
				t.Fatalf("Init with MCP expiry %q = %v, want a usage error", answer, err)
			}
			if !strings.Contains(detailOf(err), "MCP token expiry") {
				t.Fatalf("detail = %q", detailOf(err))
			}
			entries, err := os.ReadDir(e.Paths.DataDir)
			if err != nil || len(entries) != 1 || entries[0].Name() != "taskronin.lock" {
				t.Fatalf("invalid expiry left data: %v, %v", entries, err)
			}
			requireAbsent(t, e.Paths.OwnerToken)
			requireAbsent(t, e.Paths.ServerConfig)
			requireAbsent(t, e.Paths.ClientConfig)
			requireAbsent(t, e.Paths.TokenFile("mcp"))
		})
	}
}

func TestInitSystemd(t *testing.T) {
	e, o := initEnv(t)
	o.NoSystemd, o.Binary = false, "/usr/local/bin/tkr"
	var calls [][]string
	o.Systemctl = func(_ context.Context, args ...string) error {
		held, err := lock.Acquire(e.Paths.LockFile())
		if err != nil {
			t.Fatalf("instance lock still held when enabling systemd: %v", err)
		}
		if err := held.Release(); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, args)
		return nil
	}
	if err := os.MkdirAll(e.Paths.UnitsDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(e.Paths.UnitsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := offline.Init(t.Context(), e, o); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, [][]string{{"daemon-reload"}, {"enable", "--now", "taskronin.socket"}, {"enable", "taskronin.service"}}) {
		t.Fatalf("systemctl calls = %v", calls)
	}
	for _, name := range []string{"taskronin.socket", "taskronin.service"} {
		requireMode(t, filepath.Join(e.Paths.UnitsDir(), name), 0o644)
	}
	requireMode(t, e.Paths.UnitsDir(), 0o755)
	if !strings.Contains(string(fileBytes(t, filepath.Join(e.Paths.UnitsDir(), "taskronin.service"))), "ExecStart=/usr/local/bin/tkr serve") {
		t.Fatal("binary not rendered")
	}
}

func TestInitUnitsOnly(t *testing.T) {
	e, o := initEnv(t)
	if _, err := offline.Init(t.Context(), e, o); err != nil {
		t.Fatal(err)
	}
	protected := []string{e.Paths.DB, e.Paths.OwnerToken, e.Paths.ServerConfig, e.Paths.ClientConfig, filepath.Join(e.Paths.StagingDir, "leftover"), e.Paths.SwapJournal()}
	writeFile(t, protected[4], "staged data")
	writeFile(t, protected[5], "journal must not be inspected")
	before := make(map[string][]byte)
	modTimes := make(map[string]time.Time)
	for _, path := range protected {
		before[path] = fileBytes(t, path)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		modTimes[path] = info.ModTime()
	}
	for _, dir := range []string{e.Paths.DataDir, e.Paths.StateDir} {
		if err := os.Chmod(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	held, err := lock.Acquire(e.Paths.LockFile())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Release() }()
	e.Server, e.In = nil, nil
	_, err = offline.Init(t.Context(), e, offline.InitOptions{UnitsOnly: true, Binary: "/moved/tkr", Systemctl: func(context.Context, ...string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range protected {
		info, err := os.Stat(path)
		if err != nil || !info.ModTime().Equal(modTimes[path]) || !bytes.Equal(before[path], fileBytes(t, path)) {
			t.Fatalf("units-only touched %s: %v", path, err)
		}
	}
	for _, dir := range []string{e.Paths.DataDir, e.Paths.StateDir} {
		requireMode(t, dir, 0o750)
	}
	if !strings.Contains(string(fileBytes(t, filepath.Join(e.Paths.UnitsDir(), "taskronin.service"))), "ExecStart=/moved/tkr serve") {
		t.Fatal("units were not refreshed")
	}
}

func TestInitPasswordPrompts(t *testing.T) {
	for _, variant := range []string{"twice", "mismatch", "no-TTY", "read-error"} {
		t.Run(variant, func(t *testing.T) {
			e, o := initEnv(t)
			o.PasswordStdin, e.TTY = false, variant != "no-TTY"
			calls := 0
			e.ReadPassword = func(string) (string, error) {
				calls++
				if variant == "read-error" {
					return "", errors.New("read failed")
				}
				if variant == "mismatch" && calls == 2 {
					return "wrong", nil
				}
				return testutil.Password, nil
			}
			_, err := offline.Init(t.Context(), e, o)
			if variant == "twice" {
				if err != nil || calls != 2 {
					t.Fatalf("password prompts = %d, %v", calls, err)
				}
				return
			}
			if err == nil {
				t.Fatal("accepted invalid password input")
			}
			requireAbsent(t, e.Paths.DB)
			requireAbsent(t, e.Paths.OwnerToken)
		})
	}
}

func TestInitPreservesConfigAndUsesConfiguredPaths(t *testing.T) {
	e, o := initEnv(t)
	root := filepath.Dir(o.PasswdPath)
	s := config.DefaultServer(e.Paths, "America/Denver")
	s.Paths.DataDir, s.Backup.Dir = filepath.Join(root, "custom"), filepath.Join(root, "archive")
	s.Paths.Socket = filepath.Join(root, "sock", "api")
	s.Log.Level = "debug"
	if err := fsutil.EnsureDir0700(e.Paths.ConfigDir); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteServer(e.Paths.ServerConfig, s); err != nil {
		t.Fatal(err)
	}
	client := "# existing client settings\ncolor = 'never'\n"
	writeFile(t, e.Paths.ClientConfig, client)
	saved := fileBytes(t, e.Paths.ServerConfig)
	defaultDB := e.Paths.DB
	o.NoSystemd, o.Binary = false, "/bin/tkr"
	o.Systemctl = func(context.Context, ...string) error { return nil }
	if _, err := offline.Init(t.Context(), e, o); err != nil {
		t.Fatal(err)
	}
	requireMode(t, e.Paths.RuntimeDir, 0o700)
	requireAbsent(t, defaultDB)
	if !bytes.Equal(saved, fileBytes(t, e.Paths.ServerConfig)) || string(fileBytes(t, e.Paths.ClientConfig)) != client {
		t.Fatal("existing config overwritten")
	}
	requireToken(t, openDB(t, filepath.Join(s.Paths.DataDir, "taskronin.db")), e.Paths.OwnerToken)
	unit := string(fileBytes(t, filepath.Join(e.Paths.UnitsDir(), "taskronin.service")))
	if !strings.Contains(unit, s.Backup.Dir) || !strings.Contains(unit, s.Paths.DataDir) {
		t.Fatal("units omitted configured writable paths")
	}
}

func TestInitCustomSocketSurvivesRuntimeCleanup(t *testing.T) {
	for _, location := range []string{"persistent", "runtime"} {
		t.Run(location, func(t *testing.T) {
			e, o := initEnv(t)
			runtimeRoot := filepath.Dir(e.Paths.RuntimeDir)
			socketDir := filepath.Join(filepath.Dir(o.PasswdPath), "sockets")
			if location == "runtime" {
				socketDir = filepath.Join(runtimeRoot, "custom")
			}
			s := config.DefaultServer(e.Paths, "America/Denver")
			s.Paths.Socket = filepath.Join(socketDir, "api")
			if err := fsutil.EnsureDir0700(e.Paths.ConfigDir); err != nil {
				t.Fatal(err)
			}
			if err := config.WriteServer(e.Paths.ServerConfig, s); err != nil {
				t.Fatal(err)
			}
			o.NoSystemd, o.Binary = false, "/bin/tkr"
			o.Systemctl = func(context.Context, ...string) error { return nil }
			if _, err := offline.Init(t.Context(), e, o); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(runtimeRoot); err != nil {
				t.Fatal(err)
			}
			checkActivationPaths := func() {
				t.Helper()
				socket := string(fileBytes(t, filepath.Join(e.Paths.UnitsDir(), "taskronin.socket")))
				if !strings.Contains(socket, "ListenStream="+s.Paths.Socket+"\n") || !strings.Contains(socket, "DirectoryMode=0700\n") {
					t.Fatal("socket unit does not provision the configured socket parent")
				}
				// Model the socket unit's activation after reboot: it creates
				// only the configured socket's parent, not the unused XDG path.
				if err := os.MkdirAll(socketDir, 0o700); err != nil {
					t.Fatal(err)
				}
				requireAbsent(t, e.Paths.RuntimeDir)
				unit := string(fileBytes(t, filepath.Join(e.Paths.UnitsDir(), "taskronin.service")))
				for line := range strings.SplitSeq(unit, "\n") {
					if value, ok := strings.CutPrefix(line, "ReadWritePaths="); ok {
						paths := strings.Fields(value)
						for _, path := range paths {
							if _, err := os.Stat(path); err != nil {
								t.Fatalf("service requires an unavailable path after runtime cleanup: %s: %v", path, err)
							}
						}
						want := []string{e.Paths.DataDir, e.Paths.StateDir, socketDir}
						if !reflect.DeepEqual(paths, want) {
							t.Fatalf("service writable paths = %v, want %v", paths, want)
						}
						return
					}
				}
				t.Fatal("missing service writable paths")
			}
			checkActivationPaths()
			e.Server = nil // Reload the persisted custom path for --units-only.
			o.UnitsOnly = true
			if _, err := offline.Init(t.Context(), e, o); err != nil {
				t.Fatal(err)
			}
			checkActivationPaths()
		})
	}
}

func TestInitTokenWriteFailureDoesNotPublish(t *testing.T) {
	e, o := initEnv(t)
	if err := os.MkdirAll(e.Paths.OwnerToken, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := offline.Init(t.Context(), e, o); err == nil {
		t.Fatal("accepted owner token write failure")
	}
	requireAbsent(t, e.Paths.DB)
	if err := os.Remove(e.Paths.OwnerToken); err != nil {
		t.Fatal(err)
	}
	e.In = strings.NewReader(testutil.Password + "\n")
	if _, err := offline.Init(t.Context(), e, o); err != nil {
		t.Fatal(err)
	}
	requireToken(t, openDB(t, e.Paths.DB), e.Paths.OwnerToken)
}

func TestInitRejectsOversizedPassword(t *testing.T) {
	for _, size := range []int{1025, 100000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			e, o := initEnv(t)
			e.In = strings.NewReader(strings.Repeat("x", size) + "\n") // Invalid length, never a credential.
			_, err := offline.Init(t.Context(), e, o)
			testutil.RequireCode(t, err, domain.CodeValidation)
			requireAbsent(t, e.Paths.DB)
			requireAbsent(t, e.Paths.OwnerToken)
		})
	}
}

func TestInitConfirmation(t *testing.T) {
	for _, variant := range []string{"confirm", "decline-timezone", "decline-loopback", "no-TTY"} {
		t.Run(variant, func(t *testing.T) {
			e, o := initEnv(t)
			zone := filepath.Join(filepath.Dir(o.PasswdPath), "zoneinfo", "America", "Denver")
			writeFile(t, zone, "timezone fixture")
			o.LocaltimePath = filepath.Join(filepath.Dir(o.PasswdPath), "localtime")
			if err := os.Symlink(zone, o.LocaltimePath); err != nil {
				t.Fatal(err)
			}
			o.Timezone, o.Yes, e.TTY = "", false, variant != "no-TTY"
			calls := 0
			e.Confirm = func(prompt string, _ bool) (bool, error) {
				calls++
				if calls == 1 && !strings.Contains(prompt, "America/Denver") {
					t.Fatal("timezone confirmation omitted detected zone")
				}
				if calls == 1 && variant == "decline-timezone" || calls == 2 && variant == "decline-loopback" {
					return false, nil
				}
				return true, nil
			}
			r, err := offline.Init(t.Context(), e, o)
			if variant == "decline-timezone" || variant == "no-TTY" {
				testutil.RequireCode(t, err, domain.CodeValidation)
				requireAbsent(t, e.Paths.DB)
				return
			}
			if err != nil || calls != 2 || r.AllowPlainLoopback != (variant == "confirm") {
				t.Fatalf("confirmation = %d, allow = %v, error = %v", calls, r.AllowPlainLoopback, err)
			}
		})
	}
}

func TestInitSystemdFailureKeepsPublishedDB(t *testing.T) {
	e, o := initEnv(t)
	o.NoSystemd = false
	failure := errors.New("systemctl failed")
	calls := 0
	o.Systemctl = func(context.Context, ...string) error { calls++; return failure }
	r, err := offline.Init(t.Context(), e, o)
	if !errors.Is(err, failure) || calls != 1 || r.DB != e.Paths.DB {
		t.Fatalf("systemd failure = %+v, %v, calls = %d", r, err, calls)
	}
	requireToken(t, openDB(t, e.Paths.DB), e.Paths.OwnerToken)
}
