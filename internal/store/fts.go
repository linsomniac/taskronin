package store

import (
	"context"
	"fmt"
)

// FTSIntegrityCheck verifies both FTS indexes against their live content views.
// It issues FTS commands and therefore requires a writable Q.
func FTSIntegrityCheck(ctx context.Context, q Q) error {
	// AIDEV-NOTE: rank 1 checks external content as well as the index structure;
	// the default rank 0 would miss rows lost or left behind by broken triggers.
	for _, query := range []string{
		"INSERT INTO tasks_fts (tasks_fts, rank) VALUES ('integrity-check', 1)",
		"INSERT INTO log_entries_fts (log_entries_fts, rank) VALUES ('integrity-check', 1)",
	} {
		if _, err := q.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("store: FTS integrity check: %w", err)
		}
	}
	return nil
}
