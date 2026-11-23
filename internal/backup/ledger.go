// Package backup provides durable recovery files and the purge ledger.
package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/fsutil"
)

const (
	// LedgerPending records the intent to purge an exact set of entities.
	LedgerPending string = "pending"
	// LedgerComplete records completion of a purge and its recovery-file step.
	LedgerComplete string = "complete"
)

// LedgerRecord is one checksummed JSON line describing a purge intent or completion.
type LedgerRecord struct {
	V             int              `json:"v"`
	Rec           string           `json:"rec"` // random 128-bit hex
	At            int64            `json:"at"`
	State         string           `json:"state"` // "pending" | "complete"
	Tasks         []domain.TaskID  `json:"tasks,omitempty"`
	Entries       []domain.EntryID `json:"entries,omitempty"`
	RecoveryFiles string           `json:"recovery_files,omitempty"` // complete lines: "deleted", "kept", or "none" (H1)
	Sum           string           `json:"sum"`                      // hex SHA-256 of this record's JSON encoding with Sum = ""
}

// LedgerHealth identifies complete corrupt lines skipped while loading a ledger.
type LedgerHealth struct {
	Damaged      bool
	DroppedLines []int // 1-based line numbers; retained on disk until Repair
}

// Ledger serializes access to a file and its cached valid records. OpenLedger
// must be called under the application's instance lock; Path must not be changed.
type Ledger struct {
	Path      string
	mu        sync.Mutex
	records   []LedgerRecord
	health    LedgerHealth
	appendErr error
}

// OpenLedger loads valid records, retaining complete corrupt lines on disk and
// truncating only an unterminated final line. A missing file is empty and healthy.
func OpenLedger(path string) (*Ledger, LedgerHealth, error) {
	l := &Ledger{Path: path}
	data, err := os.ReadFile(path) //nolint:gosec // G304: the configured ledger path under the instance lock.
	if errors.Is(err, fs.ErrNotExist) {
		return l, LedgerHealth{}, nil
	}
	if err != nil {
		return nil, LedgerHealth{}, fmt.Errorf("read purge ledger: %w", err)
	}
	end := bytes.LastIndexByte(data, '\n') + 1
	if end < len(data) {
		// AIDEV-NOTE: torn-tail truncation must finish and fsync before an append.
		// A complete bad line is corruption, even at EOF, and must stay on disk.
		f, err := os.OpenFile(path, os.O_WRONLY, 0o600) //nolint:gosec // G304: the configured ledger path for torn-tail truncation.
		if err != nil {
			return nil, LedgerHealth{}, fmt.Errorf("open torn purge ledger: %w", err)
		}
		defer func() { _ = f.Close() }()
		if err := f.Truncate(int64(end)); err != nil {
			return nil, LedgerHealth{}, fmt.Errorf("truncate torn purge ledger: %w", err)
		}
		if err := f.Sync(); err != nil {
			return nil, LedgerHealth{}, fmt.Errorf("sync truncated purge ledger: %w", err)
		}
		if err := f.Close(); err != nil {
			return nil, LedgerHealth{}, fmt.Errorf("close truncated purge ledger: %w", err)
		}
	}
	if end > 0 {
		lineNumber := 0
		for line := range bytes.SplitSeq(data[:end-1], []byte{'\n'}) {
			lineNumber++
			var r LedgerRecord
			parseErr := json.Unmarshal(line, &r)
			sum, err := ledgerSum(r)
			if err != nil {
				return nil, LedgerHealth{}, err
			}
			if parseErr != nil || sum != r.Sum {
				l.health.Damaged = true
				l.health.DroppedLines = append(l.health.DroppedLines, lineNumber)
				continue
			}
			l.records = append(l.records, r)
		}
	}
	return l, l.Health(), nil
}

// Append computes the checksum and durably appends one record. A damaged ledger
// refuses both pending and complete records until explicitly repaired.
func (l *Ledger) Append(r LedgerRecord) (err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.health.Damaged {
		return errors.New("purge ledger is damaged; run `tkr purge --repair-ledger`")
	}
	if l.appendErr != nil {
		return fmt.Errorf("purge ledger has an uncertain append; reopen or repair it before appending: %w", l.appendErr)
	}
	sum, err := ledgerSum(r)
	if err != nil {
		return err
	}
	r.Sum = sum
	line, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("encode purge ledger record: %w", err)
	}
	line = append(line, '\n')
	f, err := os.OpenFile(l.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open purge ledger for append: %w", err)
	}
	// AIDEV-NOTE: an I/O failure may leave a partial line or uncertain durability.
	// Refuse retries until reopen/repair, and keep every fully written record so
	// a repair cannot discard an intent merely because its subsequent fsync failed.
	defer func() {
		_ = f.Close()
		if err != nil {
			l.appendErr = err
		}
	}()
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("append purge ledger: %w", err)
	}
	l.records = append(l.records, cloneRecord(r))
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync appended purge ledger: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close appended purge ledger: %w", err)
	}
	if err := fsutil.FsyncDir(filepath.Dir(l.Path)); err != nil {
		return fmt.Errorf("sync purge ledger directory: %w", err)
	}
	return nil
}

// Records returns independent copies of all valid records in file order.
func (l *Ledger) Records() ([]LedgerRecord, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	records := make([]LedgerRecord, len(l.records))
	for i, r := range l.records {
		records[i] = cloneRecord(r)
	}
	return records, nil
}

// Health returns a copy of the ledger's corruption status.
func (l *Ledger) Health() LedgerHealth {
	l.mu.Lock()
	defer l.mu.Unlock()
	return LedgerHealth{Damaged: l.health.Damaged, DroppedLines: slices.Clone(l.health.DroppedLines)}
}

// Pending returns pending records with no later complete line for the same Rec,
// in file order. Timestamps do not determine completion order.
func (l *Ledger) Pending() ([]LedgerRecord, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	complete := make(map[string]bool)
	var pending []LedgerRecord
	for i := len(l.records) - 1; i >= 0; i-- {
		r := l.records[i]
		if r.State == LedgerComplete {
			complete[r.Rec] = true
		} else if r.State == LedgerPending && !complete[r.Rec] {
			pending = append(pending, cloneRecord(r))
		}
	}
	slices.Reverse(pending)
	return pending, nil
}

// Repair atomically rewrites only valid records, returning the dropped 1-based
// line numbers. Health is cleared only after the replacement is durable.
func (l *Ledger) Repair() ([]int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	for _, r := range l.records {
		if err := encoder.Encode(r); err != nil {
			return nil, fmt.Errorf("encode repaired purge ledger: %w", err)
		}
	}
	if err := fsutil.WriteFileAtomic(l.Path, data.Bytes(), 0o600); err != nil {
		return nil, fmt.Errorf("repair purge ledger: %w", err)
	}
	dropped := slices.Clone(l.health.DroppedLines)
	l.health = LedgerHealth{}
	l.appendErr = nil
	return dropped, nil
}

func ledgerSum(r LedgerRecord) (string, error) {
	r.Sum = ""
	data, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("encode purge ledger checksum: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func cloneRecord(r LedgerRecord) LedgerRecord {
	r.Tasks = slices.Clone(r.Tasks)
	r.Entries = slices.Clone(r.Entries)
	return r
}
