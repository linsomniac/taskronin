package offline_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"taskronin/internal/api"
	"taskronin/internal/backup"
	"taskronin/internal/civil"
	"taskronin/internal/client"
	"taskronin/internal/config"
	"taskronin/internal/domain"
	"taskronin/internal/lock"
	"taskronin/internal/offline"
	"taskronin/internal/testutil"
)

// purgeFix is the PRG world. Catalog IDs map to this fixture as: #42 -> task 1,
// e100 -> e1 (work), e101 -> e2 (completion entry, via done), e102 -> e3 (work
// on another day). Task 2 and its live entry e4 are untouched bystanders.
type purgeFix struct {
	srv          *testutil.Server
	s1           string // snapshot taken while #1 was still open
	e            *offline.Env
	confirms     *[]string
	passwordRead *int
}

const (
	otherTitle = "bystander task"
	// linkedNote sits on the linked work entries e1 and e3, which a plain purge keeps.
	linkedNote = "linked work note"
	e1, e2, e3 = int64(1), int64(2), int64(3)
)

func purgeFixture(t *testing.T) *purgeFix {
	t.Helper()
	srv := testutil.StartServer(t)
	ctx := t.Context()
	c := srv.Client
	one, err := c.AddTask(ctx, client.Mut{}, api.AddTaskRequest{Title: secretTitle})
	if err != nil {
		t.Fatal(err)
	}
	taskOpt := api.Opt[int64]{Set: true, V: one.Task.ID}
	if _, err := c.AddEntry(ctx, client.Mut{}, api.AddEntryRequest{TaskID: taskOpt, Title: "did work", Notes: linkedNote}); err != nil {
		t.Fatal(err)
	}
	snap, err := c.RunBackup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CompleteTask(ctx, client.Mut{IfMatch: etagOf(t, c, one.Task.ID)}, one.Task.ID, api.CompleteRequest{Notes: secretNote}); err != nil {
		t.Fatal(err)
	}
	other := civil.MustDate("2026-10-27")
	if _, err := c.AddEntry(ctx, client.Mut{}, api.AddEntryRequest{TaskID: taskOpt, Title: "more work", Notes: linkedNote, WorkDate: &other}); err != nil {
		t.Fatal(err)
	}
	two, err := c.AddTask(ctx, client.Mut{}, api.AddTaskRequest{Title: otherTitle})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddEntry(ctx, client.Mut{}, api.AddEntryRequest{TaskID: api.Opt[int64]{Set: true, V: two.Task.ID}, Title: "bystander work"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeleteTask(ctx, client.Mut{IfMatch: etagOf(t, c, one.Task.ID)}, one.Task.ID); err != nil {
		t.Fatal(err)
	}
	srv.Stop()
	f := &purgeFix{srv: srv, s1: filepath.Join(srv.Paths.BackupDir, snap.Name)}
	if !bytes.Contains(rawBytes(t, f.s1), []byte(secretTitle)) {
		t.Fatal("fixture: S1 does not hold the secret")
	}
	f.e = purgeEnv(t, f)
	return f
}

// purgeEnv returns a fresh Env (empty output) with the right password and a
// recording Confirm that answers yes to everything.
func purgeEnv(t *testing.T, f *purgeFix) *offline.Env {
	t.Helper()
	e := passwdEnv(t, f.srv, "")
	e.TTY = true
	reads := 0
	var prompts []string
	e.ReadPassword = func(string) (string, error) { reads++; return testutil.Password, nil }
	e.Confirm = func(q string, _ bool) (bool, error) { prompts = append(prompts, q); return true, nil }
	f.passwordRead, f.confirms = &reads, &prompts
	return e
}

func purgeTask1(o ...func(*offline.PurgeOptions)) offline.PurgeOptions {
	opt := offline.PurgeOptions{Target: "task", ID: 1, Yes: true}
	for _, f := range o {
		f(&opt)
	}
	return opt
}

func opsCount(t *testing.T, srv *testutil.Server) string {
	t.Helper()
	dir := t.TempDir()
	cp := filepath.Join(dir, "copy.db")
	copyFile(t, srv.Paths.DB, cp)
	if _, err := os.Stat(srv.Paths.DB + "-wal"); err == nil {
		copyFile(t, srv.Paths.DB+"-wal", cp+"-wal")
	}
	return strings.Join(queryStrings(t, cp, "SELECT COUNT(*) FROM ops"), ",")
}

// requireNoText fails if text appears in any file under the data, backup or
// state directories, sidecars included.
func requireNoText(t *testing.T, srv *testutil.Server, text string) {
	t.Helper()
	dirs := []string{srv.Paths.DataDir, srv.Paths.BackupDir, srv.Paths.StateDir}
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil || d.IsDir() || !d.Type().IsRegular() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if bytes.Contains(data, []byte(text)) {
				t.Errorf("%s still holds %q", path, text)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func requireNoSecrets(t *testing.T, srv *testutil.Server) {
	t.Helper()
	requireNoText(t, srv, secretTitle)
	requireNoText(t, srv, secretNote)
}

func requireNoLedger(t *testing.T, srv *testutil.Server) {
	t.Helper()
	if _, err := os.Lstat(srv.Paths.LedgerFile); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ledger file exists: %v", err)
	}
}

func TestPRG01_ServerRunning(t *testing.T) {
	f := purgeFixture(t)
	held, err := lock.Acquire(f.srv.Paths.LockFile())
	if err != nil {
		t.Fatal(err)
	}
	f.e.ReadPassword = func(string) (string, error) { t.Error("prompted while the server holds the lock"); return "", nil }
	_, err = offline.Purge(t.Context(), f.e, purgeTask1())
	if rerr := held.Release(); rerr != nil {
		t.Fatal(rerr)
	}
	testutil.RequireCode(t, err, domain.CodeBusy)
	if !strings.Contains(err.Error(), "systemctl --user stop taskronin.socket taskronin.service") {
		t.Fatalf("error = %v", err)
	}
	requireNoLedger(t, f.srv)

	for name, journal := range map[string]string{
		"swap.journal":  f.srv.Paths.SwapJournal(),
		"scrub.journal": config.ScrubJournal(f.srv.Paths.BackupDir),
	} {
		if err := os.WriteFile(journal, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		f.e = purgeEnv(t, f)
		f.e.ReadPassword = func(string) (string, error) {
			t.Errorf("%s: prompted before the journal refusal", name)
			return "", nil
		}
		_, err := offline.Purge(t.Context(), f.e, purgeTask1())
		testutil.RequireCode(t, err, domain.CodeConflict)
		requireNoLedger(t, f.srv)
		if err := os.Remove(journal); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPRG02_NoTTY(t *testing.T) {
	f := purgeFixture(t)
	f.e.TTY = false
	_, err := offline.Purge(t.Context(), f.e, purgeTask1())
	testutil.RequireCode(t, err, domain.CodePasswordRequired)
	requireNoLedger(t, f.srv)
}

func TestPRG03_WrongPassword(t *testing.T) {
	f := purgeFixture(t)
	f.e.Clock = instantClock{f.srv.Clock}
	f.e.ReadPassword = func(string) (string, error) { return "wrong", nil } // deliberately invalid, not a credential fixture
	opsBefore := opsCount(t, f.srv)
	hash := fileHash(t, f.srv.Paths.DB)
	_, err := offline.Purge(t.Context(), f.e, purgeTask1())
	testutil.RequireCode(t, err, domain.CodePasswordInvalid)
	requireNoLedger(t, f.srv)
	if got := opsCount(t, f.srv); got != opsBefore {
		t.Fatalf("ops rows %s -> %s", opsBefore, got)
	}
	if fileHash(t, f.srv.Paths.DB) != hash {
		t.Fatal("database bytes changed")
	}
}

func TestPRG04_NotTombstoned(t *testing.T) {
	f := purgeFixture(t)
	f.e.Confirm = func(string, bool) (bool, error) {
		t.Error("prompted for a target that cannot be purged")
		return false, nil
	}
	// Task 2 and entry e4 are live; task 1's entry e1 is live under a deleted task.
	_, err := offline.Purge(t.Context(), f.e, offline.PurgeOptions{Target: "task", ID: 2})
	testutil.RequireCode(t, err, domain.CodeConflict)
	if !strings.Contains(err.Error(), "delete it first") {
		t.Fatalf("error = %v", err)
	}
	_, err = offline.Purge(t.Context(), f.e, offline.PurgeOptions{Target: "entry", ID: 4})
	testutil.RequireCode(t, err, domain.CodeConflict)
	_, err = offline.Purge(t.Context(), f.e, offline.PurgeOptions{Target: "entry", ID: e1})
	testutil.RequireCode(t, err, domain.CodeConflict)
	if !strings.Contains(err.Error(), "tkr purge task 1 --with-entries") {
		t.Fatalf("error = %v", err)
	}
	_, err = offline.Purge(t.Context(), f.e, offline.PurgeOptions{Target: "task", ID: 99})
	testutil.RequireCode(t, err, domain.CodeNotFound)
	_, err = offline.Purge(t.Context(), f.e, offline.PurgeOptions{Target: "entry", ID: 99})
	testutil.RequireCode(t, err, domain.CodeNotFound)
	requireNoLedger(t, f.srv)
}

func TestPurgeConfirmation(t *testing.T) {
	f := purgeFixture(t)
	var prompts []string
	f.e.Confirm = func(q string, def bool) (bool, error) {
		if def {
			t.Error("the purge prompt must default to no")
		}
		prompts = append(prompts, q)
		return false, nil
	}
	opsBefore := opsCount(t, f.srv)
	hash, s1Hash := fileHash(t, f.srv.Paths.DB), fileHash(t, f.s1)
	_, err := offline.Purge(t.Context(), f.e, purgeTask1(func(o *offline.PurgeOptions) { o.Yes = false }))
	testutil.RequireCode(t, err, domain.CodeValidation)
	if !strings.Contains(err.Error(), "purge cancelled; nothing was changed") {
		t.Fatalf("error = %v", err)
	}
	if len(prompts) != 1 {
		t.Fatalf("prompts = %q", prompts)
	}
	for _, want := range []string{"#1", "'" + secretTitle + "'", "e2", "e1, e3", "--with-entries", "cannot be undone"} {
		if !strings.Contains(prompts[0], want) {
			t.Errorf("prompt %q lacks %q", prompts[0], want)
		}
	}
	requireNoLedger(t, f.srv)
	if got := opsCount(t, f.srv); got != opsBefore {
		t.Fatalf("ops rows %s -> %s", opsBefore, got)
	}
	if fileHash(t, f.srv.Paths.DB) != hash || fileHash(t, f.s1) != s1Hash {
		t.Fatal("a declined purge changed database or snapshot bytes")
	}

	// With --with-entries the prompt lists every entry and keeps none.
	prompts = nil
	_, err = offline.Purge(t.Context(), f.e, purgeTask1(func(o *offline.PurgeOptions) { o.Yes, o.WithEntries = false, true }))
	testutil.RequireCode(t, err, domain.CodeValidation)
	if !strings.Contains(prompts[0], "e1, e2, e3") || strings.Contains(prompts[0], "are kept") {
		t.Fatalf("prompt = %q", prompts[0])
	}

	// With Yes no confirmation call happens.
	f.e.Confirm = func(q string, _ bool) (bool, error) {
		if strings.Contains(q, "Purge task") {
			t.Error("Confirm called although Yes was set")
		}
		return true, nil
	}
	if _, err := offline.Purge(t.Context(), f.e, purgeTask1()); err != nil {
		t.Fatal(err)
	}
}

func TestPRG05_PurgeTask(t *testing.T) {
	f := purgeFixture(t)
	rep, err := offline.Purge(t.Context(), f.e, purgeTask1())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.TaskIDs, []int64{1}) || !slices.Equal(rep.EntryIDs, []int64{e2}) ||
		!slices.Equal(rep.LinkedNotIncluded, []int64{e1, e3}) || !rep.Changed || !rep.Complete {
		t.Fatalf("report = %+v", rep)
	}
	requireStatus(t, rep.Sets, f.s1, "rewritten")
	requireNoSecrets(t, f.srv)
	if got := queryStrings(t, f.s1, "SELECT title FROM tasks WHERE id = 1"); !slices.Equal(got, []string{"[purged]"}) {
		t.Fatalf("S1 title = %q", got)
	}
	// e1 and e3 are linked, not included: their notes are deliberately kept.
	if !bytes.Contains(rawBytes(t, f.srv.Paths.DB), []byte(linkedNote)) {
		t.Fatal("linked entries were purged without --with-entries")
	}
	if fi, err := os.Stat(f.srv.Paths.DB + "-wal"); err == nil && fi.Size() != 0 {
		t.Fatalf("live WAL has %d bytes after purge", fi.Size())
	}
	recs := ledgerRecords(t, f.srv)
	if len(recs) != 2 || recs[0].State != backup.LedgerPending || recs[1].State != backup.LedgerComplete ||
		recs[0].Rec != recs[1].Rec || recs[1].RecoveryFiles != "none" ||
		!slices.Equal(recs[0].Tasks, []domain.TaskID{1}) || !slices.Equal(recs[0].Entries, []domain.EntryID{domain.EntryID(e2)}) {
		t.Fatalf("ledger = %+v", recs)
	}
	out := f.e.Out.(*bytes.Buffer).String()
	for _, want := range []string{"purged task #1 and entries e2", "linked entries not purged: e1, e3 (use --with-entries)",
		filepath.Base(f.s1) + ": rewritten", "recovery files: none", "Exports and log lines made earlier are out of reach."} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if *f.passwordRead != 1 {
		t.Fatalf("password read %d times", *f.passwordRead)
	}
}

func TestPRG06_WithEntries(t *testing.T) {
	f := purgeFixture(t)
	rep, err := offline.Purge(t.Context(), f.e, purgeTask1(func(o *offline.PurgeOptions) { o.WithEntries = true }))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.EntryIDs, []int64{e1, e2, e3}) || len(rep.LinkedNotIncluded) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	requireNoSecrets(t, f.srv)
	requireNoText(t, f.srv, linkedNote)
	if strings.Contains(f.e.Out.(*bytes.Buffer).String(), "linked entries not purged") {
		t.Fatal("output lists linked entries")
	}
}

func TestPRG13_TornTailThenRerun(t *testing.T) {
	f := purgeFixture(t)
	if err := os.WriteFile(f.srv.Paths.LedgerFile, []byte(`{"v":1,"rec":"abc","at":1,"sta`), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := offline.Purge(t.Context(), f.e, purgeTask1())
	if err != nil || !rep.Complete {
		t.Fatalf("report = %+v, err = %v", rep, err)
	}
	l, health, err := backup.OpenLedger(f.srv.Paths.LedgerFile)
	if err != nil || health.Damaged {
		t.Fatalf("health = %+v, err = %v", health, err)
	}
	if recs, _ := l.Records(); len(recs) != 2 {
		t.Fatalf("ledger = %+v", recs)
	}
	requireNoSecrets(t, f.srv)
}

func TestPRG14_DamagedRefuses(t *testing.T) {
	f := purgeFixture(t)
	bad := "{\"v\":1,\"rec\":\"zz\",\"at\":1,\"state\":\"pending\",\"tasks\":[2],\"sum\":\"bad\"}\n"
	if err := os.WriteFile(f.srv.Paths.LedgerFile, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	hash := fileHash(t, f.s1)
	_, err := offline.Purge(t.Context(), f.e, purgeTask1())
	testutil.RequireCode(t, err, domain.CodeConflict)
	if !strings.Contains(err.Error(), "run `tkr purge --repair-ledger`") {
		t.Fatalf("error = %v", err)
	}
	if fileHash(t, f.s1) != hash {
		t.Fatal("a snapshot was scrubbed")
	}
	got, err := os.ReadFile(f.srv.Paths.LedgerFile)
	if err != nil || string(got) != bad {
		t.Fatalf("ledger changed: %q, %v", got, err)
	}
}

func TestPRG15_RepairLedger(t *testing.T) {
	f := purgeFixture(t)
	// Harvest two valid lines from scratch ledgers, then put a bad-sum line between them.
	scratch := filepath.Join(t.TempDir(), "scratch")
	sl, _, err := backup.OpenLedger(scratch)
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range []string{randomRec(t), randomRec(t)} {
		if err := sl.Append(backup.LedgerRecord{V: 1, Rec: rec, At: 1, State: backup.LedgerPending, Tasks: []domain.TaskID{99}}); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(scratch)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(raw), "\n")
	content := lines[0] + "{\"v\":1,\"rec\":\"zz\",\"at\":1,\"state\":\"pending\",\"tasks\":[2],\"sum\":\"bad\"}\n" + lines[1]
	if err := os.WriteFile(f.srv.Paths.LedgerFile, []byte(content), 0o644); err != nil { // 0644 on purpose: repair must restore 0600
		t.Fatal(err)
	}
	dropped, err := offline.RepairLedger(t.Context(), f.e)
	if err != nil || !slices.Equal(dropped, []int{2}) {
		t.Fatalf("dropped = %v, err = %v", dropped, err)
	}
	if fi, err := os.Stat(f.srv.Paths.LedgerFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("ledger mode: %v, %v", fi, err)
	}
	if recs := ledgerRecords(t, f.srv); len(recs) != 2 {
		t.Fatalf("ledger = %+v", recs)
	}
	if !strings.Contains(f.e.Out.(*bytes.Buffer).String(), "2") {
		t.Fatalf("output = %q", f.e.Out.(*bytes.Buffer).String())
	}
	f.e = purgeEnv(t, f)
	if _, err := offline.Purge(t.Context(), f.e, purgeTask1()); err != nil {
		t.Fatal(err)
	}
	requireNoSecrets(t, f.srv)
}

// TestRepairLedgerReportsDroppedFirst is final review B-M5: the rewritten
// ledger no longer holds the dropped lines, so their numbers are printed right
// after the repair, before a replay or checkpoint that could still fail.
func TestRepairLedgerReportsDroppedFirst(t *testing.T) {
	f := purgeFixture(t)
	writeDamagedLedger(t, f.srv.Paths.LedgerFile)
	failure := errors.New("simulated replay failure")
	ctx := offline.WithCrashHook(t.Context(), func(p string) error {
		if p == "repair:repaired" {
			return failure
		}
		return nil
	})
	dropped, err := offline.RepairLedger(ctx, f.e)
	if !errors.Is(err, failure) || !slices.Equal(dropped, []int{2}) {
		t.Fatalf("dropped = %v, err = %v", dropped, err)
	}
	if out := f.e.Out.(*bytes.Buffer).String(); out != "dropped damaged ledger line(s): 2\n" {
		t.Fatalf("output = %q", out)
	}
}

func TestRepairLedgerNeedsPassword(t *testing.T) {
	f := purgeFixture(t)
	f.e.TTY = false
	_, err := offline.RepairLedger(t.Context(), f.e)
	testutil.RequireCode(t, err, domain.CodePasswordRequired)
	requireNoLedger(t, f.srv)
}

func TestPRG16_Rerun(t *testing.T) {
	f := purgeFixture(t)
	if _, err := offline.Purge(t.Context(), f.e, purgeTask1()); err != nil {
		t.Fatal(err)
	}
	recs := ledgerRecords(t, f.srv)
	ops := opsCount(t, f.srv)
	f.e = purgeEnv(t, f)
	rep, err := offline.Purge(t.Context(), f.e, purgeTask1())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Changed || !slices.Equal(rep.TaskIDs, []int64{1}) {
		t.Fatalf("report = %+v", rep)
	}
	if got := ledgerRecords(t, f.srv); len(got) != len(recs) {
		t.Fatalf("ledger grew: %+v", got)
	}
	if opsCount(t, f.srv) != ops {
		t.Fatal("a rerun wrote an ops row")
	}
	requireNoSecrets(t, f.srv)
}

func TestPRG20_RestoreAfterPurgeStaysPurged(t *testing.T) {
	f := purgeFixture(t)
	// S0: an older copy outside the backup rotation that still holds the content.
	s0 := filepath.Join(t.TempDir(), "s0.db")
	copyFile(t, f.s1, s0)
	if _, err := offline.Purge(t.Context(), f.e, purgeTask1(func(o *offline.PurgeOptions) { o.WithEntries = true })); err != nil {
		t.Fatal(err)
	}
	f.e = purgeEnv(t, f)
	if _, err := offline.Restore(t.Context(), f.e, s0, true); err != nil {
		t.Fatal(err)
	}
	requireNoSecrets(t, f.srv)
	f.srv.Start(t)
	detail, err := f.srv.Client.GetTask(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Title != "[purged]" {
		t.Fatalf("restored task title = %q", detail.Title)
	}
	f.srv.Stop()
	f.e = purgeEnv(t, f)
	results, err := offline.ScrubBackups(t.Context(), f.e, offline.ScrubOptions{Yes: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Status != "clean" && r.Status != "rewritten" {
			t.Fatalf("scrub result %+v", r)
		}
	}
	requireNoSecrets(t, f.srv)
}

// TestSequentialPurgesLeaveNoSecret is R3-1: a secret typed as a completion
// retitle must vanish from the DB, its WAL and every snapshot across a
// reopen, an entry purge and a task purge.
func TestSequentialPurgesLeaveNoSecret(t *testing.T) {
	const word = "hunter2" // not "secret": the schema has a secret_hash column
	srv := testutil.StartServer(t)
	ctx := t.Context()
	c := srv.Client
	task, err := c.AddTask(ctx, client.Mut{}, api.AddTaskRequest{Title: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	as := word + " retitle"
	if _, err := c.CompleteTask(ctx, client.Mut{IfMatch: etagOf(t, c, task.Task.ID)}, task.Task.ID, api.CompleteRequest{As: &as}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RunBackup(ctx); err != nil { // a snapshot holding the secret
		t.Fatal(err)
	}
	if _, err := c.ReopenTask(ctx, client.Mut{IfMatch: etagOf(t, c, task.Task.ID)}, task.Task.ID, api.ReopenRequest{RemoveEntry: true}); err != nil {
		t.Fatal(err)
	}
	entries, err := c.ListEntries(ctx, client.EntryQuery{Task: &task.Task.ID, IncludeDeleted: true})
	if err != nil || len(entries.Items) != 1 {
		t.Fatalf("entries = %+v, %v", entries, err)
	}
	entryID := entries.Items[0].ID
	srv.Stop()
	f := &purgeFix{srv: srv}
	f.e = purgeEnv(t, f)
	if _, err := offline.Purge(ctx, f.e, offline.PurgeOptions{Target: "entry", ID: entryID, Yes: true}); err != nil {
		t.Fatal(err)
	}
	srv.Start(t)
	if _, err := srv.Client.DeleteTask(ctx, client.Mut{IfMatch: etagOf(t, srv.Client, task.Task.ID)}, task.Task.ID); err != nil {
		t.Fatal(err)
	}
	srv.Stop()
	f.e = purgeEnv(t, f)
	rep, err := offline.Purge(ctx, f.e, offline.PurgeOptions{Target: "task", ID: task.Task.ID, WithEntries: true, Yes: true})
	if err != nil || !rep.Complete {
		t.Fatalf("report = %+v, err = %v", rep, err)
	}
	requireNoText(t, srv, word)
	for _, p := range []string{srv.Paths.DB, srv.Paths.DB + "-wal"} {
		if data, err := os.ReadFile(p); err == nil && bytes.Contains(data, []byte(word)) {
			t.Fatalf("%s holds %q", p, word)
		}
	}
}

var errPurgeCrash = errors.New("simulated purge crash")

func requireComplete(t *testing.T, srv *testutil.Server) {
	t.Helper()
	recs := ledgerRecords(t, srv)
	if len(completeLines(recs)) != 1 {
		t.Fatalf("ledger = %+v, want one complete line", recs)
	}
}

// TestPurgeCrashPoints crashes the purge at every step and proves a rerun (or,
// after a swap crash that leaves a journal, scrub-backups) finishes the job.
func TestPurgeCrashPoints(t *testing.T) {
	purgePoints := []string{"purge:committed", "purge:checkpointed", "purge:before-complete"}
	for _, point := range purgePoints {
		t.Run(point, func(t *testing.T) {
			f := purgeFixture(t)
			ctx := offline.WithCrashHook(t.Context(), func(p string) error {
				if p == point {
					return errPurgeCrash
				}
				return nil
			})
			if _, err := offline.Purge(ctx, f.e, purgeTask1()); !errors.Is(err, errPurgeCrash) {
				t.Fatalf("crash run error = %v", err)
			}
			if len(completeLines(ledgerRecords(t, f.srv))) != 0 {
				t.Fatal("complete line written by a crashed purge")
			}
			f.e = purgeEnv(t, f)
			rep, err := offline.Purge(t.Context(), f.e, purgeTask1())
			if err != nil || !rep.Complete {
				t.Fatalf("rerun: %+v, %v", rep, err)
			}
			requireComplete(t, f.srv)
			requireNoSecrets(t, f.srv)
		})
	}
	// Crash with the pending line appended but the scrub never committed (the
	// ledger-append step): the rerun replays the intent and finishes.
	t.Run("ledger-append", func(t *testing.T) {
		f := purgeFixture(t)
		l, _, err := backup.OpenLedger(f.srv.Paths.LedgerFile)
		if err != nil {
			t.Fatal(err)
		}
		if err := l.Append(backup.LedgerRecord{V: 1, Rec: randomRec(t), At: 1, State: backup.LedgerPending,
			Tasks: []domain.TaskID{1}, Entries: []domain.EntryID{domain.EntryID(e2)}}); err != nil {
			t.Fatal(err)
		}
		rep, err := offline.Purge(t.Context(), f.e, purgeTask1())
		if err != nil || !rep.Complete {
			t.Fatalf("rerun: %+v, %v", rep, err)
		}
		requireComplete(t, f.srv)
		requireNoSecrets(t, f.srv)
	})
	for _, point := range []string{"prepared", "moved-old:done", "moved-old", "installed:done", "installed", "on-installed", "aside-deleted:done", "aside-deleted"} {
		t.Run("swap:"+point, func(t *testing.T) {
			f := purgeFixture(t)
			ctx := backup.WithStepHook(t.Context(), func(p string) error {
				if p == point {
					return errPurgeCrash
				}
				return nil
			})
			if _, err := offline.Purge(ctx, f.e, purgeTask1()); !errors.Is(err, errPurgeCrash) {
				t.Fatalf("crash run error = %v", err)
			}
			if len(completeLines(ledgerRecords(t, f.srv))) != 0 {
				t.Fatal("complete line written by a crashed purge")
			}
			// A purge is refused while the journal exists; scrub-backups resumes it.
			f.e = purgeEnv(t, f)
			if _, err := os.Lstat(config.ScrubJournal(f.srv.Paths.BackupDir)); err == nil {
				_, err := offline.Purge(t.Context(), f.e, purgeTask1())
				testutil.RequireCode(t, err, domain.CodeConflict)
			}
			if _, err := offline.ScrubBackups(t.Context(), f.e, offline.ScrubOptions{Yes: true}); err != nil {
				t.Fatal(err)
			}
			requireComplete(t, f.srv)
			requireNoSecrets(t, f.srv)
		})
	}
}

func seedRecoveryFiles(t *testing.T, srv *testutil.Server) []string {
	t.Helper()
	for _, d := range []string{srv.Paths.PendingDir, srv.Paths.DraftsDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return seedRecovery(t, srv.Paths)
}

func TestPurgeRecoveryFiles(t *testing.T) {
	deletePrompt := func(answer bool, asked *int) func(string, bool) (bool, error) {
		return func(q string, _ bool) (bool, error) {
			if strings.HasPrefix(q, "Delete ") {
				*asked++
				return answer, nil
			}
			return true, nil
		}
	}
	t.Run("default yes deletes", func(t *testing.T) {
		f := purgeFixture(t)
		files := seedRecoveryFiles(t, f.srv)
		asked := 0
		f.e.Confirm = deletePrompt(true, &asked)
		rep, err := offline.Purge(t.Context(), f.e, purgeTask1(func(o *offline.PurgeOptions) { o.Yes = false }))
		if err != nil || !rep.Complete || rep.RecoveryFiles != "deleted" || asked != 1 || countExisting(files) != 0 {
			t.Fatalf("rep = %+v, err = %v, asked = %d, left = %d", rep, err, asked, countExisting(files))
		}
		if done := completeLines(ledgerRecords(t, f.srv)); len(done) != 1 || done[0].RecoveryFiles != "deleted" {
			t.Fatalf("complete = %+v", done)
		}
		requireNoSecrets(t, f.srv)
	})
	t.Run("keep", func(t *testing.T) {
		f := purgeFixture(t)
		files := seedRecoveryFiles(t, f.srv)
		asked := 0
		f.e.Confirm = deletePrompt(true, &asked)
		rep, err := offline.Purge(t.Context(), f.e, purgeTask1(func(o *offline.PurgeOptions) { o.KeepRecoveryFiles = true }))
		if err != nil || !rep.Complete || rep.RecoveryFiles != "kept" || asked != 0 || countExisting(files) != 3 {
			t.Fatalf("rep = %+v, err = %v, asked = %d, left = %d", rep, err, asked, countExisting(files))
		}
		if done := completeLines(ledgerRecords(t, f.srv)); len(done) != 1 || done[0].RecoveryFiles != "kept" {
			t.Fatalf("complete = %+v", done)
		}
	})
	t.Run("answer no leaves records pending", func(t *testing.T) {
		f := purgeFixture(t)
		files := seedRecoveryFiles(t, f.srv)
		asked := 0
		f.e.Confirm = deletePrompt(false, &asked)
		rep, err := offline.Purge(t.Context(), f.e, purgeTask1(func(o *offline.PurgeOptions) { o.Yes = false }))
		if err != nil || rep.Complete || rep.RecoveryFiles != "" || countExisting(files) != 3 {
			t.Fatalf("rep = %+v, err = %v, left = %d", rep, err, countExisting(files))
		}
		out := f.e.Out.(*bytes.Buffer).String()
		if !strings.Contains(out, "--keep-recovery-files") || !strings.Contains(out, "tkr scrub-backups") {
			t.Fatalf("output = %q", out)
		}
		if len(completeLines(ledgerRecords(t, f.srv))) != 0 {
			t.Fatal("completed although recovery files were kept")
		}
	})
}
