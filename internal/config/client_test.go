package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"taskronin/internal/config"
)

func TestLoadClientDefaults(t *testing.T) {
	p := configPaths(t)
	want := config.Client{
		Socket: p.Socket, TokenFile: p.OwnerToken,
		Timeout: 20 * time.Second, StatusTimeout: 300 * time.Millisecond,
		Editor: "", Color: "auto",
	}
	want.MCP.TokenFile = p.TokenFile("mcp")
	if got := config.DefaultClient(p); *got != want {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
	path := filepath.Join(t.TempDir(), "client.toml")
	got, err := config.LoadClient(path, p)
	if err != nil || got == nil || *got != want {
		t.Fatalf("missing client = %+v, %v; want %+v", got, err, want)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("loading defaults created config: %v", err)
	}
	want.Editor = "code --wait"
	got, err = config.LoadClient(writeConfig(t, `editor = "code --wait"`), p)
	if err != nil || got == nil || *got != want {
		t.Fatalf("partial client = %+v, %v; want %+v", got, err, want)
	}
}

func TestValidateClient(t *testing.T) {
	tests := []struct {
		contents string
		want     string
	}{
		{`timeout = "abc"`, "timeout"},
		{`status_timeout = "abc"`, "status_timeout"},
		{`status_timeout = "3s"`, "2s"},
		{`status_timeout = "0s"`, "status_timeout"},
		{`status_timeout = "-1s"`, "status_timeout"},
		{`timeout = "0s"`, "timeout"},
		{`timeout = "14s"`, "15s"},
		{`color = "rainbow"`, "color"},
		{`socket = "/` + strings.Repeat("a", 107) + `"`, "socket path too long"},
		{`socket = "relative"`, "absolute"},
		{`socket = "/run/../sock"`, "clean"},
		{`socket = "/run/x?y"`, "?"},
		{`socket = "/run/x#y"`, "#"},
		{`token_file = "relative"`, "absolute"},
		{`token_file = "/tokens/../owner.token"`, "clean"},
		{`token_file = "/tokens/owner?x"`, "?"},
		{"[mcp]\ntoken_file = \"relative\"", "absolute"},
		{"[mcp]\ntoken_file = \"/tokens/mcp#x\"", "#"},
	}
	for _, tt := range tests {
		t.Run(tt.contents, func(t *testing.T) {
			c, err := config.LoadClient(writeConfig(t, tt.contents), configPaths(t))
			if c != nil || err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadClient = %+v, %v; want %q", c, err, tt.want)
			}
		})
	}
	for _, color := range []string{"auto", "always", "never"} {
		c := config.DefaultClient(configPaths(t))
		c.Color, c.Timeout, c.StatusTimeout = color, 15*time.Second, 2*time.Second
		if err := config.ValidateClient(c); err != nil {
			t.Fatal(err)
		}
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"socket", "token_file", "mcp.token_file"} {
		t.Run(field+" parent", func(t *testing.T) {
			c := config.DefaultClient(configPaths(t))
			path := filepath.Join(parent, "missing", "file")
			switch field {
			case "socket":
				c.Socket = path
			case "token_file":
				c.TokenFile = path
			case "mcp.token_file":
				c.MCP.TokenFile = path
			}
			err := config.ValidateClient(c)
			if err == nil || !strings.Contains(err.Error(), "world-writable") || !strings.Contains(err.Error(), parent) {
				t.Fatalf("ValidateClient = %v; want world-writable parent %s", err, parent)
			}
		})
	}
}

func TestLoadClientErrors(t *testing.T) {
	p := configPaths(t)
	for _, contents := range []string{"editor = [", "editor = 42"} {
		if c, err := config.LoadClient(writeConfig(t, contents), p); c != nil || err == nil {
			t.Fatalf("malformed client = %+v, %v", c, err)
		}
	}
	c, err := config.LoadClient(writeConfig(t, "unknown = 1\n[mcp]\nunknown = true\n"), p)
	if c != nil || err == nil || !strings.Contains(err.Error(), "unknown") || !strings.Contains(err.Error(), "mcp.unknown") {
		t.Fatalf("unknown client keys = %+v, %v", c, err)
	}
	if c, err := config.LoadClient(t.TempDir(), p); c != nil || err == nil || !errors.As(err, new(*os.PathError)) {
		t.Fatalf("read directory = %+v, %v; want underlying I/O error", c, err)
	}
	p.Socket = "relative"
	if c, err := config.LoadClient(filepath.Join(t.TempDir(), "missing.toml"), p); c != nil || err == nil {
		t.Fatalf("invalid default paths = %+v, %v", c, err)
	}
}

func TestWriteLoadRoundTrip(t *testing.T) {
	p := configPaths(t)
	s := config.DefaultServer(p, "America/Denver")
	c := config.DefaultClient(p)
	c.Editor = `code --wait "two words"`
	c.MCP.TokenFile = "/taskronin-config-test/mcp-custom.token"
	serverPath, clientPath := filepath.Join(t.TempDir(), "server.toml"), filepath.Join(t.TempDir(), "client.toml")
	for _, existing := range []bool{false, true} {
		if existing {
			for _, path := range []string{serverPath, clientPath} {
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			s.WeekStart, s.Web.AllowPlainLoopback = "sunday", true
		}
		if err := config.WriteServer(serverPath, s); err != nil {
			t.Fatal(err)
		}
		if err := config.WriteClient(clientPath, c); err != nil {
			t.Fatal(err)
		}
		gotServer, err := config.LoadServer(serverPath)
		if err != nil || gotServer == nil || *gotServer != *s {
			t.Fatalf("server round trip = %+v, %v; want %+v", gotServer, err, s)
		}
		gotClient, err := config.LoadClient(clientPath, p)
		if err != nil || gotClient == nil || *gotClient != *c {
			t.Fatalf("client round trip = %+v, %v; want %+v", gotClient, err, c)
		}
		for _, path := range []string{serverPath, clientPath} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("%s mode = %o, want 600", path, info.Mode().Perm())
			}
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if path == serverPath && strings.SplitN(string(b), "\n", 2)[0] != "# Written by `tkr init`. Unknown keys are an error." {
				t.Fatalf("server comment missing: %s", b)
			}
			if path == clientPath && (!strings.Contains(string(b), `timeout = "20s"`) || !strings.Contains(string(b), `status_timeout = "300ms"`)) {
				t.Fatalf("durations not encoded as strings: %s", b)
			}
		}
	}
}

func TestWriteConfigErrors(t *testing.T) {
	p := configPaths(t)
	path := filepath.Join(t.TempDir(), "missing", "config.toml")
	if err := config.WriteServer(path, config.DefaultServer(p, "UTC")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("WriteServer = %v; want missing parent error", err)
	}
	if err := config.WriteClient(path, config.DefaultClient(p)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("WriteClient = %v; want missing parent error", err)
	}
}
