package offline_test

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"taskronin/internal/api"
	"taskronin/internal/backup"
	"taskronin/internal/civil"
	"taskronin/internal/client"
	"taskronin/internal/clock"
	"taskronin/internal/config"
	"taskronin/internal/domain"
	"taskronin/internal/offline"
	"taskronin/internal/server"
	"taskronin/internal/store"
	"taskronin/internal/testutil"
)

// AIDEV-TODO: add TestScrubOldSchemaSnapshot with the first NNNN migration (fixture: a snapshot built at schema 1).

const (
	secretTitle = "secret keycloak"
	secretNote  = "secret note"
	// secretToken is a word of secretTitle that occurs nowhere else in the
	// fixtures. FTS5 stores tokens, not phrases: a whole-phrase search cannot
	// see a title left in the tasks_fts index (final review B-M14, parked 6).
	secretToken = "keycloak"
)

type scrubFix struct {
	srv *testutil.Server
	s1  string // backup snapshot made while the secret was live
	p1  string // <db>.pre-restore-*/taskronin.db, with its -wal beside it
	e   *offline.Env
}

// instantClock makes the password-failure delay immediate.
type instantClock struct{ *clock.Fake }

func (c instantClock) After(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- c.Now()
	return ch
}

func etagOf(t *testing.T, c *client.Client, id int64) string {
	t.Helper()
	detail, err := c.GetTask(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return detail.ETag
}

// scrubFixture builds the PRG-10 world: a secret task and entry captured in a
// snapshot (S1) and in a pre-restore set whose WAL still holds it (P1), then
// deleted, with a pending purge intent on the ledger and the server stopped.
func scrubFixture(t *testing.T, opts ...testutil.ServerOpt) *scrubFix {
	t.Helper()
	srv := testutil.StartServer(t, opts...)
	ctx := t.Context()
	c := srv.Client
	one, err := c.AddTask(ctx, client.Mut{}, api.AddTaskRequest{Title: secretTitle})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := c.AddEntry(ctx, client.Mut{}, api.AddEntryRequest{TaskID: api.Opt[int64]{Set: true, V: one.Task.ID}, Title: "did work", Notes: secretNote})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.BlockTask(ctx, client.Mut{IfMatch: etagOf(t, c, one.Task.ID)}, one.Task.ID, api.BlockRequest{Date: civil.MustDate("2026-10-29"), Start: 9 * 60, End: 10 * 60}); err != nil {
		t.Fatal(err)
	}
	two, err := c.AddTask(ctx, client.Mut{}, api.AddTaskRequest{Title: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.BlockTask(ctx, client.Mut{IfMatch: etagOf(t, c, two.Task.ID)}, two.Task.ID, api.BlockRequest{Date: civil.MustDate("2026-10-28"), Start: 13 * 60, End: 14 * 60}); err != nil {
		t.Fatal(err)
	}
	// P1: copy the live DB and its WAL while the server runs (the secret may sit
	// only in the WAL).
	p1Dir := srv.Paths.DB + ".pre-restore-20261028T160000Z"
	if err := os.Mkdir(p1Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p1 := filepath.Join(p1Dir, "taskronin.db")
	copyFile(t, srv.Paths.DB, p1)
	if _, err := os.Stat(srv.Paths.DB + "-wal"); err == nil {
		copyFile(t, srv.Paths.DB+"-wal", p1+"-wal")
	}
	snap, err := c.RunBackup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s1 := filepath.Join(srv.Paths.BackupDir, snap.Name)
	if _, err := c.DeleteTask(ctx, client.Mut{IfMatch: etagOf(t, c, one.Task.ID)}, one.Task.ID); err != nil {
		t.Fatal(err)
	}
	srv.Clock.Set(testutil.Clock(t, "2026-10-29 10:00").Now())
	srv.Stop()
	for _, p := range []string{s1, p1} {
		if !bytes.Contains(rawBytes(t, p), []byte(secretTitle)) {
			t.Fatalf("fixture: %s does not hold the secret before scrubbing", p)
		}
	}
	ledger, _, err := backup.OpenLedger(srv.Paths.LedgerFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Append(backup.LedgerRecord{V: 1, Rec: randomRec(t), At: srv.Clock.Now().UnixMilli(), State: backup.LedgerPending,
		Tasks: []domain.TaskID{domain.TaskID(one.Task.ID)}, Entries: []domain.EntryID{domain.EntryID(entry.Entry.ID)}}); err != nil {
		t.Fatal(err)
	}
	e := passwdEnv(t, srv, "")
	e.TTY = true
	e.ReadPassword = func(string) (string, error) { return testutil.Password, nil }
	return &scrubFix{srv: srv, s1: s1, p1: p1, e: e}
}

func randomRec(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// rawBytes returns a database file plus its -wal and -shm sidecars, if any.
func rawBytes(t *testing.T, path string) []byte {
	t.Helper()
	var all []byte
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, err := os.ReadFile(path + suffix)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, data...)
	}
	return all
}

func requireNoSecret(t *testing.T, path string) {
	t.Helper()
	raw := rawBytes(t, path)
	for _, secret := range []string{secretTitle, secretNote, secretToken} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("%s still holds %q", path, secret)
		}
	}
}

// requireNoSecretAnywhere walks the whole data directory, including backups,
// aside dirs, staging and sidecars, and the configured backup dir even when it
// lies outside the data dir (final review B-M14, parked item 8b).
func requireNoSecretAnywhere(t *testing.T, srv *testutil.Server) {
	t.Helper()
	dirs := []string{srv.DataDir}
	if rel, err := filepath.Rel(srv.DataDir, srv.Paths.BackupDir); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		dirs = append(dirs, srv.Paths.BackupDir)
	}
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, secret := range []string{secretTitle, secretNote, secretToken} {
				if bytes.Contains(data, []byte(secret)) {
					t.Errorf("%s still holds %q", path, secret)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func queryStrings(t *testing.T, path, query string) []string {
	t.Helper()
	st, err := store.OpenStaging(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	rows, err := st.Reader.QueryContext(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func resultMap(results []offline.SetResult) map[string]string {
	m := make(map[string]string)
	for _, r := range results {
		m[r.Path] = r.Status
	}
	return m
}

func requireStatus(t *testing.T, results []offline.SetResult, path, want string) {
	t.Helper()
	if got := resultMap(results)[path]; got != want {
		t.Fatalf("status of %s = %q, want %q (all: %+v)", path, got, want, results)
	}
}

func ledgerRecords(t *testing.T, srv *testutil.Server) []backup.LedgerRecord {
	t.Helper()
	l, _, err := backup.OpenLedger(srv.Paths.LedgerFile)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := l.Records()
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func completeLines(recs []backup.LedgerRecord) []backup.LedgerRecord {
	var out []backup.LedgerRecord
	for _, r := range recs {
		if r.State == backup.LedgerComplete {
			out = append(out, r)
		}
	}
	return out
}

func snapshotNames(t *testing.T, dir string) []string {
	t.Helper()
	infos, err := backup.List(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, i := range infos {
		names = append(names, i.Name)
	}
	slices.Sort(names)
	return names
}

func TestPRG10_ScrubRewritesSets(t *testing.T) {
	f := scrubFixture(t)
	before := snapshotNames(t, f.srv.Paths.BackupDir)
	results, err := offline.ScrubBackups(t.Context(), f.e, offline.ScrubOptions{Yes: true})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, results, f.s1, "rewritten")
	requireStatus(t, results, f.p1, "rewritten")
	requireNoSecret(t, f.s1)
	requireNoSecret(t, f.p1)
	requireNoSecretAnywhere(t, f.srv)

	if got := queryStrings(t, f.s1, "SELECT title FROM tasks WHERE id = 1"); !slices.Equal(got, []string{"[purged]"}) {
		t.Fatalf("S1 task 1 title = %q", got)
	}
	if got := queryStrings(t, f.s1, "SELECT status || '/' || cancel_reason FROM time_blocks WHERE task_id = 1"); !slices.Equal(got, []string{"cancelled/task_closed"}) {
		t.Fatalf("S1 block of #1 = %q", got)
	}
	// Historical replay does not reconcile: #2's block from yesterday stays planned.
	if got := queryStrings(t, f.s1, "SELECT status FROM time_blocks WHERE task_id = 2 AND date = '2026-10-28' AND start_min = 780"); !slices.Equal(got, []string{"planned"}) {
		t.Fatalf("S1 block of #2 = %q", got)
	}

	after := snapshotNames(t, f.srv.Paths.BackupDir)
	var extra []string
	for _, name := range after {
		if !slices.Contains(before, name) {
			extra = append(extra, name)
		}
	}
	if len(extra) != 1 || !strings.HasSuffix(extra[0], "-prescrub.db") {
		t.Fatalf("new snapshots = %v, want exactly one prescrub", extra)
	}
	if _, err := os.Stat(f.p1 + "-wal"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("P1 WAL survived: %v", err)
	}
	done := completeLines(ledgerRecords(t, f.srv))
	if len(done) != 1 || done[0].RecoveryFiles != "none" {
		t.Fatalf("complete lines = %+v", done)
	}
	if _, err := os.Lstat(config.ScrubJournal(f.srv.Paths.BackupDir)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("scrub journal remains: %v", err)
	}
	if !strings.Contains(f.e.Out.(*bytes.Buffer).String(), filepath.Base(f.s1)+": rewritten") {
		t.Fatalf("output = %q", f.e.Out.(*bytes.Buffer).String())
	}
	// A second run finds nothing to do.
	results, err = offline.ScrubBackups(t.Context(), f.e, offline.ScrubOptions{Yes: true})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, results, f.s1, "clean")
	requireStatus(t, results, f.p1, "clean")
}

// TestScrubOutOfTreeBackupDir is final review B-M14 (parked item 8b, in the
// spirit of BF01): with backup.dir outside data_dir, scrub-backups still finds,
// rewrites and verifies the snapshots there.
func TestScrubOutOfTreeBackupDir(t *testing.T) {
	var backupDir string
	f := scrubFixture(t, testutil.WithConfig(func(c *config.Server) {
		// <root>/d/taskronin -> <root>/elsewhere/backups: a sibling of the XDG data home.
		backupDir = filepath.Join(filepath.Dir(filepath.Dir(c.Paths.DataDir)), "elsewhere", "backups")
		c.Backup.Dir = backupDir
	}))
	if f.srv.Paths.BackupDir != backupDir || filepath.Dir(f.s1) != backupDir {
		t.Fatalf("fixture: backup dir %s, snapshot %s; want both in %s", f.srv.Paths.BackupDir, f.s1, backupDir)
	}
	if rel, err := filepath.Rel(f.srv.DataDir, backupDir); err != nil || !strings.HasPrefix(rel, "..") {
		t.Fatalf("fixture: %s is inside the data dir", backupDir)
	}
	results, err := offline.ScrubBackups(t.Context(), f.e, offline.ScrubOptions{Yes: true})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, results, f.s1, "rewritten")
	requireStatus(t, results, f.p1, "rewritten")
	requireNoSecret(t, f.s1)
	requireNoSecretAnywhere(t, f.srv)
	if done := completeLines(ledgerRecords(t, f.srv)); len(done) != 1 {
		t.Fatalf("complete lines = %+v", done)
	}
	if prescrub, _ := filepath.Glob(filepath.Join(backupDir, "*-prescrub.db")); len(prescrub) != 1 {
		t.Fatalf("prescrub snapshot in the configured dir = %v", prescrub)
	}
}

func TestPRG22_CheckOnly(t *testing.T) {
	f := scrubFixture(t)
	hashes := map[string][32]byte{f.s1: fileHash(t, f.s1), f.p1: fileHash(t, f.p1)}
	walHash := map[string][32]byte{}
	if _, err := os.Stat(f.p1 + "-wal"); err == nil {
		walHash[f.p1+"-wal"] = fileHash(t, f.p1+"-wal")
	}
	before := snapshotNames(t, f.srv.Paths.BackupDir)
	results, err := offline.ScrubBackups(t.Context(), f.e, offline.ScrubOptions{Check: true, Yes: true})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, results, f.s1, "contains")
	requireStatus(t, results, f.p1, "contains")
	for p, h := range hashes {
		if fileHash(t, p) != h {
			t.Fatalf("%s changed in check mode", p)
		}
	}
	for p, h := range walHash {
		if fileHash(t, p) != h {
			t.Fatalf("%s changed in check mode", p)
		}
	}
	if after := snapshotNames(t, f.srv.Paths.BackupDir); !slices.Equal(before, after) {
		t.Fatalf("check mode changed the snapshot set: %v -> %v", before, after)
	}
	if recs := ledgerRecords(t, f.srv); len(completeLines(recs)) != 0 || len(recs) != 1 {
		t.Fatalf("check mode wrote to the ledger: %+v", recs)
	}
	if _, err := os.Lstat(config.ScrubJournal(f.srv.Paths.BackupDir)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("check mode left a journal")
	}
}

func TestCheckWithCleanSets(t *testing.T) {
	f := scrubFixture(t)
	if err := os.MkdirAll(f.srv.Paths.PendingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.srv.Paths.DraftsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(f.srv.Paths.PendingDir, "k1.json")
	draft := filepath.Join(f.srv.Paths.DraftsDir, "d1.md")
	for _, p := range []string{pending, draft} {
		if err := os.WriteFile(p, []byte(secretTitle), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// First run: decline the recovery-file deletion, so the record stays pending
	// while every set is rewritten.
	f.e.Confirm = func(string, bool) (bool, error) { return false, nil }
	if _, err := offline.ScrubBackups(t.Context(), f.e, offline.ScrubOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(completeLines(ledgerRecords(t, f.srv))) != 0 {
		t.Fatal("completed although the recovery-file prompt was declined")
	}
	f.e.Confirm = func(string, bool) (bool, error) {
		t.Fatal("check mode prompted")
		return false, nil
	}
	results, err := offline.ScrubBackups(t.Context(), f.e, offline.ScrubOptions{Check: true})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, results, f.s1, "clean")
	requireStatus(t, results, f.p1, "clean")
	for _, p := range []string{pending, draft} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("check mode touched %s: %v", p, err)
		}
	}
	if len(completeLines(ledgerRecords(t, f.srv))) != 0 {
		t.Fatal("check mode completed the record")
	}
}

var errScrubCrash = errors.New("simulated scrub crash")

func TestPRG12_ResumeAfterCrash(t *testing.T) {
	f := scrubFixture(t)
	crash := backup.WithStepHook(t.Context(), func(p string) error {
		if p == "moved-old" {
			return errScrubCrash
		}
		return nil
	})
	if _, err := offline.ScrubBackups(crash, f.e, offline.ScrubOptions{Yes: true}); !errors.Is(err, errScrubCrash) {
		t.Fatalf("crash run error = %v", err)
	}
	journal := config.ScrubJournal(f.srv.Paths.BackupDir)
	if _, err := os.Stat(journal); err != nil {
		t.Fatalf("no scrub journal after the crash: %v", err)
	}
	if len(completeLines(ledgerRecords(t, f.srv))) != 0 {
		t.Fatal("completed after a crash")
	}
	// The server and the other offline commands refuse until the swap finishes.
	cfg, err := config.LoadServer(f.srv.Paths.ServerConfig)
	if err != nil {
		t.Fatal(err)
	}
	err = server.Run(t.Context(), cfg, server.Deps{Paths: f.srv.Paths, Clock: f.srv.Clock, Logger: slog.New(slog.DiscardHandler)})
	if err == nil || !strings.Contains(err.Error(), "tkr scrub-backups --resume") {
		t.Fatalf("server.Run error = %v", err)
	}
	testutil.RequireCode(t, offline.Migrate(t.Context(), f.e, true), domain.CodeConflict)

	results, err := offline.ScrubBackups(t.Context(), f.e, offline.ScrubOptions{Resume: true, Yes: true})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, results, f.s1, "rewritten")
	requireStatus(t, results, f.p1, "rewritten")
	requireNoSecret(t, f.s1)
	requireNoSecret(t, f.p1)
	requireNoSecretAnywhere(t, f.srv)
	if _, err := os.Lstat(journal); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("journal remains after resume: %v", err)
	}
	if done := completeLines(ledgerRecords(t, f.srv)); len(done) != 1 {
		t.Fatalf("complete lines = %+v", done)
	}
}

func TestScrubBatchErrors(t *testing.T) {
	f := scrubFixture(t)
	corrupt := filepath.Join(f.srv.Paths.BackupDir, "taskronin-20261020T090000.000Z-abcd-manual.db")
	if err := os.WriteFile(corrupt, []byte("this is not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	results, err := offline.ScrubBackups(t.Context(), f.e, offline.ScrubOptions{Yes: true})
	if err == nil {
		t.Fatal("a failed set must make the command fail")
	}
	requireStatus(t, results, corrupt, "error")
	requireStatus(t, results, f.s1, "rewritten")
	requireStatus(t, results, f.p1, "rewritten")
	for _, r := range results {
		// B-I1: the cause, not a bare code, so the owner can tell why.
		if r.Path == corrupt && (r.Err == "" || r.Err == string(domain.CodeInternal)) {
			t.Fatalf("error result has no cause: %q", r.Err)
		}
	}
	if out := f.e.Out.(*bytes.Buffer).String(); strings.Contains(out, "error (internal)") || !strings.Contains(out, filepath.Base(corrupt)+": error (") {
		t.Fatalf("per-set lines = %q", out)
	}
	if len(completeLines(ledgerRecords(t, f.srv))) != 0 {
		t.Fatal("completed although a set failed")
	}
	if _, err := os.Lstat(config.ScrubJournal(f.srv.Paths.BackupDir)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a journal-less failure left a journal")
	}
	entries, err := os.ReadDir(f.srv.Paths.StagingDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("staging not empty: %v, %v", entries, err)
	}
}

func TestScrubRefusals(t *testing.T) {
	t.Run("swap journal", func(t *testing.T) {
		f := scrubFixture(t)
		if err := os.WriteFile(f.srv.Paths.SwapJournal(), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		f.e.ReadPassword = func(string) (string, error) { t.Fatal("prompted before the journal refusal"); return "", nil }
		hash := fileHash(t, f.s1)
		_, err := offline.ScrubBackups(t.Context(), f.e, offline.ScrubOptions{Yes: true})
		testutil.RequireCode(t, err, domain.CodeConflict)
		if fileHash(t, f.s1) != hash {
			t.Fatal("snapshot changed")
		}
	})
	t.Run("wrong password with a scrub journal", func(t *testing.T) {
		f := scrubFixture(t)
		crash := backup.WithStepHook(t.Context(), func(p string) error {
			if p == "moved-old" {
				return errScrubCrash
			}
			return nil
		})
		if _, err := offline.ScrubBackups(crash, f.e, offline.ScrubOptions{Yes: true}); !errors.Is(err, errScrubCrash) {
			t.Fatal(err)
		}
		journal := config.ScrubJournal(f.srv.Paths.BackupDir)
		want, err := os.ReadFile(journal)
		if err != nil {
			t.Fatal(err)
		}
		j, err := backup.ReadJournal(journal)
		if err != nil {
			t.Fatal(err)
		}
		f.e.Clock = instantClock{f.srv.Clock}
		f.e.ReadPassword = func(string) (string, error) { return "wrong", nil } // deliberately invalid, not a credential fixture
		_, err = offline.ScrubBackups(t.Context(), f.e, offline.ScrubOptions{Resume: true, Yes: true})
		testutil.RequireCode(t, err, domain.CodePasswordInvalid)
		got, err := os.ReadFile(journal)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("journal changed by a refused run: %v", err)
		}
		// The swap members stay where the crash left them: aside holds the old set,
		// the replacement file is still waiting.
		for _, p := range []string{j.New, j.Aside} {
			if _, err := os.Lstat(p); err != nil {
				t.Fatalf("%s was disturbed by a refused run: %v", p, err)
			}
		}
	})
}

func TestScrubDamagedLedger(t *testing.T) {
	f := scrubFixture(t)
	file, err := os.OpenFile(f.srv.Paths.LedgerFile, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	// A well-formed purge of task 2 with a wrong checksum must never be applied.
	if _, err := file.WriteString("{\"v\":1,\"rec\":\"zz\",\"at\":1,\"state\":\"pending\",\"tasks\":[2],\"sum\":\"bad\"}\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	results, err := offline.ScrubBackups(t.Context(), f.e, offline.ScrubOptions{Yes: true})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, results, f.s1, "rewritten")
	requireStatus(t, results, f.p1, "rewritten")
	requireNoSecret(t, f.s1)
	requireNoSecret(t, f.p1)
	if len(completeLines(ledgerRecords(t, f.srv))) != 0 {
		t.Fatal("complete line appended to a damaged ledger")
	}
	if got := queryStrings(t, f.s1, "SELECT title FROM tasks WHERE id = 2"); !slices.Equal(got, []string{"other"}) {
		t.Fatalf("a bad-checksum ledger line was applied: task 2 title = %q", got)
	}
	if out := f.e.Out.(*bytes.Buffer).String(); !strings.Contains(out, "--repair-ledger") {
		t.Fatalf("output lacks the repair hint: %q", out)
	}
}

func recoveryPaths(t *testing.T) config.Paths {
	t.Helper()
	root := t.TempDir()
	p := config.Paths{PendingDir: filepath.Join(root, "pending"), DraftsDir: filepath.Join(root, "drafts")}
	for _, d := range []string{p.PendingDir, p.DraftsDir} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func seedRecovery(t *testing.T, p config.Paths) []string {
	t.Helper()
	files := []string{
		filepath.Join(p.PendingDir, "a.json"), filepath.Join(p.PendingDir, "b.json"),
		filepath.Join(p.DraftsDir, "20261028T100000Z-0a0b0c0d.md"),
	}
	for _, f := range files {
		if err := os.WriteFile(f, []byte(secretTitle), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return files
}

func countExisting(files []string) int {
	n := 0
	for _, f := range files {
		if _, err := os.Lstat(f); err == nil {
			n++
		}
	}
	return n
}

func TestCleanRecoveryFiles(t *testing.T) {
	ctx := t.Context()
	fail := func(string, bool) (bool, error) { t.Fatal("unexpected prompt"); return false, nil }
	t.Run("yes deletes", func(t *testing.T) {
		p := recoveryPaths(t)
		files := seedRecovery(t, p)
		var asked string
		mode, err := offline.CleanRecoveryFiles(ctx, p, false, false, func(q string, def bool) (bool, error) {
			asked = q
			if !def {
				t.Error("default must be yes")
			}
			return true, nil
		})
		if err != nil || mode != "deleted" || countExisting(files) != 0 {
			t.Fatalf("mode=%q err=%v left=%d", mode, err, countExisting(files))
		}
		if !strings.Contains(asked, "Delete 2 pending request(s) and 1 draft(s) that may hold purged text?") {
			t.Fatalf("prompt = %q", asked)
		}
	})
	t.Run("keep", func(t *testing.T) {
		p := recoveryPaths(t)
		files := seedRecovery(t, p)
		mode, err := offline.CleanRecoveryFiles(ctx, p, true, false, fail)
		if err != nil || mode != "kept" || countExisting(files) != 3 {
			t.Fatalf("mode=%q err=%v left=%d", mode, err, countExisting(files))
		}
	})
	t.Run("none", func(t *testing.T) {
		mode, err := offline.CleanRecoveryFiles(ctx, recoveryPaths(t), false, false, fail)
		if err != nil || mode != "none" {
			t.Fatalf("mode=%q err=%v", mode, err)
		}
	})
	t.Run("missing directories", func(t *testing.T) {
		mode, err := offline.CleanRecoveryFiles(ctx, config.Paths{PendingDir: filepath.Join(t.TempDir(), "x"), DraftsDir: filepath.Join(t.TempDir(), "y")}, false, false, fail)
		if err != nil || mode != "none" {
			t.Fatalf("mode=%q err=%v", mode, err)
		}
	})
	t.Run("no", func(t *testing.T) {
		p := recoveryPaths(t)
		files := seedRecovery(t, p)
		mode, err := offline.CleanRecoveryFiles(ctx, p, false, false, func(string, bool) (bool, error) { return false, nil })
		if err != nil || mode != "" || countExisting(files) != 3 {
			t.Fatalf("mode=%q err=%v left=%d", mode, err, countExisting(files))
		}
	})
	t.Run("assume yes", func(t *testing.T) {
		p := recoveryPaths(t)
		files := seedRecovery(t, p)
		mode, err := offline.CleanRecoveryFiles(ctx, p, false, true, fail)
		if err != nil || mode != "deleted" || countExisting(files) != 0 {
			t.Fatalf("mode=%q err=%v left=%d", mode, err, countExisting(files))
		}
	})
	t.Run("prompt error", func(t *testing.T) {
		p := recoveryPaths(t)
		files := seedRecovery(t, p)
		boom := errors.New("boom")
		mode, err := offline.CleanRecoveryFiles(ctx, p, false, false, func(string, bool) (bool, error) { return false, boom })
		if !errors.Is(err, boom) || mode != "" || countExisting(files) != 3 {
			t.Fatalf("mode=%q err=%v left=%d", mode, err, countExisting(files))
		}
	})
}

// TestScrubRecoveryFilesEndToEnd proves the leftover client files are gone and
// the ledger records the cleanup, with the purged text nowhere on disk.
func TestScrubRecoveryFilesEndToEnd(t *testing.T) {
	f := scrubFixture(t)
	files := seedRecovery(t, f.srv.Paths)
	for _, p := range files {
		if _, err := os.Stat(p); err != nil {
			t.Fatal(err)
		}
	}
	f.e.Confirm = func(string, bool) (bool, error) { return true, nil }
	if _, err := offline.ScrubBackups(t.Context(), f.e, offline.ScrubOptions{}); err != nil {
		t.Fatal(err)
	}
	if countExisting(files) != 0 {
		t.Fatal("recovery files survived")
	}
	done := completeLines(ledgerRecords(t, f.srv))
	if len(done) != 1 || done[0].RecoveryFiles != "deleted" {
		t.Fatalf("complete lines = %+v", done)
	}
	requireNoSecretAnywhere(t, f.srv)
	var leftovers []string
	err := filepath.WalkDir(f.srv.Paths.StateDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(secretTitle)) {
			leftovers = append(leftovers, path)
		}
		return nil
	})
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("state dir still holds the secret: %v, %v", leftovers, err)
	}
}

// TestScrubResumeAtEveryStep crashes the swap after each journal write and
// shows that a later run finishes it with the secret gone everywhere.
func TestScrubResumeAtEveryStep(t *testing.T) {
	for _, point := range []string{"prepared", "moved-old", "installed", "aside-deleted"} {
		t.Run(point, func(t *testing.T) {
			f := scrubFixture(t)
			crash := backup.WithStepHook(t.Context(), func(p string) error {
				if p == point {
					return errScrubCrash
				}
				return nil
			})
			if _, err := offline.ScrubBackups(crash, f.e, offline.ScrubOptions{Yes: true}); !errors.Is(err, errScrubCrash) {
				t.Fatalf("crash run error = %v", err)
			}
			if _, err := os.Stat(config.ScrubJournal(f.srv.Paths.BackupDir)); err != nil {
				t.Fatalf("no journal: %v", err)
			}
			if _, err := offline.ScrubBackups(t.Context(), f.e, offline.ScrubOptions{Yes: true}); err != nil {
				t.Fatal(err)
			}
			requireNoSecretAnywhere(t, f.srv)
			if len(completeLines(ledgerRecords(t, f.srv))) != 1 {
				t.Fatal("not completed after resume")
			}
		})
	}
}

// TestScrubCleanSetWithSidecar: a set whose logical content is already clean but
// whose original -wal holds purged bytes must be reported by --check and rewritten.
func TestScrubCleanSetWithSidecar(t *testing.T) {
	f := scrubFixture(t)
	if _, err := offline.ScrubBackups(t.Context(), f.e, offline.ScrubOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	dir := f.srv.Paths.DB + ".pre-restore-20261029T000000Z"
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p2 := filepath.Join(dir, "taskronin.db")
	copyFile(t, f.s1, p2) // logically clean after the first scrub
	// SQLite ignores a WAL with a bad header, but its bytes are still on disk.
	stale := append(bytes.Repeat([]byte{0}, 64), []byte(secretTitle+secretNote)...)
	if err := os.WriteFile(p2+"-wal", stale, 0o600); err != nil {
		t.Fatal(err)
	}
	hash := fileHash(t, p2+"-wal")
	results, err := offline.ScrubBackups(t.Context(), f.e, offline.ScrubOptions{Check: true})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, results, p2, "contains")
	requireStatus(t, results, f.s1, "clean")
	if fileHash(t, p2+"-wal") != hash {
		t.Fatal("check mode changed the sidecar")
	}
	results, err = offline.ScrubBackups(t.Context(), f.e, offline.ScrubOptions{Yes: true})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, results, p2, "rewritten")
	requireNoSecret(t, p2)
	requireNoSecretAnywhere(t, f.srv)
	if _, err := os.Stat(p2 + "-wal"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stale WAL survived: %v", err)
	}
	results, err = offline.ScrubBackups(t.Context(), f.e, offline.ScrubOptions{Check: true})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, results, p2, "clean")
}
