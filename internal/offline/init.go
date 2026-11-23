package offline

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/linsomniac/taskronin/internal/auth"
	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/config"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/fsutil"
	"github.com/linsomniac/taskronin/internal/service"
	"github.com/linsomniac/taskronin/internal/store"
	"github.com/linsomniac/taskronin/internal/systemd"
	"github.com/linsomniac/taskronin/internal/textsafe"
)

// InitOptions controls first-run initialization and unit regeneration.
type InitOptions struct {
	Timezone                                           string
	PasswordStdin, NoSystemd, Yes, MCPToken, UnitsOnly bool
	LocaltimePath, PasswdPath                          string
	Systemctl                                          func(ctx context.Context, args ...string) error
	Binary                                             string
	// Nix profile bin directories searched for a link to the binary; nil means the standard ones for this user.
	ProfileBins []string
}

// InitResult contains file paths, never bearer secrets.
type InitResult struct {
	DB, OwnerToken, MCPToken string
	AllowPlainLoopback       bool
}

// Init bootstraps a private instance, publishing the DB only after its owner
// token is durable. UnitsOnly reads configuration and installs units exclusively.
func Init(ctx context.Context, e *Env, o InitOptions) (result InitResult, err error) {
	defer func() { err = offlineError(err) }()
	fsutil.SetUmask(0o077)
	if err := ctx.Err(); err != nil {
		return result, err
	}
	// AIDEV-NOTE: P§6 requires resolving the unit binary before LoadServer,
	// e.begin, and any prompt or write. BF03 forbids e.begin on the units-only
	// path because its chmod, lock and temp cleanup would mutate data/state.
	var binary string
	if o.UnitsOnly || !o.NoSystemd {
		binary, err = initUnitBinary(o)
		if err != nil {
			return result, err
		}
	}
	if o.UnitsOnly {
		// Load configuration without opening the database.
		e.Server, err = config.LoadServer(e.Paths.ServerConfig)
		if err != nil {
			return result, err
		}
		e.Paths = e.Paths.WithServerPaths(e.Server.Paths.DataDir, e.Server.Paths.Socket, e.Server.Backup.Dir)
		return result, installInitUnits(ctx, e, binary, o.Systemctl)
	}
	serverExists, err := initFileExists(e.Paths.ServerConfig)
	if err != nil {
		return result, err
	}
	if serverExists {
		e.Server, err = config.LoadServer(e.Paths.ServerConfig)
		if err != nil {
			return result, err
		}
	}
	release, err := e.begin(ctx, refuseJournals)
	if err != nil {
		return result, err
	}
	defer release()
	if exists, err := initFileExists(e.Paths.DB); err != nil {
		return result, err
	} else if exists {
		return result, domain.Errorf(domain.CodeConflict, "already initialized")
	}
	if err := refuseInitLeftovers(e.Paths); err != nil {
		return result, err
	}
	zoneName, err := initTimezone(e, o)
	if err != nil {
		return result, err
	}
	if e.Server == nil {
		e.Server = config.DefaultServer(e.Paths, zoneName)
	}
	// One buffered reader keeps an MCP expiry line available after password-stdin.
	input := e.In
	if input == nil {
		input = strings.NewReader("")
	}
	reader := bufio.NewReader(input)
	hash, err := initPassword(ctx, e, o, reader)
	if err != nil {
		return result, err
	}
	allow, err := initLoopback(e, o)
	if err != nil {
		return result, err
	}
	// AIDEV-NOTE: final review B-I3. The MCP expiry is read and validated here,
	// after the password and loopback answers (stdin order) but BEFORE any
	// config, token or DB is written: a bad answer used to fail after the DB
	// was published, leaving a half-initialized instance.
	var mcpExpiry sql.Null[time.Time]
	if o.MCPToken {
		if mcpExpiry, err = initMCPExpiry(e, reader); err != nil {
			return result, err
		}
	}
	if !serverExists {
		e.Server.Web.AllowPlainLoopback = allow
	}
	if err := config.ValidateServer(e.Server); err != nil {
		return result, domain.Errorf(domain.CodeValidation, "%s", err)
	}
	client := config.DefaultClient(e.Paths)
	if err := config.ValidateClient(client); err != nil {
		return result, domain.Errorf(domain.CodeValidation, "%s", err)
	}
	for _, dir := range []string{e.Paths.ConfigDir, e.Paths.StagingDir, e.Paths.BackupDir,
		e.Paths.TokensDir, e.Paths.DraftsDir, e.Paths.PendingDir, e.Paths.RuntimeDir, filepath.Dir(e.Paths.Socket)} {
		if dir == "" {
			continue // XDG_RUNTIME_DIR may be unset; the socket then uses StateDir.
		}
		if err := fsutil.EnsureDir0700(dir); err != nil {
			return result, err
		}
	}
	if !serverExists {
		if err := config.WriteServer(e.Paths.ServerConfig, e.Server); err != nil {
			return result, err
		}
	}
	if exists, err := initFileExists(e.Paths.ClientConfig); err != nil {
		return result, err
	} else if !exists {
		if err := config.WriteClient(e.Paths.ClientConfig, client); err != nil {
			return result, err
		}
	}
	if err := publishInit(ctx, e, hash); err != nil {
		return result, err
	}
	result = InitResult{DB: e.Paths.DB, OwnerToken: e.Paths.OwnerToken, AllowPlainLoopback: e.Server.Web.AllowPlainLoopback}
	if o.MCPToken {
		if err := initMCPToken(ctx, e, mcpExpiry); err != nil {
			return result, err
		}
		result.MCPToken = e.Paths.TokenFile("mcp")
		if _, err := fmt.Fprintln(e.Out, `{"command": "tkr", "args": ["mcp"]}`); err != nil {
			return result, err
		}
	}
	// The socket may activate the service as soon as systemctl enables it.
	// All DB work is closed before releasing the instance lock here.
	release()
	if !o.NoSystemd {
		if err := installInitUnits(ctx, e, binary, o.Systemctl); err != nil {
			return result, err
		}
	}
	_, err = fmt.Fprintln(e.Out, "Next: tkr add \"…\"\nShell/tmux status line: tkr status --short")
	return result, err
}

// refuseInitLeftovers refuses a fresh init over files an earlier database left
// in the data dir (A§8.5 step 1).
//
// AIDEV-NOTE: final review B-C1. Ledger records carry bare row ids with no
// lineage, and every server start, offline command and import replays the
// data dir's purge-ledger against whatever database lives there. A new DB
// restarts its sequences at 1, so a leftover ledger would silently scrub the
// new tasks and entries that reuse the purged ids; a stray -wal could even be
// replayed into the newly published file. Never delete or ignore these files
// here: the owner must restore (same lineage) or move them aside deliberately.
// begin refuses both journals first, with --resume/--rollback guidance; they
// are listed again as defence in depth should begin's policy change.
func refuseInitLeftovers(p config.Paths) error {
	var found []string
	for _, path := range []string{p.LedgerFile, p.DB + "-wal", p.DB + "-shm", p.SwapJournal(), config.ScrubJournal(p.BackupDir)} {
		exists, err := initFileExists(path)
		if err != nil {
			return err
		}
		if exists {
			found = append(found, textsafe.SanitizeLine(path))
		}
	}
	if len(found) == 0 {
		return nil
	}
	return domain.Errorf(domain.CodeConflict, "refusing to initialize: no database exists, but %s remain from an earlier one "+
		"(a leftover purge ledger would scrub the new database's tasks). `tkr restore SNAPSHOT` recovers that database from a backup; "+
		"to start fresh instead, deliberately move these files aside first", strings.Join(found, ", "))
}

func initFileExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func initTimezone(e *Env, o InitOptions) (string, error) {
	name := o.Timezone
	if e.Server != nil {
		if name != "" && name != e.Server.Timezone {
			return "", domain.Errorf(domain.CodeValidation, "--timezone differs from the existing server configuration")
		}
		name = e.Server.Timezone
	}
	if name == "" {
		path := o.LocaltimePath
		if path == "" {
			path = "/etc/localtime"
		}
		var err error
		name, err = localtimeZone(path)
		if err != nil {
			return "", domain.Errorf(domain.CodeValidation, "cannot detect timezone; use --timezone: %s", err)
		}
		ok, err := initConfirm(e, o.Yes, "Use timezone "+textsafe.SanitizeLine(name)+"?", true)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", domain.Errorf(domain.CodeValidation, "timezone confirmation required; use --timezone or --yes")
		}
	}
	if _, err := civil.LoadZone(name); err != nil {
		return "", domain.Errorf(domain.CodeValidation, "%s", err)
	}
	return name, nil
}

func localtimeZone(path string) (string, error) {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	root, name, ok := strings.Cut(target, "/zoneinfo/")
	if !ok {
		return "", errors.New("localtime must link into an IANA zoneinfo directory")
	}
	name = strings.TrimPrefix(strings.TrimPrefix(name, "posix/"), "right/")
	// AIDEV-NOTE: civil.LoadZone rejects slash-less links such as GB and Japan.
	// EvalSymlinks resolves symbolic aliases; tzdata.zi also resolves hard links
	// (used by some distributions), without guessing from today's UTC offset.
	f, err := os.Open(filepath.Join(root, "zoneinfo", "tzdata.zi")) //nolint:gosec // G304: zone metadata adjacent to the resolved localtime target
	if err == nil {
		defer func() { _ = f.Close() }()
		links := make(map[string]string)
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) >= 3 && (fields[0] == "L" || fields[0] == "Link") {
				links[fields[2]] = fields[1]
			}
		}
		if err := scanner.Err(); err != nil {
			return "", err
		}
		for hops := 0; links[name] != ""; hops++ {
			if hops >= len(links) {
				return "", errors.New("cyclic timezone links")
			}
			name = links[name]
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if _, err := civil.LoadZone(name); err != nil {
		return "", err
	}
	return name, nil
}

func initPassword(ctx context.Context, e *Env, o InitOptions, reader *bufio.Reader) (string, error) {
	var password string
	var err error
	if o.PasswordStdin {
		password, err = initReadLine(reader)
	} else {
		if !e.TTY {
			return "", domain.Errorf(domain.CodePasswordRequired, "enter the password on a terminal or use --password-stdin")
		}
		if e.ReadPassword == nil {
			return "", domain.Errorf(domain.CodeInternal, "password reader is not configured")
		}
		password, err = e.ReadPassword("New password: ")
		if err == nil {
			var repeat string
			repeat, err = e.ReadPassword("Repeat password: ")
			if err == nil && password != repeat {
				return "", domain.Errorf(domain.CodeValidation, "passwords do not match")
			}
		}
	}
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := auth.ValidateNewPassword(password); err != nil {
		return "", domain.Errorf(domain.CodeValidation, "%s", err)
	}
	return auth.HashPassword(password)
}

func initReadLine(reader *bufio.Reader) (string, error) {
	// ReadSlice bounds allocation for untrusted stdin and leaves later prompt
	// answers in the same reader. Only remove line endings, never password spaces.
	b, err := reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return "", domain.Errorf(domain.CodeValidation, "input line is too long")
	}
	if err != nil && (!errors.Is(err, io.EOF) || len(b) == 0) {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r"), nil
}

func initConfirm(e *Env, yes bool, prompt string, defaultYes bool) (bool, error) {
	if yes {
		return true, nil
	}
	if !e.TTY {
		return false, nil
	}
	if e.Confirm == nil {
		return false, domain.Errorf(domain.CodeInternal, "confirmation reader is not configured")
	}
	return e.Confirm(prompt, defaultYes)
}

func initLoopback(e *Env, o InitOptions) (bool, error) {
	path := o.PasswdPath
	if path == "" {
		path = "/etc/passwd"
	}
	names, err := config.OtherHumanAccounts(path, os.Getuid())
	if err != nil {
		return false, err
	}
	if len(names) > 0 {
		_, err := fmt.Fprintf(e.Err, "Warning: other user accounts (%s); plain HTTP loopback is unsafe on a shared host. Keep allow_plain_loopback = false.\n", textsafe.SanitizeLine(strings.Join(names, ", ")))
		return false, err
	}
	return initConfirm(e, o.Yes, "Single-user host: allow plain HTTP on loopback (no TLS)?", false)
}

func publishInit(ctx context.Context, e *Env, hash string) (err error) {
	dir, err := os.MkdirTemp(e.Paths.StagingDir, "init-*")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	path := filepath.Join(dir, "taskronin.db")
	token, err := bootstrapInit(ctx, e, path, hash)
	if err != nil {
		return err
	}
	f, err := os.Open(path) //nolint:gosec // G304: newly created DB in the private staging directory
	if err != nil {
		return err
	}
	if err := errors.Join(f.Sync(), f.Close()); err != nil {
		return err
	}
	if err := crashPoint(ctx, "init:staged"); err != nil {
		return err
	}
	// AIDEV-NOTE: the owner credential MUST be durable before the DB becomes
	// live. Before the rename, retry can replace both; afterwards it must refuse.
	if err := fsutil.WriteFileAtomic(e.Paths.OwnerToken, []byte(token+"\n"), 0o600); err != nil {
		return err
	}
	if err := crashPoint(ctx, "init:token-written"); err != nil {
		return err
	}
	if err := os.Rename(path, e.Paths.DB); err != nil {
		return err
	}
	return fsutil.FsyncDir(e.Paths.DataDir)
}

func bootstrapInit(ctx context.Context, e *Env, path, hash string) (token string, err error) {
	st, err := store.OpenStaging(ctx, path)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, st.Close()) }()
	if _, err := st.Migrate(ctx, nil); err != nil {
		return "", err
	}
	now := e.Clock.Now()
	name, err := auth.OwnerTokenName(now)
	if err != nil {
		return "", err
	}
	secret, err := auth.NewSecret()
	if err != nil {
		return "", err
	}
	key, err := auth.NewSecret()
	if err != nil {
		return "", err
	}
	tx, err := st.Writer.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	id, err := store.BootstrapDB(ctx, tx, store.Bootstrap{
		Generation: domain.NewGeneration(), TZ: e.Server.Timezone,
		Auth:  store.AuthState{PasswordHash: hash, PasswordChangedAt: now, IdemHMACKey: key},
		Owner: domain.Token{Name: name, SecretHash: auth.HashSecret(secret), Scopes: domain.AllScopes, IsOwner: true, CreatedAt: now},
	})
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return auth.FormatToken(id, secret), nil
}

// initMCPExpiry prompts on a terminal for the MCP token expiry and parses it
// with the `tkr token create` grammar. Without a terminal the token never
// expires, as before. An invalid answer is a usage error (ErrUsage).
func initMCPExpiry(e *Env, reader *bufio.Reader) (sql.Null[time.Time], error) {
	if !e.TTY {
		return sql.Null[time.Time]{}, nil
	}
	if _, err := fmt.Fprint(e.Err, "MCP token expiry (e.g. 30d, 2026-12-31, never): "); err != nil {
		return sql.Null[time.Time]{}, err
	}
	line, err := initReadLine(reader)
	if err != nil {
		return sql.Null[time.Time]{}, err
	}
	zone, err := e.Server.Zone()
	if err != nil {
		return sql.Null[time.Time]{}, err
	}
	expires, err := domain.ParseTokenExpiry(strings.TrimSpace(line), e.Clock.Now(), zone)
	if err != nil {
		return expires, &domain.Error{Code: domain.CodeValidation, Detail: "MCP token expiry " + err.Error(), Err: ErrUsage}
	}
	return expires, nil
}

func initMCPToken(ctx context.Context, e *Env, expires sql.Null[time.Time]) (err error) {
	st, err := e.openStore(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, st.Close()) }()
	svc, err := e.newService(st, nil)
	if err != nil {
		return err
	}
	created, err := svc.CreateToken(ctx, service.Meta{Actor: domain.Actor{Kind: domain.ActorOwner, Scopes: domain.AllScopes, Channel: domain.ChanCLI}, Gen: svc.Generation()},
		service.CreateTokenIn{Name: "mcp", Scopes: domain.MCPDefaultScopes, MutationsPerHour: 60, ExpiresAt: expires})
	if err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(e.Paths.TokenFile("mcp"), []byte(created.Secret+"\n"), 0o600); err != nil {
		return err
	}
	return checkpoint(ctx, st)
}

func initUnitBinary(o InitOptions) (string, error) {
	binary := o.Binary
	if binary == "" {
		var err error
		binary, err = os.Executable()
		if err != nil {
			return "", err
		}
	}
	if !filepath.IsAbs(binary) {
		return "", domain.Errorf(domain.CodeValidation, "systemd binary path must be absolute")
	}
	bins := o.ProfileBins
	if bins == nil {
		home, _ := os.UserHomeDir()
		var login string
		if u, err := user.Current(); err == nil {
			login = u.Username
		}
		bins = defaultProfileBins(home, os.Getenv("XDG_STATE_HOME"), login)
	}
	path, err := systemd.StableBinary(binary, bins)
	if errors.Is(err, systemd.ErrStoreBinary) {
		suffix := ", or pass --no-systemd."
		if o.UnitsOnly {
			suffix = "."
		}
		return "", domain.Errorf(domain.CodeValidation, "tkr is running from the Nix store (%s) and is not installed in a Nix profile, "+
			"so systemd units would break when that store path is collected. Install tkr into a profile (on NixOS: programs.taskronin.enable = true)%s", binary, suffix)
	}
	if err != nil {
		return "", err
	}
	return path, systemd.CheckUnitPath("Binary", path)
}

func defaultProfileBins(home, stateHome, login string) []string {
	if !filepath.IsAbs(home) {
		home = ""
	}
	if !filepath.IsAbs(stateHome) {
		stateHome = ""
	}
	return systemd.NixProfileBins(home, stateHome, login)
}

func installInitUnits(ctx context.Context, e *Env, binary string, run func(context.Context, ...string) error) error {
	// AIDEV-NOTE: socket activation recreates the configured socket's parent.
	// An unused XDG runtime directory can disappear at reboot; requiring it in
	// ReadWritePaths would then prevent the service from starting.
	d := systemd.UnitData{Binary: binary, Socket: e.Paths.Socket, DataDir: e.Paths.DataDir, StateDir: e.Paths.StateDir, RuntimeDir: filepath.Dir(e.Paths.Socket)}
	if relative, err := filepath.Rel(e.Paths.DataDir, e.Paths.BackupDir); err != nil {
		return err
	} else if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		d.ExtraRW = []string{e.Paths.BackupDir}
	}
	svc, socket, err := systemd.RenderUnits(d)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(e.Paths.UnitsDir(), 0o700); err != nil {
		return err
	}
	for _, unit := range []struct {
		name string
		data []byte
	}{{"taskronin.service", svc}, {"taskronin.socket", socket}} {
		if err := fsutil.WriteFileAtomic(filepath.Join(e.Paths.UnitsDir(), unit.name), unit.data, 0o644); err != nil {
			return err
		}
	}
	if run == nil {
		run = func(ctx context.Context, args ...string) error {
			args = append([]string{"--user"}, args...)
			cmd := exec.CommandContext(ctx, "systemctl", args...) //nolint:gosec // G204: fixed binary, fixed subcommands and unit names
			return cmd.Run()
		}
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "--now", "taskronin.socket"}, {"enable", "taskronin.service"}} {
		if err := run(ctx, args...); err != nil {
			return fmt.Errorf("systemctl --user %s: %w", strings.Join(args, " "), err)
		}
	}
	return nil
}
