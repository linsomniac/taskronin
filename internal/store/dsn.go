// Package store provides SQLite connections and persistence primitives.
package store

import "net/url"

// Pool identifies a connection's transaction and journaling policy.
type Pool uint8

const (
	// PoolWriter serializes writes in immediate transactions on the live database.
	PoolWriter Pool = iota + 1
	// PoolReader permits only reads, using deferred transactions.
	PoolReader Pool = iota + 1
	// PoolSnapshot permits VACUUM INTO without an immediate transaction.
	PoolSnapshot Pool = iota + 1
	// PoolStaging creates or opens disposable copies in DELETE journal mode.
	PoolStaging Pool = iota + 1
)

// DSN builds an escaped SQLite file URI for an absolute path and pool.
// PoolStaging must only be used for new files or staged copies.
func DSN(absPath string, p Pool) string {
	q := commonParams()
	q.Set("mode", "rw")
	q.Set("_journal_mode", "WAL")
	switch p {
	case PoolWriter:
		q.Set("_txlock", "immediate")
	case PoolReader:
		q.Set("_txlock", "deferred")
		q.Set("_query_only", "1")
	case PoolSnapshot:
		q.Set("_txlock", "deferred")
	case PoolStaging:
		q.Set("mode", "rwc")
		q.Set("_txlock", "immediate")
		q.Set("_journal_mode", "DELETE")
	default:
		panic("store: invalid pool")
	}
	return (&url.URL{Scheme: "file", OmitHost: true, Path: absPath, RawQuery: q.Encode()}).String()
}

func commonParams() url.Values {
	return url.Values{
		"_busy_timeout": {"5000"},
		"_foreign_keys": {"1"},
		"_synchronous":  {"FULL"},
		"_defensive":    {"1"},
		"_dqs":          {"0"},
		"_pragma":       {"secure_delete(1)", "trusted_schema(0)"},
	}
}
