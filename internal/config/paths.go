// Package config resolves Taskronin's configuration and filesystem paths.
package config

import (
	"fmt"
	"path/filepath"
)

// Paths contains the resolved locations of Taskronin's files and directories.
type Paths struct {
	ConfigDir, DataDir, StateDir, RuntimeDir                     string
	ServerConfig, ClientConfig, Socket, OwnerToken, TokensDir    string
	DraftsDir, PendingDir, StagingDir, BackupDir, LedgerFile, DB string
}

// DefaultPaths resolves XDG paths without accessing the filesystem. Relative
// XDG values are ignored. An absolute home is required for any home fallback.
// Callers create and check private directories before using these paths.
func DefaultPaths(getenv func(string) string, home string) (Paths, error) {
	xdgDir := func(key, fallback string) (string, error) {
		if dir := getenv(key); filepath.IsAbs(dir) {
			return filepath.Join(dir, "taskronin"), nil
		}
		if !filepath.IsAbs(home) {
			return "", fmt.Errorf("%s is unset or relative and home is not absolute", key)
		}
		return filepath.Join(home, fallback, "taskronin"), nil
	}
	configDir, err := xdgDir("XDG_CONFIG_HOME", ".config")
	if err != nil {
		return Paths{}, err
	}
	dataDir, err := xdgDir("XDG_DATA_HOME", filepath.Join(".local", "share"))
	if err != nil {
		return Paths{}, err
	}
	stateDir, err := xdgDir("XDG_STATE_HOME", filepath.Join(".local", "state"))
	if err != nil {
		return Paths{}, err
	}
	p := Paths{
		ConfigDir:    configDir,
		StateDir:     stateDir,
		ServerConfig: filepath.Join(configDir, "server.toml"),
		ClientConfig: filepath.Join(configDir, "client.toml"),
		OwnerToken:   filepath.Join(stateDir, "owner.token"),
		TokensDir:    filepath.Join(stateDir, "tokens"),
		DraftsDir:    filepath.Join(stateDir, "drafts"),
		PendingDir:   filepath.Join(stateDir, "pending"),
	}
	socketDir := stateDir
	if runtimeDir := getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(runtimeDir) {
		p.RuntimeDir = filepath.Join(runtimeDir, "taskronin")
		socketDir = p.RuntimeDir
	}
	return p.WithServerPaths(dataDir, filepath.Join(socketDir, "sock"), filepath.Join(dataDir, "backups")), nil
}

// WithServerPaths returns paths rebased to the effective server configuration.
// All three arguments must be fully resolved, validated configuration values;
// empty arguments do not mean "keep the default".
func (p Paths) WithServerPaths(dataDir, socket, backupDir string) Paths {
	// AIDEV-NOTE: BF01 requires server/offline callers to apply configuration
	// overrides once, before opening any files, so the DB and its lock, ledger,
	// journals and staging directory cannot use competing data roots.
	p.DataDir = dataDir
	p.DB = filepath.Join(dataDir, "taskronin.db")
	p.LedgerFile = filepath.Join(dataDir, "purge-ledger")
	p.StagingDir = filepath.Join(dataDir, "staging")
	p.Socket = socket
	p.BackupDir = backupDir
	return p
}

// LockFile is the instance lock shared by the server and offline commands.
func (p Paths) LockFile() string { return filepath.Join(p.DataDir, "taskronin.lock") }

// SwapJournal is the recovery journal for a database swap.
func (p Paths) SwapJournal() string { return filepath.Join(p.DataDir, "swap.journal") }

// UnitsDir is the systemd user unit directory under the XDG config home.
func (p Paths) UnitsDir() string { return filepath.Join(filepath.Dir(p.ConfigDir), "systemd", "user") }

// TokenFile returns the token path for an already-validated token name.
func (p Paths) TokenFile(name string) string { return filepath.Join(p.TokensDir, name+".token") }

// ScrubJournal is the scrub recovery journal within the effective backup dir.
func ScrubJournal(backupDir string) string { return filepath.Join(backupDir, "scrub.journal") }

// MaxSocketPath leaves one byte for the NUL in Linux's 108-byte sun_path.
const MaxSocketPath = 107

// CheckSocketPath checks the encoded byte length, not the number of runes.
func CheckSocketPath(p string) error {
	if len(p) > MaxSocketPath {
		return fmt.Errorf("socket path too long (%d bytes, max 107): %s", len(p), p)
	}
	return nil
}
