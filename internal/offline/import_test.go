package offline_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/client"
	"taskronin/internal/domain"
	"taskronin/internal/offline"
	"taskronin/internal/store"
	"taskronin/internal/testutil"
)

// importSource builds server A's data (tasks with tags and a project, entries,
// a block, focus, an undo + redo and a revert) and returns its export bytes.
func importSource(t *testing.T) []byte {
	t.Helper()
	ctx := t.Context()
	a := testutil.StartServer(t)
	c := a.Client
	one, err := c.AddTask(ctx, client.Mut{}, api.AddTaskRequest{Title: "alpha", Notes: "first\nnotes", Tags: []string{"x", "y"}, Project: "proj", Priority: "H"})
	if err != nil {
		t.Fatal(err)
	}
	two := restoreAdd(t, c, "beta")
	three := restoreAdd(t, c, "gamma")
	if _, err := c.BlockTask(ctx, client.Mut{IfMatch: one.Task.ETag}, one.Task.ID, api.BlockRequest{Date: civil.MustDate("2026-10-30"), Start: 9 * 60, End: 10 * 60}); err != nil {
		t.Fatal(err)
	}
	entry, err := c.AddEntry(ctx, client.Mut{}, api.AddEntryRequest{TaskID: api.Opt[int64]{Set: true, V: one.Task.ID}, Title: "did work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddEntry(ctx, client.Mut{}, api.AddEntryRequest{TaskID: api.Opt[int64]{Set: true, Null: true}, Title: "floating"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetFocus(ctx, client.Mut{}, api.FocusRequest{TaskID: api.Opt[int64]{Set: true, V: two}}); err != nil {
		t.Fatal(err)
	}
	detail, err := c.GetTask(ctx, three)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CompleteTask(ctx, client.Mut{IfMatch: detail.ETag}, three, api.CompleteRequest{Log: "finished gamma"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Undo(ctx, client.Mut{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Redo(ctx, client.Mut{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Revert(ctx, client.Mut{}, *entry.OpID); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := c.Export(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeExport(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "export.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// mutateExport decodes an export without losing integer precision, applies f
// and re-encodes it.
func mutateExport(t *testing.T, data []byte, f func(doc map[string]any)) []byte {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	f(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func tableRows(doc map[string]any, name string) []any {
	return doc["tables"].(map[string]any)[name].([]any)
}

func setTable(doc map[string]any, name string, rows []any) {
	doc["tables"].(map[string]any)[name] = rows
}

func decodeExport(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	mutateExport(t, data, func(d map[string]any) { doc = d })
	return doc
}

// emptyDest starts and stops a destination server so its generation is known
// and the live files are quiescent.
func emptyDest(t *testing.T, opts ...testutil.ServerOpt) (*testutil.Server, string) {
	t.Helper()
	b := testutil.StartServer(t, opts...)
	who, err := b.Client.WhoAmI(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	b.Stop()
	return b, who.Generation
}

func requireImportRefused(t *testing.T, b *testutil.Server, path string, code domain.Code, want string) {
	t.Helper()
	live := fileHash(t, b.Paths.DB)
	_, err := offline.Import(t.Context(), passwdEnv(t, b, ""), path, true)
	testutil.RequireCode(t, err, code)
	if want != "" && !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want it to mention %q", err, want)
	}
	if fileHash(t, b.Paths.DB) != live {
		t.Fatal("a refused import changed the live database")
	}
	requireNoLeftovers(t, b)
}

func queryInt(t *testing.T, path, query string, args ...any) int64 {
	t.Helper()
	st, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	var n int64
	if err := st.Reader.QueryRowContext(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestImportRoundTrip(t *testing.T) {
	data := importSource(t)
	b, before := emptyDest(t)
	res, err := offline.Import(t.Context(), passwdEnv(t, b, ""), writeExport(t, data), true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Counts["tasks"] != 3 || !strings.HasPrefix(res.Aside, b.Paths.DB+".pre-import-") {
		t.Fatalf("result = %+v", res)
	}
	requireNoLeftovers(t, b)
	b.Start(t)
	who, err := b.Client.WhoAmI(t.Context())
	if err != nil {
		t.Fatalf("B's owner token does not work after import: %v", err)
	}
	src := decodeExport(t, data)
	if who.Generation == before || who.Generation == src["db_generation"] {
		t.Fatalf("generation %q must differ from B's %q and A's %v", who.Generation, before, src["db_generation"])
	}
	for _, table := range []string{"tasks", "task_tags", "tags", "log_entries", "time_blocks", "ops", "events", "undo_stack"} {
		if len(tableRows(src, table)) == 0 {
			t.Fatalf("fixture has no %s rows", table)
		}
	}
	var buf bytes.Buffer
	if err := b.Client.Export(t.Context(), &buf); err != nil {
		t.Fatal(err)
	}
	got := decodeExport(t, buf.Bytes())
	importOps := map[any]bool{}
	for _, table := range store.ExportTables {
		want := tableRows(src, table)
		have := tableRows(got, table)
		switch table {
		case "ops":
			have = dropImportOps(have, importOps)
		case "events":
			have = dropImportEvents(have, importOps)
		}
		if len(want) == 0 && table != "focus" {
			continue
		}
		if !reflect.DeepEqual(want, have) {
			t.Fatalf("table %s differs:\n want %v\n have %v", table, want, have)
		}
	}
	if len(importOps) != 1 {
		t.Fatalf("expected exactly one system.import op, saw %d", len(importOps))
	}
	if id := restoreAdd(t, b.Client, "next"); id <= 3 {
		t.Fatalf("next task id = %d", id)
	}
}

func dropImportOps(rows []any, seen map[any]bool) []any {
	var keep []any
	for _, r := range rows {
		row := r.(map[string]any)
		if row["kind"] == "system.import" {
			seen[row["op_id"]] = true
			continue
		}
		keep = append(keep, r)
	}
	return keep
}

func dropImportEvents(rows []any, seen map[any]bool) []any {
	var keep []any
	for _, r := range rows {
		if !seen[r.(map[string]any)["op_id"]] {
			keep = append(keep, r)
		}
	}
	return keep
}

func TestImportRefusesNonEmpty(t *testing.T) {
	data := importSource(t)
	b := testutil.StartServer(t)
	restoreAdd(t, b.Client, "already here")
	b.Stop()
	requireImportRefused(t, b, writeExport(t, data), domain.CodeConflict, "import needs a freshly initialized database")
}

func TestImportKeepsLiveHighWater(t *testing.T) {
	data := importSource(t)
	b, _ := emptyDest(t)
	st, err := store.Open(t.Context(), b.Paths.DB)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RaiseSequences(t.Context(), st.Writer, map[string]int64{"tasks": 500}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := offline.Import(t.Context(), passwdEnv(t, b, ""), writeExport(t, data), true); err != nil {
		t.Fatal(err)
	}
	b.Start(t)
	if id := restoreAdd(t, b.Client, "after"); id != 501 {
		t.Fatalf("next task id = %d, want 501 (the live high-water mark)", id)
	}
}

// minimalExport is the smallest graph that imports: one op, one dropped task, one event.
func minimalExport(rank, taskID, opAt string) string {
	return `{"format":"taskronin-export","version":1,"schema_version":1,"exported_at":"2026-10-26T09:00:00.000-06:00",
	"db_generation":"0123456789abcdef","sequences":{},"settings":{},"tables":{
	"tasks":[{"id":` + taskID + `,"title":"big","status":"dropped","rank":` + rank + `,"origin":"cli","created_at":1760000000000,"updated_at":1760000000000}],
	"ops":[{"seq":1,"op_id":"` + strings.Repeat("1", 32) + `","kind":"task.add","actor":"owner","channel":"cli","at":` + opAt + `,"scopes":"write","revertible":1}],
	"events":[{"op_id":"` + strings.Repeat("1", 32) + `","entity":"task","entity_id":` + taskID + `,"task_id":` + taskID + `,"action":"insert"}]}}`
}

func TestImportIntegerPrecision(t *testing.T) {
	const big = "9007199254740993"
	b, _ := emptyDest(t)
	res, err := offline.Import(t.Context(), passwdEnv(t, b, ""), writeExport(t, []byte(minimalExport("1000", big, big))), true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Counts["tasks"] != 1 {
		t.Fatalf("counts = %v", res.Counts)
	}
	if got := queryInt(t, b.Paths.DB, "SELECT id FROM tasks"); got != 9007199254740993 {
		t.Fatalf("task id = %d", got)
	}
	if got := queryInt(t, b.Paths.DB, "SELECT at FROM ops WHERE kind = 'task.add'"); got != 9007199254740993 {
		t.Fatalf("op at = %d", got)
	}
	if got := queryInt(t, b.Paths.DB, "SELECT task_id FROM events WHERE entity = 'task'"); got != 9007199254740993 {
		t.Fatalf("event task id = %d", got)
	}
	for _, bad := range []string{"1.5", "9223372036854775808", "1e3"} {
		b, _ := emptyDest(t)
		requireImportRefused(t, b, writeExport(t, []byte(minimalExport(bad, "1", "5"))), domain.CodeValidation, `tasks row 1 column "rank"`)
	}
}

func TestImportHeaderChecks(t *testing.T) {
	data := importSource(t)
	for name, f := range map[string]func(doc map[string]any){
		"format":  func(d map[string]any) { d["format"] = "other" },
		"version": func(d map[string]any) { d["version"] = json.Number("2") },
		"schema":  func(d map[string]any) { d["schema_version"] = json.Number("99") },
	} {
		t.Run(name, func(t *testing.T) {
			b, _ := emptyDest(t)
			requireImportRefused(t, b, writeExport(t, mutateExport(t, data, f)), domain.CodeValidation, "")
		})
	}
	t.Run("garbage", func(t *testing.T) {
		b, _ := emptyDest(t)
		requireImportRefused(t, b, writeExport(t, []byte("not json")), domain.CodeValidation, "")
	})
	t.Run("missing file", func(t *testing.T) {
		b, _ := emptyDest(t)
		requireImportRefused(t, b, filepath.Join(t.TempDir(), "absent.json"), domain.CodeNotFound, "")
	})
}

func TestImportRejectsBadRows(t *testing.T) {
	data := importSource(t)
	for name, tc := range map[string]struct {
		f    func(doc map[string]any)
		code domain.Code
		want string
	}{
		"unknown column": {func(d map[string]any) { tableRows(d, "tasks")[0].(map[string]any)["color"] = "red" }, domain.CodeValidation, `"color"`},
		"bidi title": {func(d map[string]any) {
			tableRows(d, "tasks")[0].(map[string]any)["title"] = "evil\u202etitle"
		}, domain.CodeValidation, "tasks/1"},
		"bidi entry notes": {func(d map[string]any) {
			tableRows(d, "log_entries")[0].(map[string]any)["notes"] = "a\u202eb"
		}, domain.CodeValidation, "log_entries/"},
		"secret setting": {func(d map[string]any) { d["settings"].(map[string]any)["idem_hmac_key"] = "x" }, domain.CodeValidation, "idem_hmac_key"},
		"unknown table":  {func(d map[string]any) { d["tables"].(map[string]any)["auth_state"] = []any{} }, domain.CodeValidation, "auth_state"},
	} {
		t.Run(name, func(t *testing.T) {
			b, _ := emptyDest(t)
			requireImportRefused(t, b, writeExport(t, mutateExport(t, data, tc.f)), tc.code, tc.want)
		})
	}
}

// bootstrapFixture returns B (with an MCP token), the file ops' highest seq and
// B's token.create op with its events as ExportTable rows.
func bootstrapFixture(t *testing.T, data []byte) (b *testutil.Server, maxSeq int64, op map[string]any, events []map[string]any) {
	t.Helper()
	b, _ = emptyDest(t, testutil.WithMCPToken())
	st, err := store.Open(t.Context(), b.Paths.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := store.ExportTable(t.Context(), st.Reader, "ops", func(row map[string]any) error {
		if row["kind"] == "token.create" {
			op = row
		}
		return nil
	}); err != nil || op == nil {
		t.Fatalf("B has no token.create op: %v", err)
	}
	if err := store.ExportTable(t.Context(), st.Reader, "events", func(row map[string]any) error {
		if row["op_id"] == op["op_id"] {
			events = append(events, row)
		}
		return nil
	}); err != nil || len(events) == 0 {
		t.Fatalf("token.create has no events: %v", err)
	}
	for _, r := range tableRows(decodeExport(t, data), "ops") {
		n, err := r.(map[string]any)["seq"].(json.Number).Int64()
		if err != nil {
			t.Fatal(err)
		}
		maxSeq = max(maxSeq, n)
	}
	return b, maxSeq, op, events
}

func TestImportRenumbersBootstrap(t *testing.T) {
	data := importSource(t)
	b, maxSeq, op, events := bootstrapFixture(t, data)
	if _, err := offline.Import(t.Context(), passwdEnv(t, b, ""), writeExport(t, data), true); err != nil {
		t.Fatal(err)
	}
	// The file's ops keep their sequence numbers 1..N (no gaps, no shift).
	if got := queryInt(t, b.Paths.DB, "SELECT COUNT(*) FROM ops WHERE seq <= ? AND kind NOT LIKE 'token.%'", maxSeq); got != maxSeq {
		t.Fatalf("ops with seq <= %d = %d", maxSeq, got)
	}
	seq := queryInt(t, b.Paths.DB, "SELECT seq FROM ops WHERE op_id = ?", op["op_id"])
	if seq <= maxSeq {
		t.Fatalf("token.create seq = %d, want > %d", seq, maxSeq)
	}
	if got := queryInt(t, b.Paths.DB, "SELECT COUNT(*) FROM events WHERE op_id = ?", op["op_id"]); got != int64(len(events)) {
		t.Fatalf("token.create events = %d, want %d", got, len(events))
	}
	b.Start(t)
	if _, err := b.Client.WhoAmI(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestImportDedupesBootstrap(t *testing.T) {
	data := importSource(t)
	b, maxSeq, op, events := bootstrapFixture(t, data)
	if _, err := offline.Import(t.Context(), passwdEnv(t, b, ""), writeExport(t, withOpFor(t, data, op, events, maxSeq)), true); err != nil {
		t.Fatalf("identical bootstrap op: %v", err)
	}
	if got := queryInt(t, b.Paths.DB, "SELECT COUNT(*) FROM ops WHERE op_id = ?", op["op_id"]); got != 1 {
		t.Fatalf("copies of the token.create op = %d, want 1", got)
	}
	if got := queryInt(t, b.Paths.DB, "SELECT seq FROM ops WHERE op_id = ?", op["op_id"]); got != maxSeq+1 {
		t.Fatalf("the file's copy must be kept: seq = %d, want %d", got, maxSeq+1)
	}
	if got := queryInt(t, b.Paths.DB, "SELECT COUNT(*) FROM events WHERE op_id = ?", op["op_id"]); got != int64(len(events)) {
		t.Fatalf("events of the deduped op = %d, want %d", got, len(events))
	}

	// The same op_id with a different "at" is an error and nothing changes.
	b2, _, op2, events2 := bootstrapFixture(t, data)
	different := mutateExport(t, withOpFor(t, data, op2, events2, maxSeq), func(d map[string]any) {
		for _, r := range tableRows(d, "ops") {
			if row := r.(map[string]any); row["op_id"] == op2["op_id"] {
				row["at"] = json.Number(itoa(op2["at"].(int64) + 1))
			}
		}
	})
	requireImportRefused(t, b2, writeExport(t, different), domain.CodeConflict, "")
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

// withOpFor appends the destination's bootstrap op and events to a file.
func withOpFor(t *testing.T, data []byte, op map[string]any, events []map[string]any, maxSeq int64) []byte {
	t.Helper()
	return mutateExport(t, data, func(d map[string]any) {
		o := map[string]any{}
		for k, v := range op {
			o[k] = v
		}
		o["seq"] = json.Number(itoa(maxSeq + 1))
		setTable(d, "ops", append(tableRows(d, "ops"), o))
		var last int64
		for _, r := range tableRows(d, "events") {
			n, _ := r.(map[string]any)["id"].(json.Number).Int64()
			last = max(last, n)
		}
		evs := tableRows(d, "events")
		for i, e := range events {
			ev := map[string]any{}
			for k, v := range e {
				ev[k] = v
			}
			ev["id"] = json.Number(itoa(last + int64(i) + 1))
			evs = append(evs, ev)
		}
		setTable(d, "events", evs)
	})
}

func TestImportCrashResumeRollback(t *testing.T) {
	data := importSource(t)
	t.Run("resume", func(t *testing.T) {
		b, _ := emptyDest(t)
		_, err := offline.Import(crashAtSwap(t.Context(), "installed"), passwdEnv(t, b, ""), writeExport(t, data), true)
		if !errors.Is(err, errRestoreCrash) {
			t.Fatalf("crash = %v", err)
		}
		requireRunRefuses(t, b)
		if err := offline.RestoreResume(t.Context(), passwdEnv(t, b, "")); err != nil {
			t.Fatal(err)
		}
		requireNoLeftovers(t, b)
		b.Start(t)
		requireTitles(t, b.Client, "alpha", "beta", "gamma")
	})
	t.Run("rollback", func(t *testing.T) {
		b, _ := emptyDest(t)
		_, err := offline.Import(crashAtSwap(t.Context(), "installed"), passwdEnv(t, b, ""), writeExport(t, data), true)
		if !errors.Is(err, errRestoreCrash) {
			t.Fatalf("crash = %v", err)
		}
		if err := offline.RestoreRollback(t.Context(), passwdEnv(t, b, "")); err != nil {
			t.Fatal(err)
		}
		requireNoLeftovers(t, b)
		asides, _ := filepath.Glob(b.Paths.DB + ".pre-import-*")
		if len(asides) != 0 {
			t.Fatalf("aside remains after rollback: %v", asides)
		}
		b.Start(t)
		requireTitles(t, b.Client)
	})
}
