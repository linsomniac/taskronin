package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"taskronin/internal/api"
	"taskronin/internal/auth"
	"taskronin/internal/client"
	"taskronin/internal/config"
	"taskronin/internal/datespec"
	"taskronin/internal/fsutil"
)

func runCLI(t *testing.T, sys sysDeps, stdin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, err bytes.Buffer
	code = run(t.Context(), args, strings.NewReader(stdin), &out, &err, sys)
	return out.String(), err.String(), code
}

func privateDir(t *testing.T) string {
	t.Helper()
	// Config rejects world-writable ancestors, including /tmp.
	dir, err := os.MkdirTemp(".", ".t-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	return dir
}

func testSys(t *testing.T) (sysDeps, config.Paths) {
	t.Helper()
	root := privateDir(t)
	s := sysDeps{Home: root, Getenv: func(key string) string {
		switch key {
		case "XDG_CONFIG_HOME":
			return filepath.Join(root, "c")
		case "XDG_DATA_HOME":
			return filepath.Join(root, "d")
		case "XDG_STATE_HOME":
			return filepath.Join(root, "s")
		case "XDG_RUNTIME_DIR":
			return filepath.Join(root, "r")
		default:
			return ""
		}
	}}
	p, err := config.DefaultPaths(s.Getenv, s.Home)
	if err != nil {
		t.Fatal(err)
	}
	old := fsutil.SetUmask(0o077)
	t.Cleanup(func() { fsutil.SetUmask(old) })
	return s, p
}

func fakeAPI(t *testing.T, h http.Handler) (socket, tokenFile string) {
	t.Helper()
	dir := privateDir(t)
	socket, tokenFile = filepath.Join(dir, "sock"), filepath.Join(dir, "token")
	secret, err := auth.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenFile, []byte(auth.FormatToken(1, secret)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Taskronin-Channel") != "cli" {
			t.Error("missing cli channel")
		}
		if r.URL.Path == "/api/v1/whoami" {
			writeJSON(t, w, api.WhoAmI{Actor: "owner", Generation: "test-generation"})
			return
		}
		h.ServeHTTP(w, r)
	})}
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close(); <-done })
	return socket, tokenFile
}

func writeJSON(t *testing.T, w io.Writer, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Error(err)
	}
}

func init() {
	register(func(e *env) *cobra.Command {
		return &cobra.Command{Use: "__test-clock", Hidden: true, RunE: func(cmd *cobra.Command, _ []string) error {
			if envFrom(cmd) != e {
				return usagef("wrong command environment")
			}
			_, err := e.clock()
			return err
		}}
	})
	register(func(e *env) *cobra.Command {
		cmd := &cobra.Command{Use: "__test-mutate [add|settings]", Hidden: true, Annotations: map[string]string{"mutates": "true"}}
		cmd.RunE = func(_ *cobra.Command, args []string) error {
			c, err := e.client()
			if err != nil {
				return err
			}
			if len(args) > 0 && args[0] == "settings" {
				_, err = c.UpdateSettings(e.ctx, api.SettingsRequest{})
			} else {
				_, err = c.AddTask(e.ctx, client.Mut{}, api.AddTaskRequest{Title: "test"})
			}
			return err
		}
		cmd.Flags().StringVar(&e.draftPath, "draft", "", "test draft")
		return cmd
	})
	register(func(e *env) *cobra.Command {
		return &cobra.Command{Use: "__test-ref", Hidden: true, RunE: func(_ *cobra.Command, _ []string) error {
			for _, mode := range []datespec.Mode{datespec.Past, datespec.Future} {
				ref, err := e.ref(mode)
				if err != nil {
					return err
				}
				if ref.Today.String() != "2026-10-28" || ref.WeekStart.String() != "Monday" {
					return usagef("incorrect date reference: %v", ref)
				}
			}
			return nil
		}}
	})
}

func canceledRun(t *testing.T, s sysDeps) int {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return run(ctx, []string{"__test-clock"}, strings.NewReader(""), io.Discard, io.Discard, s)
}
