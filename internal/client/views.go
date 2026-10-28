package client

import (
	"context"
	"net/http"

	"taskronin/internal/api"
)

// Health checks the server's unversioned health endpoint.
func (c *Client) Health(ctx context.Context) error {
	_, err := c.do(ctx, call{Kind: callRead, Method: http.MethodGet, Path: "/healthz"}, nil) //nolint:bodyclose // do consumes and closes the response body.
	return err
}

// Clock reads the server clock and instance settings.
func (c *Client) Clock(ctx context.Context) (*api.Clock, error) {
	var out api.Clock
	if _, err := c.do(ctx, call{Kind: callRead, Method: http.MethodGet, Path: "/clock"}, &out); err != nil { //nolint:bodyclose // do consumes and closes the response body.
		return nil, err
	}
	return &out, nil
}

// WhoAmI reads the authenticated actor, scopes and current database generation.
func (c *Client) WhoAmI(ctx context.Context) (*api.WhoAmI, error) {
	var out api.WhoAmI
	if _, err := c.do(ctx, call{Kind: callRead, Method: http.MethodGet, Path: "/whoami"}, &out); err != nil { //nolint:bodyclose // do consumes and closes the response body.
		return nil, err
	}
	return &out, nil
}
