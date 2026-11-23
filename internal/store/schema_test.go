package store

import (
	"crypto/rand"
	"errors"
	"testing"

	"github.com/linsomniac/taskronin/internal/domain"
)

func TestOverlapTrigger(t *testing.T) {
	s := newTestStore(t)
	seedSchemaTasks(t, s)
	const insert = "INSERT INTO time_blocks (id, task_id, date, start_min, end_min, created_at) VALUES (?, ?, ?, ?, ?, ?)"
	mustExec(t, s.Writer, insert, 1, 1, "2026-10-28", 13*60, 15*60, 0)
	_, err := s.Writer.ExecContext(t.Context(), insert, 2, 2, "2026-10-28", 14*60+59, 16*60, 0)
	assertSchemaError(t, err, domain.CodeConflict, "overlapping planned block")
	mustExec(t, s.Writer, insert, 2, 2, "2026-10-28", 15*60, 16*60, 0)
	// Cancelled and tombstoned rows may overlap live planned rows.
	mustExec(t, s.Writer, `INSERT INTO time_blocks
		(id, task_id, date, start_min, end_min, status, cancel_reason, resolved_at, resolved_seq, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, 3, 2, "2026-10-28", 13*60, 16*60, "cancelled", "user", 0, 1, 0)
	mustExec(t, s.Writer, `INSERT INTO time_blocks
		(id, task_id, date, start_min, end_min, deleted_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, 4, 2, "2026-10-28", 13*60, 16*60, 0, 0)
	for _, query := range []string{
		"UPDATE time_blocks SET deleted_at = NULL WHERE id = 4",
		"UPDATE time_blocks SET status = 'planned', cancel_reason = NULL, resolved_at = NULL, resolved_seq = NULL WHERE id = 3",
		"UPDATE time_blocks SET start_min = 899 WHERE id = 2",
	} {
		_, err := s.Writer.ExecContext(t.Context(), query)
		assertSchemaError(t, err, domain.CodeConflict, "overlapping planned block")
	}
	// The update guard excludes its own row; the insert guard ignores inactive rows.
	mustExec(t, s.Writer, "UPDATE time_blocks SET start_min = ? WHERE id = ?", 13*60, 1)
	mustExec(t, s.Writer, "UPDATE time_blocks SET deleted_at = ? WHERE id IN (?, ?)", 0, 1, 2)
	mustExec(t, s.Writer, insert, 5, 1, "2026-10-28", 14*60, 15*60, 0)
	mustExec(t, s.Writer, insert, 6, 2, "2026-10-29", 14*60, 15*60, 0)
}

func TestNoDeleteTriggers(t *testing.T) {
	s := newTestStore(t)
	seedSchemaGuards(t, s)
	for _, tc := range []struct{ table, detail string }{
		{"tasks", "tasks are tombstoned, never deleted"},
		{"log_entries", "entries are tombstoned, never deleted"},
		{"time_blocks", "blocks are tombstoned, never deleted"},
		{"interruptions", "interruptions are tombstoned, never deleted"},
		{"ops", "ops are append-only"},
		{"events", "events are append-only"},
	} {
		t.Run(tc.table, func(t *testing.T) {
			_, err := s.Writer.ExecContext(t.Context(), "DELETE FROM "+tc.table)
			assertSchemaError(t, err, domain.CodeConflict, tc.detail)
		})
	}
}

func TestEventsFrozen(t *testing.T) {
	s := newTestStore(t)
	seedSchemaGuards(t, s)
	for _, tc := range []struct {
		column string
		value  any
	}{
		{"id", 2}, {"op_id", "00000000000000000000000000000002"}, {"entity", "entry"},
		{"entity_id", 2}, {"task_id", 2}, {"action", "tombstone"}, {"derived", 1},
	} {
		t.Run(tc.column, func(t *testing.T) {
			_, err := s.Writer.ExecContext(t.Context(), "UPDATE events SET "+tc.column+" = ? WHERE id = ?", tc.value, 1)
			assertSchemaError(t, err, domain.CodeConflict, "events are append-only")
		})
	}
	mustExec(t, s.Writer, "UPDATE events SET before_json = ?, after_json = ? WHERE id = ?", `{"title":"scrubbed before"}`, `{"title":"scrubbed after"}`, 1)
	var before, after string
	if err := s.Reader.QueryRowContext(t.Context(), "SELECT before_json, after_json FROM events WHERE id = ?", 1).Scan(&before, &after); err != nil {
		t.Fatal(err)
	}
	if before != `{"title":"scrubbed before"}` || after != `{"title":"scrubbed after"}` {
		t.Fatalf("content scrub = %q, %q", before, after)
	}
}

func TestEntryGuards(t *testing.T) {
	s := newTestStore(t)
	seedSchemaGuards(t, s)
	for _, tc := range []struct{ query, detail string }{
		{"UPDATE log_entries SET kind = 'work' WHERE id = 1", "entry kind is immutable"},
		{"UPDATE log_entries SET kind = 'complete' WHERE id = 2", "entry kind is immutable"},
		{"UPDATE log_entries SET task_id = 2 WHERE id = 1", "complete entries cannot be relinked"},
		{"UPDATE log_entries SET task_id = NULL WHERE id = 1", "complete entries cannot be relinked"},
	} {
		_, err := s.Writer.ExecContext(t.Context(), tc.query)
		assertSchemaError(t, err, domain.CodeConflict, tc.detail)
	}
	mustExec(t, s.Writer, "UPDATE log_entries SET kind = ?, task_id = ? WHERE id = ?", "complete", 1, 1)
	mustExec(t, s.Writer, "UPDATE log_entries SET task_id = ? WHERE id = ?", 2, 2)
	mustExec(t, s.Writer, "UPDATE log_entries SET task_id = NULL WHERE id = ?", 2)
}

func TestBlockTaskFixed(t *testing.T) {
	s := newTestStore(t)
	seedSchemaGuards(t, s)
	_, err := s.Writer.ExecContext(t.Context(), "UPDATE time_blocks SET task_id = ? WHERE id = ?", 2, 1)
	assertSchemaError(t, err, domain.CodeConflict, "block task is immutable")
	mustExec(t, s.Writer, "UPDATE time_blocks SET task_id = ? WHERE id = ?", 1, 1)
}

func TestDateChecks(t *testing.T) {
	s := newTestStore(t)
	seedSchemaTasks(t, s)
	for _, date := range []string{"2026-02-30", "2026-02-29", "2026-13-01", "2026-00-01", "2026-10-28 00:00:00", "not-a-date", ""} {
		t.Run(date, func(t *testing.T) {
			_, err := s.Writer.ExecContext(t.Context(), "UPDATE tasks SET due_date = ? WHERE id = ?", date, 1)
			assertSchemaError(t, err, domain.CodeValidation, "")
		})
	}
	for _, date := range []any{nil, "2026-02-28", "2024-02-29"} {
		mustExec(t, s.Writer, "UPDATE tasks SET due_date = ? WHERE id = ?", date, 1)
	}
}

func TestFTSTriggers(t *testing.T) {
	for _, table := range []string{"tasks", "log_entries"} {
		t.Run(table, func(t *testing.T) {
			s := newTestStore(t)
			insertFTSRow(t, s, table, nil)
			assertFTSMatch(t, s, table, "keycloak", 1)
			mustExec(t, s.Writer, "UPDATE "+table+" SET deleted_at = ? WHERE id = ?", 0, 1)
			assertFTSMatch(t, s, table, "keycloak", 0)
			mustExec(t, s.Writer, "UPDATE "+table+" SET deleted_at = NULL WHERE id = ?", 1)
			assertFTSMatch(t, s, table, "keycloak", 1)
			mustExec(t, s.Writer, "UPDATE "+table+" SET title = ? WHERE id = ?", "postgres cluster", 1)
			assertFTSMatch(t, s, table, "keycloak", 0)
			assertFTSMatch(t, s, table, "postgres", 1)
			mustExec(t, s.Writer, "UPDATE "+table+" SET notes = ? WHERE id = ?", "replication", 1)
			assertFTSMatch(t, s, table, "replication", 1)
			mustExec(t, s.Writer, "UPDATE "+table+" SET deleted_at = ? WHERE id = ?", 0, 1)
			assertFTSMatch(t, s, table, "postgres", 0)
			mustExec(t, s.Writer, "UPDATE "+table+" SET title = ? WHERE id = ?", "redis cache", 1)
			assertFTSMatch(t, s, table, "redis", 0)
			mustExec(t, s.Writer, "UPDATE "+table+" SET deleted_at = NULL WHERE id = ?", 1)
			assertFTSMatch(t, s, table, "redis", 1)
		})
	}
}

func TestFTSTombstonedInsert(t *testing.T) {
	for _, table := range []string{"tasks", "log_entries"} {
		t.Run(table, func(t *testing.T) {
			s := newTestStore(t)
			insertFTSRow(t, s, table, 0)
			assertFTSMatch(t, s, table, "keycloak", 0)
			mustExec(t, s.Writer, "UPDATE "+table+" SET deleted_at = NULL WHERE id = ?", 1)
			assertFTSMatch(t, s, table, "keycloak", 1)
		})
	}
}

func TestFTSIntegrityChecksContent(t *testing.T) {
	for _, table := range []string{"tasks", "log_entries"} {
		t.Run(table, func(t *testing.T) {
			s := newTestStore(t)
			insertFTSRow(t, s, table, nil)
			fts := table + "_fts"
			mustExec(t, s.Writer, "INSERT INTO "+fts+" ("+fts+", rowid, title, notes) VALUES (?, ?, ?, ?)", "delete", 1, "keycloak cluster", "")
			// The index is internally valid but no longer agrees with its content view.
			mustExec(t, s.Writer, "INSERT INTO "+fts+" ("+fts+", rank) VALUES (?, ?)", "integrity-check", 0)
			if err := FTSIntegrityCheck(t.Context(), s.Writer); err == nil {
				t.Fatal("content mismatch was not detected (rank 1 required)")
			}
		})
	}
}

func TestOwnerTokenUnique(t *testing.T) {
	s := newTestStore(t)
	var hash [32]byte
	if _, err := rand.Read(hash[:]); err != nil {
		t.Fatal(err)
	}
	const insert = "INSERT INTO api_tokens (name, secret_hash, scopes, is_owner, created_at) VALUES (?, ?, ?, ?, ?)"
	mustExec(t, s.Writer, insert, "owner.first", hash[:], "read,write,admin", 1, 0)
	_, err := s.Writer.ExecContext(t.Context(), insert, "owner.second", hash[:], "read,write,admin", 1, 0)
	assertSchemaError(t, err, domain.CodeConflict, "")
	_, err = s.Writer.ExecContext(t.Context(), insert, "owner.x", hash[:], "read", 0, 0)
	assertSchemaError(t, err, domain.CodeValidation, "")
	mustExec(t, s.Writer, "UPDATE api_tokens SET revoked_at = ? WHERE name = ?", 0, "owner.first")
	mustExec(t, s.Writer, insert, "owner.second", hash[:], "read,write,admin", 1, 0)
	_, err = s.Writer.ExecContext(t.Context(), "UPDATE api_tokens SET revoked_at = NULL WHERE name = ?", "owner.first")
	assertSchemaError(t, err, domain.CodeConflict, "")
	mustExec(t, s.Writer, insert, "reader", hash[:], "read", 0, 0)
}

func seedSchemaTasks(t *testing.T, s *Store) {
	t.Helper()
	for _, id := range []int{1, 2} {
		mustExec(t, s.Writer, "INSERT INTO tasks (id, title, rank, origin, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)", id, "task", id*1024, "test", 0, 0)
	}
}

func seedSchemaGuards(t *testing.T, s *Store) {
	t.Helper()
	seedSchemaTasks(t, s)
	for _, id := range []string{"00000000000000000000000000000001", "00000000000000000000000000000002"} {
		mustExec(t, s.Writer, "INSERT INTO ops (op_id, kind, actor, channel, at, scopes, revertible) VALUES (?, ?, ?, ?, ?, ?, ?)", id, "test", "human", "cli", 0, "read,write", 1)
	}
	for i, kind := range []string{"complete", "work"} {
		mustExec(t, s.Writer, "INSERT INTO log_entries (id, task_id, kind, title, work_date, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)", i+1, 1, kind, "entry", "2026-10-28", 0, 0)
	}
	mustExec(t, s.Writer, "INSERT INTO time_blocks (id, task_id, date, start_min, end_min, created_at) VALUES (?, ?, ?, ?, ?, ?)", 1, 1, "2026-10-28", 13*60, 15*60, 0)
	mustExec(t, s.Writer, "INSERT INTO interruptions (id, at, date, from_task_id, to_task_id, op_id) VALUES (?, ?, ?, ?, ?, ?)", 1, 0, "2026-10-28", 1, 2, "00000000000000000000000000000001")
	mustExec(t, s.Writer, "INSERT INTO events (id, op_id, entity, entity_id, task_id, action) VALUES (?, ?, ?, ?, ?, ?)", 1, "00000000000000000000000000000001", "task", 1, 1, "update")
}

func assertSchemaError(t *testing.T, cause error, code domain.Code, detail string) {
	t.Helper()
	err := MapErr(cause)
	var de *domain.Error
	if cause == nil || !errors.As(err, &de) || de.Code != code || de.Detail != detail || !errors.Is(err, cause) {
		t.Fatalf("MapErr(%v) = %#v; want %s, %q, preserved cause", cause, err, code, detail)
	}
}

func insertFTSRow(t *testing.T, s *Store, table string, deletedAt any) {
	t.Helper()
	if table == "tasks" {
		mustExec(t, s.Writer, "INSERT INTO tasks (title, rank, origin, created_at, updated_at, deleted_at) VALUES (?, ?, ?, ?, ?, ?)", "keycloak cluster", 1024, "test", 0, 0, deletedAt)
	} else {
		mustExec(t, s.Writer, "INSERT INTO log_entries (kind, title, work_date, created_at, updated_at, deleted_at) VALUES (?, ?, ?, ?, ?, ?)", "work", "keycloak cluster", "2026-10-28", 0, 0, deletedAt)
	}
}

func assertFTSMatch(t *testing.T, s *Store, table, term string, want int) {
	t.Helper()
	fts := table + "_fts"
	var count int
	if err := s.Reader.QueryRowContext(t.Context(), "SELECT count(*) FROM "+fts+" WHERE "+fts+" MATCH ?", term).Scan(&count); err != nil || count != want {
		t.Fatalf("%s MATCH %q = %d, %v; want %d", fts, term, count, err, want)
	}
	if err := FTSIntegrityCheck(t.Context(), s.Writer); err != nil {
		t.Fatal(err)
	}
}
