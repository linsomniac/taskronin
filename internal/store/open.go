package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"

	"modernc.org/sqlite"
)

// Store owns the live writer, reader and snapshot pools, or a single staging pool.
type Store struct {
	Writer, Reader, Snap *sql.DB
	Path                 string
}

// Open opens an existing live database, verifies its connections and checks it.
// It never creates a missing database.
func Open(ctx context.Context, absPath string) (*Store, error) {
	if _, err := os.Stat(absPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("database not found; run `tkr init`: %w", err)
		}
		return nil, fmt.Errorf("store: stat database: %w", err)
	}
	return open(ctx, absPath, false)
}

// OpenStaging creates or opens a staged copy with one pool in DELETE mode.
// It must never be used on a live database or an original snapshot.
func OpenStaging(ctx context.Context, absPath string) (*Store, error) {
	return open(ctx, absPath, true)
}

func open(ctx context.Context, path string, staging bool) (_ *Store, err error) {
	s := &Store{Path: path}
	defer func() {
		if err != nil {
			err = errors.Join(err, s.Close())
		}
	}()
	pool := PoolWriter
	if staging {
		pool = PoolStaging
	}
	s.Writer, err = openPool(ctx, path, pool, 1)
	if err != nil {
		return nil, err
	}
	if staging {
		s.Reader = s.Writer
	} else {
		s.Reader, err = openPool(ctx, path, PoolReader, 4)
		if err != nil {
			return nil, err
		}
		s.Snap, err = openPool(ctx, path, PoolSnapshot, 1)
		if err != nil {
			return nil, err
		}
	}
	if err := QuickCheck(ctx, s.Writer); err != nil {
		return nil, err
	}
	var version string
	if err := s.Writer.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&version); err != nil {
		return nil, fmt.Errorf("store: SQLite version: %w", err)
	}
	if err := checkVersion(version); err != nil {
		return nil, err
	}
	return s, nil
}

func openPool(ctx context.Context, path string, pool Pool, maxConns int) (*sql.DB, error) {
	db, err := sql.Open("sqlite", DSN(path, pool))
	if err != nil {
		return nil, fmt.Errorf("store: open pool %d: %w", pool, err)
	}
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	if err := db.PingContext(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("store: ping pool %d: %w", pool, err), db.Close())
	}
	return db, nil
}

func checkVersion(version string) error {
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return fmt.Errorf("store: invalid SQLite version %q", version)
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	if majorErr != nil || minorErr != nil || major < 3 || (major == 3 && minor < 44) {
		return fmt.Errorf("store: SQLite >= 3.44 required, got %q", version)
	}
	return nil
}

// Close closes every owned pool, closing a shared staging pool only once.
func (s *Store) Close() error {
	var errs []error
	seen := make(map[*sql.DB]bool)
	for _, db := range []*sql.DB{s.Snap, s.Reader, s.Writer} {
		if db != nil && !seen[db] {
			seen[db] = true
			errs = append(errs, db.Close())
		}
	}
	return errors.Join(errs...)
}

// CheckpointTruncate requires a successful WAL checkpoint with no remaining log.
func (s *Store) CheckpointTruncate(ctx context.Context) error {
	var busy, log, checkpointed int
	if err := s.Writer.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed); err != nil {
		return MapErr(fmt.Errorf("store: truncate checkpoint: %w", err))
	}
	if busy != 0 || log != 0 {
		return fmt.Errorf("store: incomplete truncate checkpoint: busy=%d log=%d checkpointed=%d", busy, log, checkpointed)
	}
	return nil
}

func init() {
	// AIDEV-NOTE: DSN parameters are requests, not proof of connection state.
	// Verify every physical connection (including replacements opened by sql.DB)
	// and reject foreign DSNs so a silently ignored setting cannot weaken safety.
	sqlite.RegisterConnectionHook(verifyConnection)
}

func verifyConnection(conn sqlite.ExecQuerierContext, dsn string) error {
	u, err := url.Parse(dsn)
	if err != nil {
		return fmt.Errorf("store: connection not built by store.DSN: %w", err)
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return fmt.Errorf("store: connection not built by store.DSN: %w", err)
	}
	if u.Scheme != "file" {
		return errors.New("store: connection not built by store.DSN: expected file URI")
	}
	for key, values := range commonParams() {
		if key == "_pragma" {
			for _, value := range values {
				if !slices.Contains(q[key], value) {
					return fmt.Errorf("store: connection not built by store.DSN: missing %s=%s", key, value)
				}
			}
		} else if len(q[key]) != 1 || q.Get(key) == "" {
			return fmt.Errorf("store: connection not built by store.DSN: missing or repeated %s", key)
		}
	}
	for _, key := range []string{"mode", "_txlock", "_journal_mode"} {
		if len(q[key]) != 1 || q.Get(key) == "" {
			return fmt.Errorf("store: connection not built by store.DSN: missing or repeated %s", key)
		}
	}
	queryOnly := "0"
	if q.Get("_query_only") != "" {
		queryOnly = q.Get("_query_only")
	}
	ctx := context.Background() // The driver hook does not receive the opener's context.
	for _, check := range []struct{ query, want string }{
		{"PRAGMA journal_mode", strings.ToLower(q.Get("_journal_mode"))},
		{"PRAGMA synchronous", "2"},
		{"PRAGMA foreign_keys", "1"},
		{"PRAGMA busy_timeout", "5000"},
		{"PRAGMA secure_delete", "1"},
		{"PRAGMA trusted_schema", "0"},
		{"PRAGMA query_only", queryOnly},
	} {
		if err := verifyPragma(ctx, conn, check.query, check.want); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA writable_schema=ON", nil); err != nil {
		return fmt.Errorf("store: defensive probe: %w", err)
	}
	if err := verifyPragma(ctx, conn, "PRAGMA writable_schema", "0"); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `SELECT "taskronin_dqs_probe"`, nil); err == nil {
		return errors.New("store: pragma mismatch: dqs: double-quoted string literal accepted")
	}
	return nil
}

func verifyPragma(ctx context.Context, conn sqlite.ExecQuerierContext, query, want string) (err error) {
	rows, err := conn.QueryContext(ctx, query, nil)
	if err != nil {
		return fmt.Errorf("store: verify %s: %w", query, err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	if len(rows.Columns()) != 1 {
		return fmt.Errorf("store: pragma mismatch: %s: expected one column", query)
	}
	values := make([]driver.Value, 1)
	if err := rows.Next(values); err != nil {
		return fmt.Errorf("store: verify %s: %w", query, err)
	}
	if got := fmt.Sprint(values[0]); got != want {
		return fmt.Errorf("store: pragma mismatch: %s = %s, want %s", query, got, want)
	}
	if err := rows.Next(values); !errors.Is(err, io.EOF) {
		if err != nil {
			return fmt.Errorf("store: verify %s: %w", query, err)
		}
		return fmt.Errorf("store: pragma mismatch: %s: expected one row", query)
	}
	return nil
}
