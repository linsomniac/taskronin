package fsutil

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "private")
	for _, data := range [][]byte{[]byte("first\n"), bytes.Repeat([]byte("replacement\n"), 10000), nil} {
		if err := WriteFileAtomic(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("content length = %d, want %d; err = %v", len(got), len(data), err)
		}
		requireMode(t, path, 0o600)
		requireNoTemps(t, dir)
	}
	if err := WriteFileAtomic(path, []byte("read-only"), 0o400); err != nil {
		t.Fatal(err)
	}
	requireMode(t, path, 0o400)
}

func TestWriteFileAtomicFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "directory")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(target, []byte("cannot replace a directory"), 0o600); err == nil {
		t.Fatal("rename over directory succeeded")
	}
	requireNoTemps(t, dir)
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Fatalf("original directory lost: %v", err)
	}
	if err := WriteFileAtomic(filepath.Join(dir, "missing", "file"), nil, 0o600); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing parent: %v", err)
	}
}

func TestCheckPrivate(t *testing.T) {
	dir := t.TempDir()
	private := filepath.Join(dir, "private")
	if err := os.WriteFile(private, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	public := filepath.Join(dir, "public")
	if err := os.WriteFile(public, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(public, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(private, link); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		path string
		ok   bool
	}{
		{"private", private, true},
		{"public", public, false},
		{"symlink", link, false},
		{"directory", dir, false},
		{"missing", filepath.Join(dir, "missing"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := CheckPrivate(tc.path); (err == nil) != tc.ok {
				t.Fatalf("CheckPrivate = %v, want success %v", err, tc.ok)
			}
		})
	}
}

func TestEnsureDir0700(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "new", "nested")
	if err := EnsureDir0700(nested); err != nil {
		t.Fatal(err)
	}
	requireMode(t, nested, 0o700)
	requireMode(t, filepath.Dir(nested), 0o700)
	if err := EnsureDir0700(nested); err != nil {
		t.Fatalf("already-private directory: %v", err)
	}
	if err := os.Chmod(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir0700(nested); err != nil {
		t.Fatal(err)
	}
	requireMode(t, nested, 0o700)
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir0700(file); err == nil {
		t.Fatal("accepted a regular file")
	}
	if err := os.Chmod(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(nested, link); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir0700(link); err == nil {
		t.Fatal("accepted a symlink")
	}
	requireMode(t, nested, 0o755)
}

func TestForeignOwner(t *testing.T) {
	// / is owned by root on this Linux host; no privilege changes are needed.
	if os.Geteuid() == 0 {
		t.Skip("requires an unprivileged effective UID")
	}
	if err := EnsureDir0700("/"); err == nil {
		t.Fatal("accepted a directory owned by another user")
	}
	// /proc/version is a regular root-owned file. Its mode also disallows it.
	if err := CheckPrivate("/proc/version"); err == nil {
		t.Fatal("accepted a file owned by another user")
	}
}

func TestFsyncDir(t *testing.T) {
	dir := t.TempDir()
	if err := FsyncDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := FsyncDir(filepath.Join(dir, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing directory: %v", err)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := FsyncDir(file); err == nil {
		t.Fatal("accepted a regular file")
	}
}

func TestSetUmask(t *testing.T) {
	// Umask is process-wide: this test and its package never run in parallel.
	old := SetUmask(0o077)
	defer SetUmask(old)
	path := filepath.Join(t.TempDir(), "masked")
	if err := os.WriteFile(path, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	requireMode(t, path, 0o600)
	if got := SetUmask(0o077); got != 0o077 {
		t.Fatalf("previous mask = %#o", got)
	}
}

func requireMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("%s mode = %o, want %o", path, info.Mode().Perm(), want)
	}
}

func requireNoTemps(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".tkr-tmp-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files = %v, err = %v", matches, err)
	}
}
