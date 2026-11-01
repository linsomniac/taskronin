package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"taskronin/internal/domain"
)

// ErrNotFound indicates that a store query found no matching row.
var ErrNotFound = errors.New("store: not found")

// MapErr translates SQLite failures to domain errors while retaining their causes.
// Cancellation, domain errors and ErrNotFound pass through unchanged.
func MapErr(err error) error {
	var de *domain.Error
	if err == nil || errors.Is(err, ErrNotFound) || errors.As(err, &de) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	mapped := &domain.Error{Code: domain.CodeInternal, Err: err}
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return mapped
	}
	// AIDEV-NOTE: The low byte includes extended BUSY/LOCKED variants; retain
	// the full code for constraint classification below.
	switch se.Code() & 0xff {
	case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
		mapped.Code = domain.CodeBusy
		mapped.RetryAfter = time.Second
		return mapped
	case sqlite3.SQLITE_CONSTRAINT:
		if _, detail, ok := strings.Cut(se.Error(), "taskronin: "); ok {
			mapped.Code = domain.CodeConflict
			// modernc appends the numeric result code to SQLite's message.
			mapped.Detail = strings.TrimSuffix(detail, fmt.Sprintf(" (%d)", se.Code()))
			return mapped
		}
	}
	switch se.Code() {
	case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY, sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY:
		mapped.Code = domain.CodeConflict
	case sqlite3.SQLITE_CONSTRAINT_CHECK, sqlite3.SQLITE_CONSTRAINT_NOTNULL:
		mapped.Code = domain.CodeValidation
	}
	return mapped
}

// IsCorrupt reports whether err shows a damaged database file: SQLite CORRUPT
// or NOTADB (including extended codes), or a consistency check that ran and
// failed (ErrIntegrity). Permission, I/O, busy, lock, out-of-memory and every
// other failure are not corruption: the file may be intact.
func IsCorrupt(err error) bool {
	if errors.Is(err, ErrIntegrity) {
		return true
	}
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	switch se.Code() & 0xff {
	case sqlite3.SQLITE_CORRUPT, sqlite3.SQLITE_NOTADB:
		return true
	}
	return false
}
