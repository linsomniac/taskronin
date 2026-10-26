package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"taskronin/internal/clock"
	"taskronin/internal/domain"
)

func TestDSN(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task ronin?#.db")
	for _, tc := range []struct {
		name, mode, lock, journal, queryOnly string
		pool                                 Pool
	}{
		{"writer", "rw", "immediate", "WAL", "", PoolWriter},
		{"reader", "rw", "deferred", "WAL", "1", PoolReader},
		{"snapshot", "rw", "deferred", "WAL", "", PoolSnapshot},
		{"staging", "rwc", "immediate", "DELETE", "", PoolStaging},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(DSN(path, tc.pool))
			if err != nil {
				t.Fatal(err)
			}
			if u.Scheme != "file" || u.Host != "" || u.Path != path || u.Fragment != "" {
				t.Fatalf("DSN URI = %#v; want escaped file URI for %q", u, path)
			}
			want := url.Values{
				"mode": {tc.mode}, "_txlock": {tc.lock}, "_journal_mode": {tc.journal},
				"_busy_timeout": {"5000"}, "_foreign_keys": {"1"}, "_synchronous": {"FULL"},
				"_defensive": {"1"}, "_dqs": {"0"},
				"_pragma": {"secure_delete(1)", "trusted_schema(0)"},
			}
			if tc.queryOnly != "" {
				want.Set("_query_only", tc.queryOnly)
			}
			if got := u.Query(); !reflect.DeepEqual(got, want) {
				t.Fatalf("DSN params = %v; want %v", got, want)
			}
		})
	}
}

func TestOpenMissingFileFails(t *testing.T) {
	p := filepath.Join(t.TempDir(), "taskronin.db")
	if s, err := Open(context.Background(), p); err == nil {
		closeAtCleanup(t, s)
		t.Fatal("Open created or opened a missing database")
	} else if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "database not found; run `tkr init`") {
		t.Fatalf("missing file error = %v", err)
	}
	if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("file appeared: %v", err)
	}
}

func TestOpenStagingThenOpen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "task ronin?#.db")
	s, err := OpenStaging(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	closeAtCleanup(t, s)
	if s.Path != p || s.Writer != s.Reader || s.Snap != nil || s.Writer.Stats().MaxOpenConnections != 1 {
		t.Fatalf("staging pools = %#v, stats = %+v", s, s.Writer.Stats())
	}
	assertPragma(t, s.Writer, "PRAGMA journal_mode", "delete")
	execSQL(t, s.Writer, "CREATE TABLE t(x INTEGER) STRICT")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + "-wal"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("staging WAL exists: %v", err)
	}
	s, err = Open(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	closeAtCleanup(t, s)
	if s.Path != p || s.Writer == s.Reader || s.Snap == nil || s.Snap == s.Writer || s.Snap == s.Reader {
		t.Fatalf("live pools = %#v", s)
	}
	for _, tc := range []struct {
		name           string
		db             *sql.DB
		max, queryOnly int
	}{
		{"writer", s.Writer, 1, 0}, {"reader", s.Reader, 4, 1}, {"snapshot", s.Snap, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.db.Stats().MaxOpenConnections; got != tc.max {
				t.Fatalf("MaxOpenConnections = %d; want %d", got, tc.max)
			}
			// Hold every pool slot to force verification on newly created connections.
			for range tc.max {
				conn, err := tc.db.Conn(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				closeAtCleanup(t, conn)
				assertPragma(t, conn, "PRAGMA journal_mode", "wal")
				for query, want := range map[string]int{
					"PRAGMA synchronous": 2, "PRAGMA foreign_keys": 1, "PRAGMA busy_timeout": 5000,
					"PRAGMA secure_delete": 1, "PRAGMA trusted_schema": 0, "PRAGMA query_only": tc.queryOnly,
				} {
					assertPragma(t, conn, query, want)
				}
				execSQL(t, conn, "PRAGMA writable_schema=ON")
				assertPragma(t, conn, "PRAGMA writable_schema", 0)
				var quoted string
				if err := conn.QueryRowContext(t.Context(), `SELECT "taskronin_dqs_probe"`).Scan(&quoted); err == nil {
					t.Fatal("double-quoted string literal accepted")
				}
			}
		})
	}
	if _, err := s.Reader.ExecContext(t.Context(), "INSERT INTO t VALUES (?)", 1); err == nil {
		t.Fatal("reader accepted a write")
	}
	execSQL(t, s.Writer, "INSERT INTO t VALUES (?)", 1)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, db := range []*sql.DB{s.Writer, s.Reader, s.Snap} {
		if err := db.PingContext(t.Context()); err == nil {
			t.Fatal("pool still open after Store.Close")
		}
	}
}

func TestHookRejectsForeignDSN(t *testing.T) {
	p := filepath.Join(t.TempDir(), "foreign.db")
	db := openDB(t, "file:"+p)
	if err := db.PingContext(t.Context()); err == nil || !strings.Contains(err.Error(), "connection not built by store.DSN") {
		t.Fatalf("foreign DSN ping = %v", err)
	}
	for _, key := range []string{"_busy_timeout", "_foreign_keys", "_synchronous", "_defensive", "_dqs", "secure_delete(1)", "trusted_schema(0)"} {
		t.Run(key, func(t *testing.T) {
			dsn := editDSN(t, DSN(p, PoolStaging), func(q url.Values) {
				switch key {
				case "secure_delete(1)":
					q["_pragma"] = []string{"trusted_schema(0)"}
				case "trusted_schema(0)":
					q["_pragma"] = []string{"secure_delete(1)"}
				default:
					q.Del(key)
				}
			})
			db := openDB(t, dsn)
			if err := db.PingContext(t.Context()); err == nil || !strings.Contains(err.Error(), "connection not built by store.DSN") {
				t.Fatalf("missing %s: ping = %v", key, err)
			}
		})
	}
}

func TestHookRejectsMismatch(t *testing.T) {
	s := stagingStore(t)
	p := s.Path
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	dsn := editDSN(t, DSN(p, PoolReader), func(q url.Values) { q.Set("mode", "ro") })
	db := openDB(t, dsn)
	// AIDEV-NOTE: modernc v1.60.1 applies _journal_mode before invoking the
	// connection hook, so this fixture fails first with SQLITE_READONLY.
	if err := db.PingContext(t.Context()); err == nil {
		t.Fatal("read-only DELETE database accepted the WAL DSN")
	}
	for _, tc := range []struct{ key, value, pragma string }{
		{"_synchronous", "NORMAL", "synchronous"}, {"_foreign_keys", "0", "foreign_keys"},
		{"_busy_timeout", "100", "busy_timeout"}, {"_defensive", "0", "writable_schema"},
		{"_dqs", "1", "dqs"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			dsn := editDSN(t, DSN(p, PoolStaging), func(q url.Values) { q.Set(tc.key, tc.value) })
			db := openDB(t, dsn)
			if err := db.PingContext(t.Context()); err == nil || !strings.Contains(err.Error(), "pragma mismatch") || !strings.Contains(err.Error(), tc.pragma) {
				t.Fatalf("altered %s: ping = %v", tc.key, err)
			}
		})
	}
	for _, tc := range []struct{ query, pragma string }{
		{"PRAGMA query_only=1", "query_only"},
		{"PRAGMA secure_delete=0", "secure_delete"},
		{"PRAGMA trusted_schema=1", "trusted_schema"},
		{"PRAGMA journal_mode=MEMORY", "journal_mode"},
	} {
		t.Run("changed_"+tc.pragma, func(t *testing.T) {
			s := stagingStore(t)
			conn, err := s.Writer.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			closeAtCleanup(t, conn)
			execSQL(t, conn, tc.query)
			err = conn.Raw(func(raw any) error {
				return verifyConnection(raw.(sqlite.ExecQuerierContext), DSN(s.Path, PoolStaging))
			})
			if err == nil || !strings.Contains(err.Error(), "pragma mismatch") || !strings.Contains(err.Error(), tc.pragma) {
				t.Fatalf("verification after %s = %v; want hook mismatch", tc.query, err)
			}
		})
	}
}

func TestMapErr(t *testing.T) {
	s := stagingStore(t)
	execSQL(t, s.Writer, "CREATE TABLE t(x TEXT UNIQUE CHECK (length(x) > 0)) STRICT")
	execSQL(t, s.Writer, "CREATE TRIGGER guard BEFORE INSERT ON t WHEN NEW.x = 'trigger' BEGIN SELECT RAISE(ABORT, 'taskronin: nope'); END")
	execSQL(t, s.Writer, "INSERT INTO t VALUES (?)", "duplicate")
	execSQL(t, s.Writer, "CREATE TABLE parent(id INTEGER PRIMARY KEY) STRICT")
	execSQL(t, s.Writer, "CREATE TABLE child(id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES parent(id), x TEXT NOT NULL) STRICT")
	execSQL(t, s.Writer, "INSERT INTO child VALUES (?, ?, ?)", 1, nil, "x")
	for _, tc := range []struct {
		name, query string
		args        []any
		code        domain.Code
		detail      string
	}{
		{"unique", "INSERT INTO t VALUES (?)", []any{"duplicate"}, domain.CodeConflict, ""},
		{"check", "INSERT INTO t VALUES (?)", []any{""}, domain.CodeValidation, ""},
		{"trigger", "INSERT INTO t VALUES (?)", []any{"trigger"}, domain.CodeConflict, "nope"},
		{"primary key", "INSERT INTO child VALUES (?, ?, ?)", []any{1, nil, "x"}, domain.CodeConflict, ""},
		{"foreign key", "INSERT INTO child VALUES (?, ?, ?)", []any{2, 99, "x"}, domain.CodeConflict, ""},
		{"not null", "INSERT INTO child VALUES (?, ?, ?)", []any{2, nil, nil}, domain.CodeValidation, ""},
		{"unknown SQLite error", "SELECT missing FROM t", nil, domain.CodeInternal, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, cause := s.Writer.ExecContext(t.Context(), tc.query, tc.args...)
			if cause == nil {
				t.Fatal("statement unexpectedly succeeded")
			}
			for _, err := range []error{cause, fmt.Errorf("wrapped: %w", cause)} {
				got := MapErr(err)
				var de *domain.Error
				if !errors.As(got, &de) || de.Code != tc.code || de.Detail != tc.detail || !errors.Is(got, err) {
					t.Fatalf("MapErr(%v) = %#v; want %s, detail %q, wrapped cause", err, got, tc.code, tc.detail)
				}
			}
		})
	}
	if got := MapErr(sql.ErrNoRows); !errors.Is(got, ErrNotFound) {
		t.Fatalf("MapErr(ErrNoRows) = %v", got)
	}
	if got := MapErr(fmt.Errorf("wrapped: %w", sql.ErrNoRows)); !errors.Is(got, ErrNotFound) {
		t.Fatalf("MapErr(wrapped ErrNoRows) = %v", got)
	}
	de := &domain.Error{Code: domain.CodeValidation, Detail: "unchanged"}
	for _, err := range []error{nil, ErrNotFound, de, context.Canceled, context.DeadlineExceeded,
		fmt.Errorf("wrapped: %w", de), fmt.Errorf("wrapped: %w", context.Canceled),
		fmt.Errorf("wrapped: %w", context.DeadlineExceeded), fmt.Errorf("wrapped: %w", ErrNotFound)} {
		if got := MapErr(err); !errors.Is(got, err) {
			t.Errorf("MapErr(%v) = %v; want original error", err, got)
		}
	}
	for _, cause := range []error{errors.New("private cause"), errors.New("taskronin: not a SQLite trigger")} {
		got := MapErr(cause)
		var de *domain.Error
		if !errors.As(got, &de) || de.Code != domain.CodeInternal || de.Detail != "" || !errors.Is(got, cause) {
			t.Fatalf("MapErr(%v) = %#v; want internal with private cause", cause, got)
		}
	}
}

func TestMapErrBusy(t *testing.T) {
	s := liveStore(t)
	db := openDB(t, DSN(s.Path, PoolWriter))
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	closeAtCleanup(t, conn)
	execSQL(t, conn, "BEGIN IMMEDIATE")
	t.Cleanup(func() {
		if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	start := (clock.Real{}).Now()
	tx, err := s.Writer.BeginTx(ctx, nil)
	elapsed := (clock.Real{}).Now().Sub(start)
	if err == nil {
		rollbackAtCleanup(t, tx)
		t.Fatal("second writer acquired reserved lock")
	}
	if elapsed < 4500*time.Millisecond || elapsed > 10*time.Second {
		t.Fatalf("busy wait = %v; want approximately 5s", elapsed)
	}
	got := MapErr(err)
	var de *domain.Error
	if !errors.As(got, &de) || de.Code != domain.CodeBusy || de.RetryAfter != time.Second || !errors.Is(got, err) {
		t.Fatalf("MapErr(%v) = %#v; want busy with RetryAfter 1s", err, got)
	}
}

func TestCheckpointTruncate(t *testing.T) {
	s := liveStore(t)
	execSQL(t, s.Writer, "CREATE TABLE t(x INTEGER) STRICT")
	execSQL(t, s.Writer, "INSERT INTO t VALUES (?)", 1)
	if err := s.CheckpointTruncate(t.Context()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.Path + "-wal")
	if err != nil || info.Size() != 0 {
		t.Fatalf("WAL after truncate = %v, %v; want empty file", info, err)
	}
	execSQL(t, s.Writer, "INSERT INTO t VALUES (?)", 2)
	tx, err := s.Reader.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	rollbackAtCleanup(t, tx)
	var count int
	if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM t").Scan(&count); err != nil || count != 2 {
		t.Fatalf("snapshot count = %d, %v", count, err)
	}
	execSQL(t, s.Writer, "INSERT INTO t VALUES (?)", 3)
	if err := s.CheckpointTruncate(t.Context()); err == nil {
		t.Fatal("checkpoint succeeded with a reader holding a WAL snapshot")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckpointTruncate(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestMapErrLockedAndBusySnapshot(t *testing.T) {
	t.Run("locked", func(t *testing.T) {
		s := stagingStore(t)
		execSQL(t, s.Writer, "CREATE TABLE t(x INTEGER) STRICT")
		execSQL(t, s.Writer, "INSERT INTO t VALUES (?), (?)", 1, 2)
		conn, err := s.Writer.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		closeAtCleanup(t, conn)
		rows, err := conn.QueryContext(t.Context(), "SELECT x FROM t")
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := rows.Close(); err != nil {
				t.Error(err)
			}
		}()
		if !rows.Next() {
			t.Fatalf("expected an active cursor: %v", rows.Err())
		}
		_, err = conn.ExecContext(t.Context(), "DROP TABLE t")
		assertBusyError(t, err, sqlite3.SQLITE_LOCKED)
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("busy snapshot", func(t *testing.T) {
		s := liveStore(t)
		execSQL(t, s.Writer, "CREATE TABLE t(x INTEGER) STRICT")
		execSQL(t, s.Writer, "INSERT INTO t VALUES (?)", 1)
		tx, err := s.Snap.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		rollbackAtCleanup(t, tx)
		var count int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM t").Scan(&count); err != nil {
			t.Fatal(err)
		}
		execSQL(t, s.Writer, "INSERT INTO t VALUES (?)", 2)
		_, err = tx.ExecContext(t.Context(), "INSERT INTO t VALUES (?)", 3)
		assertBusyError(t, err, sqlite3.SQLITE_BUSY_SNAPSHOT)
	})
}

func TestChecks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check func(context.Context, Q) error
	}{
		{"quick_check", QuickCheck}, {"integrity_check", IntegrityCheck},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := stagingStore(t)
			if err := tc.check(t.Context(), s.Writer); err != nil {
				t.Fatal(err)
			}
			execSQL(t, s.Writer, "CREATE TABLE t(x INTEGER CHECK (x > 0)) STRICT")
			execSQL(t, s.Writer, "PRAGMA ignore_check_constraints=ON")
			execSQL(t, s.Writer, "INSERT INTO t VALUES (?)", -1)
			execSQL(t, s.Writer, "PRAGMA ignore_check_constraints=OFF")
			if err := tc.check(t.Context(), s.Writer); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
				t.Fatalf("check on invalid data = %v", err)
			}
		})
	}
}

func TestOpenRejectsCorruptFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "corrupt.db")
	if err := os.WriteFile(p, []byte(strings.Repeat("not a SQLite database", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(t.Context(), p); err == nil {
		closeAtCleanup(t, s)
		t.Fatal("Open accepted corrupt database")
	}
}

func TestOpenRejectsFailedQuickCheck(t *testing.T) {
	s := stagingStore(t)
	execSQL(t, s.Writer, "CREATE TABLE t(x INTEGER CHECK (x > 0)) STRICT")
	execSQL(t, s.Writer, "PRAGMA ignore_check_constraints=ON")
	execSQL(t, s.Writer, "INSERT INTO t VALUES (?)", -1)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if opened, err := Open(t.Context(), s.Path); err == nil {
		closeAtCleanup(t, opened)
		t.Fatal("Open accepted a database failing quick_check")
	} else if !strings.Contains(err.Error(), "PRAGMA quick_check: CHECK constraint failed") {
		t.Fatalf("Open failed for an unexpected reason: %v", err)
	}
}

func TestCheckVersion(t *testing.T) {
	for _, tc := range []struct {
		version string
		accept  bool
	}{
		{"2.99.0", false}, {"3.9.0", false}, {"3.43.9", false},
		{"3.44.0", true}, {"3.53.0", true}, {"4.0.0", true},
		{"", false}, {"3", false}, {"invalid.44", false}, {"3.invalid", false},
	} {
		t.Run(tc.version, func(t *testing.T) {
			if err := checkVersion(tc.version); (err == nil) != tc.accept {
				t.Fatalf("checkVersion(%q) = %v; want accepted=%t", tc.version, err, tc.accept)
			}
		})
	}
}

func stagingStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStaging(t.Context(), filepath.Join(t.TempDir(), "taskronin.db"))
	if err != nil {
		t.Fatal(err)
	}
	closeAtCleanup(t, s)
	return s
}

func liveStore(t *testing.T) *Store {
	t.Helper()
	s := stagingStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.Context(), s.Path)
	if err != nil {
		t.Fatal(err)
	}
	closeAtCleanup(t, s)
	return s
}

func openDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	closeAtCleanup(t, db)
	return db
}

func closeAtCleanup(t *testing.T, c interface{ Close() error }) {
	t.Helper()
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	})
}

func rollbackAtCleanup(t *testing.T, tx *sql.Tx) {
	t.Helper()
	t.Cleanup(func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Error(err)
		}
	})
}

func execSQL(t *testing.T, q Q, query string, args ...any) {
	t.Helper()
	if _, err := q.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func assertBusyError(t *testing.T, err error, code int) {
	t.Helper()
	var se *sqlite.Error
	if !errors.As(err, &se) || se.Code() != code {
		t.Fatalf("SQLite error = %v; want code %d", err, code)
	}
	got := MapErr(err)
	var de *domain.Error
	if !errors.As(got, &de) || de.Code != domain.CodeBusy || de.RetryAfter != time.Second || !errors.Is(got, err) {
		t.Fatalf("MapErr(%v) = %#v; want busy with RetryAfter 1s and preserved cause", err, got)
	}
}

func assertPragma[T comparable](t *testing.T, q Q, query string, want T) {
	t.Helper()
	var got T
	if err := q.QueryRowContext(t.Context(), query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s = %v; want %v", query, got, want)
	}
}

func editDSN(t *testing.T, dsn string, edit func(url.Values)) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	edit(q)
	u.RawQuery = q.Encode()
	return u.String()
}
