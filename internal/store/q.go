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

// ErrIntegrity marks a consistency check that ran and reported damage (any
// result other than a single "ok"), as opposed to failing to run at all.
var ErrIntegrity = errors.New("store: integrity check failed")

// integrityError keeps the check's own message while matching ErrIntegrity.
type integrityError struct{ msg string }

func (e *integrityError) Error() string        { return e.msg }
func (e *integrityError) Is(target error) bool { return target == ErrIntegrity }

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
			return &integrityError{msg: fmt.Sprintf("store: %s: %s", query, result)}
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: %s: %w", query, err)
	}
	if count != 1 {
		return &integrityError{msg: fmt.Sprintf("store: %s: expected one ok result, got %d rows", query, count)}
	}
	return nil
}
