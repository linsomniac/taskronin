package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"taskronin/internal/api"
	"taskronin/internal/clock"
	"taskronin/internal/fsutil"
)

// ResendResult holds exactly one successful keyed response: an operation or
// revoked token metadata. Task 18's Resend returns *ResendResult.
// AIDEV-NOTE: BF18 requires typed revocation recovery; it cannot be decoded as
// OpResponse. CLI pending retry must render whichever field is populated.
type ResendResult struct {
	Op    *api.OpResponse
	Token *api.TokenInfo
}

// PendingRequest preserves a keyed mutation for guarded, explicit recovery.
type PendingRequest struct {
	Key           string          `json:"key"`
	Method        string          `json:"method"`
	Path          string          `json:"path"`
	IfMatch       string          `json:"if_match"`
	Generation    string          `json:"generation"`
	Actor         string          `json:"actor"`
	Body          json.RawMessage `json:"body"`
	CreatedAt     time.Time       `json:"created_at"`
	LastError     string          `json:"last_error"`
	LastAttemptAt time.Time       `json:"last_attempt_at"`
}

// PendingStore stores private, durable request files under Dir.
type PendingStore struct {
	Dir   string
	Clock clock.Clock
}

// Save writes <Dir>/<key>.json atomically, assigning the current creation time.
func (s *PendingStore) Save(p PendingRequest) error {
	if !api.ValidIdempotencyKey(p.Key) {
		return fmt.Errorf("write pending request: invalid idempotency key")
	}
	clk := s.Clock
	if clk == nil {
		clk = clock.Real{}
	}
	p.CreatedAt = clk.Now()
	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("write pending request: encode: %w", err)
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("write pending request: create directory: %w", err)
	}
	info, err := os.Lstat(s.Dir)
	if err != nil {
		return fmt.Errorf("write pending request: stat directory: %w", err)
	}
	// AIDEV-NOTE: refuse symlinks and non-private/read-only existing directories;
	// saving a request must not silently grant access or change their permissions.
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("write pending request: directory %s must have mode 0700 and not be a symlink", s.Dir)
	}
	if err := fsutil.WriteFileAtomic(filepath.Join(s.Dir, p.Key+".json"), data, 0o600); err != nil {
		return fmt.Errorf("write pending request: %w", err)
	}
	return nil
}
