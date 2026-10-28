package testutil

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"taskronin/internal/client"
	"taskronin/internal/clock"
	"taskronin/internal/config"
	"taskronin/internal/domain"
	"taskronin/internal/fsutil"
	"taskronin/internal/offline"
	"taskronin/internal/server"
	"taskronin/internal/service"
)

// SyncBuffer is a goroutine-safe log sink.
type SyncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *SyncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *SyncBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buf.String() }

// Server is a real, restartable socket server with isolated data and fake time.
type Server struct {
	Client          *client.Client
	Clock           *clock.Fake
	Svc             *service.Service
	DataDir, Socket string
	Paths           config.Paths
	WebURL          string
	Log             *SyncBuffer
	mu              sync.Mutex
	cancel          context.CancelFunc
	done            chan error
	t               testing.TB
}

type serverOpts struct {
	local         string
	mcp, initOnly bool
	config        []func(*config.Server)
}

// ServerOpt configures a StartServer fixture.
type ServerOpt func(*serverOpts)

// WithClock selects the initial catalog wall time.
func WithClock(local string) ServerOpt { return func(o *serverOpts) { o.local = local } }

// WithMCPToken initializes the default MCP token as well as the owner token.
func WithMCPToken() ServerOpt { return func(o *serverOpts) { o.mcp = true } }

// WithConfig changes effective server configuration before initialization.
func WithConfig(f func(*config.Server)) ServerOpt {
	return func(o *serverOpts) { o.config = append(o.config, f) }
}

// InitOnly creates the instance without running the server.
func InitOnly() ServerOpt { return func(o *serverOpts) { o.initOnly = true } }

// WithWeb enables plain loopback HTTP on an ephemeral port when an adapter exists.
func WithWeb() ServerOpt {
	return WithConfig(func(c *config.Server) {
		c.Web.Enabled = true
		c.Web.AllowPlainLoopback = true
		c.Web.Listen = "127.0.0.1:0"
	})
}

// StartServer initializes real data and starts Run, waiting for its health endpoint.
// It changes environment variables and must not be used by parallel tests.
func StartServer(t testing.TB, opts ...ServerOpt) *Server {
	t.Helper()
	o := serverOpts{local: DefaultNow}
	for _, opt := range opts {
		opt(&o)
	}
	oldMask := fsutil.SetUmask(0o077)
	t.Cleanup(func() { fsutil.SetUmask(oldMask) })
	// AIDEV-NOTE: config rejects world-writable ancestors, including /tmp.
	// Use a private workspace temp root and redirect MkdirTemp's default there;
	// the short runtime name also stays below Unix sun_path's 107-byte limit.
	root, err := os.MkdirTemp(".", ".tkr-")
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
	t.Setenv("TMPDIR", root)
	for key, dir := range map[string]string{"XDG_CONFIG_HOME": "c", "XDG_DATA_HOME": "d", "XDG_STATE_HOME": "s"} {
		t.Setenv(key, filepath.Join(root, dir))
	}
	runtimeDir, err := os.MkdirTemp("", "tkr")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	paths, err := config.DefaultPaths(os.Getenv, root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultServer(paths, "America/Denver")
	cfg.Web.Enabled = false
	for _, f := range o.config {
		f(cfg)
	}
	paths = paths.WithServerPaths(cfg.Paths.DataDir, cfg.Paths.Socket, cfg.Backup.Dir)
	s := &Server{Paths: paths, DataDir: paths.DataDir, Socket: paths.Socket, Clock: Clock(t, o.local), Log: new(SyncBuffer), t: t}
	initConfig := *cfg
	e := &offline.Env{Paths: paths, Server: &initConfig, Clock: s.Clock, In: strings.NewReader(Password + "\n"), Logger: slog.New(slog.NewJSONHandler(s.Log, nil))}
	if _, err := offline.Init(t.Context(), e, offline.InitOptions{Timezone: "America/Denver", PasswordStdin: true, NoSystemd: true, Yes: true, MCPToken: o.mcp}); err != nil {
		t.Fatal(err)
	}
	// Init may derive web consent; test configuration is authoritative here.
	if err := config.WriteServer(paths.ServerConfig, cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	if !o.initOnly {
		s.Start(t)
	}
	return s
}

// Stop cancels Run and waits without abandoning its drain. Repeated calls are safe.
func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel == nil {
		return
	}
	s.cancel()
	if err := <-s.done; err != nil {
		s.t.Errorf("server shutdown: %v", err)
	}
	s.cancel = nil
}

// Start runs the same initialized data again and captures its new service.
func (s *Server) Start(t testing.TB) {
	t.Helper()
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		t.Fatal("server is already started")
	}
	cfg, err := config.LoadServer(s.Paths.ServerConfig)
	if err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done, started := make(chan error, 1), make(chan server.Startup, 1)
	s.cancel, s.done, s.t = cancel, done, t
	s.mu.Unlock()
	go func() {
		done <- server.Run(ctx, cfg, server.Deps{Paths: s.Paths, Clock: s.Clock, Logger: slog.New(slog.NewJSONHandler(s.Log, nil)), Started: func(v server.Startup) { started <- v }})
	}()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case v := <-started:
		s.Svc, s.Socket, s.WebURL = v.Svc, v.Socket, ""
		if v.WebAddr != "" {
			s.WebURL = "http://" + v.WebAddr
		}
	case err := <-done:
		// Leave the result available for idempotent cleanup.
		done <- err
		t.Fatalf("server startup: %v", err)
	case <-timer.C:
		cancel()
		t.Fatal("server startup timed out")
	}
	clientCfg := config.DefaultClient(s.Paths)
	clientCfg.Socket = s.Socket
	s.Client, err = client.NewFromConfig(clientCfg, "cli", nil)
	if err != nil {
		t.Fatal(err)
	}
	healthCtx, cancelHealth := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelHealth()
	if err := s.Client.Health(healthCtx); err != nil {
		t.Fatalf("server health: %v", err)
	}
}

// TokenClient mints a scoped token through the service and writes its private file.
func (s *Server) TokenClient(t testing.TB, name string, scopes domain.Scopes, perHour int, channel string) *client.Client {
	t.Helper()
	created, err := s.Svc.CreateToken(t.Context(), Meta(Owner(), s.Svc.Generation()), service.CreateTokenIn{Name: name, Scopes: scopes, MutationsPerHour: perHour})
	if err != nil {
		t.Fatal(err)
	}
	path := s.Paths.TokenFile(name)
	if err := fsutil.WriteFileAtomic(path, []byte(created.Secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultClient(s.Paths)
	cfg.Socket, cfg.TokenFile = s.Socket, path
	c, err := client.NewFromConfig(cfg, channel, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
