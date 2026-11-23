package server_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/backup"
	"github.com/linsomniac/taskronin/internal/cli"
	"github.com/linsomniac/taskronin/internal/client"
	"github.com/linsomniac/taskronin/internal/config"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/editor"
	"github.com/linsomniac/taskronin/internal/mcpserver"
	"github.com/linsomniac/taskronin/internal/offline"
	"github.com/linsomniac/taskronin/internal/store"
	"github.com/linsomniac/taskronin/internal/testutil"
)

// AIDEV-NOTE: BF30 - the only fixed password is testutil.Password; the new
// password and every token secret are generated at runtime. The sweep assumes
// a secret is high-entropy, so a substring hit anywhere is a real leak.

func randomPassword(t *testing.T) string {
	t.Helper()
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return "pw " + hex.EncodeToString(b[:])
}

// secretPart returns the secret portion of a "tkr_<id>_<secret>" token.
func secretPart(t *testing.T, token string) string {
	t.Helper()
	parts := strings.SplitN(strings.TrimSpace(token), "_", 3)
	if len(parts) != 3 || parts[0] != "tkr" || len(parts[2]) < 16 {
		t.Fatalf("not a token: %d parts", len(parts))
	}
	return parts[2]
}

func readSecretPart(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return secretPart(t, string(data))
}

// dropProxy forwards every request to the real socket, but closes the
// connection after a non-GET request has been processed: the server commits,
// the client never sees the response.
func dropProxy(t *testing.T, upstream string) string {
	t.Helper()
	path := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "drop.sock")
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL = &url.URL{Scheme: "http", Host: "taskronin", Path: r.In.URL.Path, RawQuery: r.In.URL.RawQuery}
			r.Out.Host = r.In.Host
		},
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", upstream)
		}},
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			rp.ServeHTTP(w, r)
			return
		}
		rp.ServeHTTP(httptest.NewRecorder(), r)
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			_ = conn.Close()
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return path
}

// sweep records everything a flow produced that a secret could hide in.
type sweep struct {
	s         *testutil.Server
	tokens    []string // token secret parts
	passwords []string // every password ever set
	output    bytes.Buffer
	deleted   int64  // a deleted task, for the offline purge
	snapshot  string // a backup snapshot name
}

// cli runs tkr in-process, recording both streams, and returns the exit code.
func (w *sweep) cli(t *testing.T, stdin string, args ...string) int {
	t.Helper()
	var out, errOut bytes.Buffer
	code := cli.Main(t.Context(), args, strings.NewReader(stdin), &out, &errOut)
	w.output.Write(out.Bytes())
	w.output.Write(errOut.Bytes())
	return code
}

func (w *sweep) mustCLI(t *testing.T, args ...string) {
	t.Helper()
	if code := w.cli(t, "", args...); code != 0 {
		out := w.output.String()
		t.Fatalf("tkr %v exit %d; last output: %s", args, code, out[max(0, len(out)-300):])
	}
}

// editorScript is a fake $EDITOR recording its argv and environment, then
// writing notes into the draft (its last argument).
func editorScript(t *testing.T) (script, argvFile, envFile string) {
	t.Helper()
	dir := t.TempDir()
	script, argvFile, envFile = filepath.Join(dir, "editor.sh"), filepath.Join(dir, "argv"), filepath.Join(dir, "env")
	body := "#!/bin/sh\nprintf '%s\\n' \"$0\" \"$@\" > " + argvFile + "\nenv > " + envFile + "\nfor last; do :; done\nprintf 'edited notes\\n' > \"$last\"\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return script, argvFile, envFile
}

// settingsLowering is the password-bearing settings request used by the flow.
func settingsLowering(password string) api.SettingsRequest {
	keep := 3
	return api.SettingsRequest{BackupKeepDaily: &keep, Password: password}
}

// flow exercises every path that handles credentials or writes files. It
// returns the secrets to hunt for, and the files only a CLI child can see.
func flow(t *testing.T, s *testutil.Server) *sweep {
	t.Helper()
	w := &sweep{s: s, passwords: []string{testutil.Password, randomPassword(t), randomPassword(t)}}
	pw1, pw2, pw3 := w.passwords[0], w.passwords[1], w.passwords[2]

	// Password-bearing requests whose responses are lost must never be saved
	// as pending (they are exempt calls), yet the server did process them.
	lossy, err := client.New(client.Options{Socket: dropProxy(t, s.Socket), TokenFile: s.Paths.OwnerToken, Channel: "cli",
		Pending: &client.PendingStore{Dir: s.Paths.PendingDir, Clock: s.Clock}})
	if err != nil {
		t.Fatal(err)
	}
	if err := lossy.ChangePassword(t.Context(), api.PasswordRequest{Current: pw1, New: pw2}); err == nil {
		t.Fatal("lossy ChangePassword unexpectedly succeeded")
	}
	if _, err := lossy.UpdateSettings(t.Context(), settingsLowering(pw2)); err == nil {
		t.Fatal("lossy UpdateSettings unexpectedly succeeded")
	}
	if got, err := s.Client.Settings(t.Context()); err != nil || got.BackupKeepDaily != 3 {
		t.Fatalf("lossy settings change was not processed: %+v, %v", got, err)
	}
	// A keyed mutation whose response is lost IS saved: the control for the above.
	_, err = lossy.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: "lost response"})
	var unknown *client.OutcomeUnknownError
	if !errors.As(err, &unknown) || !unknown.Saved {
		t.Fatalf("AddTask error = %v, want a saved outcome-unknown error", err)
	}
	pending, err := os.ReadDir(s.Paths.PendingDir)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending files = %v, %v; want exactly the AddTask", pending, err)
	}

	if err := s.Client.ChangePassword(t.Context(), api.PasswordRequest{Current: pw2, New: pw3}); err != nil {
		t.Fatalf("change password: %v", err)
	}
	ai := s.TokenClient(t, "ai", domain.MCPDefaultScopes, 100, "mcp")
	if _, err := ai.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: "from the ai token"}); err != nil {
		t.Fatalf("ai add: %v", err)
	}
	mcpClient, err := client.New(client.Options{Socket: s.Socket, TokenFile: s.Paths.TokenFile("mcp"), Channel: "mcp"})
	if err != nil {
		t.Fatal(err)
	}
	w.mcpRoundTrip(t, ai)
	w.mcpRoundTrip(t, mcpClient)

	// A deleted task for the offline purge, and a snapshot that holds it.
	added, err := s.Client.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: "to be purged"})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := s.Client.GetTask(t.Context(), added.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Client.DeleteTask(t.Context(), client.Mut{IfMatch: detail.ETag}, added.Task.ID); err != nil {
		t.Fatal(err)
	}
	w.deleted = added.Task.ID
	snap, err := s.Client.RunBackup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	w.snapshot = snap.Name

	// CLI: every stream is recorded. Verbs that take a password refuse stdin.
	w.mustCLI(t, "export", "-o", filepath.Join(s.Paths.DataDir, "export.json"))
	for _, args := range [][]string{{"status"}, {"today"}, {"ls", "--all"}, {"settings"}, {"token", "list"}, {"backup", "--list"}, {"pending"}} {
		w.mustCLI(t, args...)
	}
	w.mustCLI(t, "token", "create", "filetok", "--mcp", "--expires", "never", "--write-file")
	if code := w.cli(t, pw3+"\n"+pw3+"\n", "passwd"); code != 4 {
		t.Fatalf("tkr passwd with the password on stdin exit %d, want 4 (terminal only)", code)
	}
	if code := w.cli(t, pw3+"\n", "settings", "set", "backup.keep_daily", "3"); code != 0 {
		t.Logf("settings set exit %d", code) // not lowering: either outcome is fine
	}
	script, argvFile, envFile := editorScript(t)
	t.Setenv("VISUAL", script) // takes precedence over $EDITOR
	t.Setenv("EDITOR", script)
	w.mustCLI(t, "add", "-e", "edited task")
	for _, f := range []string{argvFile, envFile} {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("editor did not run: %v", err)
		}
		w.output.Write(data) // argv and environment of the editor child are scanned too
	}
	if _, err := (editor.Drafts{Dir: s.Paths.DraftsDir, Clock: s.Clock}).New("draft body"); err != nil {
		t.Fatal(err)
	}
	w.tokens = []string{readSecretPart(t, s.Paths.OwnerToken), readSecretPart(t, s.Paths.TokenFile("ai")),
		readSecretPart(t, s.Paths.TokenFile("mcp")), readSecretPart(t, s.Paths.TokenFile("filetok"))}
	return w
}

// mcpRoundTrip drives a few MCP tools in memory and records every result.
func (w *sweep) mcpRoundTrip(t *testing.T, c *client.Client) {
	t.Helper()
	srv, err := mcpserver.New(t.Context(), mcpserver.Options{Client: c, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "sweep", Version: "0"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	for _, tool := range []struct {
		name string
		args map[string]any
	}{
		{"add_task", map[string]any{"title": "mcp added"}}, {"today", nil}, {"list_tasks", nil},
		{"search", map[string]any{"query": "mcp"}}, {"get_task", map[string]any{"id": 999999}},
	} {
		res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tool.name, Arguments: tool.args})
		if err != nil {
			t.Fatalf("CallTool %s: %v", tool.name, err)
		}
		data, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		w.output.Write(data)
	}
}

// secrets lists every secret under its descriptive name.
func (w *sweep) secrets() map[string]string {
	m := map[string]string{}
	for i, p := range w.passwords {
		m[fmt.Sprintf("password %d", i+1)] = p
	}
	for i, tok := range w.tokens {
		m[fmt.Sprintf("token secret %d", i+1)] = tok
	}
	return m
}

func (w *sweep) check(t *testing.T, where string, data []byte) {
	t.Helper()
	for name, secret := range w.secrets() {
		if bytes.Contains(data, []byte(secret)) {
			t.Errorf("%s contains %s", where, name)
		}
	}
}

// ownTokenFile reports whether path is a token file; its own secret is its job.
func isTokenFile(s *testutil.Server, path string) bool {
	return path == s.Paths.OwnerToken || (filepath.Dir(path) == s.Paths.TokensDir && strings.HasSuffix(path, ".token"))
}

func dumpTables(t *testing.T, dbPath string) string {
	t.Helper()
	st, err := store.Open(t.Context(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var tables []string
	queryRows(t, st, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`, func(_ []string, vals []any) {
		tables = append(tables, fmt.Sprint(vals[0]))
	})
	var dump strings.Builder
	for _, name := range tables {
		// The name comes from sqlite_master, never from input.
		queryRows(t, st, `SELECT * FROM "`+strings.ReplaceAll(name, `"`, `""`)+`"`, func(_ []string, vals []any) {
			fmt.Fprintf(&dump, "%s:", name)
			for _, v := range vals {
				if b, ok := v.([]byte); ok {
					dump.Write(b)
				} else {
					fmt.Fprint(&dump, v)
				}
				dump.WriteByte('|')
			}
			dump.WriteByte('\n')
		})
	}
	return dump.String()
}

// queryRows calls each with every row of query, scanned into generic values.
func queryRows(t *testing.T, st *store.Store, query string, each func(cols []string, vals []any)) {
	t.Helper()
	rows, err := st.Reader.QueryContext(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		each(cols, vals)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestIDM04_NoSecretsAnywhere(t *testing.T) {
	// Debug level logs the most, so it is the level a leak would show at.
	s := testutil.StartServer(t, testutil.WithMCPToken(), testutil.WithConfig(func(c *config.Server) { c.Log.Level = "debug" }))
	w := flow(t, s)
	s.Stop()

	w.check(t, "table dump", []byte(dumpTables(t, s.Paths.DB)))
	w.check(t, "server log", []byte(s.Log.String()))
	w.check(t, "CLI/MCP/editor output", w.output.Bytes())
	if !strings.Contains(s.Log.String(), "HTTP request") || w.output.Len() == 0 {
		t.Fatal("nothing was logged or printed; the scan would be vacuous")
	}
	for _, root := range []string{s.Paths.DataDir, s.Paths.StateDir, s.Paths.ConfigDir} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() || isTokenFile(s, path) {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			w.check(t, path, data)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// A token file holds exactly its own secret and nothing else.
	files := map[string]string{s.Paths.OwnerToken: w.tokens[0], s.Paths.TokenFile("ai"): w.tokens[1],
		s.Paths.TokenFile("mcp"): w.tokens[2], s.Paths.TokenFile("filetok"): w.tokens[3]}
	for path, own := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for i, other := range w.tokens {
			if other != own && bytes.Contains(data, []byte(other)) {
				t.Errorf("%s holds token secret %d", path, i+1)
			}
		}
		for i, pw := range w.passwords {
			if bytes.Contains(data, []byte(pw)) {
				t.Errorf("%s holds password %d", path, i+1)
			}
		}
	}
}

// requireModes walks the data, state and config trees: every regular file must
// be 0600 and every directory 0700. It returns the regular files it saw.
func requireModes(t *testing.T, s *testutil.Server) map[string]bool {
	t.Helper()
	seen := map[string]bool{}
	for _, root := range []string{s.Paths.DataDir, s.Paths.StateDir, s.Paths.ConfigDir} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			switch {
			case d.IsDir():
				if info.Mode().Perm() != 0o700 {
					t.Errorf("dir %s mode %v, want 0700", path, info.Mode().Perm())
				}
			case info.Mode().IsRegular():
				seen[path] = true
				if info.Mode().Perm() != 0o600 {
					t.Errorf("file %s mode %v, want 0600", path, info.Mode().Perm())
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return seen
}

func requireFiles(t *testing.T, seen map[string]bool, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if !seen[p] {
			t.Errorf("expected %s to exist (the mode check would be vacuous)", p)
		}
	}
}

func requireDirNotEmpty(t *testing.T, name, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Errorf("no %s files in %s (%v)", name, dir, err)
	}
}

func TestFileModes(t *testing.T) {
	s := testutil.StartServer(t, testutil.WithMCPToken())
	w := flow(t, s)
	// While running: the live database, its WAL and the instance lock.
	live := requireModes(t, s)
	requireFiles(t, live, s.Paths.DB, s.Paths.DB+"-wal", s.Paths.LockFile(), s.Paths.ClientConfig, s.Paths.ServerConfig,
		s.Paths.OwnerToken, s.Paths.TokenFile("ai"), s.Paths.TokenFile("mcp"), s.Paths.TokenFile("filetok"),
		filepath.Join(s.Paths.DataDir, "export.json"))
	// Purge deletes recovery files by design, so check these kinds now.
	requireDirNotEmpty(t, "backup", s.Paths.BackupDir)
	requireDirNotEmpty(t, "pending", s.Paths.PendingDir)
	requireDirNotEmpty(t, "draft", s.Paths.DraftsDir)
	s.Stop()

	cfg, err := config.LoadServer(s.Paths.ServerConfig)
	if err != nil {
		t.Fatal(err)
	}
	env := func() *offline.Env {
		return &offline.Env{Paths: s.Paths, Server: cfg, Clock: s.Clock, In: strings.NewReader(""), Out: new(bytes.Buffer), Err: new(bytes.Buffer),
			TTY: true, ReadPassword: func(string) (string, error) { return w.passwords[2], nil },
			Confirm: func(string, bool) (bool, error) { return true, nil }}
	}
	// Purge writes the purge ledger.
	if _, err := offline.Purge(t.Context(), env(), offline.PurgeOptions{Target: "task", ID: w.deleted, Yes: true}); err != nil {
		t.Fatalf("purge: %v", err)
	}
	// An interrupted restore leaves the swap journal and a pre-restore set.
	crash := errors.New("simulated crash")
	ctx := backup.WithStepHook(t.Context(), func(p string) error {
		if p == "moved-old" {
			return crash
		}
		return nil
	})
	if _, err := offline.Restore(ctx, env(), w.snapshot, true); !errors.Is(err, crash) {
		t.Fatalf("restore error = %v, want the simulated crash", err)
	}
	// Units, installed without touching systemd.
	unitEnv := &offline.Env{Paths: s.Paths, Clock: s.Clock, In: strings.NewReader(""), Out: new(bytes.Buffer), Err: new(bytes.Buffer)}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := offline.Init(t.Context(), unitEnv, offline.InitOptions{UnitsOnly: true, Binary: exe,
		Systemctl: func(context.Context, ...string) error { return nil }}); err != nil {
		t.Fatalf("install units: %v", err)
	}

	seen := requireModes(t, s)
	requireFiles(t, seen, s.Paths.LedgerFile, s.Paths.SwapJournal(), s.Paths.ClientConfig, s.Paths.ServerConfig, s.Paths.OwnerToken,
		filepath.Join(s.Paths.DataDir, "export.json"))
	units, err := os.ReadDir(s.Paths.UnitsDir())
	if err != nil {
		t.Fatal(err)
	}
	var unitCount int
	for _, u := range units {
		info, err := u.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().IsRegular() {
			unitCount++
			if info.Mode().Perm() != 0o644 {
				t.Errorf("unit %s mode %v, want 0644", u.Name(), info.Mode().Perm())
			}
		}
	}
	if unitCount != 2 {
		t.Errorf("found %d unit files, want taskronin.service and taskronin.socket", unitCount)
	}
}

func TestTokenSecretsNotLogged(t *testing.T) {
	s := testutil.StartServer(t, testutil.WithMCPToken(), testutil.WithConfig(func(c *config.Server) { c.Log.Level = "debug" }))
	ai := s.TokenClient(t, "ai", domain.MCPDefaultScopes, 1000, "mcp")
	mcpClient, err := client.New(client.Options{Socket: s.Socket, TokenFile: s.Paths.TokenFile("mcp"), Channel: "mcp"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := client.New(client.Options{Socket: s.Socket, TokenFile: s.Paths.OwnerToken, Channel: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	secrets := []string{readSecretPart(t, s.Paths.OwnerToken), readSecretPart(t, s.Paths.TokenFile("mcp")), readSecretPart(t, s.Paths.TokenFile("ai"))}
	for i := range 20 {
		for _, c := range []*client.Client{owner, mcpClient, ai} {
			if _, err := c.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: fmt.Sprintf("task %d", i)}); err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := c.Today(t.Context(), ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Password-bearing requests, the LogValuer redaction path.
	pw := randomPassword(t)
	if err := owner.ChangePassword(t.Context(), api.PasswordRequest{Current: testutil.Password, New: pw}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.UpdateSettings(t.Context(), settingsLowering(pw)); err != nil {
		t.Fatal(err)
	}
	secrets = append(secrets, testutil.Password, pw)
	// Rejection paths: a malformed and a wrong-secret bearer.
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", s.Socket)
	}}}
	wrong := strings.Repeat("A", 43)
	for _, auth := range []string{"Bearer tkr_1_" + wrong, "Bearer garbage"} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://taskronin/api/v1/whoami", nil)
		req.Header.Set("Authorization", auth)
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	secrets = append(secrets, wrong)
	s.Stop()
	logs := s.Log.String()
	if !strings.Contains(logs, "HTTP request") {
		t.Fatal("the access log recorded nothing; the check would be vacuous")
	}
	for i, secret := range secrets {
		if strings.Contains(logs, secret) {
			t.Errorf("server log contains secret %d", i)
		}
	}
}
