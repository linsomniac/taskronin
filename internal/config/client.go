package config

import (
	"fmt"
	"time"
)

// Client is the client.toml schema. Missing keys keep DefaultClient values.
type Client struct {
	Socket        string        `toml:"socket"`
	TokenFile     string        `toml:"token_file"`
	Timeout       time.Duration `toml:"timeout"`
	StatusTimeout time.Duration `toml:"status_timeout"`
	Editor        string        `toml:"editor"`
	Color         string        `toml:"color"`
	MCP           struct {
		TokenFile string `toml:"token_file"`
	} `toml:"mcp"`
}

// TokenFileError identifies token path failures during configuration validation,
// before the client can check the credential file itself.
type TokenFileError struct {
	Path string
	Err  error
}

func (e *TokenFileError) Error() string { return fmt.Sprintf("token file %s: %v", e.Path, e.Err) }

// Unwrap preserves the path validation failure.
func (e *TokenFileError) Unwrap() error { return e.Err }

// AIDEV-NOTE: decoding directly into time.Duration also accepts TOML integers
// as nanoseconds. Keep durations as strings at the file boundary, then parse
// them explicitly so a missing unit cannot silently change the time budget.
type clientTOML struct {
	Socket        string `toml:"socket"`
	TokenFile     string `toml:"token_file"`
	Timeout       string `toml:"timeout"`
	StatusTimeout string `toml:"status_timeout"`
	Editor        string `toml:"editor"`
	Color         string `toml:"color"`
	MCP           struct {
		TokenFile string `toml:"token_file"`
	} `toml:"mcp"`
}

func (c *Client) tomlConfig() clientTOML {
	return clientTOML{
		Socket: c.Socket, TokenFile: c.TokenFile,
		Timeout: c.Timeout.String(), StatusTimeout: c.StatusTimeout.String(),
		Editor: c.Editor, Color: c.Color, MCP: c.MCP,
	}
}

func (c clientTOML) client() (*Client, error) {
	timeout, err := time.ParseDuration(c.Timeout)
	if err != nil {
		return nil, fmt.Errorf("timeout: %w", err)
	}
	statusTimeout, err := time.ParseDuration(c.StatusTimeout)
	if err != nil {
		return nil, fmt.Errorf("status_timeout: %w", err)
	}
	return &Client{
		Socket: c.Socket, TokenFile: c.TokenFile,
		Timeout: timeout, StatusTimeout: statusTimeout,
		Editor: c.Editor, Color: c.Color, MCP: c.MCP,
	}, nil
}

// DefaultClient supplies paths and time budgets for a missing client.toml.
func DefaultClient(p Paths) *Client {
	c := &Client{
		Socket: p.Socket, TokenFile: p.OwnerToken,
		Timeout: 20 * time.Second, StatusTimeout: 300 * time.Millisecond,
		Editor: "", Color: "auto",
	}
	c.MCP.TokenFile = p.TokenFile("mcp")
	return c
}

// ValidateClient checks client paths, time budgets and terminal color mode.
func ValidateClient(c *Client) error {
	if err := CheckSocketPath(c.Socket); err != nil {
		return err
	}
	if err := validatePath("socket", c.Socket, false); err != nil {
		return err
	}
	if err := validatePath("token_file", c.TokenFile, false); err != nil {
		return &TokenFileError{Path: c.TokenFile, Err: err}
	}
	if err := validatePath("mcp.token_file", c.MCP.TokenFile, false); err != nil {
		return &TokenFileError{Path: c.MCP.TokenFile, Err: err}
	}
	if c.Timeout < 15*time.Second {
		return fmt.Errorf("timeout must be at least 15s (the server operation budget)")
	}
	if c.StatusTimeout <= 0 || c.StatusTimeout > 2*time.Second {
		return fmt.Errorf("status_timeout must be positive and at most 2s")
	}
	switch c.Color {
	case "auto", "always", "never":
	default:
		return fmt.Errorf("color must be auto, always or never")
	}
	return nil
}
