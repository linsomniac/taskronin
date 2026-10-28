package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"taskronin/internal/config"
)

func TestDefaultPathsXDG(t *testing.T) {
	env := map[string]string{
		"XDG_CONFIG_HOME": "/x/c", "XDG_DATA_HOME": "/x/d",
		"XDG_STATE_HOME": "/x/s", "XDG_RUNTIME_DIR": "/run/user/1000",
	}
	want := config.Paths{
		ConfigDir: "/x/c/taskronin", DataDir: "/x/d/taskronin", StateDir: "/x/s/taskronin",
		RuntimeDir:   "/run/user/1000/taskronin",
		ServerConfig: "/x/c/taskronin/server.toml", ClientConfig: "/x/c/taskronin/client.toml",
		Socket: "/run/user/1000/taskronin/sock", OwnerToken: "/x/s/taskronin/owner.token",
		TokensDir: "/x/s/taskronin/tokens", DraftsDir: "/x/s/taskronin/drafts",
		PendingDir: "/x/s/taskronin/pending", StagingDir: "/x/d/taskronin/staging",
		BackupDir: "/x/d/taskronin/backups", LedgerFile: "/x/d/taskronin/purge-ledger",
		DB: "/x/d/taskronin/taskronin.db",
	}
	for _, home := range []string{"/h", ""} {
		p, err := config.DefaultPaths(func(k string) string { return env[k] }, home)
		if err != nil {
			t.Fatal(err)
		}
		if p != want {
			t.Fatalf("home %q: paths = %+v, want %+v", home, p, want)
		}
		for name, pair := range map[string][2]string{
			"lock":          {p.LockFile(), "/x/d/taskronin/taskronin.lock"},
			"swap journal":  {p.SwapJournal(), "/x/d/taskronin/swap.journal"},
			"units":         {p.UnitsDir(), "/x/c/systemd/user"},
			"token":         {p.TokenFile("mcp"), "/x/s/taskronin/tokens/mcp.token"},
			"scrub journal": {config.ScrubJournal("/b"), "/b/scrub.journal"},
		} {
			if pair[0] != pair[1] {
				t.Errorf("%s = %q, want %q", name, pair[0], pair[1])
			}
		}
	}
}

func TestDefaultPathsHomeFallback(t *testing.T) {
	p, err := config.DefaultPaths(func(string) string { return "" }, "/h")
	if err != nil {
		t.Fatal(err)
	}
	want := config.Paths{
		ConfigDir: "/h/.config/taskronin", DataDir: "/h/.local/share/taskronin",
		StateDir: "/h/.local/state/taskronin", RuntimeDir: "",
		ServerConfig: "/h/.config/taskronin/server.toml", ClientConfig: "/h/.config/taskronin/client.toml",
		Socket: "/h/.local/state/taskronin/sock", OwnerToken: "/h/.local/state/taskronin/owner.token",
		TokensDir: "/h/.local/state/taskronin/tokens", DraftsDir: "/h/.local/state/taskronin/drafts",
		PendingDir: "/h/.local/state/taskronin/pending", StagingDir: "/h/.local/share/taskronin/staging",
		BackupDir: "/h/.local/share/taskronin/backups", LedgerFile: "/h/.local/share/taskronin/purge-ledger",
		DB: "/h/.local/share/taskronin/taskronin.db",
	}
	if p != want {
		t.Fatalf("paths = %+v, want %+v", p, want)
	}
	if got := p.UnitsDir(); got != "/h/.config/systemd/user" {
		t.Errorf("UnitsDir = %q", got)
	}
}

func TestDefaultPathsRelativeXDGIgnored(t *testing.T) {
	want, err := config.DefaultPaths(func(string) string { return "" }, "/h")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR"} {
		t.Run(key, func(t *testing.T) {
			p, err := config.DefaultPaths(func(k string) string {
				if k == key {
					return "rel/dir"
				}
				return ""
			}, "/h")
			if err != nil || p != want {
				t.Fatalf("paths = %+v, err = %v; want %+v", p, err, want)
			}
		})
	}
}

func TestDefaultPathsNoHome(t *testing.T) {
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		t.Run(key, func(t *testing.T) {
			for _, home := range []string{"", "relative/home"} {
				for _, value := range []string{"", "rel/dir"} {
					p, err := config.DefaultPaths(func(k string) string {
						if k == key {
							return value
						}
						return "/x"
					}, home)
					if err == nil || p != (config.Paths{}) {
						t.Fatalf("home %q, XDG %q: paths = %+v, err = %v; want zero paths and an error", home, value, p, err)
					}
				}
			}
		})
	}
	if _, err := config.DefaultPaths(func(string) string { return "" }, ""); err == nil {
		t.Fatal("accepted missing home and XDG variables")
	}
}

func TestDefaultPathsDoesNotCreateDirectories(t *testing.T) {
	home := t.TempDir()
	if _, err := config.DefaultPaths(func(string) string { return "" }, home); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("path resolution changed home: %v, err = %v", entries, err)
	}
}

func TestPathsWithServerPaths(t *testing.T) {
	p, err := config.DefaultPaths(func(string) string { return "" }, "/h")
	if err != nil {
		t.Fatal(err)
	}
	// BF01: one effective data root must govern the DB, lock, ledger and staging.
	want := p
	want.DataDir = "/srv/tasks"
	want.DB = "/srv/tasks/taskronin.db"
	want.LedgerFile = "/srv/tasks/purge-ledger"
	want.StagingDir = "/srv/tasks/staging"
	want.Socket = "/run/tasks/api.sock"
	want.BackupDir = "/archive/tasks"
	got := p.WithServerPaths(want.DataDir, want.Socket, want.BackupDir)
	if got != want {
		t.Fatalf("effective paths = %+v, want %+v", got, want)
	}
	if got.LockFile() != "/srv/tasks/taskronin.lock" || got.SwapJournal() != "/srv/tasks/swap.journal" ||
		config.ScrubJournal(got.BackupDir) != "/archive/tasks/scrub.journal" {
		t.Fatalf("effective lock or journals use the wrong root: %+v", got)
	}
	if p.DataDir != "/h/.local/share/taskronin" {
		t.Fatal("normalization changed the original defaults")
	}
	if got := p.WithServerPaths(p.DataDir, p.Socket, p.BackupDir); got != p {
		t.Fatalf("normalizing defaults changed paths: %+v", got)
	}
}

func TestDefaultPathsMixedXDG(t *testing.T) {
	p, err := config.DefaultPaths(func(k string) string {
		if k == "XDG_DATA_HOME" {
			return "/custom/data/"
		}
		return ""
	}, "/h")
	if err != nil {
		t.Fatal(err)
	}
	if p.DataDir != "/custom/data/taskronin" || p.DB != filepath.Join(p.DataDir, "taskronin.db") ||
		p.ConfigDir != "/h/.config/taskronin" || p.Socket != "/h/.local/state/taskronin/sock" {
		t.Fatalf("mixed paths = %+v", p)
	}
}

func TestCheckSocketPath(t *testing.T) {
	if config.MaxSocketPath != 107 {
		t.Fatalf("MaxSocketPath = %d, want 107", config.MaxSocketPath)
	}
	for _, p := range []string{
		"/sock", "/" + strings.Repeat("a", 106), "/" + strings.Repeat("a", 107),
		"/" + strings.Repeat("é", 53), "/" + strings.Repeat("é", 54),
	} {
		err := config.CheckSocketPath(p)
		if len(p) <= 107 {
			if err != nil {
				t.Errorf("%d-byte socket path: %v", len(p), err)
			}
			continue
		}
		want := fmt.Sprintf("socket path too long (%d bytes, max 107): %s", len(p), p)
		if err == nil || err.Error() != want {
			t.Errorf("CheckSocketPath = %v, want %q", err, want)
		}
	}
}
