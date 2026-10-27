package store

import "context"

// Sequences returns the allocated AUTOINCREMENT high-water marks.
func Sequences(ctx context.Context, q Q) (map[string]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT name, seq FROM sqlite_sequence ORDER BY name`)
	if err != nil {
		return nil, MapErr(err)
	}
	defer rows.Close()
	sequences := make(map[string]int64)
	for rows.Next() {
		var name string
		var seq int64
		if err := rows.Scan(&name, &seq); err != nil {
			return nil, MapErr(err)
		}
		sequences[name] = seq
	}
	return sequences, MapErr(rows.Err())
}

// RaiseSequences raises each named high-water mark without lowering an existing
// value, inserting missing rows. The caller supplies a transaction for atomicity.
func RaiseSequences(ctx context.Context, q Q, m map[string]int64) error {
	for name, seq := range m {
		if _, err := q.ExecContext(ctx, `UPDATE sqlite_sequence SET seq = MAX(seq, ?) WHERE name = ?`, seq, name); err != nil {
			return MapErr(err)
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO sqlite_sequence (name, seq)
			SELECT ?1, ?2 WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = ?1)`, name, seq); err != nil {
			return MapErr(err)
		}
	}
	return nil
}
