package offline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/linsomniac/taskronin/internal/backup"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/fsutil"
	"github.com/linsomniac/taskronin/internal/service"
	"github.com/linsomniac/taskronin/internal/store"
	"github.com/linsomniac/taskronin/internal/textsafe"
)

// ImportResult describes a published import.
type ImportResult struct {
	Counts map[string]int
	Aside  string
}

// liveImportState is what an import carries over from the live database.
type liveImportState struct {
	Auth      store.AuthState
	Tokens    []domain.Token
	Seq       map[string]int64
	Bootstrap []store.BootstrapOp
	TZ        string
	Latest    int // newest schema version this binary knows
}

// importEmptyTables are the user-data tables that must be empty (A§12.5 step 1).
var importEmptyTables = []string{"tasks", "log_entries", "time_blocks", "tags", "interruptions"}

// Import replaces a freshly initialized database with the contents of an
// export file (A§12.5). The live database is only read until the swap.
//
// AIDEV-NOTE: CRASH-SAFETY ORDER, mirrors Restore. Do not reorder:
//  1. begin(refuseJournals): lock, no swap journal, CleanupTemps.
//  2. read the live DB (must exist, be empty of user data); close it.
//  3. decode the file with UseNumber, check header, re-validate titles/notes.
//  4. confirm; stage a NEW database under StagingDir; ImportData in one tx;
//     then (BF25, approved) RecordSystemOp in a second staged tx; replay the
//     ledger, Reconcile, CheckAll, integrity/foreign-key/FTS checks; close.
//  5. backup.Swap{Kind: import}. Only after this point is the live DB touched;
//     an interrupted swap is finished or undone by `tkr restore --resume` or
//     `--rollback`. Any failure before it removes the staging directory.
func Import(ctx context.Context, e *Env, path string, yes bool) (_ ImportResult, err error) {
	release, err := e.begin(ctx, refuseJournals)
	if err != nil {
		return ImportResult{}, err
	}
	defer release()
	if e.Server == nil {
		return ImportResult{}, domain.Errorf(domain.CodeInternal, "server configuration is required")
	}
	live, err := readLiveForImport(ctx, e)
	if err != nil {
		return ImportResult{}, err
	}
	file, err := readExportFile(path)
	if err != nil {
		return ImportResult{}, err
	}
	if err := checkExportHeader(file, live.Latest); err != nil {
		return ImportResult{}, err
	}
	if err := validateExportText(file); err != nil {
		return ImportResult{}, err
	}
	res := ImportResult{Aside: e.Paths.DB + ".pre-import-" + e.clock().Now().UTC().Format("20060102T150405Z")}
	if _, err := os.Lstat(res.Aside); err == nil {
		return res, domain.Errorf(domain.CodeConflict, "%s already exists (another import finished this second) — retry in a moment", textsafe.SanitizeLine(res.Aside))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return res, offlineError(err)
	}
	if !yes {
		if !e.TTY {
			return res, domain.Errorf(domain.CodeValidation, "import needs confirmation: run it on a terminal or pass --yes")
		}
		if e.Confirm == nil {
			return res, domain.Errorf(domain.CodeInternal, "confirmation reader is not configured")
		}
		ok, err := e.Confirm(textsafe.SanitizeLine(fmt.Sprintf("Import %s? The current (empty) database moves to %s.", filepath.Base(path), res.Aside)), false)
		if err != nil {
			return res, offlineError(err)
		}
		if !ok {
			return res, domain.Errorf(domain.CodeValidation, "import cancelled")
		}
	}
	if err := fsutil.EnsureDir0700(e.Paths.StagingDir); err != nil {
		return res, offlineError(err)
	}
	dir, err := os.MkdirTemp(e.Paths.StagingDir, "import-*")
	if err != nil {
		return res, offlineError(err)
	}
	staged := &backup.Staged{Dir: dir, Path: filepath.Join(dir, "taskronin.db")}
	swapStarted := false
	defer func() {
		// A started swap owns the staged file (its journal references it);
		// CleanupTemps spares it and --resume/--rollback finish the job.
		if !swapStarted || err == nil {
			err = errors.Join(err, staged.Remove())
		}
	}()
	if res.Counts, err = prepareImport(ctx, e, live, file, staged); err != nil {
		return res, err
	}
	swapStarted = true
	if err := backup.Swap(ctx, e.Paths.SwapJournal(), &backup.SwapJournal{
		Kind: backup.SwapImport, New: staged.Path, Target: e.Paths.DB, Aside: res.Aside, StartedAt: e.clock().Now().UTC(),
	}, nil); err != nil {
		e.swapFailureHint("import")
		return res, err
	}
	_, err = fmt.Fprintf(e.Out, "imported %d tasks, %d entries and %d blocks; the previous database is in %s\n",
		res.Counts["tasks"], res.Counts["log_entries"], res.Counts["time_blocks"], textsafe.SanitizeLine(res.Aside))
	return res, offlineError(err)
}

// readLiveForImport opens the live database, requires it to hold no user data
// and reads what the import carries over. The store is closed before return.
func readLiveForImport(ctx context.Context, e *Env) (_ *liveImportState, err error) {
	if _, err := os.Stat(e.Paths.DB); errors.Is(err, fs.ErrNotExist) {
		return nil, domain.Errorf(domain.CodeNotFound, "no database at %s: run `tkr init` first", textsafe.SanitizeLine(e.Paths.DB))
	}
	st, err := e.openStore(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, offlineError(st.Close())) }()
	status, err := st.SchemaStatus(ctx)
	if err != nil {
		return nil, offlineError(err)
	}
	for _, table := range importEmptyTables {
		var used bool
		// Table names are the constants above.
		if err := st.Reader.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM "+table+")").Scan(&used); err != nil {
			return nil, offlineError(err)
		}
		if used {
			return nil, domain.Errorf(domain.CodeConflict, "import needs a freshly initialized database")
		}
	}
	live := &liveImportState{Latest: status.Latest}
	if live.Auth, err = store.GetAuthState(ctx, st.Reader); err != nil {
		return nil, offlineError(err)
	}
	if live.Tokens, err = store.ListTokens(ctx, st.Reader); err != nil {
		return nil, offlineError(err)
	}
	if live.Seq, err = store.Sequences(ctx, st.Reader); err != nil {
		return nil, offlineError(err)
	}
	if live.Bootstrap, err = store.ReadBootstrapOps(ctx, st.Reader); err != nil {
		return nil, offlineError(err)
	}
	if live.TZ, err = store.GetSetting(ctx, st.Reader, "tz_last_seen"); err != nil {
		return nil, offlineError(err)
	}
	return live, nil
}

// readExportFile decodes the export with UseNumber so integers keep full
// precision (ruling P8). Syntax and type problems are validation errors.
func readExportFile(path string) (*store.ExportFile, error) {
	f, err := os.Open(path) //nolint:gosec // G304: export file named by the user on the command line
	if errors.Is(err, fs.ErrNotExist) {
		return nil, domain.Errorf(domain.CodeNotFound, "export file not found: %s", textsafe.SanitizeLine(path))
	}
	if err != nil {
		return nil, offlineError(err)
	}
	defer func() { _ = f.Close() }()
	dec := json.NewDecoder(f)
	dec.UseNumber()
	dec.DisallowUnknownFields()
	var file store.ExportFile
	err = dec.Decode(&file)
	if err == nil {
		// Exactly one document: anything after it is an error.
		if _, trailing := dec.Token(); !errors.Is(trailing, io.EOF) {
			err = errors.New("unexpected data after the export document")
		}
	}
	if err != nil {
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			return nil, offlineError(err)
		}
		return nil, domain.Errorf(domain.CodeValidation, "not a valid export file: %s", textsafe.SanitizeLine(err.Error()))
	}
	return &file, nil
}

func checkExportHeader(f *store.ExportFile, latest int) error {
	switch {
	case f.Format != store.ExportFormat:
		return domain.Errorf(domain.CodeValidation, "not a Taskronin export (format %q)", textsafe.SanitizeLine(f.Format))
	case f.Version < 1 || f.Version > store.ExportVersion:
		return domain.Errorf(domain.CodeValidation, "unsupported export version %d (this binary reads up to %d)", f.Version, store.ExportVersion)
	case f.SchemaVersion < 1 || f.SchemaVersion > latest:
		return domain.Errorf(domain.CodeValidation, "export schema %d is newer than this binary (%d)", f.SchemaVersion, latest)
	}
	return nil
}

// validateExportText re-validates every task and entry title and notes with
// textsafe and stores the normalized text back into the row.
func validateExportText(f *store.ExportFile) error {
	for _, table := range []string{"tasks", "log_entries"} {
		for _, row := range f.Tables[table] {
			id := "?"
			if n, ok := row["id"].(json.Number); ok {
				id = n.String()
			}
			for _, col := range []struct {
				name     string
				validate func(string) (string, error)
			}{{"title", textsafe.ValidateTitle}, {"notes", textsafe.ValidateNotes}} {
				raw, present := row[col.name]
				if !present {
					continue
				}
				text, ok := raw.(string)
				if !ok {
					return fieldError(table, id, col.name, "must be text")
				}
				clean, err := col.validate(text)
				if err != nil {
					return fieldError(table, id, col.name, err.Error())
				}
				row[col.name] = clean
			}
		}
	}
	return nil
}

func fieldError(table, id, column, msg string) error {
	de := domain.ValidationError(domain.FieldError{Field: table + "/" + id + "." + column, Message: msg})
	de.Detail = fmt.Sprintf("invalid %s/%s: %s %s", table, id, column, msg)
	return de
}

// prepareImport builds the staged database, leaving staged.Path a verified,
// stamped, checked, fsynced single file. The live database is not touched.
func prepareImport(ctx context.Context, e *Env, live *liveImportState, file *store.ExportFile, staged *backup.Staged) (counts map[string]int, err error) {
	st, err := store.OpenStaging(ctx, staged.Path)
	if err != nil {
		return nil, offlineError(err)
	}
	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, offlineError(st.Close()))
		}
	}()
	if _, err := st.Migrate(ctx, nil); err != nil {
		return nil, offlineError(err)
	}
	counts, err = importStaged(ctx, e, st, live, file)
	if err != nil {
		return nil, err
	}
	ledger, health, err := backup.OpenLedger(e.Paths.LedgerFile)
	if err != nil {
		return nil, offlineError(err)
	}
	if err := e.warnDamagedLedger(health, "import"); err != nil {
		return nil, err
	}
	recs, err := ledger.Records()
	if err != nil {
		return nil, offlineError(err)
	}
	svc, err := e.newService(st, ledger)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(counts)
	if err != nil {
		return nil, offlineError(err)
	}
	// AIDEV-NOTE: BF25 (approved) — this is the second staged transaction; the
	// staged file is unpublished until every commit and check below succeeds.
	if _, err := svc.RecordSystemOp(ctx, domain.OpSystemImport, payload); err != nil {
		return nil, err
	}
	if _, err := svc.ReplayLedger(ctx, recs, service.ReplayLive); err != nil {
		return nil, err
	}
	if err := svc.Reconcile(ctx); err != nil {
		return nil, err
	}
	if err := verifyAndSealStage(ctx, e, st, svc, staged.Path, true, &closed); err != nil {
		return nil, err
	}
	return counts, nil
}

// importStaged is A§12.5 steps 3-4: carry over the live auth state and tokens,
// then ImportData with a new db_generation, all in one staged transaction.
func importStaged(ctx context.Context, e *Env, st *store.Store, live *liveImportState, file *store.ExportFile) (map[string]int, error) {
	tx, err := st.Writer.BeginTx(ctx, nil)
	if err != nil {
		return nil, offlineError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := store.PutAuthState(ctx, tx, live.Auth); err != nil {
		return nil, offlineError(err)
	}
	if err := store.ReplaceTokens(ctx, tx, live.Tokens); err != nil {
		return nil, offlineError(err)
	}
	if err := store.SetSetting(ctx, tx, "tz_last_seen", live.TZ); err != nil {
		return nil, offlineError(err)
	}
	counts, err := store.ImportData(ctx, tx, store.ImportIn{File: file, Bootstrap: live.Bootstrap,
		Generation: domain.NewGeneration(), Now: e.clock().Now(), LiveSequences: live.Seq})
	if err != nil {
		return nil, offlineError(err)
	}
	return counts, offlineError(tx.Commit())
}
