package store

import (
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// SchemaStatus describes the applied and available embedded migrations.
type SchemaStatus struct {
	Current, Latest int
	Pending         []int
}

var (
	// ErrSchemaTooNew indicates an applied migration absent from this binary.
	ErrSchemaTooNew = errors.New("store: database schema is newer than this binary")
	// ErrChecksumMismatch indicates that an applied migration's bytes changed.
	ErrChecksumMismatch = errors.New("store: migration checksum mismatch")
)

type migration struct {
	version             int
	name, checksum, sql string
}

// SchemaStatus checks migration compatibility without modifying the database.
func (s *Store) SchemaStatus(ctx context.Context) (SchemaStatus, error) {
	migrations, err := readMigrations()
	if err != nil {
		return SchemaStatus{}, err
	}
	return schemaStatus(ctx, s.Reader, migrations)
}

// Migrate applies pending migrations in order, running preMigrate once before
// changing the database when it is non-nil. It returns the committed versions,
// including when a later migration or the final integrity checks fail.
func (s *Store) Migrate(ctx context.Context, preMigrate func(context.Context) error) ([]int, error) {
	migrations, err := readMigrations()
	if err != nil {
		return nil, err
	}
	status, err := schemaStatus(ctx, s.Reader, migrations)
	if err != nil {
		return nil, err
	}
	var applied []int
	if len(status.Pending) != 0 {
		if preMigrate != nil {
			if err := preMigrate(ctx); err != nil {
				return nil, fmt.Errorf("store: pre-migration snapshot: %w", err)
			}
		}
		if _, err := s.Writer.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL,
    checksum   TEXT    NOT NULL,   -- lowercase hex SHA-256 of the file bytes
    applied_at INTEGER NOT NULL    -- unix ms
) STRICT;`); err != nil {
			return nil, fmt.Errorf("store: bootstrap migrations: %w", err)
		}
		for _, m := range migrations {
			if !slices.Contains(status.Pending, m.version) {
				continue
			}
			if err := s.applyMigration(ctx, m); err != nil {
				return applied, fmt.Errorf("store: migration %04d_%s: %w", m.version, m.name, err)
			}
			applied = append(applied, m.version)
		}
	}
	if err := foreignKeyCheck(ctx, s.Writer); err != nil {
		return applied, err
	}
	if err := IntegrityCheck(ctx, s.Writer); err != nil {
		return applied, err
	}
	return applied, nil
}

func readMigrations() ([]migration, error) {
	files, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("store: list migrations: %w", err)
	}
	migrations := make([]migration, 0, len(files))
	seen := make(map[int]bool, len(files))
	for _, file := range files {
		prefix, name, ok := strings.Cut(strings.TrimSuffix(file.Name(), ".sql"), "_")
		version, err := strconv.Atoi(prefix)
		if !ok || len(prefix) != 4 || strings.Trim(prefix, "0123456789") != "" ||
			err != nil || version <= 0 || name == "" || !strings.HasSuffix(file.Name(), ".sql") || file.IsDir() {
			return nil, fmt.Errorf("store: invalid migration filename %q", file.Name())
		}
		if seen[version] {
			return nil, fmt.Errorf("store: duplicate migration version %d", version)
		}
		seen[version] = true
		data, err := migrationFiles.ReadFile("migrations/" + file.Name())
		if err != nil {
			return nil, fmt.Errorf("store: read migration %s: %w", file.Name(), err)
		}
		migrations = append(migrations, migration{
			version: version, name: name, checksum: fmt.Sprintf("%x", sha256.Sum256(data)), sql: string(data),
		})
	}
	slices.SortFunc(migrations, func(a, b migration) int { return cmp.Compare(a.version, b.version) })
	return migrations, nil
}

func schemaStatus(ctx context.Context, q Q, migrations []migration) (_ SchemaStatus, err error) {
	var status SchemaStatus
	known := make(map[int]string, len(migrations))
	for _, m := range migrations {
		known[m.version] = m.checksum
		status.Latest = max(status.Latest, m.version)
	}
	var exists bool
	if err := q.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM sqlite_schema WHERE type = ? AND name = ?)", "table", "schema_migrations").Scan(&exists); err != nil {
		return status, fmt.Errorf("store: find migration history: %w", err)
	}
	applied := make(map[int]bool)
	if exists {
		rows, queryErr := q.QueryContext(ctx, "SELECT version, checksum FROM schema_migrations ORDER BY version")
		if queryErr != nil {
			return status, fmt.Errorf("store: read migration history: %w", queryErr)
		}
		defer func() { err = errors.Join(err, rows.Close()) }()
		for rows.Next() {
			var version int
			var checksum string
			if err := rows.Scan(&version, &checksum); err != nil {
				return status, fmt.Errorf("store: read migration history: %w", err)
			}
			want, ok := known[version]
			if !ok {
				return status, fmt.Errorf("%w: version %d", ErrSchemaTooNew, version)
			}
			if checksum != want {
				return status, fmt.Errorf("%w: version %d", ErrChecksumMismatch, version)
			}
			applied[version] = true
			status.Current = max(status.Current, version)
		}
		if err := rows.Err(); err != nil {
			return status, fmt.Errorf("store: read migration history: %w", err)
		}
	}
	for _, m := range migrations {
		if !applied[m.version] {
			status.Pending = append(status.Pending, m.version)
		}
	}
	return status, nil
}

func (s *Store) applyMigration(ctx context.Context, m migration) (err error) {
	// AIDEV-NOTE: both writer DSNs use _txlock=immediate, so BeginTx issues
	// BEGIN IMMEDIATE. Keep the whole SQL file and its checksum in one transaction;
	// never split statements (trigger bodies contain semicolons).
	tx, err := s.Writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, rollbackErr)
		}
	}()
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, checksum, applied_at)
		VALUES (?, ?, ?, CAST(strftime('%s','now') AS INTEGER) * 1000)`, m.version, m.name, m.checksum); err != nil {
		return err
	}
	return tx.Commit()
}

func foreignKeyCheck(ctx context.Context, q Q) (err error) {
	rows, err := q.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("store: foreign_key_check: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	if rows.Next() {
		return errors.New("store: foreign_key_check: foreign key violation")
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: foreign_key_check: %w", err)
	}
	return nil
}
