package store

import (
	"context"
	"database/sql"

	"github.com/linsomniac/taskronin/internal/domain"
)

func scanFocus(row rowScanner) (domain.Focus, error) {
	var f domain.Focus
	var at sql.Null[int64]
	if err := row.Scan(&f.TaskID, &f.Date, &at, &f.Version); err != nil {
		return domain.Focus{}, MapErr(err)
	}
	f.SetAt = nullTime(at)
	return f, nil
}

// GetFocus returns the singleton focus row, initially empty with version 1.
func GetFocus(ctx context.Context, q Q) (domain.Focus, error) {
	return scanFocus(q.QueryRowContext(ctx, `SELECT task_id, date, set_at, version FROM focus WHERE id = 1`))
}

// PutFocus replaces the singleton's task, date and set time, preserving its version.
func PutFocus(ctx context.Context, q Q, f domain.Focus) error {
	result, err := q.ExecContext(ctx, `UPDATE focus SET task_id = ?, date = ?, set_at = ? WHERE id = 1`, f.TaskID, f.Date, nullMillis(f.SetAt))
	return requireAffected(result, err)
}
