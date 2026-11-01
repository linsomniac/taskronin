package server_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"taskronin/internal/api"
	"taskronin/internal/cli"
	"taskronin/internal/client"
	"taskronin/internal/config"
	"taskronin/internal/domain"
	"taskronin/internal/editor"
	"taskronin/internal/store"
	"taskronin/internal/testutil"
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
// connection after a POST has been processed: the server commits, the client
// never sees the response (a pending, outcome-unknown save).
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
		if r.Method != http.MethodPost {
			rp.ServeHTTP(w, r)
			return
		}
		rp.ServeHTTP(httptest.NewRecorder(), r)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return path
}

// savePending makes a real AddTask whose response is lost and returns the saved key.
func savePending(t *testing.T, s *testutil.Server) {
	t.Helper()
	c, err := client.New(client.Options{Socket: dropProxy(t, s.Socket), TokenFile: s.Paths.OwnerToken, Channel: "cli",
		Pending: &client.PendingStore{Dir: s.Paths.PendingDir, Clock: s.Clock}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: "lost response"})
	var unknown *client.OutcomeUnknownError
	if !errors.As(err, &unknown) || !unknown.Saved {
		t.Fatalf("AddTask error = %v, want a saved outcome-unknown error", err)
	}
}

func runCLI(t *testing.T, args ...string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := cli.Main(t.Context(), args, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("tkr %v exit %d: %s", args, code, errOut.String())
	}
	return out.String()
}

// flow exercises the paths that handle credentials or write files.
func flow(t *testing.T, s *testutil.Server, newPassword string) []string {
	t.Helper()
	if err := s.Client.ChangePassword(t.Context(), api.PasswordRequest{Current: testutil.Password, New: newPassword}); err != nil {
		t.Fatalf("change password: %v", err)
	}
	keep := 3
	if _, err := s.Client.UpdateSettings(t.Context(), api.SettingsRequest{BackupKeepDaily: &keep, Password: newPassword}); err != nil {
		t.Fatalf("update settings: %v", err)
	}
	ai := s.TokenClient(t, "ai", domain.MCPDefaultScopes, 100, "mcp")
	if _, err := ai.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: "from the ai token"}); err != nil {
		t.Fatalf("ai add: %v", err)
	}
	savePending(t, s)
	runCLI(t, "backup")
	runCLI(t, "export", "-o", filepath.Join(s.Paths.DataDir, "export.json"))
	if _, err := (editor.Drafts{Dir: s.Paths.DraftsDir, Clock: s.Clock}).New("draft body"); err != nil {
		t.Fatal(err)
	}
	return []string{readSecretPart(t, s.Paths.OwnerToken), readSecretPart(t, s.Paths.TokenFile("ai")), readSecretPart(t, s.Paths.TokenFile("mcp"))}
}

func isTokenFile(s *testutil.Server, path string) bool {
	return path == s.Paths.OwnerToken || filepath.Dir(path) == s.Paths.TokensDir && strings.HasSuffix(path, ".token")
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
	s := testutil.StartServer(t, testutil.WithMCPToken())
	newPassword := randomPassword(t)
	tokens := flow(t, s, newPassword)
	s.Stop()

	secrets := map[string]string{"initial password": testutil.Password, "changed password": newPassword}
	for i, tok := range tokens {
		secrets[fmt.Sprintf("token secret %d", i)] = tok
	}
	check := func(where string, data []byte) {
		for name, secret := range secrets {
			if bytes.Contains(data, []byte(secret)) {
				t.Errorf("%s contains the %s", where, name)
			}
		}
	}
	check("table dump", []byte(dumpTables(t, s.Paths.DB)))
	check("server log", []byte(s.Log.String()))
	for _, root := range []string{s.Paths.DataDir, s.Paths.StateDir, s.Paths.ConfigDir} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() || isTokenFile(s, path) {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			check(path, data)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestFileModes(t *testing.T) {
	s := testutil.StartServer(t, testutil.WithMCPToken())
	flow(t, s, randomPassword(t))
	s.Stop()
	var regular int
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
				regular++
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
	// The flow must actually have produced each kind of file under test.
	for name, dir := range map[string]string{"backup": s.Paths.BackupDir, "pending": s.Paths.PendingDir, "draft": s.Paths.DraftsDir} {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) == 0 {
			t.Errorf("no %s files in %s (%v)", name, dir, err)
		}
	}
	for _, path := range []string{s.Paths.ServerConfig, filepath.Join(s.Paths.DataDir, "export.json")} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected file: %v", err)
		}
	}
	if regular == 0 {
		t.Fatal("walked no files")
	}
	units, err := os.ReadDir(s.Paths.UnitsDir())
	if err == nil {
		for _, u := range units {
			if info, err := u.Info(); err == nil && info.Mode().IsRegular() && info.Mode().Perm() != 0o644 {
				t.Errorf("unit %s mode %v, want 0644", u.Name(), info.Mode().Perm())
			}
		}
	}
}

func TestTokenSecretsNotLogged(t *testing.T) {
	s := testutil.StartServer(t, testutil.WithMCPToken(), testutil.WithConfig(func(c *config.Server) { c.Log.Level = "debug" }))
	secrets := []string{readSecretPart(t, s.Paths.OwnerToken), readSecretPart(t, s.Paths.TokenFile("mcp"))}
	bad := client.Options{Socket: s.Socket, TokenFile: s.Paths.OwnerToken, Channel: "cli"}
	c, err := client.New(bad)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		if _, err := c.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: fmt.Sprintf("task %d", i)}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Tokens(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	// Also drive the rejection paths: a malformed and a wrong-secret bearer.
	for _, auth := range []string{"Bearer tkr_1_" + strings.Repeat("A", 43), "Bearer garbage"} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://taskronin/api/v1/whoami", nil)
		req.Header.Set("Authorization", auth)
		hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", s.Socket)
		}}}
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		secrets = append(secrets, strings.Repeat("A", 43))
	}
	s.Stop()
	logs := s.Log.String()
	if !strings.Contains(logs, "HTTP request") {
		t.Fatal("the access log recorded nothing; the check would be vacuous")
	}
	for _, secret := range secrets {
		if strings.Contains(logs, secret) {
			t.Error("server log contains a token secret")
		}
	}
}
