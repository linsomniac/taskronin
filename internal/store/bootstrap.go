package store

import (
	"context"

	"taskronin/internal/domain"
)

// Bootstrap supplies initial settings, authentication and an owner token.
// Owner must have IsOwner set, an owner.<ts>-<hex> name and AllScopes; its ID is ignored.
type Bootstrap struct {
	Generation domain.Generation
	TZ         string
	Auth       AuthState
	Owner      domain.Token
}

// BootstrapDB initializes a new database without recording operations or events.
// Callers supply a transaction when atomic initialization is required.
func BootstrapDB(ctx context.Context, q Q, b Bootstrap) (domain.TokenID, error) {
	if _, err := q.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('db_generation', ?)`, string(b.Generation)); err != nil {
		return 0, MapErr(err)
	}
	if err := SetSetting(ctx, q, "tz_last_seen", b.TZ); err != nil {
		return 0, err
	}
	if err := PutAuthState(ctx, q, b.Auth); err != nil {
		return 0, err
	}
	if err := InsertToken(ctx, q, &b.Owner); err != nil {
		return 0, err
	}
	return b.Owner.ID, nil
}
