package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	"github.com/linsomniac/taskronin/internal/domain"
)

const tokenColumns = `id, name, secret_hash, scopes, is_owner, mutations_per_hour,
	created_at, expires_at, last_used_at, revoked_at`

func scanToken(row rowScanner) (domain.Token, error) {
	var t domain.Token
	var hash []byte
	var scopes string
	var created int64
	var expires, used, revoked sql.Null[int64]
	if err := row.Scan(&t.ID, &t.Name, &hash, &scopes, &t.IsOwner, &t.MutationsPerHour,
		&created, &expires, &used, &revoked); err != nil {
		return domain.Token{}, MapErr(err)
	}
	if len(hash) != len(t.SecretHash) {
		return domain.Token{}, MapErr(fmt.Errorf("store: invalid token hash length"))
	}
	copy(t.SecretHash[:], hash)
	var err error
	t.Scopes, err = domain.ParseScopes(scopes)
	if err != nil {
		return domain.Token{}, MapErr(err)
	}
	t.CreatedAt = time.UnixMilli(created).UTC()
	t.ExpiresAt, t.LastUsedAt, t.RevokedAt = nullTime(expires), nullTime(used), nullTime(revoked)
	return t, nil
}

// InsertToken inserts t, assigning its generated ID on success.
func InsertToken(ctx context.Context, q Q, t *domain.Token) error {
	var id domain.TokenID
	err := q.QueryRowContext(ctx, `INSERT INTO api_tokens (name, secret_hash, scopes,
		is_owner, mutations_per_hour, created_at, expires_at, last_used_at, revoked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`, t.Name, t.SecretHash[:], t.Scopes.String(),
		t.IsOwner, t.MutationsPerHour, t.CreatedAt.UnixMilli(), nullMillis(t.ExpiresAt),
		nullMillis(t.LastUsedAt), nullMillis(t.RevokedAt)).Scan(&id)
	if err != nil {
		return MapErr(err)
	}
	t.ID = id
	return nil
}

// GetToken returns a token, including expired or revoked tokens.
func GetToken(ctx context.Context, q Q, id domain.TokenID) (domain.Token, error) {
	return scanToken(q.QueryRowContext(ctx, `SELECT `+tokenColumns+` FROM api_tokens WHERE id = ?`, id))
}

// ListTokens returns every token in ascending ID order.
func ListTokens(ctx context.Context, q Q) ([]domain.Token, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+tokenColumns+` FROM api_tokens ORDER BY id`)
	if err != nil {
		return nil, MapErr(err)
	}
	defer rows.Close()
	var tokens []domain.Token
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, MapErr(rows.Err())
}

// RevokeToken sets the token's revocation time.
func RevokeToken(ctx context.Context, q Q, id domain.TokenID, at time.Time) error {
	result, err := q.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = ? WHERE id = ?`, at.UnixMilli(), id)
	return requireAffected(result, err)
}

// RevokeAllTokens revokes every unrevoked token and returns those IDs in ascending order.
func RevokeAllTokens(ctx context.Context, q Q, at time.Time) ([]domain.TokenID, error) {
	rows, err := q.QueryContext(ctx, `UPDATE api_tokens SET revoked_at = ? WHERE revoked_at IS NULL RETURNING id`, at.UnixMilli())
	if err != nil {
		return nil, MapErr(err)
	}
	defer rows.Close()
	var ids []domain.TokenID
	for rows.Next() {
		var id domain.TokenID
		if err := rows.Scan(&id); err != nil {
			return nil, MapErr(err)
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, MapErr(rows.Err())
}

// TouchTokens advances last_used_at without moving stored times backward.
func TouchTokens(ctx context.Context, q Q, last map[domain.TokenID]time.Time) error {
	for id, at := range last {
		if _, err := q.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ?1
			WHERE id = ?2 AND (last_used_at IS NULL OR last_used_at < ?1)`, at.UnixMilli(), id); err != nil {
			return MapErr(err)
		}
	}
	return nil
}
