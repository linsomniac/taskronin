package editor

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/linsomniac/taskronin/internal/clock"
	"github.com/linsomniac/taskronin/internal/fsutil"
)

// Drafts stores editor buffers privately under Dir. A nil Clock uses real time.
type Drafts struct {
	Dir   string
	Clock clock.Clock
}

// Draft is a buffer retained until its mutation is confirmed or discarded.
type Draft struct{ Path string }

// New durably writes initial to a private <UTC timestamp>-<8 hex>.md file.
func (d Drafts) New(initial string) (*Draft, error) {
	if err := fsutil.EnsureDir0700(d.Dir); err != nil {
		return nil, fmt.Errorf("create drafts directory: %w", err)
	}
	clk := d.Clock
	if clk == nil {
		clk = clock.Real{}
	}
	stamp := clk.Now().UTC().Format("20060102T150405Z")
	for {
		var suffix [4]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return nil, fmt.Errorf("generate draft name: %w", err)
		}
		path := filepath.Join(d.Dir, fmt.Sprintf("%s-%x.md", stamp, suffix))
		// AIDEV-NOTE: reserve the name exclusively before the atomic write so
		// a random collision cannot overwrite an unconfirmed draft.
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // G304: generated draft name reserved exclusively under the user's state dir
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("create draft: %w", err)
		}
		if err := file.Close(); err != nil {
			_ = os.Remove(path)
			return nil, fmt.Errorf("close draft reservation: %w", err)
		}
		if err := fsutil.WriteFileAtomic(path, []byte(initial), 0o600); err != nil {
			_ = os.Remove(path)
			return nil, fmt.Errorf("write draft: %w", err)
		}
		return &Draft{Path: path}, nil
	}
}

// Discard removes the buffer. Call only after confirmation or explicit discard.
func (d *Draft) Discard() error {
	if err := os.Remove(d.Path); err != nil {
		return fmt.Errorf("discard draft: %w", err)
	}
	return fsutil.FsyncDir(filepath.Dir(d.Path))
}

// FormatTaskBuffer places the title on line one and separates notes with a blank.
func FormatTaskBuffer(title, notes string) string { return title + "\n\n" + notes }

// ParseTaskBuffer trims the first line as the title and skips one blank separator
// line, preserving the remaining notes verbatim.
func ParseTaskBuffer(s string) (title, notes string, err error) {
	title, notes, _ = strings.Cut(s, "\n")
	title = strings.TrimSpace(title)
	if title == "" {
		return "", "", errors.New("empty title")
	}
	if line, rest, _ := strings.Cut(notes, "\n"); strings.TrimSpace(line) == "" {
		notes = rest
	}
	return title, notes, nil
}

// ParseLogBuffer uses the first non-empty line as the title, then parses notes
// like ParseTaskBuffer.
func ParseLogBuffer(s string) (title, notes string, err error) {
	// AIDEV-NOTE: BF19 / A§9.4 lets log -e skip leading blank lines. Task
	// edits deliberately reject an empty first line instead.
	for {
		line, rest, found := strings.Cut(s, "\n")
		if strings.TrimSpace(line) != "" || !found {
			return ParseTaskBuffer(s)
		}
		s = rest
	}
}
