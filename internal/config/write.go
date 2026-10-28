package config

import (
	"bytes"
	"fmt"

	"github.com/BurntSushi/toml"

	"taskronin/internal/fsutil"
)

// WriteServer atomically writes server.toml with mode 0600. The parent directory
// must already exist; init is responsible for creating private directories.
func WriteServer(path string, s *Server) error {
	return writeConfig(path, "# Written by `tkr init`. Unknown keys are an error.\n", s)
}

// WriteClient atomically writes client.toml with mode 0600 and string durations.
// The parent directory must already exist.
func WriteClient(path string, c *Client) error {
	// AIDEV-NOTE: toml decodes duration strings into time.Duration, but encodes
	// time.Duration as integer nanoseconds. This mirror preserves the file's
	// human-readable string schema and makes writing and loading symmetric.
	return writeConfig(path, "", c.tomlConfig())
}

func writeConfig(path, header string, v any) error {
	var b bytes.Buffer
	b.WriteString(header)
	if err := toml.NewEncoder(&b).Encode(v); err != nil {
		return fmt.Errorf("encode config %s: %w", path, err)
	}
	if err := fsutil.WriteFileAtomic(path, b.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
}
