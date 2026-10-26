package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Q is the query interface shared by databases, connections and transactions.
type Q interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// QuickCheck requires SQLite's quick consistency check to return only "ok".
func QuickCheck(ctx context.Context, q Q) error {
	return check(ctx, q, "PRAGMA quick_check")
}

// IntegrityCheck requires SQLite's full consistency check to return only "ok".
func IntegrityCheck(ctx context.Context, q Q) error {
	return check(ctx, q, "PRAGMA integrity_check")
}

func check(ctx context.Context, q Q, query string) (err error) {
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("store: %s: %w", query, err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	count := 0
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return fmt.Errorf("store: %s: %w", query, err)
		}
		if result != "ok" {
			return fmt.Errorf("store: %s: %s", query, result)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: %s: %w", query, err)
	}
	if count != 1 {
		return fmt.Errorf("store: %s: expected one ok result, got %d rows", query, count)
	}
	return nil
}
