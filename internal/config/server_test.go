package config_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"taskronin/internal/config"
)

func configPaths(t *testing.T) config.Paths {
	t.Helper()
	// These are configuration values, not files to create. Their existing
	// ancestors are private enough even when the test's TMPDIR is world-writable.
	p, err := config.DefaultPaths(func(string) string { return "" }, "/taskronin-config-test")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func serverExample(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/server.toml")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDefaultServer(t *testing.T) {
	p := configPaths(t)
	want := config.Server{
		Timezone: "America/Denver", WeekStart: "monday", DueSoonDays: 3,
		Paths:  config.ServerPaths{DataDir: p.DataDir, Socket: p.Socket},
		Web:    config.WebConfig{Enabled: true, Listen: "127.0.0.1:7878", AllowPlainLoopback: false},
		Backup: config.BackupConfig{Dir: p.BackupDir, Time: "03:30"},
		Log:    config.LogConfig{Level: "info", Format: "text"},
	}
	got := config.DefaultServer(p, "America/Denver")
	if *got != want {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
	if err := config.ValidateServer(got); err != nil {
		t.Fatalf("default config validation: %v", err)
	}
}

func TestLoadServerExample(t *testing.T) {
	s, err := config.LoadServer("testdata/server.toml")
	if err != nil {
		t.Fatal(err)
	}
	want := config.Server{
		Timezone: "America/Denver", WeekStart: "monday", DueSoonDays: 3,
		Paths: config.ServerPaths{
			DataDir: "/home/sean/.local/share/taskronin", Socket: "/run/user/1000/taskronin/sock",
		},
		Web:    config.WebConfig{Enabled: true, Listen: "127.0.0.1:7878", AllowPlainLoopback: true},
		Backup: config.BackupConfig{Dir: "/home/sean/.local/share/taskronin/backups", Time: "03:30"},
		Log:    config.LogConfig{Level: "info", Format: "text"},
	}
	if *s != want {
		t.Fatalf("config = %+v, want %+v", s, want)
	}
	z, err := s.Zone()
	if err != nil || z.Name() != "America/Denver" {
		t.Fatalf("Zone = %v, %v", z, err)
	}
	day, err := s.WeekStartDay()
	if err != nil || day != time.Monday {
		t.Fatalf("WeekStartDay = %v, %v", day, err)
	}
	minute, err := s.BackupMinute()
	if err != nil || minute != 210 {
		t.Fatalf("BackupMinute = %v, %v", minute, err)
	}
}

func TestLoadServerUnknownKey(t *testing.T) {
	contents := strings.Replace(serverExample(t), "[web]\n", "[web]\nport = 1\n", 1)
	contents += "extra = true\n"
	s, err := config.LoadServer(writeConfig(t, contents))
	if s != nil || err == nil || !strings.Contains(err.Error(), "web.port") || !strings.Contains(err.Error(), "log.extra") {
		t.Fatalf("LoadServer = %+v, %v; want every dotted unknown key", s, err)
	}
}

func TestLoadServerErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.toml")
	s, err := config.LoadServer(path)
	want := fmt.Sprintf("server config %s not found — run `tkr init`", path)
	if s != nil || err == nil || err.Error() != want {
		t.Fatalf("missing config = %+v, %v; want %q", s, err, want)
	}
	for name, contents := range map[string]string{
		"syntax":           "timezone = [",
		"missing timezone": strings.Replace(serverExample(t), `timezone      = "America/Denver"`, `timezone      = ""`, 1),
		"range":            strings.Replace(serverExample(t), "due_soon_days = 3", "due_soon_days = 31", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if s, err := config.LoadServer(writeConfig(t, contents)); s != nil || err == nil {
				t.Fatalf("invalid config = %+v, %v", s, err)
			}
		})
	}
	if s, err := config.LoadServer(t.TempDir()); s != nil || err == nil || !errors.As(err, new(*os.PathError)) {
		t.Fatalf("read directory = %+v, %v; want underlying I/O error", s, err)
	}
}

func TestValidateServer(t *testing.T) {
	tests := []struct {
		name string
		set  func(*config.Server)
		want string
	}{
		{"empty timezone", func(s *config.Server) { s.Timezone = "" }, "timezone"},
		{"local timezone", func(s *config.Server) { s.Timezone = "Local" }, "timezone"},
		{"POSIX timezone", func(s *config.Server) { s.Timezone = "UTC0" }, "timezone"},
		{"unknown timezone", func(s *config.Server) { s.Timezone = "Mars/Base" }, "timezone"},
		{"week start", func(s *config.Server) { s.WeekStart = "funday" }, "week_start"},
		{"negative due soon", func(s *config.Server) { s.DueSoonDays = -1 }, "due_soon_days"},
		{"large due soon", func(s *config.Server) { s.DueSoonDays = 31 }, "due_soon_days"},
		{"relative data", func(s *config.Server) { s.Paths.DataDir = "rel" }, "absolute"},
		{"unclean data", func(s *config.Server) { s.Paths.DataDir = "/a/../b" }, "clean"},
		{"data query", func(s *config.Server) { s.Paths.DataDir = "/data?x" }, "?"},
		{"data fragment", func(s *config.Server) { s.Paths.DataDir = "/data#x" }, "#"},
		{"socket query", func(s *config.Server) { s.Paths.Socket = "/run/x?y" }, "?"},
		{"socket fragment", func(s *config.Server) { s.Paths.Socket = "/run/x#y" }, "#"},
		{"relative socket", func(s *config.Server) { s.Paths.Socket = "sock" }, "absolute"},
		{"unclean socket", func(s *config.Server) { s.Paths.Socket = "/run/../sock" }, "clean"},
		{"long socket", func(s *config.Server) { s.Paths.Socket = "/" + strings.Repeat("a", 107) }, "socket path too long"},
		{"wildcard web", func(s *config.Server) { s.Web.Listen = "0.0.0.0:7878" }, "loopback"},
		{"remote web", func(s *config.Server) { s.Web.Listen = "192.168.1.5:7878" }, "loopback"},
		{"remote DNS", func(s *config.Server) { s.Web.Listen = "example.com:7878" }, "loopback"},
		{"no web port", func(s *config.Server) { s.Web.Listen = "127.0.0.1" }, "web.listen"},
		{"large web port", func(s *config.Server) { s.Web.Listen = "127.0.0.1:65536" }, "web.listen"},
		{"IPv6 wildcard", func(s *config.Server) { s.Web.Listen = "[::]:7878" }, "loopback"},
		{"backup midnight", func(s *config.Server) { s.Backup.Time = "24:00" }, "backup.time"},
		{"backup syntax", func(s *config.Server) { s.Backup.Time = "9:5" }, "backup.time"},
		{"relative backup", func(s *config.Server) { s.Backup.Dir = "rel" }, "absolute"},
		{"unclean backup", func(s *config.Server) { s.Backup.Dir = "/data/../backups" }, "clean"},
		{"backup query", func(s *config.Server) { s.Backup.Dir = "/backups?x" }, "?"},
		{"log level", func(s *config.Server) { s.Log.Level = "trace" }, "log.level"},
		{"log format", func(s *config.Server) { s.Log.Format = "yaml" }, "log.format"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := config.DefaultServer(configPaths(t), "UTC")
			tt.set(s)
			if err := config.ValidateServer(s); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateServer = %v; want %q", err, tt.want)
			}
		})
	}
	for _, listen := range []string{"localhost:7878", "[::1]:7878", "127.0.0.2:7878", "127.0.0.1:0"} {
		t.Run(listen, func(t *testing.T) {
			s := config.DefaultServer(configPaths(t), "UTC")
			s.Web.Listen = listen
			if err := config.ValidateServer(s); err != nil {
				t.Fatal(err)
			}
			addr, err := s.WebAddr()
			want := strings.Replace(listen, "localhost", "127.0.0.1", 1)
			if err != nil || addr.String() != want {
				t.Fatalf("WebAddr = %v, %v; want %s", addr, err, want)
			}
		})
	}
	for day := time.Sunday; day <= time.Saturday; day++ {
		s := config.DefaultServer(configPaths(t), "UTC")
		s.WeekStart = strings.ToLower(day.String())
		if got, err := s.WeekStartDay(); err != nil || got != day {
			t.Fatalf("WeekStartDay(%q) = %v, %v", s.WeekStart, got, err)
		}
	}
	for _, days := range []int{0, 30} {
		s := config.DefaultServer(configPaths(t), "UTC")
		s.DueSoonDays = days
		if err := config.ValidateServer(s); err != nil {
			t.Fatalf("due_soon_days %d: %v", days, err)
		}
	}
	for _, level := range []string{"debug", "info", "warn", "error"} {
		for _, format := range []string{"text", "json"} {
			s := config.DefaultServer(configPaths(t), "UTC")
			s.Log = config.LogConfig{Level: level, Format: format}
			if err := config.ValidateServer(s); err != nil {
				t.Fatalf("log %+v: %v", s.Log, err)
			}
		}
	}
}

func TestValidateServerWorldWritableParents(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"paths.data_dir", "paths.socket", "backup.dir"} {
		t.Run(field, func(t *testing.T) {
			s := config.DefaultServer(configPaths(t), "UTC")
			path := filepath.Join(parent, "missing", "child")
			switch field {
			case "paths.data_dir":
				s.Paths.DataDir = path
			case "paths.socket":
				s.Paths.Socket = path
			case "backup.dir":
				s.Backup.Dir = path
			}
			err := config.ValidateServer(s)
			if err == nil || !strings.Contains(err.Error(), "world-writable") || !strings.Contains(err.Error(), parent) {
				t.Fatalf("ValidateServer = %v; want world-writable parent %s", err, parent)
			}
			if _, err := os.Stat(filepath.Join(parent, "missing")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("validation created a directory: %v", err)
			}
		})
	}
}

func TestWebListenerAllowed(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, allow := range []bool{false, true} {
			s := config.DefaultServer(configPaths(t), "UTC")
			s.Web.Enabled, s.Web.AllowPlainLoopback = enabled, allow
			err := s.WebListenerAllowed()
			if !enabled || allow {
				if err != nil {
					t.Fatalf("enabled=%v allow=%v: %v", enabled, allow, err)
				}
				continue
			}
			for _, want := range []string{"no TLS in v1", "allow_plain_loopback = true", "single-user host"} {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("listener error = %v; want %q", err, want)
				}
			}
		}
	}
}

func TestValidateConfigSymlinkParents(t *testing.T) {
	// A lexical path under /tmp would already fail the ancestor-mode check,
	// hiding the symlink bypass. Keep its parents safe in the private checkout.
	root, err := os.MkdirTemp(".", ".paths-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	shared := t.TempDir()
	if err := os.Chmod(shared, 0o777); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(shared, "private")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"alias":    private,
		"dangling": filepath.Join(shared, "absent"),
	} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, field := range []string{"paths.data_dir", "paths.socket", "backup.dir", "socket", "token_file", "mcp.token_file"} {
		t.Run(field, func(t *testing.T) {
			validate := func(path string) error {
				s := config.DefaultServer(configPaths(t), "UTC")
				c := config.DefaultClient(configPaths(t))
				switch field {
				case "paths.data_dir":
					s.Paths.DataDir = path
				case "paths.socket":
					s.Paths.Socket = path
				case "backup.dir":
					s.Backup.Dir = path
				case "socket":
					c.Socket = path
				case "token_file":
					c.TokenFile = path
				case "mcp.token_file":
					c.MCP.TokenFile = path
				}
				if err := config.ValidateServer(s); err != nil {
					return err
				}
				return config.ValidateClient(c)
			}
			missing := filepath.Join(root, "missing", "child")
			if err := validate(missing); err != nil {
				t.Fatalf("safe missing directories: %v", err)
			}
			if _, err := os.Lstat(filepath.Dir(missing)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("validation created missing directory: %v", err)
			}
			for _, name := range []string{"alias", "dangling"} {
				t.Run(name, func(t *testing.T) {
					link := filepath.Join(root, name)
					for _, path := range []string{link, filepath.Join(link, "missing", "child")} {
						err := validate(path)
						if err == nil || !strings.Contains(err.Error(), "symlink") || !strings.Contains(err.Error(), link) {
							t.Errorf("validation of %s = %v; want rejected symlink %s", path, err, link)
						}
					}
				})
			}
		})
	}
}

func TestLoadServerEffectivePaths(t *testing.T) {
	contents := strings.NewReplacer(
		"/home/sean/.local/share/taskronin/backups", "/taskronin-backup-test/archive",
		"/home/sean/.local/share/taskronin", "/taskronin-data-test/custom",
		"/run/user/1000/taskronin/sock", "/taskronin-runtime-test/sock",
	).Replace(serverExample(t))
	s, err := config.LoadServer(writeConfig(t, contents))
	if err != nil {
		t.Fatal(err)
	}
	p := configPaths(t)
	// BF01: this is the shared normalization handoff to server/offline callers.
	effective := p.WithServerPaths(s.Paths.DataDir, s.Paths.Socket, s.Backup.Dir)
	if effective.DataDir != "/taskronin-data-test/custom" || effective.DB != "/taskronin-data-test/custom/taskronin.db" ||
		effective.LockFile() != "/taskronin-data-test/custom/taskronin.lock" ||
		effective.LedgerFile != "/taskronin-data-test/custom/purge-ledger" ||
		effective.StagingDir != "/taskronin-data-test/custom/staging" ||
		effective.SwapJournal() != "/taskronin-data-test/custom/swap.journal" ||
		effective.Socket != "/taskronin-runtime-test/sock" || effective.BackupDir != "/taskronin-backup-test/archive" {
		t.Fatalf("effective paths = %+v", effective)
	}
	if effective.ConfigDir != p.ConfigDir || effective.StateDir != p.StateDir || effective.OwnerToken != p.OwnerToken {
		t.Fatalf("server overrides changed client/config paths: %+v", effective)
	}
}
