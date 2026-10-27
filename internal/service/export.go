package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"time"

	"taskronin/internal/domain"
	"taskronin/internal/store"
)

// Export streams a portable JSON export from one reader transaction for an administrator.
// It checks cancellation between rows but cannot interrupt a blocked Write. Callers
// with blocking writers must interrupt them when Stopping closes or their context
// is canceled. Writer errors are returned unchanged.
func (s *Service) Export(ctx context.Context, a domain.Actor, w io.Writer) error {
	if !a.Scopes.Has(domain.ScopeAdmin) {
		return domain.Errorf(domain.CodeForbidden, "insufficient scopes")
	}
	leave, err := s.enter()
	if err != nil {
		return err
	}
	defer leave()
	ctx, cancel := s.longOpContext(ctx)
	defer cancel()
	tx, err := s.store.Reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return serviceError(err)
	}
	defer func() { _ = tx.Rollback() }()
	out := &exportOutput{ctx: ctx, writer: w}
	if err := s.exportTx(ctx, tx, out); err != nil {
		// AIDEV-NOTE: The writer's error is part of Export's output contract;
		// only database and context errors pass through serviceError.
		if out.err != nil {
			return out.err
		}
		return serviceError(err)
	}
	return serviceError(tx.Commit())
}

type exportOutput struct {
	ctx    context.Context
	writer io.Writer
	err    error
}

// Write checks cancellation before each chunk and preserves the underlying error.
func (w *exportOutput) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := w.writer.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	w.err = err
	return n, err
}

func (s *Service) exportTx(ctx context.Context, tx *sql.Tx, out *exportOutput) error {
	now := s.clock.Now().Truncate(time.Millisecond)
	var schema int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&schema); err != nil {
		return err
	}
	gen, err := store.GetSetting(ctx, tx, "db_generation")
	if err != nil {
		return err
	}
	sequences, err := store.Sequences(ctx, tx)
	if err != nil {
		return err
	}
	settings := make(map[string]string, len(store.ExportSettings))
	for _, key := range store.ExportSettings {
		value, err := store.GetSetting(ctx, tx, key)
		if err != nil {
			return err
		}
		settings[key] = value
	}
	enc := json.NewEncoder(out)
	if _, err := io.WriteString(out, "{"); err != nil {
		return err
	}
	for i, field := range []struct {
		name  string
		value any
	}{
		{"format", store.ExportFormat}, {"version", store.ExportVersion}, {"schema_version", schema},
		{"exported_at", now.In(s.zone.Location()).Format("2006-01-02T15:04:05.000Z07:00")},
		{"db_generation", gen}, {"sequences", sequences}, {"settings", settings},
	} {
		if i > 0 {
			if _, err := io.WriteString(out, ","); err != nil {
				return err
			}
		}
		if err := enc.Encode(field.name); err != nil {
			return err
		}
		if _, err := io.WriteString(out, ":"); err != nil {
			return err
		}
		if err := enc.Encode(field.value); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(out, `,"tables":{`); err != nil {
		return err
	}
	for i, table := range store.ExportTables {
		if i > 0 {
			if _, err := io.WriteString(out, ","); err != nil {
				return err
			}
		}
		if err := enc.Encode(table); err != nil {
			return err
		}
		if _, err := io.WriteString(out, ":["); err != nil {
			return err
		}
		first := true
		if err := store.ExportTable(ctx, tx, table, func(row map[string]any) error {
			if !first {
				if _, err := io.WriteString(out, ","); err != nil {
					return err
				}
			}
			first = false
			return enc.Encode(row)
		}); err != nil {
			return err
		}
		if _, err := io.WriteString(out, "]"); err != nil {
			return err
		}
	}
	_, err = io.WriteString(out, "}}\n")
	return err
}
