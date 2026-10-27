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
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"taskronin/internal/domain"
)

func TestPRG08(t *testing.T) {
	l, h, err := OpenLedger(filepath.Join(t.TempDir(), "purge-ledger"))
	if err != nil || h.Damaged {
		t.Fatalf("open: health = %+v, err = %v", h, err)
	}
	r := sampleRecord()
	r.Sum = "caller-supplied checksum must be replaced"
	if err := l.Append(r); err != nil {
		t.Fatal(err)
	}
	r.State, r.RecoveryFiles = LedgerComplete, "deleted"
	if err := l.Append(r); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, l.Path)
	re := regexp.MustCompile(`^\{"v":1,"rec":"[0-9a-f]{32}","at":\d+,"state":"pending","tasks":\[42\],"entries":\[101\],"sum":"[0-9a-f]{64}"\}$`)
	if len(lines) != 2 || !re.MatchString(lines[0]) || !strings.Contains(lines[1], `"state":"complete"`) ||
		!strings.Contains(lines[1], `"recovery_files":"deleted"`) {
		t.Fatalf("ledger = %q", lines)
	}
	for _, line := range lines {
		var stored LedgerRecord
		if err := json.Unmarshal([]byte(line), &stored); err != nil {
			t.Fatal(err)
		}
		if stored.Sum != checksum(t, stored) || stored.Rec != r.Rec {
			t.Fatalf("invalid stored record: %+v", stored)
		}
	}
	info, err := os.Stat(l.Path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("ledger mode: %v, %v", info, err)
	}
	reopened, h, err := OpenLedger(l.Path)
	if err != nil || h.Damaged {
		t.Fatalf("reopen: %+v, %v", h, err)
	}
	if got, err := reopened.Records(); err != nil || len(got) != 2 {
		t.Fatalf("reopened records = %v, %v", got, err)
	}
}

func TestPRG13(t *testing.T) {
	r := sampleRecord()
	valid := recordLine(t, r)
	for _, tc := range []struct {
		name string
		data string
		want string
	}{
		{"half line", valid + `{"v":1,"rec":"partial`, valid},
		{"only torn line", `{"v":1`, ""},
		{"complete JSON without newline", valid + strings.TrimSuffix(valid, "\n"), valid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeLedger(t, tc.data)
			l, h, err := OpenLedger(path)
			if err != nil || h.Damaged || len(h.DroppedLines) != 0 {
				t.Fatalf("open: %+v, %v", h, err)
			}
			requireFile(t, path, tc.want)
			before, err := l.Records()
			if err != nil || len(before) != strings.Count(tc.want, "\n") {
				t.Fatalf("records = %+v, %v", before, err)
			}
			if err := l.Append(r); err != nil {
				t.Fatal(err)
			}
			requireFile(t, path, tc.want+valid)
		})
	}
}

func TestPRG14(t *testing.T) {
	valid := recordLine(t, sampleRecord())
	wrong := strings.Replace(valid, `"tasks":[42]`, `"tasks":[43]`, 1)
	for _, tc := range []struct {
		name    string
		data    string
		dropped []int
		count   int
	}{
		{"wrong checksum middle", valid + wrong + valid, []int{2}, 2},
		{"unparsable middle", valid + "not json\n" + valid, []int{2}, 2},
		{"complete bad final line", valid + wrong, []int{2}, 1},
		{"blank first line", "\n" + valid, []int{1}, 1},
		{"multiple and torn tail", "bad\n" + valid + "{}\n" + `{"torn":`, []int{1, 3}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeLedger(t, tc.data)
			l, h, err := OpenLedger(path)
			if err != nil || !h.Damaged || !slices.Equal(h.DroppedLines, tc.dropped) {
				t.Fatalf("open: %+v, %v", h, err)
			}
			wantDisk := tc.data[:strings.LastIndex(tc.data, "\n")+1]
			requireFile(t, path, wantDisk)
			got, err := l.Records()
			if err != nil || len(got) != tc.count {
				t.Fatalf("valid records = %+v, %v", got, err)
			}
			for _, r := range got {
				if !reflect.DeepEqual(r, signedRecord(t, sampleRecord())) {
					t.Fatalf("unexpected valid record: %+v", r)
				}
			}
			for _, state := range []string{LedgerPending, LedgerComplete} {
				r := sampleRecord()
				r.State = state
				if err := l.Append(r); err == nil || !strings.Contains(err.Error(), "--repair-ledger") {
					t.Fatalf("damaged append = %v", err)
				}
			}
			requireFile(t, path, wantDisk)
			if got := l.Health(); !reflect.DeepEqual(got, h) {
				t.Fatalf("health = %+v, want %+v", got, h)
			}
		})
	}
}

func TestPRG15(t *testing.T) {
	valid := recordLine(t, sampleRecord())
	path := writeLedger(t, "bad\n"+valid+"{}\n"+valid)
	l, _, err := OpenLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	dropped, err := l.Repair()
	if err != nil || !slices.Equal(dropped, []int{1, 3}) {
		t.Fatalf("repair = %v, %v", dropped, err)
	}
	requireFile(t, path, valid+valid)
	if h := l.Health(); h.Damaged || len(h.DroppedLines) != 0 {
		t.Fatalf("health after repair = %+v", h)
	}
	if err := l.Append(sampleRecord()); err != nil {
		t.Fatal(err)
	}
	requireFile(t, path, valid+valid+valid)
	reopened, h, err := OpenLedger(path)
	if err != nil || h.Damaged {
		t.Fatalf("reopen = %+v, %v", h, err)
	}
	if got, err := reopened.Records(); err != nil || len(got) != 3 {
		t.Fatalf("repaired records = %+v, %v", got, err)
	}
	if dropped, err := l.Repair(); err != nil || len(dropped) != 0 {
		t.Fatalf("repeated repair = %v, %v", dropped, err)
	}
}

func TestLedgerPending(t *testing.T) {
	l, _, err := OpenLedger(filepath.Join(t.TempDir(), "purge-ledger"))
	if err != nil {
		t.Fatal(err)
	}
	r := sampleRecord()
	if err := l.Append(r); err != nil {
		t.Fatal(err)
	}
	if got, err := l.Pending(); err != nil || len(got) != 1 || got[0].Rec != r.Rec {
		t.Fatalf("pending = %+v, %v", got, err)
	}
	r.State, r.RecoveryFiles = LedgerComplete, "none"
	if err := l.Append(r); err != nil {
		t.Fatal(err)
	}
	if got, err := l.Pending(); err != nil || len(got) != 0 {
		t.Fatalf("completed pending = %+v, %v", got, err)
	}
	// Only a later completion satisfies an intent; timestamps do not order it.
	r.State, r.RecoveryFiles, r.At = LedgerPending, "", r.At-1000
	if err := l.Append(r); err != nil {
		t.Fatal(err)
	}
	other := sampleRecord()
	other.Rec = strings.Repeat("b", 32)
	if err := l.Append(other); err != nil {
		t.Fatal(err)
	}
	if got, err := l.Pending(); err != nil || len(got) != 2 || got[0].Rec != r.Rec || got[1].Rec != other.Rec {
		t.Fatalf("later intents = %+v, %v", got, err)
	}
}

func TestLedgerMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "purge-ledger")
	l, h, err := OpenLedger(path)
	if err != nil || h.Damaged || len(h.DroppedLines) != 0 || l.Path != path {
		t.Fatalf("missing ledger = %+v, %v", h, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("open created the file: %v", err)
	}
	if got, err := l.Records(); err != nil || len(got) != 0 {
		t.Fatalf("missing records = %+v, %v", got, err)
	}
	if got, err := l.Pending(); err != nil || len(got) != 0 {
		t.Fatalf("missing pending = %+v, %v", got, err)
	}
	if err := l.Append(sampleRecord()); err != nil {
		t.Fatal(err)
	}
	requireFile(t, path, recordLine(t, sampleRecord()))
}

func TestLedgerCopies(t *testing.T) {
	l, _, err := OpenLedger(filepath.Join(t.TempDir(), "purge-ledger"))
	if err != nil {
		t.Fatal(err)
	}
	r := sampleRecord()
	if err := l.Append(r); err != nil {
		t.Fatal(err)
	}
	r.Tasks[0], r.Entries[0] = 999, 999
	for _, get := range []func() ([]LedgerRecord, error){l.Records, l.Pending} {
		records, err := get()
		if err != nil || len(records) != 1 || records[0].Tasks[0] != 42 || records[0].Entries[0] != 101 {
			t.Fatalf("aliased append input: %+v, %v", records, err)
		}
		records[0].Tasks[0], records[0].Entries[0], records[0].State = 999, 999, LedgerComplete
	}
	if _, err := l.Repair(); err != nil {
		t.Fatal(err)
	}
	requireFile(t, l.Path, recordLine(t, sampleRecord()))
	bad, h, err := OpenLedger(writeLedger(t, "bad\n"))
	if err != nil {
		t.Fatal(err)
	}
	h.DroppedLines[0] = 999
	h = bad.Health()
	if !slices.Equal(h.DroppedLines, []int{1}) {
		t.Fatalf("aliased open health: %+v", h)
	}
	h.DroppedLines[0] = 999
	if got := bad.Health(); !slices.Equal(got.DroppedLines, []int{1}) {
		t.Fatalf("aliased returned health: %+v", got)
	}
}

func TestLedgerConcurrent(t *testing.T) {
	l, _, err := OpenLedger(filepath.Join(t.TempDir(), "purge-ledger"))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 24 {
		wg.Go(func() {
			r := sampleRecord()
			r.Rec = fmt.Sprintf("%032x", i)
			if err := l.Append(r); err != nil {
				t.Error(err)
			}
			if _, err := l.Records(); err != nil {
				t.Error(err)
			}
			if _, err := l.Pending(); err != nil {
				t.Error(err)
			}
			if _, err := l.Repair(); err != nil {
				t.Error(err)
			}
			if l.Health().Damaged {
				t.Error("concurrent ledger damaged")
			}
		})
	}
	wg.Wait()
	reopened, h, err := OpenLedger(l.Path)
	if err != nil || h.Damaged {
		t.Fatalf("reopen: %+v, %v", h, err)
	}
	records, err := reopened.Records()
	if err != nil || len(records) != 24 {
		t.Fatalf("records = %d, %v", len(records), err)
	}
	seen := make(map[string]bool)
	for _, r := range records {
		if seen[r.Rec] {
			t.Fatalf("duplicate record %s", r.Rec)
		}
		seen[r.Rec] = true
	}
}

func TestLedgerIOErrors(t *testing.T) {
	if _, _, err := OpenLedger(t.TempDir()); err == nil {
		t.Fatal("opened a directory as a ledger")
	}
	l, _, err := OpenLedger(filepath.Join(t.TempDir(), "missing", "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(sampleRecord()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("append with missing parent: %v", err)
	}
	if got, err := l.Records(); err != nil || len(got) != 0 {
		t.Fatalf("failed append cached: %+v, %v", got, err)
	}
	path := writeLedger(t, "bad\n"+recordLine(t, sampleRecord()))
	l, _, err = OpenLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Repair(); err == nil {
		t.Fatal("repair over directory succeeded")
	}
	if h := l.Health(); !h.Damaged || !slices.Equal(h.DroppedLines, []int{1}) {
		t.Fatalf("failed repair cleared health: %+v", h)
	}
}

func TestLedgerAppendFailureRecovery(t *testing.T) {
	for _, tc := range []struct {
		name    string
		device  string
		records int
	}{
		{"write fails", "/dev/full", 0},
		{"sync fails after full write", "/dev/null", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "purge-ledger")
			l, _, err := OpenLedger(path)
			if err != nil {
				t.Fatal(err)
			}
			// Linux devices provide real write/fsync failures without test hooks.
			if err := os.Symlink(tc.device, path); err != nil {
				t.Fatal(err)
			}
			if err := l.Append(sampleRecord()); err == nil {
				t.Fatal("append did not report the I/O failure")
			}
			if got, err := l.Records(); err != nil || len(got) != tc.records {
				t.Fatalf("records after failed append = %d, want %d; err = %v", len(got), tc.records, err)
			}
			// Once writes resume, a retry must still refuse: a partial line may
			// remain on disk until the caller explicitly repairs or reopens it.
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(`{"torn":`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := l.Append(sampleRecord()); err == nil {
				t.Fatal("retried append after an uncertain write")
			}
			requireFile(t, path, `{"torn":`)
			if _, err := l.Repair(); err != nil {
				t.Fatal(err)
			}
			requireFile(t, path, strings.Repeat(recordLine(t, sampleRecord()), tc.records))
			if err := l.Append(sampleRecord()); err != nil {
				t.Fatal(err)
			}
			requireFile(t, path, strings.Repeat(recordLine(t, sampleRecord()), tc.records+1))
		})
	}
}

func TestLedgerLongLine(t *testing.T) {
	r := sampleRecord()
	r.Tasks = make([]domain.TaskID, 40000)
	for i := range r.Tasks {
		r.Tasks[i] = domain.TaskID(i + 1)
	}
	l, h, err := OpenLedger(writeLedger(t, recordLine(t, r)))
	if err != nil || h.Damaged {
		t.Fatalf("long line: %+v, %v", h, err)
	}
	if got, err := l.Records(); err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], signedRecord(t, r)) {
		t.Fatalf("long record was not retained: %v", err)
	}
}

func FuzzLedgerLines(f *testing.F) {
	valid := recordLine(f, sampleRecord())
	for _, seed := range []string{"", "\n", "null\n", "{}\n", "bad\n", valid, valid + "torn", "bad\n" + valid, valid + "{}\n" + valid, strings.TrimSuffix(valid, "\n"), "\xff\x00\n"} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		path := writeLedger(t, string(data))
		end := bytes.LastIndexByte(data, '\n') + 1
		complete := data[:end]
		var want []LedgerRecord
		var dropped []int
		if end > 0 {
			for i, line := range bytes.Split(complete[:end-1], []byte{'\n'}) {
				var r LedgerRecord
				if json.Unmarshal(line, &r) != nil || r.Sum != checksum(t, r) {
					dropped = append(dropped, i+1)
				} else {
					want = append(want, r)
				}
			}
		}
		l, h, err := OpenLedger(path)
		if err != nil || h.Damaged != (len(dropped) > 0) || !slices.Equal(h.DroppedLines, dropped) {
			t.Fatalf("health = %+v, want dropped %v; err = %v", h, dropped, err)
		}
		requireFile(t, path, string(complete))
		got, err := l.Records()
		if err != nil || len(got) != len(want) {
			t.Fatalf("records = %d, want %d; err = %v", len(got), len(want), err)
		}
		for i := range want {
			if !reflect.DeepEqual(got[i], want[i]) {
				t.Fatalf("record %d = %+v, want %+v", i, got[i], want[i])
			}
		}
	})
}

func sampleRecord() LedgerRecord {
	return LedgerRecord{V: 1, Rec: strings.Repeat("a", 32), At: 1793196000000, State: LedgerPending, Tasks: []domain.TaskID{42}, Entries: []domain.EntryID{101}}
}

func checksum(t testing.TB, r LedgerRecord) string {
	t.Helper()
	r.Sum = ""
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func signedRecord(t testing.TB, r LedgerRecord) LedgerRecord {
	t.Helper()
	r.Sum = checksum(t, r)
	return r
}

func recordLine(t testing.TB, r LedgerRecord) string {
	t.Helper()
	data, err := json.Marshal(signedRecord(t, r))
	if err != nil {
		t.Fatal(err)
	}
	return string(data) + "\n"
}

func writeLedger(t testing.TB, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "purge-ledger")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatal("ledger must end in newline")
	}
	return strings.Split(string(data[:len(data)-1]), "\n")
}

func requireFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("file = %q, want %q; err = %v", got, want, err)
	}
}
