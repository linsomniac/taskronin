package offline

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"taskronin/internal/auth"
	"taskronin/internal/backup"
	"taskronin/internal/domain"
	"taskronin/internal/fsutil"
	"taskronin/internal/service"
	"taskronin/internal/store"
	"taskronin/internal/textsafe"
)

// RestoreResult describes a published restore.
type RestoreResult struct {
	Snapshot, Aside  string
	OwnerTokenMinted bool
}

// currentState is what a restore carries over from the live database. It is
// read from the live DB; Restore passes nil when that DB is unreadable.
type currentState struct {
	Auth   store.AuthState
	Tokens []domain.Token
	Seq    map[string]int64
}

// unreadableSeqGap is added to the best recoverable sequence marks when the live
// database cannot be read, so ids handed out since the last readable snapshot
// are never reissued (A§12.2 step 4).
const unreadableSeqGap = 10_000

// Restore replaces the live database with a snapshot (A§12.2 steps 0-6).
//
// AIDEV-NOTE: CRASH-SAFETY ORDER. Do not reorder these steps:
//  0. begin(refuseJournals): lock, refuse an existing swap journal, CleanupTemps.
//  1. confirm, BEFORE any copy; resolve/validate BEFORE the live DB is opened.
//  2. Stage (the original is only read, R4-5) -> Verify the staged copy ->
//     schema <= binary -> Migrate the staged copy.
//  3. Only now open the live DB (read auth_state, tokens, sequences). Opening
//     can checkpoint the live WAL, so it must follow every refusal above.
//  4. stampRestore: one writer tx on the STAGED store only.
//  5. system.restore op, ledger replay, reconcile, CheckAll, integrity+FTS
//     checks, close, assert no sidecars, fsync. Nothing live is touched yet.
//  6. backup.Swap with OwnerTokenPending set when a token was minted. The
//     owner.token file is written ONLY by onInstalled: after the staged DB is
//     installed and BEFORE the journal is removed (R4-10). A crash before that
//     leaves the old DB + old token consistent (rollback); a crash after
//     "installed" is finished by --resume, which cannot re-run onInstalled and
//     therefore reports a lost token (OwnerTokenPending) so the user can run
//     `tkr passwd --reset`. The secret is never journaled.
func Restore(ctx context.Context, e *Env, snapshot string, yes bool) (_ RestoreResult, err error) {
	release, err := e.begin(ctx, refuseJournals)
	if err != nil {
		return RestoreResult{}, err
	}
	defer release()
	if e.Server == nil {
		return RestoreResult{}, domain.Errorf(domain.CodeInternal, "server configuration is required")
	}
	set, err := resolveSnapshot(snapshot, e.Paths.BackupDir)
	if err != nil {
		return RestoreResult{}, err
	}
	res := RestoreResult{Snapshot: snapshotLabel(set)}
	res.Aside = e.Paths.DB + ".pre-restore-" + e.clock().Now().UTC().Format("20060102T150405Z")
	if _, err := os.Lstat(res.Aside); err == nil {
		return res, domain.Errorf(domain.CodeConflict, "%s already exists (another restore finished this second) — retry in a moment", textsafe.SanitizeLine(res.Aside))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return res, offlineError(err)
	}
	if !yes {
		if !e.TTY {
			return res, domain.Errorf(domain.CodeValidation, "restore needs confirmation: run it on a terminal or pass --yes")
		}
		if e.Confirm == nil {
			return res, domain.Errorf(domain.CodeInternal, "confirmation reader is not configured")
		}
		ok, err := e.Confirm(textsafe.SanitizeLine(fmt.Sprintf("Restore %s? The current database moves to %s.", res.Snapshot, res.Aside)), false)
		if err != nil {
			return res, offlineError(err)
		}
		if !ok {
			return res, domain.Errorf(domain.CodeValidation, "restore cancelled")
		}
	}
	if err := fsutil.EnsureDir0700(e.Paths.StagingDir); err != nil {
		return res, offlineError(err)
	}
	staged, err := backup.Stage(ctx, e.Paths.StagingDir, set)
	if err != nil {
		return res, classifyStageError(err)
	}
	swapStarted := false
	defer func() {
		// A started swap owns the staged file (its journal references it);
		// CleanupTemps spares it and --resume/--rollback finish the job.
		if !swapStarted || err == nil {
			err = errors.Join(err, staged.Remove())
		}
	}()
	owner, err := prepareStaged(ctx, e, set, staged, &res)
	if err != nil {
		return res, err
	}
	swapStarted = true
	var onInstalled func() error
	if owner != "" {
		onInstalled = func() error {
			if err := fsutil.WriteFileAtomic(e.Paths.OwnerToken, []byte(owner+"\n"), 0o600); err != nil {
				return fmt.Errorf("the database was restored but %s could not be written: %s — run `tkr restore --resume`, then `tkr passwd --reset`",
					textsafe.SanitizeLine(e.Paths.OwnerToken), textsafe.SanitizeLine(err.Error()))
			}
			return nil
		}
	}
	err = backup.Swap(ctx, e.Paths.SwapJournal(), &backup.SwapJournal{
		Kind: backup.SwapRestore, New: staged.Path, Target: e.Paths.DB, Aside: res.Aside,
		OwnerTokenPending: res.OwnerTokenMinted, StartedAt: e.clock().Now().UTC(),
	}, onInstalled)
	if err != nil {
		return res, err
	}
	_, err = fmt.Fprintf(e.Out, "restored %s; the previous database is in %s\n",
		textsafe.SanitizeLine(res.Snapshot), textsafe.SanitizeLine(res.Aside))
	return res, offlineError(err)
}

// prepareStaged runs steps 2-5: it leaves staged.Path a verified, migrated,
// stamped, checked, fsynced single file, and returns the formatted owner token
// when one was minted. The live database is read but never modified.
func prepareStaged(ctx context.Context, e *Env, set backup.FileSet, staged *backup.Staged, res *RestoreResult) (owner string, err error) {
	if _, err := backup.Verify(ctx, staged.Path); err != nil {
		return "", invalidSnapshot(err)
	}
	st, err := store.OpenStaging(ctx, staged.Path)
	if err != nil {
		return "", invalidSnapshot(err)
	}
	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, offlineError(st.Close()))
		}
	}()
	status, err := st.SchemaStatus(ctx)
	if errors.Is(err, store.ErrSchemaTooNew) {
		var snap int
		if qerr := st.Reader.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&snap); qerr != nil {
			return "", offlineError(qerr)
		}
		return "", domain.Errorf(domain.CodeValidation, "snapshot schema %d is newer than this binary (%d)", snap, status.Latest)
	}
	if err != nil {
		return "", invalidSnapshot(err)
	}
	if _, err := st.Migrate(ctx, nil); err != nil {
		return "", invalidSnapshot(err)
	}
	cur, readErr := readCurrent(ctx, e)
	if err := ctx.Err(); err != nil {
		return "", offlineError(err)
	}
	if cur == nil {
		// Unreadable live DB: recover the best sequence marks from the staged
		// copy and every readable snapshot (BF24: the live marks are lost).
		marks, err := store.Sequences(ctx, st.Reader)
		if err != nil {
			return "", offlineError(err)
		}
		mergeMarks(marks, recoverSequences(ctx, e, set))
		if err := raiseStaged(ctx, st, marks); err != nil {
			return "", err
		}
		if _, err := fmt.Fprintf(e.Err, "WARNING: the current database could not be read (%s).\n"+
			"  Every API token is revoked and a NEW owner token is written to %s.\n"+
			"  Web login is disabled until you run `tkr passwd --reset`.\n"+
			"  Id sequences are advanced by %d past the highest value found in any readable snapshot;\n"+
			"  ids issued after the newest snapshot cannot be recovered.\n",
			textsafe.SanitizeLine(readErr.Error()), textsafe.SanitizeLine(e.Paths.OwnerToken), unreadableSeqGap); err != nil {
			return "", offlineError(err)
		}
	}
	secret, err := stampRestore(ctx, st, cur, e.clock().Now())
	if err != nil {
		return "", err
	}
	if secret != nil {
		res.OwnerTokenMinted = true
		owner, err = formatMintedOwner(ctx, st, *secret)
		if err != nil {
			return "", err
		}
	}
	ledger, _, err := backup.OpenLedger(e.Paths.LedgerFile)
	if err != nil {
		return "", offlineError(err)
	}
	recs, err := ledger.Records()
	if err != nil {
		return "", offlineError(err)
	}
	svc, err := e.newService(st, ledger)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(map[string]string{"snapshot": res.Snapshot})
	if err != nil {
		return "", offlineError(err)
	}
	if _, err := svc.RecordSystemOp(ctx, domain.OpSystemRestore, payload); err != nil {
		return "", err
	}
	if _, err := svc.ReplayLedger(ctx, recs, service.ReplayLive); err != nil {
		return "", err
	}
	if err := svc.Reconcile(ctx); err != nil {
		return "", err
	}
	if err := service.CheckAll(ctx, st.Writer, e.clock().Now(), svc.Zone()); err != nil {
		return "", err
	}
	if err := store.IntegrityCheck(ctx, st.Writer); err != nil {
		return "", offlineError(err)
	}
	if err := store.FTSIntegrityCheck(ctx, st.Writer); err != nil {
		return "", offlineError(err)
	}
	closed = true
	if err := st.Close(); err != nil {
		return "", offlineError(err)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(staged.Path + suffix); err == nil {
			return "", domain.Errorf(domain.CodeInternal, "staged database sidecar remains: %s", filepath.Base(staged.Path+suffix))
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", offlineError(err)
		}
	}
	f, err := os.Open(staged.Path)
	if err != nil {
		return "", offlineError(err)
	}
	if err := errors.Join(f.Sync(), f.Close()); err != nil {
		return "", offlineError(err)
	}
	return owner, nil
}

// stampRestore is A§12.2 step 4 in one writer transaction on the staged store:
// carry over the live auth state and tokens (or, when cur is nil, revoke every
// token, mint a new owner token and disable web login), raise sequences, assign
// a new generation, and drop web sessions and idempotency rows.
//
// AIDEV-NOTE: store writes only; the system.restore op is recorded afterwards
// by the caller so the service reads the NEW generation. ClearIdempotency is
// unconditional: rows with a future created_at (clock skew) would otherwise
// survive DeleteIdemBefore. With cur == nil, sequences are bumped by
// unreadableSeqGap over whatever the staged store holds, so the caller first
// raises the staged marks to the best recoverable ones.
func stampRestore(ctx context.Context, st *store.Store, cur *currentState, now time.Time) (ownerSecret *[32]byte, err error) {
	tx, err := st.Writer.BeginTx(ctx, nil)
	if err != nil {
		return nil, offlineError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if cur != nil {
		if err := store.PutAuthState(ctx, tx, cur.Auth); err != nil {
			return nil, offlineError(err)
		}
		if err := store.ReplaceTokens(ctx, tx, cur.Tokens); err != nil {
			return nil, offlineError(err)
		}
		if err := store.RaiseSequences(ctx, tx, cur.Seq); err != nil {
			return nil, offlineError(err)
		}
	} else {
		marks, err := store.Sequences(ctx, tx)
		if err != nil {
			return nil, offlineError(err)
		}
		for name := range marks {
			marks[name] += unreadableSeqGap
		}
		if err := store.RaiseSequences(ctx, tx, marks); err != nil {
			return nil, offlineError(err)
		}
		if _, err := store.RevokeAllTokens(ctx, tx, now); err != nil {
			return nil, offlineError(err)
		}
		name, err := auth.OwnerTokenName(now)
		if err != nil {
			return nil, offlineError(err)
		}
		secret, err := auth.NewSecret()
		if err != nil {
			return nil, offlineError(err)
		}
		tok := domain.Token{Name: name, SecretHash: auth.HashSecret(secret), Scopes: domain.AllScopes, IsOwner: true, CreatedAt: now}
		if err := store.InsertToken(ctx, tx, &tok); err != nil {
			return nil, offlineError(err)
		}
		state, err := store.GetAuthState(ctx, tx)
		if err != nil {
			return nil, offlineError(err)
		}
		state.WebLoginDisabled = true
		if err := store.PutAuthState(ctx, tx, state); err != nil {
			return nil, offlineError(err)
		}
		ownerSecret = &secret
	}
	if err := store.SetSetting(ctx, tx, "db_generation", string(domain.NewGeneration())); err != nil {
		return nil, offlineError(err)
	}
	if _, err := store.DeleteAllSessions(ctx, tx); err != nil {
		return nil, offlineError(err)
	}
	if err := store.ClearIdempotency(ctx, tx); err != nil {
		return nil, offlineError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, offlineError(err)
	}
	return ownerSecret, nil
}

// formatMintedOwner finds the freshly inserted owner token by its secret hash.
func formatMintedOwner(ctx context.Context, st *store.Store, secret [32]byte) (string, error) {
	tokens, err := store.ListTokens(ctx, st.Reader)
	if err != nil {
		return "", offlineError(err)
	}
	want := auth.HashSecret(secret)
	for _, t := range tokens {
		if t.IsOwner && !t.RevokedAt.Valid && t.SecretHash == want {
			return auth.FormatToken(t.ID, secret), nil
		}
	}
	return "", domain.Errorf(domain.CodeInternal, "the new owner token was not found after staging")
}

func raiseStaged(ctx context.Context, st *store.Store, marks map[string]int64) (err error) {
	tx, err := st.Writer.BeginTx(ctx, nil)
	if err != nil {
		return offlineError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := store.RaiseSequences(ctx, tx, marks); err != nil {
		return offlineError(err)
	}
	return offlineError(tx.Commit())
}

// readCurrent reads the live DB's auth state, tokens and sequences. It returns
// nil and the reason when the database is missing or unreadable (A§12.2 step 3).
func readCurrent(ctx context.Context, e *Env) (*currentState, error) {
	st, err := store.Open(ctx, e.Paths.DB)
	if err != nil {
		return nil, err
	}
	defer func() { _ = st.Close() }()
	var cur currentState
	if cur.Auth, err = store.GetAuthState(ctx, st.Reader); err != nil {
		return nil, err
	}
	if cur.Tokens, err = store.ListTokens(ctx, st.Reader); err != nil {
		return nil, err
	}
	if cur.Seq, err = store.Sequences(ctx, st.Reader); err != nil {
		return nil, err
	}
	return &cur, nil
}

// recoverSequences returns the per-table maximum over every readable snapshot.
// Snapshots are only read through Stage copies; unreadable ones are skipped.
func recoverSequences(ctx context.Context, e *Env, restoring backup.FileSet) map[string]int64 {
	marks := make(map[string]int64)
	infos, err := backup.List(e.Paths.BackupDir)
	if err != nil {
		return marks
	}
	for _, info := range infos {
		if filepath.Clean(info.Path) == filepath.Clean(restoring.DB) {
			continue
		}
		staged, err := backup.Stage(ctx, e.Paths.StagingDir, backup.FileSet{DB: info.Path})
		if err != nil {
			continue
		}
		if st, err := store.OpenStaging(ctx, staged.Path); err == nil {
			if seq, err := store.Sequences(ctx, st.Reader); err == nil {
				mergeMarks(marks, seq)
			}
			_ = st.Close()
		}
		_ = staged.Remove()
	}
	return marks
}

func mergeMarks(dst, src map[string]int64) {
	for name, seq := range src {
		dst[name] = max(dst[name], seq)
	}
}

// resolveSnapshot maps a user argument to a database file set: an existing
// file; a directory holding taskronin.db (a pre-restore/pre-import set, whose
// -wal is part of the set); or a bare name found in the backup directory.
func resolveSnapshot(arg, backupDir string) (backup.FileSet, error) {
	if arg == "" {
		return backup.FileSet{}, domain.Errorf(domain.CodeValidation, "a snapshot name or path is required")
	}
	info, err := os.Stat(arg)
	switch {
	case err == nil && info.IsDir():
		db := filepath.Join(arg, "taskronin.db")
		if fi, err := os.Stat(db); err != nil || !fi.Mode().IsRegular() {
			return backup.FileSet{}, domain.Errorf(domain.CodeValidation, "not a valid snapshot: %s holds no taskronin.db", textsafe.SanitizeLine(arg))
		}
		return absSet(db)
	case err == nil:
		return absSet(arg)
	case !errors.Is(err, fs.ErrNotExist):
		return backup.FileSet{}, offlineError(err)
	}
	if !strings.ContainsRune(arg, filepath.Separator) {
		candidate := filepath.Join(backupDir, arg)
		if fi, err := os.Stat(candidate); err == nil && fi.Mode().IsRegular() {
			return absSet(candidate)
		}
	}
	return backup.FileSet{}, domain.Errorf(domain.CodeNotFound, "snapshot not found: %s (looked in . and %s)",
		textsafe.SanitizeLine(arg), textsafe.SanitizeLine(backupDir))
}

func absSet(path string) (backup.FileSet, error) {
	abs, err := filepath.Abs(path)
	return backup.FileSet{DB: abs}, offlineError(err)
}

// snapshotLabel names a set for prompts and the system.restore payload; a
// pre-restore set's file is always taskronin.db, so its directory names it.
func snapshotLabel(set backup.FileSet) string {
	name := filepath.Base(set.DB)
	if name == "taskronin.db" {
		return filepath.Base(filepath.Dir(set.DB))
	}
	return name
}

// classifyStageError maps SQLite-level failures from backup.Stage (not a
// database, corrupt header) to the promised validation error while keeping
// filesystem and I/O causes as they are (BF23).
func classifyStageError(err error) error {
	var se interface {
		error
		Code() int
	}
	if errors.As(err, &se) {
		return invalidSnapshot(err)
	}
	return offlineError(err)
}

// invalidSnapshot reports a snapshot that failed verification, schema or
// migration checks. Cancellation is not a verdict on the snapshot.
func invalidSnapshot(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return offlineError(err)
	}
	return &domain.Error{Code: domain.CodeValidation, Detail: "not a valid snapshot: " + textsafe.SanitizeLine(err.Error()), Err: err}
}

// RestoreResume finishes an interrupted restore or import swap. It cannot rerun
// the owner-token callback, so when a minted token was never saved it says so.
func RestoreResume(ctx context.Context, e *Env) error {
	release, err := e.begin(ctx, allowSwapJournal)
	if err != nil {
		return err
	}
	defer release()
	j, err := readSwapJournal(e)
	if err != nil {
		return err
	}
	if err := backup.ResumeSwap(ctx, e.Paths.SwapJournal()); err != nil {
		return swapRecoveryError(err)
	}
	if err := removeStageDir(e, j); err != nil {
		return err
	}
	if j.OwnerTokenPending {
		ok, err := ownerTokenWorks(ctx, e)
		if err != nil {
			return err
		}
		if !ok {
			if _, err := fmt.Fprintln(e.Err, "the new owner token was not saved — run `tkr passwd --reset`"); err != nil {
				return offlineError(err)
			}
		}
	}
	_, err = fmt.Fprintf(e.Out, "%s resumed and completed\n", j.Kind)
	return offlineError(err)
}

// RestoreRollback puts the old database back after an interrupted restore or
// import, before its old set was deleted.
func RestoreRollback(ctx context.Context, e *Env) error {
	release, err := e.begin(ctx, allowSwapJournal)
	if err != nil {
		return err
	}
	defer release()
	j, err := readSwapJournal(e)
	if err != nil {
		return err
	}
	if err := backup.RollbackSwap(ctx, e.Paths.SwapJournal()); err != nil {
		return swapRecoveryError(err)
	}
	if err := removeStageDir(e, j); err != nil {
		return err
	}
	if j.OwnerTokenPending {
		// The unreadable-DB restore may already have replaced owner.token.
		if _, err := fmt.Fprintln(e.Err, "rolled back; if the server rejects the owner token, run `tkr passwd --reset`"); err != nil {
			return offlineError(err)
		}
	}
	_, err = fmt.Fprintf(e.Out, "%s rolled back; the previous database is restored\n", j.Kind)
	return offlineError(err)
}

// removeStageDir deletes the (now empty or superseded) Stage directory that
// held the journal's New file, once the swap is finished or rolled back.
func removeStageDir(e *Env, j *backup.SwapJournal) error {
	dir := filepath.Dir(j.New)
	if filepath.Clean(filepath.Dir(dir)) != filepath.Clean(e.Paths.StagingDir) {
		return nil
	}
	return offlineError(os.RemoveAll(dir))
}

func readSwapJournal(e *Env) (*backup.SwapJournal, error) {
	j, err := backup.ReadJournal(e.Paths.SwapJournal())
	if errors.Is(err, backup.ErrNoJournal) {
		return nil, domain.Errorf(domain.CodeNotFound, "no interrupted restore or import to recover")
	}
	if err != nil {
		return nil, offlineError(err)
	}
	if j.Kind != backup.SwapRestore && j.Kind != backup.SwapImport {
		return nil, domain.Errorf(domain.CodeConflict, "the swap journal is for a %s, not a restore or import", j.Kind)
	}
	return j, nil
}

func swapRecoveryError(err error) error {
	if errors.Is(err, backup.ErrRollbackInProgress) || errors.Is(err, backup.ErrNoRollback) {
		return &domain.Error{Code: domain.CodeConflict, Detail: textsafe.SanitizeLine(err.Error()), Err: err}
	}
	return offlineError(err)
}

// ownerTokenWorks reports whether owner.token authenticates as a live owner
// token of the installed database.
func ownerTokenWorks(ctx context.Context, e *Env) (bool, error) {
	data, err := os.ReadFile(e.Paths.OwnerToken)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, offlineError(err)
	}
	id, secret, err := auth.ParseToken(strings.TrimSpace(string(data)))
	if err != nil {
		return false, nil
	}
	st, err := e.openStore(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = st.Close() }()
	tok, err := store.GetToken(ctx, st.Reader, id)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, offlineError(err)
	}
	hash := auth.HashSecret(secret)
	return tok.IsOwner && !tok.RevokedAt.Valid && subtle.ConstantTimeCompare(tok.SecretHash[:], hash[:]) == 1, nil
}
