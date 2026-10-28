package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// LoadServer strictly decodes and validates an existing server configuration.
// Before using files, server/offline callers must rebase their Paths with
// p.WithServerPaths(s.Paths.DataDir, s.Paths.Socket, s.Backup.Dir) (BF01).
func LoadServer(path string) (*Server, error) {
	var s Server
	if err := decodeConfig(path, &s); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("server config %s not found — run `tkr init`", path)
		}
		return nil, err
	}
	if err := ValidateServer(&s); err != nil {
		return nil, fmt.Errorf("server config %s: %w", path, err)
	}
	return &s, nil
}

// LoadClient strictly decodes over defaults, or uses just those defaults if the
// file is missing. In either case the resulting configuration is validated.
func LoadClient(path string, p Paths) (*Client, error) {
	wire := DefaultClient(p).tomlConfig()
	if err := decodeConfig(path, &wire); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	c, err := wire.client()
	if err != nil {
		return nil, fmt.Errorf("client config %s: %w", path, err)
	}
	if err := ValidateClient(c); err != nil {
		return nil, fmt.Errorf("client config %s: %w", path, err)
	}
	return c, nil
}

func decodeConfig(path string, dst any) error {
	b, err := os.ReadFile(path) //nolint:gosec // G304: the user's own XDG config file
	if err != nil {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	metadata, err := toml.Decode(string(b), dst)
	if err != nil {
		return fmt.Errorf("decode config %s: %w", path, err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) != 0 {
		keys := make([]string, len(undecoded))
		for i, key := range undecoded {
			keys[i] = key.String()
		}
		return fmt.Errorf("config %s: unknown keys: %s", path, strings.Join(keys, ", "))
	}
	return nil
}
