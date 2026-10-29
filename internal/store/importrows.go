package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"taskronin/internal/domain"
)

// BootstrapOp is an operation with its events, as read for import carry-over.
type BootstrapOp struct {
	Op     domain.OpRecord
	Events []domain.Event
}

// ReadBootstrapOps returns the token.* and auth.* operations with their
// events, ordered by sequence. They are the history that init --mcp-token and
// passwd --reset leave in an otherwise empty database.
func ReadBootstrapOps(ctx context.Context, q Q) ([]BootstrapOp, error) {
	out, err := readBootstrapHeads(ctx, q)
	if err != nil {
		return nil, err
	}
	// The head query is finished: a single-connection pool would deadlock here.
	for i := range out {
		if out[i].Events, err = EventsOfOp(ctx, q, out[i].Op.ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func readBootstrapHeads(ctx context.Context, q Q) ([]BootstrapOp, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+opColumns+` FROM ops
		WHERE substr(kind, 1, 6) = 'token.' OR substr(kind, 1, 5) = 'auth.' ORDER BY seq`)
	if err != nil {
		return nil, MapErr(err)
	}
	defer rows.Close()
	var out []BootstrapOp
	for rows.Next() {
		op, err := scanOp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, BootstrapOp{Op: op})
	}
	return out, MapErr(rows.Err())
}

// ImportIn is the input of ImportData.
type ImportIn struct {
	File       *ExportFile
	Bootstrap  []BootstrapOp
	Generation domain.Generation
	// Now is the time of the import; ImportData itself stamps nothing with it.
	Now time.Time
	// LiveSequences are the live DB's sqlite_sequence values (ruling P8):
	// neither the file nor the bootstrap rows carry the live high-water marks.
	LiveSequences map[string]int64
}

// importOrder is the dependency order of A§12.5 step 4a. focus is the
// singleton row 1, updated rather than inserted.
var importOrder = []string{"ops", "tasks", "tags", "task_tags", "task_triage", "log_entries",
	"time_blocks", "interruptions", "focus", "events", "undo_stack"}

// importSettings is the settings allowlist (the exported keys).
var importSettings = []string{"backup_keep_daily", "backup_keep_weekly", "ai_reviewed_through"}

// importSequences names the AUTOINCREMENT tables a file may raise.
var importSequences = []string{"tasks", "tags", "log_entries", "time_blocks", "interruptions", "ops", "events", "api_tokens"}

type importColumn struct {
	name    string
	integer bool
}

// ImportData is A§12.5 step 4a-c in the caller's transaction on the staged
// store: file rows with their ids and sequence numbers preserved (known columns
// only), sequence marks raised to max(file, live), then the destination's
// bootstrap operations with fresh sequence numbers. Step d (the system.import
// op) is recorded afterwards through the service.
//
// AIDEV-NOTE: SECURITY/PRECISION. Values arrive from json.Decoder.UseNumber:
// every INTEGER column is converted with strconv.ParseInt (no float64 hop, so
// ids above 2^53 survive; fractions, exponents and overflow are rejected).
// Table and column names that reach SQL come from PRAGMA table_info of the
// fixed importOrder tables, never from the file; unknown file keys are errors.
func ImportData(ctx context.Context, q Q, in ImportIn) (map[string]int, error) {
	f := in.File
	if f == nil {
		return nil, domain.Errorf(domain.CodeInternal, "import: no file")
	}
	for table := range f.Tables {
		if !slices.Contains(importOrder, table) {
			return nil, domain.Errorf(domain.CodeValidation, "import: table %q is not importable", table)
		}
	}
	for key := range f.Settings {
		if !slices.Contains(importSettings, key) {
			return nil, domain.Errorf(domain.CodeValidation, "import: setting %q is not importable", key)
		}
	}
	for name, seq := range f.Sequences {
		if !slices.Contains(importSequences, name) || seq < 0 {
			return nil, domain.Errorf(domain.CodeValidation, "import: sequence %q is not valid", name)
		}
	}
	counts := make(map[string]int, len(importOrder)+2)
	for _, table := range importOrder {
		rows := f.Tables[table]
		cols, err := importColumns(ctx, q, table)
		if err != nil {
			return nil, err
		}
		for i, row := range rows {
			if err := importRow(ctx, q, table, i+1, cols, row); err != nil {
				return nil, err
			}
		}
		counts[table] = len(rows)
	}
	for _, key := range importSettings {
		value, ok := f.Settings[key]
		if !ok {
			continue
		}
		if n, err := strconv.ParseInt(value, 10, 64); err != nil || n < 0 {
			return nil, domain.Errorf(domain.CodeValidation, "import: setting %q must be a non-negative integer", key)
		}
		if err := SetSetting(ctx, q, key, value); err != nil {
			return nil, err
		}
		counts["settings"]++
	}
	if err := SetSetting(ctx, q, "db_generation", string(in.Generation)); err != nil {
		return nil, err
	}
	marks := make(map[string]int64, len(f.Sequences)+len(in.LiveSequences))
	for name, seq := range f.Sequences {
		marks[name] = seq
	}
	for name, seq := range in.LiveSequences {
		marks[name] = max(marks[name], seq)
	}
	if err := RaiseSequences(ctx, q, marks); err != nil {
		return nil, err
	}
	for _, b := range in.Bootstrap {
		inserted, err := importBootstrapOp(ctx, q, b)
		if err != nil {
			return nil, err
		}
		if inserted {
			counts["bootstrap_ops"]++
		}
	}
	return counts, nil
}

func importColumns(ctx context.Context, q Q, table string) ([]importColumn, error) {
	rows, err := q.QueryContext(ctx, `SELECT name, type FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, MapErr(err)
	}
	defer rows.Close()
	var cols []importColumn
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return nil, MapErr(err)
		}
		cols = append(cols, importColumn{name: name, integer: strings.EqualFold(typ, "INTEGER")})
	}
	return cols, MapErr(rows.Err())
}

func importRow(ctx context.Context, q Q, table string, rowNum int, cols []importColumn, row map[string]any) error {
	for key := range row {
		if !slices.ContainsFunc(cols, func(c importColumn) bool { return c.name == key }) {
			return domain.Errorf(domain.CodeValidation, "import: %s row %d has unknown column %q", table, rowNum, key)
		}
	}
	var names []string
	var args []any
	for _, c := range cols {
		raw, ok := row[c.name]
		if !ok || (table == "focus" && c.name == "id") {
			continue
		}
		v, err := importValue(c, raw)
		if err != nil {
			return domain.Errorf(domain.CodeValidation, "import: %s row %d column %q %s", table, rowNum, c.name, err)
		}
		names = append(names, c.name)
		args = append(args, v)
	}
	if table == "focus" {
		if id, ok := row["id"]; ok {
			if n, err := importValue(importColumn{name: "id", integer: true}, id); err != nil || n != int64(1) {
				return domain.Errorf(domain.CodeValidation, "import: focus row %d must have id 1", rowNum)
			}
		}
		if len(names) == 0 {
			return nil
		}
		sets := make([]string, len(names))
		for i, n := range names {
			sets[i] = n + " = ?"
		}
		_, err := q.ExecContext(ctx, "UPDATE focus SET "+strings.Join(sets, ", ")+" WHERE id = 1", args...)
		return MapErr(err)
	}
	if len(names) == 0 {
		return domain.Errorf(domain.CodeValidation, "import: %s row %d is empty", table, rowNum)
	}
	// AIDEV-NOTE: names come from pragma_table_info, not from the file.
	_, err := q.ExecContext(ctx, "INSERT INTO "+table+" ("+strings.Join(names, ", ")+") VALUES ("+
		strings.TrimSuffix(strings.Repeat("?, ", len(names)), ", ")+")", args...)
	if err != nil {
		return importConstraint(table, rowNum, err)
	}
	return nil
}

// importConstraint reports a row the schema rejects as a validation error that
// names the row; other failures keep their mapped code.
func importConstraint(table string, rowNum int, err error) error {
	mapped := MapErr(err)
	if de, ok := domain.AsError(mapped); ok && (de.Code == domain.CodeValidation || de.Code == domain.CodeConflict) {
		return domain.Errorf(domain.CodeValidation, "import: %s row %d is not valid: %s", table, rowNum, de.Detail)
	}
	return mapped
}

// importValue converts one decoded value for column c. The error is the tail of
// the message ("must be an integer", ...).
func importValue(c importColumn, v any) (any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case json.Number:
		if !c.integer {
			return nil, errors.New("must be text")
		}
		n, err := strconv.ParseInt(x.String(), 10, 64)
		if err != nil {
			return nil, errors.New("must be an integer in the 64-bit range")
		}
		return n, nil
	case int64:
		if !c.integer {
			return nil, errors.New("must be text")
		}
		return x, nil
	case string:
		if c.integer {
			return nil, errors.New("must be an integer")
		}
		return x, nil
	default:
		return nil, fmt.Errorf("has unsupported type %T", v)
	}
}

// importBootstrapOp re-inserts one live bootstrap op with a fresh sequence and
// fresh event ids, keeping its op_id. An op_id already in the file must match
// (R4-4); it is then skipped.
func importBootstrapOp(ctx context.Context, q Q, b BootstrapOp) (inserted bool, err error) {
	existing, err := GetOp(ctx, q, b.Op.ID)
	switch {
	case err == nil:
		return false, compareBootstrap(ctx, q, existing, b)
	case !errors.Is(err, ErrNotFound):
		return false, err
	}
	op := b.Op
	if err := InsertOp(ctx, q, &op); err != nil {
		return false, err
	}
	for _, e := range b.Events {
		e.OpID = op.ID
		if err := InsertEvent(ctx, q, &e); err != nil {
			return false, err
		}
	}
	return true, nil
}

func compareBootstrap(ctx context.Context, q Q, existing domain.OpRecord, b BootstrapOp) error {
	mismatch := func(what string) error {
		return domain.Errorf(domain.CodeConflict, "import: the file holds operation %s with a different %s than this database's own history", b.Op.ID, what)
	}
	switch {
	case existing.Kind != b.Op.Kind:
		return mismatch("kind")
	case existing.Actor != b.Op.Actor:
		return mismatch("actor")
	case existing.Channel != b.Op.Channel:
		return mismatch("channel")
	case !existing.At.Equal(b.Op.At):
		return mismatch("time")
	case existing.Scopes != b.Op.Scopes:
		return mismatch("scopes")
	}
	events, err := EventsOfOp(ctx, q, b.Op.ID)
	if err != nil {
		return err
	}
	if len(events) != len(b.Events) {
		return mismatch("event list")
	}
	for i, e := range events {
		w := b.Events[i]
		if e.Entity != w.Entity || e.EntityID != w.EntityID || e.TaskID != w.TaskID || e.Action != w.Action ||
			e.Derived != w.Derived || !slices.Equal(e.Before, w.Before) || !slices.Equal(e.After, w.After) {
			return mismatch("event list")
		}
	}
	return nil
}

// ForeignKeyCheck fails if the database has any foreign key violation.
func ForeignKeyCheck(ctx context.Context, q Q) error { return foreignKeyCheck(ctx, q) }
