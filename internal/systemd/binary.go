package systemd

import (
	"errors"
	"path/filepath"
	"strings"
)

// NixStoreDir is the directory whose contents can vanish after an upgrade.
const NixStoreDir = "/nix/store"

// ErrStoreBinary reports an executable in the Nix store that no Nix profile links to.
var ErrStoreBinary = errors.New("binary is in the Nix store")

// NixProfileBins returns the P§6 profile bin directories in order.
// Empty home, stateHome or user drop the entries that need them.
func NixProfileBins(home, stateHome, user string) []string {
	var bins []string
	if home != "" {
		bins = append(bins, filepath.Join(home, ".nix-profile", "bin"))
		if stateHome == "" {
			stateHome = filepath.Join(home, ".local", "state")
		}
	}
	if stateHome != "" {
		bins = append(bins, filepath.Join(stateHome, "nix", "profile", "bin"))
	}
	if user != "" {
		bins = append(bins, filepath.Join("/etc/profiles/per-user", user, "bin"))
	}
	return append(bins, "/nix/var/nix/profiles/default/bin", "/run/current-system/sw/bin")
}

// StableBinary returns the path systemd units should execute for exe (P§6).
func StableBinary(exe string, profileBins []string) (string, error) {
	return stableBinary(exe, profileBins, NixStoreDir)
}

func stableBinary(exe string, profileBins []string, storeDir string) (string, error) {
	storePrefix := filepath.Clean(storeDir) + "/"
	if !strings.HasPrefix(filepath.Clean(exe), storePrefix) {
		return exe, nil
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", ErrStoreBinary
	}
	// AIDEV-NOTE: P§6 deliberately ignores PATH. Only a matching profile
	// entry can follow an upgrade, so return its path deliberately unresolved.
	for _, dir := range profileBins {
		candidate := filepath.Join(dir, filepath.Base(exe))
		if CheckUnitPath("Binary", candidate) != nil || strings.HasPrefix(candidate, storePrefix) {
			continue
		}
		if target, err := filepath.EvalSymlinks(candidate); err == nil && target == resolved {
			return candidate, nil
		}
	}
	return "", ErrStoreBinary
}
