package offline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"taskronin/internal/backup"
	"taskronin/internal/domain"
	"taskronin/internal/fsutil"
	"taskronin/internal/service"
	"taskronin/internal/store"
	"taskronin/internal/textsafe"
)

// PurgeOptions are the arguments and flags of `tkr purge task|entry ID`.
type PurgeOptions struct {
	Target                         string // "task" or "entry"
	ID                             int64
	WithEntries, KeepRecoveryFiles bool
	Yes                            bool
}

// PurgeReport is the outcome of a purge run.
type PurgeReport struct {
	TaskIDs, EntryIDs, LinkedNotIncluded []int64
	Changed                              bool
	Sets                                 []SetResult
	RecoveryFiles                        string // the mode on the complete lines; "" if not completed now
	Complete                             bool   // no pending ledger record remains
}

const (
	purgeCrashCommitted      = "purge:committed"
	purgeCrashCheckpointed   = "purge:checkpointed"
	purgeCrashBeforeComplete = "purge:before-complete"
	repairCrashRepaired      = "repair:repaired"
	purgeTitleWidth          = 60
	purgeOutOfReachNotice    = "Exports and log lines made earlier are out of reach."
	purgeRepairLedgerAdvice  = "run `tkr purge --repair-ledger`"
)

// Purge is A§12.6 steps 1-10: scrub a tombstoned task or entry from the live
// database and every backup set, offline.
//
// AIDEV-NOTE: SECURITY-CRITICAL ORDER (S4, R3-2, R4-1, BF26, BF27). Do not reorder:
//  1. begin(refuseJournals): lock, refuse swap/scrub journals, CleanupTemps.
//  2. Open the store, verify the password on a TTY (before ANY file other than
//     the DB is read), then confirm the target from READ-ONLY queries: a
//     declined or refused purge must not touch the ledger (OpenLedger itself
//     truncates a torn tail) or write an ops row.
//  3. OpenLedger ONCE (a damaged ledger refuses), replay it on the live DB and
//     checkpoint, then service.PurgeLive. PurgeLive appends the pending record
//     INSIDE its writer transaction, before ScrubEntities: the intent is durable
//     before any scrub can commit, and a crash anywhere after is finished by the
//     next replay.
//  4. Confirmed wal_checkpoint(TRUNCATE), so no purged byte survives in the
//     live -wal. Then a fresh (clean) prescrub snapshot, close the store, and
//     scrubSets over every other backup set and pre-restore/pre-import set.
//  5. CleanRecoveryFiles, and ONLY THEN complete lines for all pending records
//     (completeScrub): any earlier failure leaves the records pending so a
//     rerun or `tkr scrub-backups` finishes the job. Never append `complete`
//     before every set is clean/rewritten.
func Purge(ctx context.Context, e *Env, o PurgeOptions) (rep PurgeReport, err error) {
	if o.Target != "task" && o.Target != "entry" {
		return rep, domain.Errorf(domain.CodeValidation, "target must be task or entry")
	}
	release, err := e.begin(ctx, refuseJournals)
	if err != nil {
		return rep, err
	}
	defer release()
	if e.Server == nil {
		return rep, domain.Errorf(domain.CodeInternal, "server configuration is required")
	}
	st, err := e.openStore(ctx)
	if err != nil {
		return rep, err
	}
	closeStore := func() error {
		if st == nil {
			return nil
		}
		cerr := offlineError(st.Close())
		st = nil
		return cerr
	}
	defer func() { err = errors.Join(err, closeStore()) }()
	if _, err := st.SchemaStatus(ctx); err != nil {
		return rep, offlineError(err)
	}
	if err := e.verifyPassword(ctx, st.Reader); err != nil {
		return rep, err
	}
	// Ruling P9/BF27: validate and confirm from read-only queries.
	preview, err := previewPurge(ctx, st.Reader, o)
	if err != nil {
		return rep, err
	}
	if !o.Yes {
		if e.Confirm == nil {
			return rep, domain.Errorf(domain.CodeInternal, "confirmation reader is not configured")
		}
		ok, err := e.Confirm(preview.prompt(o), false)
		if err != nil {
			return rep, offlineError(err)
		}
		if !ok {
			return rep, domain.Errorf(domain.CodeValidation, "purge cancelled; nothing was changed")
		}
	}
	ledger, health, err := backup.OpenLedger(e.Paths.LedgerFile)
	if err != nil {
		return rep, offlineError(err)
	}
	if health.Damaged {
		return rep, domain.Errorf(domain.CodeConflict, "the purge ledger is damaged: %s", purgeRepairLedgerAdvice)
	}
	svc, err := e.newService(st, ledger)
	if err != nil {
		return rep, err
	}
	if err := svc.ReapplyLedger(ctx); err != nil {
		return rep, err
	}
	if err := checkpoint(ctx, st); err != nil {
		return rep, err
	}
	owner := domain.Actor{Kind: domain.ActorOwner, Scopes: domain.AllScopes, Channel: domain.ChanCLI, RemoteKey: "socket"}
	res, err := svc.PurgeLive(ctx, service.Meta{Actor: owner, Gen: svc.Generation()},
		service.PurgeLiveIn{Target: o.Target, ID: o.ID, WithEntries: o.WithEntries})
	if err != nil {
		return rep, err
	}
	rep.TaskIDs, rep.EntryIDs, rep.LinkedNotIncluded, rep.Changed = res.TaskIDs, res.EntryIDs, res.LinkedNotIncluded, res.Changed
	if err := crashPoint(ctx, purgeCrashCommitted); err != nil {
		return rep, err
	}
	if err := checkpoint(ctx, st); err != nil {
		return rep, err
	}
	if err := crashPoint(ctx, purgeCrashCheckpointed); err != nil {
		return rep, err
	}
	if err := fsutil.EnsureDir0700(e.Paths.BackupDir); err != nil {
		return rep, offlineError(err)
	}
	manager := backup.Manager{Dir: e.Paths.BackupDir, Clock: e.clock(), Snap: st.Snap}
	info, err := manager.Snapshot(ctx, backup.KindPrescrub)
	if err != nil {
		return rep, offlineError(err)
	}
	if err := closeStore(); err != nil {
		return rep, err
	}
	if err := e.printPurged(rep); err != nil {
		return rep, err
	}
	recs, err := ledger.Records()
	if err != nil {
		return rep, offlineError(err)
	}
	var scrubErr error
	rep.Sets, scrubErr = scrubSets(ctx, e, recs, false, info.Path)
	e.printResults(rep.Sets)
	if scrubErr != nil {
		return rep, scrubErr
	}
	for _, r := range rep.Sets {
		if r.Status == scrubError {
			return rep, domain.Errorf(domain.CodeInternal,
				"a backup set could not be scrubbed; nothing was marked complete: run `tkr scrub-backups` to finish")
		}
	}
	rep.RecoveryFiles, err = e.completeScrub(ctx, ledger, health, rep.Sets,
		ScrubOptions{KeepRecoveryFiles: o.KeepRecoveryFiles, Yes: o.Yes}, purgeCrashBeforeComplete)
	if err != nil {
		return rep, err
	}
	pending, err := ledger.Pending()
	if err != nil {
		return rep, offlineError(err)
	}
	rep.Complete = len(pending) == 0
	if rep.RecoveryFiles != "" {
		if _, err := fmt.Fprintf(e.Out, "recovery files: %s\n", rep.RecoveryFiles); err != nil {
			return rep, offlineError(err)
		}
	}
	if _, err := fmt.Fprintln(e.Out, purgeOutOfReachNotice); err != nil {
		return rep, offlineError(err)
	}
	if !rep.Complete {
		_, err = fmt.Fprintln(e.Out, "the purge is not complete: run `tkr scrub-backups` to finish")
		return rep, offlineError(err)
	}
	return rep, nil
}

func (e *Env) printPurged(rep PurgeReport) error {
	var b strings.Builder
	b.WriteString("purged")
	if len(rep.TaskIDs) != 0 {
		b.WriteString(" task " + joinIDs("#", rep.TaskIDs))
		if len(rep.EntryIDs) != 0 {
			b.WriteString(" and entries " + joinIDs("e", rep.EntryIDs))
		}
	} else {
		b.WriteString(" entries " + joinIDs("e", rep.EntryIDs))
	}
	if !rep.Changed {
		b.WriteString(" (already purged; nothing changed)")
	}
	b.WriteString("\n")
	if len(rep.LinkedNotIncluded) != 0 {
		b.WriteString("linked entries not purged: " + joinIDs("e", rep.LinkedNotIncluded) + " (use --with-entries)\n")
	}
	_, err := fmt.Fprint(e.Out, b.String())
	return offlineError(err)
}

func joinIDs(prefix string, ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = prefix + strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ", ")
}

// purgePreview is what the confirmation prompt describes.
type purgePreview struct {
	target   string // "task" or "entry"
	id       int64
	title    string
	included []int64 // entries that will be purged with a task
	linked   []int64 // entries left out
}

// previewPurge validates the target with read-only queries and mirrors the
// checks of service.PurgeLive (which still validates again inside its own
// transaction): missing -> not_found, not tombstoned -> conflict.
func previewPurge(ctx context.Context, q store.Q, o PurgeOptions) (purgePreview, error) {
	p := purgePreview{target: o.Target, id: o.ID}
	if o.Target == "entry" {
		e, err := store.GetEntry(ctx, q, domain.EntryID(o.ID))
		if errors.Is(err, store.ErrNotFound) {
			return p, domain.Errorf(domain.CodeNotFound, "entry %d not found", o.ID)
		}
		if err != nil {
			return p, offlineError(err)
		}
		p.title = e.Title
		if e.DeletedAt.Valid {
			return p, nil
		}
		if e.TaskID.Valid {
			t, err := store.GetTask(ctx, q, e.TaskID.V)
			if err != nil {
				return p, offlineError(err)
			}
			if t.DeletedAt.Valid {
				return p, domain.Errorf(domain.CodeConflict, "delete it first; use tkr purge task %d --with-entries", t.ID)
			}
		}
		return p, domain.Errorf(domain.CodeConflict, "delete it first")
	}
	t, err := store.GetTask(ctx, q, domain.TaskID(o.ID))
	if errors.Is(err, store.ErrNotFound) {
		return p, domain.Errorf(domain.CodeNotFound, "task %d not found", o.ID)
	}
	if err != nil {
		return p, offlineError(err)
	}
	p.title = t.Title
	if !t.DeletedAt.Valid {
		return p, domain.Errorf(domain.CodeConflict, "delete it first")
	}
	task := sql.Null[domain.TaskID]{V: t.ID, Valid: true}
	entries, err := store.ListEntries(ctx, q, store.EntryQuery{Task: &task, IncludeDeleted: true, Limit: -1})
	if err != nil {
		return p, offlineError(err)
	}
	for _, en := range entries {
		if o.WithEntries || en.Kind == domain.EntryComplete {
			p.included = append(p.included, int64(en.ID))
		} else {
			p.linked = append(p.linked, int64(en.ID))
		}
	}
	slices.Sort(p.included)
	slices.Sort(p.linked)
	return p, nil
}

func (p purgePreview) prompt(o PurgeOptions) string {
	title := textsafe.SanitizeLine(textsafe.Truncate(p.title, purgeTitleWidth, "…"))
	var b strings.Builder
	if p.target == "entry" {
		fmt.Fprintf(&b, "Purge entry e%d '%s'?", p.id, title)
	} else {
		fmt.Fprintf(&b, "Purge task #%d '%s'", p.id, title)
		switch {
		case len(p.included) == 0:
		case o.WithEntries:
			b.WriteString(" and its entries " + joinIDs("e", p.included))
		default:
			b.WriteString(" and its completion entries " + joinIDs("e", p.included))
		}
		b.WriteString("?")
		if len(p.linked) != 0 {
			b.WriteString(" Linked entries " + joinIDs("e", p.linked) + " are kept (use --with-entries).")
		}
	}
	b.WriteString(" This cannot be undone.")
	return b.String()
}

// RepairLedger is `tkr purge --repair-ledger` (A§12.6): under the lock and the
// owner password, rewrite the ledger from its valid records, then replay it on
// the live database and checkpoint. It returns the dropped line numbers.
func RepairLedger(ctx context.Context, e *Env) (dropped []int, err error) {
	release, err := e.begin(ctx, refuseJournals)
	if err != nil {
		return nil, err
	}
	defer release()
	if e.Server == nil {
		return nil, domain.Errorf(domain.CodeInternal, "server configuration is required")
	}
	st, err := e.openStore(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, offlineError(st.Close())) }()
	if _, err := st.SchemaStatus(ctx); err != nil {
		return nil, offlineError(err)
	}
	if err := e.verifyPassword(ctx, st.Reader); err != nil {
		return nil, err
	}
	ledger, _, err := backup.OpenLedger(e.Paths.LedgerFile)
	if err != nil {
		return nil, offlineError(err)
	}
	dropped, err = ledger.Repair()
	if err != nil {
		return nil, offlineError(err)
	}
	// AIDEV-NOTE: B-M5. The ledger is already rewritten without the dropped
	// lines, so report their numbers NOW: a replay or checkpoint failure below
	// must not lose the only record of what was dropped.
	msg := "the purge ledger had no damaged lines"
	if len(dropped) != 0 {
		nums := make([]string, len(dropped))
		for i, n := range dropped {
			nums[i] = strconv.Itoa(n)
		}
		msg = "dropped damaged ledger line(s): " + strings.Join(nums, ", ")
	}
	if _, err := fmt.Fprintln(e.Out, msg); err != nil {
		return dropped, offlineError(err)
	}
	if err := crashPoint(ctx, repairCrashRepaired); err != nil {
		return dropped, err
	}
	svc, err := e.newService(st, ledger)
	if err != nil {
		return dropped, err
	}
	if err := svc.ReapplyLedger(ctx); err != nil {
		return dropped, err
	}
	return dropped, checkpoint(ctx, st)
}
