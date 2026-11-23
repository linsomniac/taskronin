package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/clock"
	"github.com/linsomniac/taskronin/internal/fsutil"
)

// PendingResendWindow bounds a resendable pending request's age: the server's
// idempotency retention (service.IdemTTL, 7 d) minus a 1-hour safety margin.
//
// AIDEV-NOTE: final review A-Minor-3. CreatedAt is stamped when the file is
// saved, up to the 20 s call budget AFTER the server committed, while the
// server forgets rows 7 d after the commit. Without the margin, a resend of an
// op without If-Match (add, log, focus, undo/redo/revert) near 7 d could
// re-execute instead of replaying; undo/redo would hit a different stack
// entry. Keep this in step with service.IdemTTL (pending_test.go asserts it).
const PendingResendWindow = 7*24*time.Hour - time.Hour

// ResendResult holds exactly one successful keyed response: an operation or
// revoked token metadata.
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
	return s.write(p.Key, p)
}

// write preserves creation time when updating an existing pending request.
// Callers validate key before using it as a filename.
func (s *PendingStore) write(key string, p PendingRequest) error {
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
	if err := fsutil.WriteFileAtomic(filepath.Join(s.Dir, key+".json"), data, 0o600); err != nil {
		return fmt.Errorf("write pending request: %w", err)
	}
	return nil
}

// List returns pending requests ordered by creation time, oldest first.
// A missing pending directory is an empty store.
func (s *PendingStore) List() ([]PendingRequest, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list pending requests: %w", err)
	}
	var pending []PendingRequest
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		p, err := readPending(filepath.Join(s.Dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		pending = append(pending, p)
	}
	slices.SortStableFunc(pending, func(a, b PendingRequest) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	return pending, nil
}

func readPending(path string) (PendingRequest, error) {
	var p PendingRequest
	if err := fsutil.CheckPrivate(path); err != nil {
		return p, fmt.Errorf("read pending request: %w", err)
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: pending files under the user's state dir
	if err != nil {
		return p, fmt.Errorf("read pending request: %w", err)
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, fmt.Errorf("decode pending request %s: %w", path, err)
	}
	return p, nil
}

func (s *PendingStore) path(key string) (string, error) {
	if key == "" || key == "." || key == ".." || filepath.Base(key) != key {
		return "", fmt.Errorf("pending request: invalid key")
	}
	return filepath.Join(s.Dir, key+".json"), nil
}

// MarkFailed records a definitive failure without replacing the saved mutation
// or restarting its seven-day expiry window.
func (s *PendingStore) MarkFailed(key, lastError string) error {
	path, err := s.path(key)
	if err != nil {
		return err
	}
	p, err := readPending(path)
	if err != nil {
		return err
	}
	clk := s.Clock
	if clk == nil {
		clk = clock.Real{}
	}
	p.LastError, p.LastAttemptAt = lastError, clk.Now()
	return s.write(key, p)
}

// Discard removes a pending request after success or explicit user discard.
func (s *PendingStore) Discard(key string) error {
	path, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("discard pending request: %w", err)
	}
	return fsutil.FsyncDir(s.Dir)
}

// Resend checks expiry, generation and actor before retrying the saved request.
// The caller discards a success or marks a definitive failure; Resend never
// changes pending files, including after another uncertain outcome. A request
// whose age reaches PendingResendWindow is refused with ErrPendingExpired.
func (c *Client) Resend(ctx context.Context, p PendingRequest) (*ResendResult, error) {
	if c.opts.Clock.Now().Sub(p.CreatedAt) >= PendingResendWindow {
		return nil, ErrPendingExpired
	}
	ctx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()
	generation, err := c.Generation(ctx)
	if err != nil {
		return nil, mutationIdentityError(err)
	}
	if generation != p.Generation {
		return nil, ErrGenerationMismatch
	}
	actor, err := c.Actor(ctx)
	if err != nil {
		return nil, mutationIdentityError(err)
	}
	if actor != p.Actor {
		return nil, ErrActorMismatch
	}
	k := call{Kind: callKeyed, Method: p.Method, Path: p.Path, Mut: Mut{Key: p.Key, IfMatch: p.IfMatch}, Saved: &p}
	out := &ResendResult{}
	var response any
	if p.Method == http.MethodPost && strings.HasPrefix(p.Path, "/tokens/") && strings.HasSuffix(p.Path, "/revoke") {
		out.Token = &api.TokenInfo{}
		response = out.Token
	} else {
		out.Op = &api.OpResponse{}
		response = out.Op
	}
	if _, err := c.do(ctx, k, response); err != nil { //nolint:bodyclose // do consumes and closes the response body.
		return nil, err
	}
	return out, nil
}

// Age reports how long a request has been pending, using the store's clock.
func (s *PendingStore) Age(p PendingRequest) time.Duration {
	clk := s.Clock
	if clk == nil {
		clk = clock.Real{}
	}
	return clk.Now().Sub(p.CreatedAt)
}
