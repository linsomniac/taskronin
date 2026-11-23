package config

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/linsomniac/taskronin/internal/civil"
)

// Server is the server.toml schema. Paths are resolved by init; loading a
// configuration does not create directories or change their permissions.
type Server struct {
	Timezone    string       `toml:"timezone"`
	WeekStart   string       `toml:"week_start"`
	DueSoonDays int          `toml:"due_soon_days"`
	Paths       ServerPaths  `toml:"paths"`
	Web         WebConfig    `toml:"web"`
	Backup      BackupConfig `toml:"backup"`
	Log         LogConfig    `toml:"log"`
}

// ServerPaths contains the effective server data directory and socket path.
type ServerPaths struct {
	DataDir string `toml:"data_dir"`
	Socket  string `toml:"socket"`
}

// WebConfig controls the optional plain HTTP loopback listener.
type WebConfig struct {
	Enabled            bool   `toml:"enabled"`
	Listen             string `toml:"listen"`
	AllowPlainLoopback bool   `toml:"allow_plain_loopback"`
}

// BackupConfig gives the backup directory and daily time in the server zone.
type BackupConfig struct {
	Dir  string `toml:"dir"`
	Time string `toml:"time"`
}

// LogConfig selects the slog level and output format.
type LogConfig struct {
	Level  string `toml:"level"`
	Format string `toml:"format"`
}

// DefaultServer builds init's defaults without granting plain HTTP consent.
func DefaultServer(p Paths, timezone string) *Server {
	return &Server{
		Timezone: timezone, WeekStart: "monday", DueSoonDays: 3,
		Paths:  ServerPaths{DataDir: p.DataDir, Socket: p.Socket},
		Web:    WebConfig{Enabled: true, Listen: "127.0.0.1:7878", AllowPlainLoopback: false},
		Backup: BackupConfig{Dir: p.BackupDir, Time: "03:30"},
		Log:    LogConfig{Level: "info", Format: "text"},
	}
}

// ValidateServer checks configuration values. Listener consent is checked
// separately by WebListenerAllowed when a build starts the web listener.
func ValidateServer(s *Server) error {
	if _, err := s.Zone(); err != nil {
		return err
	}
	if _, err := s.WeekStartDay(); err != nil {
		return err
	}
	if s.DueSoonDays < 0 || s.DueSoonDays > 30 {
		return fmt.Errorf("due_soon_days must be between 0 and 30")
	}
	if err := validatePath("paths.data_dir", s.Paths.DataDir, true); err != nil {
		return err
	}
	if err := CheckSocketPath(s.Paths.Socket); err != nil {
		return err
	}
	if err := validatePath("paths.socket", s.Paths.Socket, false); err != nil {
		return err
	}
	if _, err := s.WebAddr(); err != nil {
		return err
	}
	if err := validatePath("backup.dir", s.Backup.Dir, true); err != nil {
		return err
	}
	// AIDEV-NOTE: final review B-M13. backup.CleanupTemps removes everything
	// under <data>/staging (and sweeps temp names in the data dir) at every
	// server start and offline command, so snapshots there would be wiped.
	// Both paths are absolute, clean and free of symlink components here.
	staging := filepath.Join(s.Paths.DataDir, "staging")
	if s.Backup.Dir == s.Paths.DataDir || s.Backup.Dir == staging || strings.HasPrefix(s.Backup.Dir, staging+string(filepath.Separator)) {
		return fmt.Errorf("backup.dir must not be the data directory %s or inside its staging directory (emptied at every start)", s.Paths.DataDir)
	}
	if _, err := s.BackupMinute(); err != nil {
		return err
	}
	switch s.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log.level must be debug, info, warn or error")
	}
	switch s.Log.Format {
	case "text", "json":
	default:
		return fmt.Errorf("log.format must be text or json")
	}
	return nil
}

// Zone loads the configured IANA timezone.
func (s *Server) Zone() (civil.Zone, error) {
	z, err := civil.LoadZone(s.Timezone)
	if err != nil {
		return civil.Zone{}, fmt.Errorf("timezone: %w", err)
	}
	return z, nil
}

// WeekStartDay returns the weekday selected by the lowercase configuration name.
func (s *Server) WeekStartDay() (time.Weekday, error) {
	for day := time.Sunday; day <= time.Saturday; day++ {
		if s.WeekStart == strings.ToLower(day.String()) {
			return day, nil
		}
	}
	return 0, fmt.Errorf("week_start must be monday through sunday")
}

// BackupMinute parses the daily backup time, excluding 24:00.
func (s *Server) BackupMinute() (civil.Minute, error) {
	minute, err := civil.ParseHHMM(s.Backup.Time, false)
	if err != nil {
		return 0, fmt.Errorf("backup.time: %w", err)
	}
	return minute, nil
}

// WebAddr parses a numeric loopback address and port without resolving DNS.
// The one accepted hostname, localhost, always maps to IPv4 loopback.
func (s *Server) WebAddr() (netip.AddrPort, error) {
	host, port, err := net.SplitHostPort(s.Web.Listen)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("web.listen: %w", err)
	}
	if host == "localhost" {
		host = "127.0.0.1"
	}
	addr, err := netip.ParseAddrPort(net.JoinHostPort(host, port))
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("web.listen must be a loopback IP address or localhost with a numeric port: %w", err)
	}
	if !addr.Addr().IsLoopback() {
		return netip.AddrPort{}, fmt.Errorf("web.listen must use a loopback address")
	}
	return addr, nil
}

// WebListenerAllowed requires explicit consent before serving plain HTTP.
func (s *Server) WebListenerAllowed() error {
	if s.Web.Enabled && !s.Web.AllowPlainLoopback {
		return fmt.Errorf("web listener refused: no TLS in v1; plain HTTP needs allow_plain_loopback = true on a single-user host")
	}
	return nil
}

// validatePath rejects symlink components and checks directory permissions.
// Directory paths themselves will contain the DB, backups, sockets or tokens.
func validatePath(field, path string, directory bool) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%s must be absolute", field)
	}
	if filepath.Clean(path) != path {
		return fmt.Errorf("%s must be clean", field)
	}
	if strings.ContainsAny(path, "?#") {
		return fmt.Errorf("%s must not contain ? or #", field)
	}
	parent := path
	// AIDEV-NOTE: init may not have created the configured directories yet.
	// Skip missing components, but still check every existing ancestor; stopping
	// at the first missing directory would hide a world-writable parent.
	// Use Lstat to reject symlinks: Stat follows the target, but the lexical
	// parent walk would miss permissions on ancestors of that target.
	for {
		info, err := os.Lstat(parent)
		switch {
		case os.IsNotExist(err):
		case err != nil:
			return fmt.Errorf("%s: inspect parent %q: %w", field, parent, err)
		case info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("%s: path component %q is a symlink", field, parent)
		case (directory || parent != path) && !info.IsDir():
			return fmt.Errorf("%s: parent %q is not a directory", field, parent)
		case (directory || parent != path) && info.Mode().Perm()&0o002 != 0:
			return fmt.Errorf("%s: parent %q is world-writable", field, parent)
		}
		if parent == filepath.Dir(parent) {
			return nil
		}
		parent = filepath.Dir(parent)
	}
}
