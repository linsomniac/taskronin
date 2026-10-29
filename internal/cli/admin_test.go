package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/clock"
	"taskronin/internal/domain"
	"taskronin/internal/testutil"
)

func TestSCP11_AIChangesAck(t *testing.T) {
	srv, sys := captureServer(t)
	ai := srv.TokenClient(t, "ai", domain.MCPDefaultScopes, 100, "mcp")
	if _, err := ai.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: "AI suggestion"}); err != nil {
		t.Fatal(err)
	}
	out := captureRun(t, sys, "", "ai-changes")
	row := regexp.MustCompile(`(?m)^op ([0-9a-f]{8}) #\d+ \S+ by token:ai at \S+ \S+$`)
	m := row.FindStringSubmatch(out)
	if m == nil || strings.Count(out, "op ") < 1 || len(strings.Split(strings.TrimSpace(out), "\n")) < 2 {
		t.Fatalf("ai-changes=%q", out)
	}
	if out := captureRun(t, sys, "", "status", "--short"); !strings.Contains(out, "ai 1") {
		t.Fatalf("status=%q", out)
	}
	captureRun(t, sys, "", "ai-changes", "--ack")
	if out := captureRun(t, sys, "", "status", "--short"); strings.Contains(out, "ai") {
		t.Fatalf("status after ack=%q", out)
	}
	if out := captureRun(t, sys, "", "ai-changes", "--ack"); !strings.Contains(out, "nothing to acknowledge") {
		t.Fatalf("second ack=%q", out)
	}
	if out := captureRun(t, sys, "", "ai-changes", "--revert", m[1]); !strings.Contains(out, "reverted") {
		t.Fatalf("revert=%q", out)
	}
	if out := captureRun(t, sys, "", "ai-changes", "--all"); !strings.Contains(out, "(reverted)") {
		t.Fatalf("--all after revert=%q", out)
	}
	if _, stderr, code := runCLI(t, sys, "", "ai-changes", "--revert", "zz"); code != 2 {
		t.Fatalf("bad op: %d %q", code, stderr)
	}
}

func TestTokenCreateWriteFile(t *testing.T) {
	srv, sys := captureServer(t)
	path := srv.Paths.TokenFile("bot")
	out := captureRun(t, sys, "", "token", "create", "bot", "--mcp", "--expires", "never", "--write-file")
	if !strings.Contains(out, path) || strings.Contains(out, "tkr_") {
		t.Fatalf("stdout=%q", out)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file: %v %v", info, err)
	}
	first, err := os.ReadFile(path)
	if err != nil || !strings.HasPrefix(string(first), "tkr_") {
		t.Fatalf("token file content: %q %v", first, err)
	}
	out, stderr, code := runCLI(t, sys, "", "token", "create", "bot", "--mcp", "--expires", "never", "--write-file")
	if code != 6 || strings.Contains(out+stderr, "tkr_") {
		t.Fatalf("second run: %d %q %q", code, out, stderr)
	}
	if again, _ := os.ReadFile(path); string(again) != string(first) {
		t.Fatal("token file changed")
	}
	tokens, err := srv.Client.Tokens(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range tokens {
		if tok.Name == "bot" && (tok.Scopes != "read,capture,write" || tok.MutationsPerHour != 60 || tok.ExpiresAt != nil) {
			t.Fatalf("bot token: %+v", tok)
		}
	}
}

func TestTokenCreatePrintsSecretOnce(t *testing.T) {
	srv, sys := captureServer(t)
	out := captureRun(t, sys, "", "token", "create", "ci", "--scopes", "read", "--expires", "30d", "--rate", "5")
	m := regexp.MustCompile(`(?m)^secret: (tkr_\S+)$`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("stdout=%q", out)
	}
	tokens, err := srv.Client.Tokens(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, tok := range tokens {
		if tok.Name == "ci" {
			found = true
			want := srv.Clock.Now().AddDate(0, 0, 30)
			if tok.MutationsPerHour != 5 || tok.Scopes != "read" || tok.ExpiresAt == nil || tok.ExpiresAt.Sub(want).Abs() > time.Minute {
				t.Fatalf("ci token: %+v want expiry %v", tok, want)
			}
		}
	}
	if !found {
		t.Fatal("token not created")
	}
	if list := captureRun(t, sys, "", "token", "list"); !strings.Contains(list, "ci") || strings.Contains(list, m[1]) {
		t.Fatalf("list=%q", list)
	}
	captureRun(t, sys, "", "token", "create", "dated", "--scopes", "read", "--expires", "2026-12-31")
	captureRun(t, sys, "", "token", "revoke", "ci", "-y")
	if _, stderr, code := runCLI(t, sys, "", "token", "revoke", "dated"); code != 2 || !strings.Contains(stderr, "-y") {
		t.Fatalf("revoke without tty or -y: %d %q", code, stderr)
	}
	if _, stderr, code := runCLI(t, sys, "", "token", "revoke", "nosuch", "-y"); code != 5 {
		t.Fatalf("revoke unknown: %d %q", code, stderr)
	}
}

func TestTokenCreateNeedsExpiry(t *testing.T) {
	srv, sys := captureServer(t)
	_, stderr, code := runCLI(t, sys, "", "token", "create", "bot", "--mcp")
	if code != 2 || !strings.Contains(stderr, "--expires") {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	for _, args := range [][]string{
		{"token", "create", "bot", "--expires", "never"},
		{"token", "create", "bot", "--mcp", "--scopes", "read", "--expires", "never"},
		{"token", "create", "bot", "--mcp", "--expires", "soon"},
		{"token", "create", "../evil", "--mcp", "--expires", "never", "--write-file"},
	} {
		if _, stderr, code := runCLI(t, sys, "", args...); code != 2 {
			t.Fatalf("%v: exit=%d stderr=%q", args, code, stderr)
		}
	}
	tokens, err := srv.Client.Tokens(t.Context())
	if err != nil || len(tokens) != 1 {
		t.Fatalf("a rejected command created a token: %v %v", tokens, err)
	}
	tty := sys
	tty.StdinTTY = true
	out, stderr, code := runCLI(t, tty, "never\n", "token", "create", "bot", "--mcp")
	if code != 0 || !strings.Contains(out, "secret: tkr_") || !strings.Contains(stderr, "Expires") {
		t.Fatalf("prompted: %d %q %q", code, out, stderr)
	}
}

func TestSettingsLowerNeedsTTY(t *testing.T) {
	_, sys := captureServer(t)
	if out := captureRun(t, sys, "", "settings"); !strings.Contains(out, "backup.keep_daily = 7\n") || !strings.Contains(out, "backup.keep_weekly = ") {
		t.Fatalf("settings=%q", out)
	}
	if out := captureRun(t, sys, "", "settings", "set", "backup.keep_daily", "9"); out != "backup.keep_daily = 9\n" {
		t.Fatalf("raise (no password needed)=%q", out)
	}
	out, stderr, code := runCLI(t, sys, "", "settings", "set", "backup.keep_daily", "5")
	if code != 4 || out != "" || !strings.Contains(stderr, "password required; run on a terminal") {
		t.Fatalf("non-TTY lower: %d %q %q", code, out, stderr)
	}
	tty := sys
	tty.StdinTTY = true
	tty.ReadPassword = func(string) (string, error) { return testutil.Password, nil }
	out, stderr, code = runCLI(t, tty, "", "settings", "set", "backup.keep_daily", "5")
	if code != 0 || !strings.Contains(out, "backup.keep_daily = 5") {
		t.Fatalf("TTY lower: %d %q %q", code, out, stderr)
	}
	if _, _, code := runCLI(t, sys, "", "settings", "set", "nope", "1"); code != 2 {
		t.Fatalf("unknown key: %d", code)
	}
	if _, _, code := runCLI(t, sys, "", "settings", "set", "backup.keep_daily", "many"); code != 2 {
		t.Fatalf("bad value: %d", code)
	}
}

func TestBackupOnlineAndOffline(t *testing.T) {
	srv, sys := captureServer(t)
	online := captureRun(t, sys, "", "backup")
	if !strings.Contains(online, srv.Paths.BackupDir) {
		t.Fatalf("online backup=%q", online)
	}
	srv.Stop()
	out, stderr, code := runCLI(t, sys, "", "backup")
	if code != 0 || !strings.Contains(out, "server not running; took an offline snapshot") {
		t.Fatalf("offline backup: %d %q %q", code, out, stderr)
	}
	var path string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, srv.Paths.BackupDir) {
			path = line
		}
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("offline snapshot %q: %v %v", path, info, err)
	}
	list := captureRun(t, sys, "", "backup", "--list")
	if strings.Count(list, srv.Paths.BackupDir) < 2 {
		t.Fatalf("offline list=%q", list)
	}
	srv.Start(t)
	if list := captureRun(t, sys, "", "backup", "--list"); strings.Count(list, srv.Paths.BackupDir) < 2 {
		t.Fatalf("online list=%q", list)
	}
}

func TestExportFile(t *testing.T) {
	_, sys := captureServer(t)
	dir := privateDir(t)
	file := filepath.Join(dir, "x.json")
	captureRun(t, sys, "", "add", "exported task")
	out := captureRun(t, sys, "", "export", "-o", file)
	if !strings.Contains(out, file) {
		t.Fatalf("stdout=%q", out)
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("export file: %v %v", info, err)
	}
	data, _ := os.ReadFile(file)
	var doc struct{ Format string }
	if err := json.Unmarshal(data, &doc); err != nil || doc.Format != "taskronin-export" {
		t.Fatalf("export content: %v %.200s", err, data)
	}
	ro := filepath.Join(dir, "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	if _, stderr, code := runCLI(t, sys, "", "export", "-o", filepath.Join(ro, "x.json")); code != 1 {
		t.Fatalf("read-only dir: exit=%d stderr=%q", code, stderr)
	}
	if left, _ := os.ReadDir(ro); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
	// The default name is dated by the server's today (2026-10-28).
	def := "taskronin-export-20261028.json"
	t.Chdir(dir)
	captureRun(t, sys, "", "export")
	if _, err := os.Stat(filepath.Join(dir, def)); err != nil {
		t.Fatal(err)
	}
}

func TestIDM09_PendingFlow(t *testing.T) {
	sys, paths := testSys(t)
	var mode atomic.Int32 // 0 drop, 1 replayed 201, 2 conflict
	opID := strings.Repeat("ab", 16)
	socket, token := fakeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		switch {
		case r.URL.Path == "/api/v1/tasks" && mode.Load() == 0:
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
		case r.URL.Path == "/api/v1/tasks" && mode.Load() == 1:
			w.WriteHeader(http.StatusCreated)
			writeJSON(t, w, api.OpResponse{OpID: &opID, Replayed: true, Task: &api.Task{ID: 1, Title: "x"}})
		default:
			w.Header().Set("Content-Type", api.ContentTypeProblem)
			w.WriteHeader(http.StatusConflict)
			writeJSON(t, w, api.Problem{Status: 409, Code: "conflict", Detail: "state changed"})
		}
	}))
	flags := []string{"--socket", socket, "--token-file", token}
	run := func(args ...string) (string, string, int) {
		return runCLI(t, sys, "", append(args, flags...)...)
	}
	_, stderr, code := run("add", "x")
	key := regexp.MustCompile(`saved as pending request (\S+);`).FindStringSubmatch(stderr)
	if code != 3 || key == nil || !strings.Contains(stderr, "outcome unknown — saved as pending request "+key[1]+"; run `tkr pending retry`") {
		t.Fatalf("add: %d %q", code, stderr)
	}
	file := filepath.Join(paths.PendingDir, key[1]+".json")
	if out, _, code := run("pending"); code != 0 || !strings.Contains(out, key[1]) || !strings.Contains(out, "POST") || !strings.Contains(out, "/tasks") {
		t.Fatalf("pending list: %d %q", code, out)
	}

	// An expired request is refused without being resent or deleted.
	var saved map[string]any
	data, _ := os.ReadFile(file)
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	saved["created_at"] = (clock.Real{}).Now().Add(-8 * 24 * time.Hour).Format(time.RFC3339)
	aged, _ := json.Marshal(saved)
	if err := os.WriteFile(file, aged, 0o600); err != nil {
		t.Fatal(err)
	}
	mode.Store(1)
	_, stderr, code = run("pending", "retry")
	if code != 1 || !strings.Contains(stderr, "tkr pending discard "+key[1]) {
		t.Fatalf("expired retry: %d %q", code, stderr)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("expired file removed")
	}
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}

	mode.Store(2)
	_, stderr, code = run("pending", "retry", key[1])
	if code != 6 || !strings.Contains(stderr, "state changed") {
		t.Fatalf("409 retry: %d %q", code, stderr)
	}
	if out, _, _ := run("pending"); !strings.Contains(out, "state changed") {
		t.Fatalf("last error not listed: %q", out)
	}

	mode.Store(1)
	out, stderr, code := run("pending", "retry")
	if code != 0 || !strings.Contains(out, "op abababab") {
		t.Fatalf("retry: %d %q %q", code, out, stderr)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("pending file still present: %v", err)
	}

	mode.Store(0)
	_, stderr, _ = run("add", "y")
	key = regexp.MustCompile(`saved as pending request (\S+);`).FindStringSubmatch(stderr)
	if key == nil {
		t.Fatal(stderr)
	}
	if out, _, code := run("pending", "discard", key[1]); code != 0 || !strings.Contains(out, key[1]) {
		t.Fatalf("discard: %d %q", code, out)
	}
	if _, _, code := run("pending", "discard", key[1]); code != 5 {
		t.Fatalf("discard twice: %d", code)
	}
	if out, _, _ := run("pending"); strings.TrimSpace(out) != "no pending requests" {
		t.Fatalf("empty list=%q", out)
	}
}
