// Package client provides the authenticated Taskronin unix-socket API client.
package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/clock"
	"taskronin/internal/config"
	"taskronin/internal/fsutil"
)

// Options configures credentials, request budgets, retries and pending storage.
type Options struct {
	Socket    string
	TokenFile string
	Channel   string
	Timeout   time.Duration
	NoRetry   bool // suppress retries and automatic generation refresh
	Pending   *PendingStore
	NewKey    func() string
	Clock     clock.Clock
}

// Client is safe for concurrent calls. Its options and credentials are immutable.
type Client struct {
	opts       Options
	token      string
	http       *http.Client
	identityMu chan struct{}
	identity   atomic.Pointer[api.WhoAmI]
}

// New checks and reads the private token file without performing network I/O.
func New(o Options) (*Client, error) {
	token, err := readToken(o.TokenFile)
	if err != nil {
		return nil, err
	}
	if o.Timeout <= 0 {
		o.Timeout = 20 * time.Second
	}
	if o.Clock == nil {
		o.Clock = clock.Real{}
	}
	if o.NewKey == nil {
		o.NewKey = api.NewIdempotencyKey
	}
	return &Client{opts: o, token: token, http: newHTTPClient(o.Socket), identityMu: make(chan struct{}, 1)}, nil
}

func readToken(path string) (string, error) {
	if err := fsutil.CheckPrivate(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w: %s not found — run `tkr init` (or pass --token-file)", ErrTokenFile, path)
		}
		return "", tokenFileError(path, err)
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: the user's own token file, checked by fsutil.CheckPrivate first
	if err != nil {
		return "", tokenFileError(path, err)
	}
	token := strings.TrimRight(string(data), "\r\n ")
	if !strings.HasPrefix(token, "tkr_") || strings.ContainsAny(token, "\r\n") {
		return "", tokenFileError(path, errors.New("invalid token content"))
	}
	return token, nil
}

func tokenFileError(path string, err error) error {
	return fmt.Errorf("%w: %s: %w — run `tkr init` (or pass --token-file)", ErrTokenFile, path, err)
}

// NewFromConfig uses the resolved client configuration and supplied channel.
func NewFromConfig(cfg *config.Client, channel string, pending *PendingStore) (*Client, error) {
	return New(Options{Socket: cfg.Socket, TokenFile: cfg.TokenFile, Timeout: cfg.Timeout, Channel: channel, Pending: pending})
}

// Generation lazily fetches and caches the database generation with the actor.
func (c *Client) Generation(ctx context.Context) (string, error) {
	who, err := c.who(ctx, false)
	return who.Generation, err
}

// RefreshGeneration invalidates the cached identity and fetches it again.
func (c *Client) RefreshGeneration(ctx context.Context) (string, error) {
	who, err := c.who(ctx, true)
	return who.Generation, err
}

// Actor returns the actor cached alongside the database generation.
func (c *Client) Actor(ctx context.Context) (string, error) {
	who, err := c.who(ctx, false)
	return who.Actor, err
}

func (c *Client) who(ctx context.Context, refresh bool) (api.WhoAmI, error) {
	// Invalidate even if the refresh's context has expired. The next mutation
	// must fetch again rather than continue using a known stale generation.
	if refresh {
		c.identity.Store(nil)
	}
	ctx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()
	// AIDEV-NOTE: a context-aware gate coalesces concurrent lazy lookups without
	// making a short-budget caller wait for another caller's longer deadline.
	select {
	case c.identityMu <- struct{}{}:
		defer func() { <-c.identityMu }()
	case <-ctx.Done():
		return api.WhoAmI{}, ctx.Err()
	}
	if refresh {
		c.identity.Store(nil)
	}
	identity := c.identity.Load()
	if identity == nil {
		who, err := c.WhoAmI(ctx)
		if err != nil {
			return api.WhoAmI{}, err
		}
		c.identity.Store(who)
		identity = who
	}
	return *identity, nil
}

// Mut supplies an entity precondition and an optional existing idempotency key.
type Mut struct {
	IfMatch string
	Key     string
}

// PageQuery specifies pagination.
type PageQuery struct {
	Limit  int
	Cursor string
}

// TaskQuery selects a task view and filters.
type TaskQuery struct {
	View, Tag, Project string
	PageQuery
}

// EntryQuery filters entries by date, task, standalone state and deletion state.
type EntryQuery struct {
	From, To                   *civil.Date
	Task                       *int64
	Standalone, IncludeDeleted bool
	PageQuery
}

// BlockQuery filters blocks by date or a concrete task.
type BlockQuery struct {
	From, To *civil.Date
	Task     *int64
}

// ReportQuery specifies a report range and optional sections.
type ReportQuery struct {
	From, To       civil.Date
	Tag, Project   string
	Todo, Upcoming bool
}

// SearchQuery specifies a search and pagination.
type SearchQuery struct {
	Q, Kind string
	PageQuery
}

// OpsQuery filters operation history.
type OpsQuery struct {
	Actor    string
	OpPrefix string
	SinceSeq int64
	PageQuery
}
