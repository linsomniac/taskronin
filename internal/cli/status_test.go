package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/client"
	"taskronin/internal/clock"
	"taskronin/internal/config"
	"taskronin/internal/domain"
	"taskronin/internal/testutil"
)

func seedDashboard(t *testing.T) sysDeps {
	t.Helper()
	srv := testutil.StartServer(t, testutil.WithClock("2026-10-26 08:00"))
	sys := sysDeps{Getenv: os.Getenv, StdinTTY: true}
	captureRun(t, sys, "", "add", "Pay invoice", "--due", "2026-10-27")
	srv.Clock.Set(testutil.Local(t, "2026-10-28 08:30"))
	captureRun(t, sys, "", "add", "Set up keycloak postgres cluster", "--block", "09:00-11:00")
	captureRun(t, sys, "", "add", "Write report")
	srv.Clock.Set(testutil.Local(t, "2026-10-28 10:20"))
	captureRun(t, sys, "", "add", "Restart stuck pg replica", "--now")
	captureRun(t, sys, "", "log", "Restarted replica, root cause disk full")
	ai := srv.TokenClient(t, "ai", domain.MCPDefaultScopes, 100, "mcp")
	if _, err := ai.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: "AI suggestion"}); err != nil {
		t.Fatal(err)
	}
	statusBackup(t, srv)
	return sys
}

func TestDashboardGolden(t *testing.T) {
	sys := seedDashboard(t)
	out := captureRun(t, sys, "")
	for _, want := range []string{
		"Wed 2026-10-28 10:20 · America/Denver\n",
		"10:20 #4 'Restart stuck pg replica' interrupted #2 (block 09:00–11:00)",
		"Overdue\n  #1  [overdue,due,new] Pay invoice  overdue",
		"AI changes: 1 unreviewed — run `tkr ai-changes`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in dashboard:\n%s", want, out)
		}
	}
	testutil.Golden(t, "dashboard", []byte(out))
	if today := captureRun(t, sys, "", "today"); today != out {
		t.Fatalf("today differs from bare tkr:\n%s", today)
	}
	var view api.Today
	if err := json.Unmarshal([]byte(captureRun(t, sys, "", "today", "--json")), &view); err != nil || view.AIChanges != 1 {
		t.Fatalf("today JSON: %+v, %v", view, err)
	}
}

func TestINT08_DashboardInterruptedTaskOutsideTopFive(t *testing.T) {
	srv, sys := captureServer(t)
	captureRun(t, sys, "", "add", "Original focus", "--now")
	captureRun(t, sys, "", "add", "Interrupted work", "--now")
	// Move the destination out of every task list embedded in Today.
	captureRun(t, sys, "", "now", "--clear")
	for range 6 {
		captureTask(t, srv, "New triage task")
	}
	out := captureRun(t, sys, "", "today")
	if !strings.Contains(out, "10:00 #2 'Interrupted work' interrupted #1\n") || strings.Contains(out, "(block ") {
		t.Fatalf("interruption without block: %s", out)
	}
}

func TestBLK48_StatusShortCurrentBlock(t *testing.T) {
	srv, sys := captureServer(t)
	captureRun(t, sys, "", "add", "Set up keycloak postgres cluster", "--block", "13:00-15:00")
	captureRun(t, sys, "", "now", "1")
	statusBackup(t, srv)
	for _, tc := range []struct{ at, want string }{
		{"12:59", "▶ #1 Set up keycloak postgres…\n"},
		{"13:00", "▶ #1 Set up keycloak postgres… (→15:00)\n"},
		{"14:30", "▶ #1 Set up keycloak postgres… (→15:00)\n"},
		{"15:00", "▶ #1 Set up keycloak postgres…\n"},
	} {
		t.Run(tc.at, func(t *testing.T) {
			srv.Clock.Set(testutil.Local(t, "2026-10-28 "+tc.at))
			if out := captureRun(t, sys, "", "status", "--short"); out != tc.want {
				t.Fatalf("stdout=%q; want %q", out, tc.want)
			}
		})
	}
	srv.Clock.Set(testutil.Local(t, "2026-10-28 14:30"))
	captureRun(t, sys, "", "now", "--clear")
	if out := captureRun(t, sys, "", "status", "--short"); out != "▷ #1 Set up keycloak postgres… (→15:00)\n" {
		t.Fatalf("without focus: %q", out)
	}
}

func TestStatusShortSegments(t *testing.T) {
	srv := testutil.StartServer(t, testutil.WithClock("2026-10-26 08:00"))
	sys := sysDeps{Getenv: os.Getenv}
	captureRun(t, sys, "", "add", "Overdue task", "--due", "2026-10-27")
	srv.Clock.Set(testutil.Local(t, "2026-10-28 10:20"))
	captureRun(t, sys, "", "add", "Focus", "--now")
	captureRun(t, sys, "", "add", "Triage task")
	ai := srv.TokenClient(t, "ai", domain.MCPDefaultScopes, 100, "mcp")
	if _, err := ai.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: "AI suggestion"}); err != nil {
		t.Fatal(err)
	}
	statusBackup(t, srv)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"status", "--short"}, "▶ #2 Focus · triage 3 · due 1 · ai 1\n"},
		{[]string{"status", "--short", "--ascii"}, "> #2 Focus | triage 3 | due 1 | ai 1\n"},
	} {
		if out := captureRun(t, sys, "", tc.args...); out != tc.want {
			t.Errorf("stdout=%q; want %q", out, tc.want)
		}
	}
}

func TestREC05_StatusShortAfterComplete(t *testing.T) {
	srv, sys := captureServer(t)
	captureRun(t, sys, "", "add", "Focus", "--now")
	captureRun(t, sys, "", "done", "1")
	statusBackup(t, srv)
	if out := captureRun(t, sys, "", "status", "--short"); out != "" {
		t.Fatalf("completed focus still shown: %q", out)
	}
}

func statusBackup(t *testing.T, srv *testutil.Server) {
	t.Helper()
	// Startup backup runs asynchronously. Establish healthy backup state through
	// a real operation before asserting exact status text or a dashboard golden.
	if _, err := srv.Client.RunBackup(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type unreadableStatusInput struct{ t *testing.T }

func (r unreadableStatusInput) Read([]byte) (int, error) {
	r.t.Error("status --short read stdin")
	return 0, fmt.Errorf("stdin must not be read")
}

func assertSilentStatus(ctx context.Context, t *testing.T, sys sysDeps, args ...string) time.Duration {
	t.Helper()
	var out, stderr bytes.Buffer
	start := (clock.Real{}).Now()
	code := run(ctx, args, unreadableStatusInput{t}, &out, &stderr, sys)
	elapsed := (clock.Real{}).Now().Sub(start)
	if elapsed >= time.Second || code != 0 || out.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("elapsed=%v exit=%d stdout=%q stderr=%q", elapsed, code, out.String(), stderr.String())
	}
	return elapsed
}

func TestStatusShortHungServer(t *testing.T) {
	for _, name := range []string{"hung", "stale socket", "missing token", "unsafe token", "empty token", "symlink token", "invalid config", "invalid paths", "canceled", "bad color"} {
		t.Run(name, func(t *testing.T) {
			sys, paths := testSys(t)
			var calls atomic.Int32
			socket, token := fakeAPI(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				<-r.Context().Done()
			}))
			ctx := t.Context()
			args := []string{"status", "--short", "--socket", socket, "--token-file", token}
			switch name {
			case "stale socket":
				socket = filepath.Join(filepath.Dir(socket), "stale")
				if err := os.WriteFile(socket, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				args[3] = socket
			case "missing token":
				if err := os.Remove(token); err != nil {
					t.Fatal(err)
				}
			case "unsafe token":
				if err := os.Chmod(token, 0o644); err != nil {
					t.Fatal(err)
				}
			case "empty token":
				if err := os.WriteFile(token, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink token":
				link := token + "-link"
				if err := os.Symlink(token, link); err != nil {
					t.Fatal(err)
				}
				args[5] = link
			case "invalid config":
				if err := os.MkdirAll(filepath.Dir(paths.ClientConfig), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(paths.ClientConfig, []byte("invalid = ["), 0o600); err != nil {
					t.Fatal(err)
				}
			case "invalid paths":
				sys.Getenv = func(string) string { return "relative" }
				sys.Home = ""
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "bad color":
				args = append(args, "--color", "invalid", "--json")
			}
			elapsed := assertSilentStatus(ctx, t, sys, args...)
			if name == "hung" && (calls.Load() != 1 || elapsed > 600*time.Millisecond) {
				t.Fatalf("calls=%d elapsed=%v; expected one request within default budget + slack", calls.Load(), elapsed)
			}
		})
	}
}

func TestStatusShortConfiguredTimeout(t *testing.T) {
	sys, paths := testSys(t)
	if err := os.MkdirAll(filepath.Dir(paths.ClientConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	socket, token := fakeAPI(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	cfg := config.DefaultClient(paths)
	cfg.Socket, cfg.TokenFile, cfg.StatusTimeout = socket, token, 40*time.Millisecond
	if err := config.WriteClient(paths.ClientConfig, cfg); err != nil {
		t.Fatal(err)
	}
	if elapsed := assertSilentStatus(t.Context(), t, sys, "status", "--short"); elapsed > 200*time.Millisecond {
		t.Fatalf("configured deadline ignored: %v", elapsed)
	}
}

func TestStatusShortParentDeadline(t *testing.T) {
	sys, paths := testSys(t)
	if err := os.MkdirAll(filepath.Dir(paths.ClientConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	socket, token := fakeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, api.Status{Triage: 1})
	}))
	cfg := config.DefaultClient(paths)
	cfg.Socket, cfg.TokenFile, cfg.StatusTimeout = socket, token, 100*time.Millisecond
	if err := config.WriteClient(paths.ClientConfig, cfg); err != nil {
		t.Fatal(err)
	}
	// Both deadlines are in the future. The earlier parent must not shift the
	// start used to calculate the configured status budget into the past.
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	var out, stderr bytes.Buffer
	code := run(ctx, []string{"status", "--short"}, unreadableStatusInput{t}, &out, &stderr, sys)
	if code != 0 || out.String() != "triage 1\n" || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), stderr.String())
	}
}

func TestStatusShortBlockedConfig(t *testing.T) {
	sys, paths := testSys(t)
	if err := os.MkdirAll(filepath.Dir(paths.ClientConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(paths.ClientConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	// A FIFO makes setup block independently of HTTP; release it after checking
	// the prompt deadline, including on failure, so no reader is left behind.
	done := make(chan struct{})
	var out, stderr bytes.Buffer
	var code int
	go func() {
		defer close(done)
		code = run(t.Context(), []string{"status", "--short"}, unreadableStatusInput{t}, &out, &stderr, sys)
	}()
	select {
	case <-done:
	case <-time.After(600 * time.Millisecond):
		t.Error("configuration loading exceeded default status deadline + slack")
	}
	f, err := os.OpenFile(paths.ClientConfig, os.O_WRONLY|unix.O_NONBLOCK, 0)
	if err == nil {
		_, _ = f.WriteString("invalid = [")
		_ = f.Close()
	} else if !errors.Is(err, unix.ENXIO) {
		t.Error(err)
	}
	<-done
	if code != 0 || out.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), stderr.String())
	}
}

func TestStatusShortSetupConsumesBudget(t *testing.T) {
	sys, paths := testSys(t)
	if err := os.MkdirAll(filepath.Dir(paths.ClientConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	socket, token := fakeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeJSON(t, w, api.Status{Triage: 1})
	}))
	cfg := config.DefaultClient(paths)
	cfg.Socket, cfg.TokenFile, cfg.StatusTimeout = socket, token, 40*time.Millisecond
	if err := config.WriteClient(paths.ClientConfig, cfg); err != nil {
		t.Fatal(err)
	}
	getenv := sys.Getenv
	sys.Getenv = func(key string) string {
		if key == "XDG_CONFIG_HOME" {
			<-(clock.Real{}).After(80 * time.Millisecond)
		}
		return getenv(key)
	}
	assertSilentStatus(t.Context(), t, sys, "status", "--short")
	if calls.Load() != 0 {
		t.Fatalf("made %d requests after setup consumed the deadline", calls.Load())
	}
}

func TestStatusShortUsageErrors(t *testing.T) {
	for _, flags := range [][]string{
		{"--unknown", "--short"}, {"--short", "--unknown"},
		{"--short", "extra"}, {"--short", "--token-file"},
		{"--json=broken", "--short"}, {"--ascii=broken", "--short"},
		{"--short=broken"},
	} {
		sys, _ := testSys(t)
		assertSilentStatus(t.Context(), t, sys, append([]string{"status"}, flags...)...)
	}
}

func TestStatusShortOneRequest(t *testing.T) {
	for _, name := range []string{"busy", "truncated", "unauthorized", "malformed"} {
		t.Run(name, func(t *testing.T) {
			sys, _ := testSys(t)
			var calls atomic.Int32
			socket, token := fakeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/status" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				switch name {
				case "busy":
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(http.StatusServiceUnavailable)
					writeJSON(t, w, api.Problem{Code: "busy", RetryAfter: 0})
				case "truncated":
					w.Header().Set("Content-Length", "1000")
					_, _ = w.Write([]byte("{"))
				case "unauthorized":
					w.WriteHeader(http.StatusUnauthorized)
					writeJSON(t, w, api.Problem{Code: "unauthenticated"})
				case "malformed":
					_, _ = w.Write([]byte("invalid"))
				}
			}))
			assertSilentStatus(t.Context(), t, sys, "--socket", socket, "status", "--short", "--token-file", token, "--json")
			if calls.Load() != 1 {
				t.Fatalf("made %d requests", calls.Load())
			}
		})
	}
}

func TestStatusShortFormatting(t *testing.T) {
	for _, tc := range []struct {
		name  string
		view  api.Status
		ascii bool
		want  string
	}{
		{"empty", api.Status{}, false, ""},
		{"warnings", api.Status{BackupWarn: true, PurgeWarning: true}, false, "⚠ backup · ⚠ purge\n"},
		{"ascii warnings", api.Status{BackupWarn: true, PurgeWarning: true}, true, "!backup | !purge\n"},
		{"ascii block", api.Status{CurrentBlock: &api.StatusBlock{TaskID: 2, Title: "Block", End: 900}}, true, ">. #2 Block (→15:00)\n"},
		{"other block", api.Status{Focus: &api.StatusTask{ID: 1, Title: "Focus"}, CurrentBlock: &api.StatusBlock{TaskID: 2, Title: "Block", End: 900}}, false, "▶ #1 Focus\n"},
		{"24 columns", api.Status{Focus: &api.StatusTask{ID: 1, Title: strings.Repeat("x", 24)}}, false, "▶ #1 " + strings.Repeat("x", 24) + "\n"},
		{"wide title", api.Status{Focus: &api.StatusTask{ID: 1, Title: strings.Repeat("界", 13)}}, false, "▶ #1 " + strings.Repeat("界", 12) + "…\n"},
		{"unsafe title", api.Status{Focus: &api.StatusTask{ID: 1, Title: "title\n\x1b"}}, false, "▶ #1 title��\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sys, paths := testSys(t)
			if err := os.MkdirAll(filepath.Dir(paths.ClientConfig), 0o700); err != nil {
				t.Fatal(err)
			}
			sys.StdoutTTY = true
			socket, token := fakeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, tc.view) }))
			cfg := config.DefaultClient(paths)
			cfg.Socket, cfg.TokenFile, cfg.Color = socket, token, "always"
			if err := config.WriteClient(paths.ClientConfig, cfg); err != nil {
				t.Fatal(err)
			}
			args := []string{"status", "--short"}
			if tc.ascii {
				args = append(args, "--ascii")
			}
			if out := captureRun(t, sys, "", args...); out != tc.want {
				t.Fatalf("stdout=%q; want %q", out, tc.want)
			}
		})
	}
}

func TestStatusLong(t *testing.T) {
	srv, sys := captureServer(t)
	captureRun(t, sys, "", "add", "Focus", "--now")
	statusBackup(t, srv)
	want := "focus: #1 Focus\ncurrent block: none\ntriage: 0\ndue: 0\nai: 0\nbackup: ok\npurge: ok\n"
	if out := captureRun(t, sys, "", "status"); out != want {
		t.Fatalf("stdout=%q; want %q", out, want)
	}
	p := paint{on: true}
	colored := captureRun(t, sys, "", "status", "--color", "always")
	if !strings.Contains(colored, p.taskID(1)+" "+p.ok("Focus")) || colorSGR.ReplaceAllString(colored, "") != want {
		t.Errorf("long status with color = %q", colored)
	}
	var status api.Status
	if err := json.Unmarshal([]byte(captureRun(t, sys, "", "status", "--json", "--color", "always")), &status); err != nil || status.Focus == nil || status.Focus.ID != 1 {
		t.Fatalf("status JSON: %+v, %v", status, err)
	}
	srv.Stop()
	out, stderr, code := runCLI(t, sys, "", "status")
	if code != 3 || out != "" || !strings.Contains(stderr, "cannot reach the Taskronin server") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
}

func TestDashboardSections(t *testing.T) {
	sys, _ := testSys(t)
	view := api.Today{Clock: api.Clock{Now: api.Time{Time: testutil.Local(t, "2026-10-28 10:20")}, Today: civil.MustDate("2026-10-28"), Timezone: "America/Denver"}}
	var response atomic.Pointer[api.Today]
	response.Store(new(view))
	socket, token := fakeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, response.Load()) }))
	args := []string{"today", "--socket", socket, "--token-file", token}
	captureRun(t, sys, "today_empty", args...)
	const clockLine = "Wed 2026-10-28 10:20 · America/Denver\n"
	dueSoon := []api.Task{{ID: 4, Title: "Soon", Due: &api.Moment{Date: civil.MustDate("2026-10-29")}}}
	view.DueSoon = dueSoon
	response.Store(new(view))
	want := clockLine + "no focus\n\nDue soon\n  #4  Soon  due Thu\n\n"
	if out := captureRun(t, sys, "", args...); out != want {
		t.Errorf("dashboard with only a due task = %q, want %q", out, want)
	}
	view.DueSoon = nil
	view.Banners = []api.Banner{{Kind: "backup_failed", Message: "Backup failed\nnotice\x1b"}}
	view.AIChanges = 1
	response.Store(new(view))
	want = clockLine + "⚠ Backup failed�notice�\nno focus\n\nNothing scheduled, nothing to triage — tkr plan shapes the day\n\nAI changes: 1 unreviewed — run `tkr ai-changes`\n"
	if out := captureRun(t, sys, "", args...); out != want {
		t.Errorf("empty dashboard with notices = %q, want %q", out, want)
	}
	view.CurrentBlock = &api.Block{ID: 1, TaskID: 1, TaskTitle: "Current", Date: view.Clock.Today, End: 660}
	view.Blocks = []api.Block{
		{ID: 1, TaskID: 1, TaskTitle: "Current", Start: 540, End: 660, Current: true, Status: "planned"},
		{ID: 2, TaskID: 2, TaskTitle: "Ended", Start: 480, End: 510, NeedsReblock: true, Status: "planned"},
		{ID: 3, TaskID: 3, TaskTitle: "Finished", Start: 420, End: 450, Status: "done"},
	}
	view.DueSoon = dueSoon
	view.Entries = []api.Entry{{Title: "Complete\nentry", Kind: "complete", TaskID: new(int64(3))}, {Title: "Standalone", Kind: "work"}}
	for i := range view.Blocks {
		view.Blocks[i].Date = view.Clock.Today
	}
	for i := range view.Entries {
		view.Entries[i].WorkDate = view.Clock.Today
	}
	response.Store(new(view))
	out := captureRun(t, sys, "", args...)
	for _, line := range []string{"⚠ Backup failed�notice�", "▷ #1 Current (→11:00)", "  b1 09:00–11:00  #1 Current  ◀ now", "  b2 08:00–08:30  #2 Ended  reblock?", "  b3 07:00–07:30  #3 Finished  done", "Due soon\n  #4  Soon  due Thu", "  ✓ Complete�entry  [#3]", "  - Standalone"} {
		if !strings.Contains(out, line+"\n") {
			t.Errorf("missing %q in:\n%s", line, out)
		}
	}
}
