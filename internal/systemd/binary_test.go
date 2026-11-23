package systemd

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestStableBinary(t *testing.T) {
	root := t.TempDir()
	path := func(rel string) string { return filepath.Join(root, rel) }
	store := path("store")
	exe := path("store/build/bin/tkr")
	other := path("store/upgrade/bin/tkr")
	outside := path("opt/bin/tkr")
	for _, file := range []string{exe, other, outside} {
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, name string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, name); err != nil {
			t.Fatal(err)
		}
	}
	first, second := path("first"), path("second")
	link(exe, filepath.Join(first, "tkr"))
	link(exe, filepath.Join(second, "tkr"))
	link(path("store/build"), path("profile"))
	link(other, path("different/tkr"))
	link(exe, path("with space/tkr"))
	link(exe, path("renamed/other"))
	link(outside, path("linked-outside/tkr"))
	link(exe, path("upgradeable/tkr"))
	link(exe, path("store/alias/bin/tkr"))
	t.Setenv("PATH", first)
	t.Chdir(root)

	for _, tc := range []struct {
		name, exe string
		bins      []string
		want      string // Empty means ErrStoreBinary.
	}{
		{"outside store", outside, []string{path("linked-outside")}, outside},
		{"outside spelling preserved", root + "/store/../opt/bin/tkr", nil, root + "/store/../opt/bin/tkr"},
		{"missing first entry", exe, []string{path("missing"), second}, filepath.Join(second, "tkr")},
		{"symlinked directory", exe, []string{path("profile/bin")}, path("profile/bin/tkr")},
		{"first match", exe, []string{first, second}, filepath.Join(first, "tkr")},
		{"relative profile", exe, []string{"first"}, ""},
		{"different build", exe, []string{path("different")}, ""},
		{"skip unsuitable entries", exe, []string{path("different"), path("with space"), first}, filepath.Join(first, "tkr")},
		{"store profile", exe, []string{path("store/alias/bin")}, ""},
		{"unsafe profile", exe, []string{path("with space")}, ""},
		{"different name", exe, []string{path("renamed")}, ""},
		{"nil profiles", exe, nil, ""},
		{"empty profiles", exe, []string{}, ""},
		{"missing executable", path("store/missing/tkr"), []string{first}, ""},
		{"store prefix only", path("store-old/bin/tkr"), nil, path("store-old/bin/tkr")},
		{"symlinked executable", path("store/alias/bin/tkr"), []string{second}, filepath.Join(second, "tkr")},
		{"cleaned store prefix", root + "/other/../store/build/bin/tkr", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := stableBinary(tc.exe, tc.bins, store)
			if tc.want == "" {
				if !errors.Is(err, ErrStoreBinary) {
					t.Fatalf("got %q, %v; want ErrStoreBinary", got, err)
				}
			} else if got != tc.want || err != nil {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}

	t.Run("profile repointed after match", func(t *testing.T) {
		got, err := stableBinary(exe, []string{path("upgradeable")}, store)
		if err != nil {
			t.Fatal(err)
		}
		name := path("upgradeable/tkr")
		if err := os.Remove(name); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(other, name); err != nil {
			t.Fatal(err)
		}
		if target, err := filepath.EvalSymlinks(got); got != name || target != other || err != nil {
			t.Fatalf("returned %q, now resolves to %q, %v; want %q pointing to %q", got, target, err, name, other)
		}
	})
}

func TestNixProfileBins(t *testing.T) {
	const (
		homeBin         = "/home/test/.nix-profile/bin"
		stateBin        = "/state/nix/profile/bin"
		defaultStateBin = "/home/test/.local/state/nix/profile/bin"
		userBin         = "/etc/profiles/per-user/test/bin"
		defaultBin      = "/nix/var/nix/profiles/default/bin"
		systemBin       = "/run/current-system/sw/bin"
	)
	for _, tc := range []struct {
		name, home, state, user string
		want                    []string
	}{
		{"all", "/home/test", "/state", "test", []string{homeBin, stateBin, userBin, defaultBin, systemBin}},
		{"default state", "/home/test", "", "test", []string{homeBin, defaultStateBin, userBin, defaultBin, systemBin}},
		{"missing home", "", "/state", "test", []string{stateBin, userBin, defaultBin, systemBin}},
		{"missing user", "/home/test", "/state", "", []string{homeBin, stateBin, defaultBin, systemBin}},
		{"system only", "", "", "", []string{defaultBin, systemBin}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := NixProfileBins(tc.home, tc.state, tc.user); !slices.Equal(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
