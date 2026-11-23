package store

import (
	"context"
	"fmt"

	"github.com/linsomniac/taskronin/internal/domain"
)

const (
	// ExportFormat identifies Taskronin's portable JSON export format.
	ExportFormat = "taskronin-export"
	// ExportVersion is the version of the export envelope.
	ExportVersion = 1
)

// ExportTables lists the tables included in portable exports.
var ExportTables = []string{"tasks", "task_triage", "tags", "task_tags", "log_entries", "time_blocks",
	"focus", "interruptions", "ops", "events", "undo_stack"}

// ExportSettings lists the settings included in portable exports.
var ExportSettings = []string{"backup_keep_daily", "backup_keep_weekly", "ai_reviewed_through"}

// ExportFile is the shared envelope for service exports and offline imports.
type ExportFile struct {
	Format        string                      `json:"format"`
	Version       int                         `json:"version"`
	SchemaVersion int                         `json:"schema_version"`
	ExportedAt    string                      `json:"exported_at"`
	DBGeneration  string                      `json:"db_generation"`
	Sequences     map[string]int64            `json:"sequences"`
	Settings      map[string]string           `json:"settings"`
	Tables        map[string][]map[string]any `json:"tables"`
}

// ExportTable streams an allowed table in primary-key order. Each callback gets
// a new column map containing only int64, string or nil values. Callback errors
// stop the stream and are returned unchanged.
func ExportTable(ctx context.Context, q Q, table string, fn func(row map[string]any) error) error {
	var query string
	switch table {
	case "tasks":
		query = `SELECT * FROM tasks ORDER BY id`
	case "task_triage":
		query = `SELECT * FROM task_triage ORDER BY task_id, reason`
	case "tags":
		query = `SELECT * FROM tags ORDER BY id`
	case "task_tags":
		query = `SELECT * FROM task_tags ORDER BY task_id, tag_id`
	case "log_entries":
		query = `SELECT * FROM log_entries ORDER BY id`
	case "time_blocks":
		query = `SELECT * FROM time_blocks ORDER BY id`
	case "focus":
		query = `SELECT * FROM focus ORDER BY id`
	case "interruptions":
		query = `SELECT * FROM interruptions ORDER BY id`
	case "ops":
		query = `SELECT * FROM ops ORDER BY seq`
	case "events":
		query = `SELECT * FROM events ORDER BY id`
	case "undo_stack":
		query = `SELECT * FROM undo_stack ORDER BY actor, stack, seq`
	default:
		return domain.Errorf(domain.CodeValidation, "table does not support export")
	}
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return MapErr(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return MapErr(err)
	}
	values, dest := make([]any, len(columns)), make([]any, len(columns))
	for i := range values {
		dest[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return MapErr(err)
		}
		row := make(map[string]any, len(columns))
		for i, column := range columns {
			switch value := values[i].(type) {
			case int64, string, nil:
				row[column] = value
			default:
				return MapErr(fmt.Errorf("store: unsupported export type %T for %s.%s", value, table, column))
			}
		}
		if err := fn(row); err != nil {
			return err
		}
	}
	return MapErr(rows.Err())
}
