package store

import (
	"context"

	"taskronin/internal/domain"
)

// ReplaceTokens deletes every api_tokens row and inserts tokens verbatim:
// ids, secret hashes, scopes and revoked_at are all kept. It is the one write
// Plan A's store lacks, used by restore and import to carry the live token
// table across a snapshot.
//
// AIDEV-NOTE: pass a transaction as q. The delete and the inserts are separate
// statements, so a bare *sql.DB would leave an empty or partial token table on
// failure. Callers must also raise sqlite_sequence (RaiseSequences) so future
// tokens never reuse a replaced id.
func ReplaceTokens(ctx context.Context, q Q, tokens []domain.Token) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM api_tokens`); err != nil {
		return MapErr(err)
	}
	for _, t := range tokens {
		if _, err := q.ExecContext(ctx, `INSERT INTO api_tokens (id, name, secret_hash, scopes,
			is_owner, mutations_per_hour, created_at, expires_at, last_used_at, revoked_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, t.ID, t.Name, t.SecretHash[:], t.Scopes.String(),
			t.IsOwner, t.MutationsPerHour, t.CreatedAt.UnixMilli(), nullMillis(t.ExpiresAt),
			nullMillis(t.LastUsedAt), nullMillis(t.RevokedAt)); err != nil {
			return MapErr(err)
		}
	}
	return nil
}
