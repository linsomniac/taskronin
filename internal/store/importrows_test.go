package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"taskronin/internal/domain"
)

func importFile(tables map[string][]map[string]any) *ExportFile {
	return &ExportFile{Format: ExportFormat, Version: ExportVersion, SchemaVersion: 1,
		Sequences: map[string]int64{}, Settings: map[string]string{}, Tables: tables}
}

func importTask(id json.Number, extra map[string]any) map[string]any {
	row := map[string]any{"id": id, "title": "t", "notes": "", "rank": json.Number("1000"), "origin": "cli",
		"created_at": json.Number("1"), "updated_at": json.Number("1")}
	for k, v := range extra {
		row[k] = v
	}
	return row
}

func runImport(t *testing.T, s *Store, in ImportIn) (map[string]int, error) {
	t.Helper()
	if in.Generation == "" {
		in.Generation = domain.NewGeneration()
	}
	if in.Now.IsZero() {
		in.Now = time.UnixMilli(1)
	}
	tx, err := s.Writer.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	counts, err := ImportData(t.Context(), tx, in)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return counts, nil
}

func TestImportDataPreservesIntegers(t *testing.T) {
	s := newTestStore(t)
	f := importFile(map[string][]map[string]any{"tasks": {importTask("9007199254740993", nil)}})
	counts, err := runImport(t, s, ImportIn{File: f})
	if err != nil || counts["tasks"] != 1 {
		t.Fatalf("counts = %v, %v", counts, err)
	}
	var id int64
	if err := s.Reader.QueryRowContext(t.Context(), `SELECT id FROM tasks`).Scan(&id); err != nil || id != 9007199254740993 {
		t.Fatalf("id = %d, %v", id, err)
	}
	seq, err := Sequences(t.Context(), s.Reader)
	if err != nil || seq["tasks"] != 9007199254740993 {
		t.Fatalf("sequences = %v, %v", seq, err)
	}
}

func TestImportDataRejects(t *testing.T) {
	for name, tc := range map[string]struct {
		file *ExportFile
		want string
	}{
		"fraction":       {importFile(map[string][]map[string]any{"tasks": {importTask("1", map[string]any{"rank": json.Number("1.5")})}}), "tasks row 1 column \"rank\""},
		"exponent":       {importFile(map[string][]map[string]any{"tasks": {importTask("1", map[string]any{"rank": json.Number("1e3")})}}), "column \"rank\""},
		"out of range":   {importFile(map[string][]map[string]any{"tasks": {importTask("1", map[string]any{"rank": json.Number("9223372036854775808")})}}), "column \"rank\""},
		"string integer": {importFile(map[string][]map[string]any{"tasks": {importTask("1", map[string]any{"rank": "7"})}}), "column \"rank\""},
		"number text":    {importFile(map[string][]map[string]any{"tasks": {importTask("1", map[string]any{"title": json.Number("7")})}}), "column \"title\""},
		"unknown column": {importFile(map[string][]map[string]any{"tasks": {importTask("1", map[string]any{"color": "red"})}}), "unknown column \"color\""},
		"unknown table":  {importFile(map[string][]map[string]any{"auth_state": {{"id": json.Number("1")}}}), "table \"auth_state\""},
		"secret setting": {&ExportFile{Settings: map[string]string{"idem_hmac_key": "x"}}, "setting \"idem_hmac_key\""},
		"bad sequence":   {&ExportFile{Sequences: map[string]int64{"sqlite_master": 3}}, "sequence \"sqlite_master\""},
	} {
		t.Run(name, func(t *testing.T) {
			s := newTestStore(t)
			_, err := runImport(t, s, ImportIn{File: tc.file})
			de, ok := domain.AsError(err)
			if !ok || de.Code != domain.CodeValidation || !strings.Contains(de.Detail, tc.want) {
				t.Fatalf("err = %v, want validation containing %q", err, tc.want)
			}
		})
	}
}

func TestImportDataSettingsAndSequences(t *testing.T) {
	s := newTestStore(t)
	f := importFile(map[string][]map[string]any{"tasks": {importTask("3", nil)}})
	f.Settings = map[string]string{"backup_keep_daily": "9", "ai_reviewed_through": "0"}
	f.Sequences = map[string]int64{"tasks": 10, "ops": 4}
	gen := domain.NewGeneration()
	if _, err := runImport(t, s, ImportIn{File: f, Generation: gen, LiveSequences: map[string]int64{"tasks": 500, "events": 7}}); err != nil {
		t.Fatal(err)
	}
	seq, err := Sequences(t.Context(), s.Reader)
	if err != nil || seq["tasks"] != 500 || seq["ops"] != 4 || seq["events"] != 7 {
		t.Fatalf("sequences = %v, %v", seq, err)
	}
	if v, _ := GetSetting(t.Context(), s.Reader, "backup_keep_daily"); v != "9" {
		t.Fatalf("backup_keep_daily = %q", v)
	}
	if v, _ := GetSetting(t.Context(), s.Reader, "db_generation"); v != string(gen) {
		t.Fatalf("db_generation = %q, want %q", v, gen)
	}
}

func TestReadBootstrapOps(t *testing.T) {
	s := newTestStore(t)
	mustExec(t, s.Writer, `INSERT INTO ops (op_id, kind, actor, channel, at, scopes, revertible) VALUES
		(?, 'task.add', 'owner', 'cli', 1, 'admin', 1), (?, 'token.create', 'owner', 'cli', 2, 'admin', 0),
		(?, 'auth.password', 'owner', 'cli', 3, 'admin', 0)`,
		strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32))
	mustExec(t, s.Writer, `INSERT INTO events (op_id, entity, entity_id, action, after_json) VALUES (?, 'token', 1, 'insert', '{}')`, strings.Repeat("b", 32))
	ops, err := ReadBootstrapOps(t.Context(), s.Reader)
	if err != nil || len(ops) != 2 || ops[0].Op.Kind != domain.OpTokenCreate || len(ops[0].Events) != 1 || ops[1].Op.Kind != domain.OpAuthPassword {
		t.Fatalf("ops = %+v, %v", ops, err)
	}
}
