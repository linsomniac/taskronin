package editor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/linsomniac/taskronin/internal/clock"
)

func TestDraftsNew(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "missing directory"
		if existing {
			name = "existing directory"
		}
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "drafts")
			if existing {
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			clk := clock.NewFake(time.Date(2026, 10, 28, 16, 0, 0, 0, time.UTC))
			store := Drafts{Dir: dir, Clock: clk}
			d, err := store.New("Hello\n\nworld")
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Dir(d.Path) != dir || !regexp.MustCompile(`^20261028T160000Z-[0-9a-f]{8}\.md$`).MatchString(filepath.Base(d.Path)) {
				t.Fatalf("unexpected draft path %q", d.Path)
			}
			assertDraft(t, d, "Hello\n\nworld")
			info, err := os.Stat(dir)
			if err != nil || info.Mode().Perm() != 0o700 {
				t.Fatalf("draft directory info = %v, %v; want mode 0700", info, err)
			}
			other, err := store.New("other")
			if err != nil || other.Path == d.Path {
				t.Fatalf("second draft = %v, %v; want distinct path", other, err)
			}
			assertDraft(t, d, "Hello\n\nworld")
			assertDraft(t, other, "other")
		})
	}
}

func TestDraftClock(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 10, 28, 10, 0, 0, 0, time.FixedZone("test", -6*60*60)))
	d, err := (Drafts{Dir: t.TempDir(), Clock: clk}).New("")
	if err != nil || !strings.HasPrefix(filepath.Base(d.Path), "20261028T160000Z-") {
		t.Fatalf("UTC draft = %v, %v", d, err)
	}
	before := (clock.Real{}).Now().UTC().Truncate(time.Second)
	d, err = (Drafts{Dir: t.TempDir()}).New("")
	if err != nil {
		t.Fatal(err)
	}
	after := (clock.Real{}).Now().UTC()
	stamp, err := time.Parse("20060102T150405Z", strings.Split(filepath.Base(d.Path), "-")[0])
	if err != nil || stamp.Before(before) || stamp.After(after) {
		t.Fatalf("nil clock timestamp = %v, %v; want between %v and %v", stamp, err, before, after)
	}
}

func TestDraftDirectoryErrors(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{file, link, ""} {
		if d, err := (Drafts{Dir: dir}).New("private"); err == nil || d != nil {
			t.Fatalf("New with directory %q = %v, %v; want error", dir, d, err)
		}
	}
}

func TestDraftDiscard(t *testing.T) {
	d := newDraft(t)
	if err := d.Discard(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discarded file still exists: %v", err)
	}
	if err := d.Discard(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discard missing file = %v", err)
	}
}

func TestEdit(t *testing.T) {
	t.Run("write and keep until confirmed", func(t *testing.T) {
		t.Setenv("TKR_HELPER_EDITOR", "write:Hello\n\nworld")
		d := newDraft(t)
		got, err := Edit(t.Context(), helperArgv(), d, nil, io.Discard, io.Discard)
		if err != nil || got != "Hello\n\nworld" {
			t.Fatalf("Edit = %q, %v", got, err)
		}
		assertDraft(t, d, got)
	})
	t.Run("exit 3 keeps draft", func(t *testing.T) {
		t.Setenv("TKR_HELPER_EDITOR", "exit:3")
		d := newDraft(t)
		got, err := Edit(t.Context(), helperArgv(), d, nil, io.Discard, io.Discard)
		if got != "" || err == nil || err.Error() != "editor exited with status 3" {
			t.Fatalf("Edit = %q, %v; want exit status 3", got, err)
		}
		assertDraft(t, d, "original")
	})
	t.Run("missing executable keeps draft", func(t *testing.T) {
		d := newDraft(t)
		_, err := Edit(t.Context(), []string{filepath.Join(t.TempDir(), "missing")}, d, nil, io.Discard, io.Discard)
		if err == nil {
			t.Fatal("missing executable succeeded")
		}
		assertDraft(t, d, "original")
	})
	t.Run("cancelled context keeps draft", func(t *testing.T) {
		t.Setenv("TKR_HELPER_EDITOR", "write:changed")
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		d := newDraft(t)
		_, err := Edit(ctx, helperArgv(), d, nil, io.Discard, io.Discard)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Edit with cancelled context = %v", err)
		}
		assertDraft(t, d, "original")
	})
	t.Run("read error", func(t *testing.T) {
		t.Setenv("TKR_HELPER_EDITOR", "remove")
		_, err := Edit(t.Context(), helperArgv(), newDraft(t), nil, io.Discard, io.Discard)
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Edit missing draft = %v", err)
		}
	})
	t.Run("empty argv", func(t *testing.T) {
		for _, argv := range [][]string{nil, {""}} {
			d := newDraft(t)
			if _, err := Edit(t.Context(), argv, d, nil, io.Discard, io.Discard); err == nil {
				t.Fatal("empty editor command succeeded")
			}
			assertDraft(t, d, "original")
		}
	})
}

func TestEditLiteralArgumentsAndStreams(t *testing.T) {
	t.Setenv("TKR_HELPER_EDITOR", "inspect")
	dir := filepath.Join(t.TempDir(), "drafts with spaces; $HOME")
	d, err := (Drafts{Dir: dir}).New("original")
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "must-not-exist")
	literal := "$(touch " + marker + "); $HOME `id` *.md"
	t.Setenv("TKR_HELPER_ARGUMENT", literal)
	t.Setenv("TKR_HELPER_PATH", d.Path)
	argv := append(helperArgv(), literal, "spare")
	wantArgv := slices.Clone(argv)
	var stdout, stderr bytes.Buffer
	got, err := Edit(t.Context(), argv[:len(argv)-1], d, strings.NewReader("from stdin"), &stdout, &stderr)
	if err != nil || got != "from stdin" {
		t.Fatalf("Edit = %q, %v; stdout %q; stderr %q", got, err, stdout.String(), stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "editor stdout\n") || stderr.String() != "editor stderr\n" {
		t.Fatalf("streams = %q, %q", stdout.String(), stderr.String())
	}
	if !slices.Equal(argv, wantArgv) {
		t.Fatalf("Edit mutated caller's argv: %q", argv)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shell command ran: %v", err)
	}
	assertDraft(t, d, "from stdin")
}

func TestHelperEditor(t *testing.T) {
	mode := os.Getenv("TKR_HELPER_EDITOR")
	if mode == "" {
		return
	}
	path := os.Args[len(os.Args)-1]
	switch {
	case strings.HasPrefix(mode, "write:"):
		if err := os.WriteFile(path, []byte(strings.TrimPrefix(mode, "write:")), 0o600); err != nil {
			t.Fatal(err)
		}
	case mode == "exit:3":
		// AIDEV-NOTE: only the helper subprocess terminates here; production
		// code returns errors and never exits the calling process.
		syscall.Exit(3)
	case mode == "remove":
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	case mode == "inspect":
		if path != os.Getenv("TKR_HELPER_PATH") || os.Args[len(os.Args)-2] != os.Getenv("TKR_HELPER_ARGUMENT") {
			t.Fatalf("arguments changed: %q", os.Args)
		}
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(os.Stdout, "editor stdout\n"); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(os.Stderr, "editor stderr\n"); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}

func TestTaskBuffer(t *testing.T) {
	if got := FormatTaskBuffer("T", "N"); got != "T\n\nN" {
		t.Fatalf("FormatTaskBuffer = %q", got)
	}
	for _, tt := range []struct{ input, title, notes string }{
		{"Title\n\nNotes\nmore", "Title", "Notes\nmore"},
		{"Title\n", "Title", ""},
		{"Title", "Title", ""},
		{"  Title \t\nNotes", "Title", "Notes"},
		{"Title\n\n\nNotes\n", "Title", "\nNotes\n"},
		{"Title\r\n\r\nNotes\r\n", "Title", "Notes\r\n"},
		{"Title\n \t\n  Notes \n", "Title", "  Notes \n"},
	} {
		t.Run(tt.input, func(t *testing.T) {
			title, notes, err := ParseTaskBuffer(tt.input)
			if err != nil || title != tt.title || notes != tt.notes {
				t.Fatalf("ParseTaskBuffer = %q, %q, %v; want %q, %q", title, notes, err, tt.title, tt.notes)
			}
		})
	}
	for _, input := range []string{"", "\n\nx", " \t\nTitle"} {
		if _, _, err := ParseTaskBuffer(input); err == nil || err.Error() != "empty title" {
			t.Fatalf("ParseTaskBuffer(%q) = %v; want empty title", input, err)
		}
	}
}

func TestLogBuffer(t *testing.T) {
	for _, input := range []string{"Title\n\nNotes\nmore", "\n\nTitle\n\nNotes\nmore", " \t\n\r\n Title \n\nNotes\nmore"} {
		title, notes, err := ParseLogBuffer(input)
		if err != nil || title != "Title" || notes != "Notes\nmore" {
			t.Fatalf("ParseLogBuffer(%q) = %q, %q, %v", input, title, notes, err)
		}
	}
	for _, input := range []string{"", "\n\n", " \t\n\r\n"} {
		if _, _, err := ParseLogBuffer(input); err == nil || err.Error() != "empty title" {
			t.Fatalf("ParseLogBuffer(%q) = %v; want empty title", input, err)
		}
	}
}

func helperArgv() []string {
	return []string{os.Args[0], "-test.run=^TestHelperEditor$", "--"}
}

func newDraft(t *testing.T) *Draft {
	t.Helper()
	d, err := (Drafts{Dir: t.TempDir()}).New("original")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func assertDraft(t *testing.T, d *Draft, want string) {
	t.Helper()
	data, err := os.ReadFile(d.Path)
	if err != nil || string(data) != want {
		t.Fatalf("draft content = %q, %v; want %q", data, err, want)
	}
	info, err := os.Stat(d.Path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("draft info = %v, %v; want regular 0600", info, err)
	}
}
