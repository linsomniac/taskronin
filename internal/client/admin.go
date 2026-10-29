package client

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"time"

	"taskronin/internal/api"
)

// Tokens lists public token metadata without credentials.
func (c *Client) Tokens(ctx context.Context) ([]api.TokenInfo, error) {
	out, err := readView[[]api.TokenInfo](ctx, c, "/tokens", nil)
	if err != nil {
		return nil, err
	}
	return *out, nil
}

// Backups returns backup health and the snapshot inventory.
func (c *Client) Backups(ctx context.Context) (*api.BackupStatus, error) {
	return readView[api.BackupStatus](ctx, c, "/backups", nil)
}

// Export streams the portable document within an eleven-minute total budget.
func (c *Client) Export(ctx context.Context, w io.Writer) error {
	_, err := c.do(ctx, call{Kind: callRead, Method: http.MethodGet, Path: "/export", Budget: 11 * time.Minute, Stream: w}, nil) //nolint:bodyclose // do consumes and closes the response body.
	return err
}

// RevokeToken revokes a non-owner token with keyed retry semantics.
func (c *Client) RevokeToken(ctx context.Context, m Mut, id int64) (*api.TokenInfo, error) {
	var out api.TokenInfo
	if _, err := c.do(ctx, call{Kind: callKeyed, Method: http.MethodPost, Path: "/tokens/" + strconv.FormatInt(id, 10) + "/revoke", Mut: m, Body: struct{}{}}, &out); err != nil { //nolint:bodyclose // do consumes and closes the response body.
		return nil, err
	}
	return &out, nil
}

// UpdateSettings changes retention; lowering requires password re-entry.
func (c *Client) UpdateSettings(ctx context.Context, r api.SettingsRequest) (*api.Settings, error) {
	var out api.Settings
	if _, err := c.do(ctx, call{Kind: callExempt, Method: http.MethodPatch, Path: "/settings", Body: r}, &out); err != nil { //nolint:bodyclose // do consumes and closes the response body.
		return nil, err
	}
	return &out, nil
}

// CreateToken returns a new token and its one-time secret.
func (c *Client) CreateToken(ctx context.Context, r api.CreateTokenRequest) (*api.TokenCreated, error) {
	var out api.TokenCreated
	if _, err := c.do(ctx, call{Kind: callExempt, Method: http.MethodPost, Path: "/tokens", Body: r}, &out); err != nil { //nolint:bodyclose // do consumes and closes the response body.
		return nil, err
	}
	return &out, nil
}

// ChangePassword changes the owner password without saving or replaying it.
func (c *Client) ChangePassword(ctx context.Context, r api.PasswordRequest) error {
	_, err := c.do(ctx, call{Kind: callExempt, Method: http.MethodPost, Path: "/password", Body: r}, nil) //nolint:bodyclose // do consumes and closes the response body.
	return err
}

// RunBackup requests a manual snapshot with an eleven-minute total budget.
func (c *Client) RunBackup(ctx context.Context) (*api.Snapshot, error) {
	var out api.Snapshot
	if _, err := c.do(ctx, call{Kind: callExempt, Method: http.MethodPost, Path: "/backups", Body: struct{}{}, Budget: 11 * time.Minute}, &out); err != nil { //nolint:bodyclose // do consumes and closes the response body.
		return nil, err
	}
	return &out, nil
}
