package auth

import (
	"container/list"
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/linsomniac/taskronin/internal/clock"
	"github.com/linsomniac/taskronin/internal/domain"
)

type keyLimiter struct {
	key     string
	limiter *rate.Limiter
}

// PasswordGate rate-limits password attempts and serializes argon2 verification.
// It is safe for concurrent use and must be constructed with NewPasswordGate.
type PasswordGate struct {
	clock  clock.Clock
	sem    chan struct{}
	mu     sync.Mutex
	global *rate.Limiter
	perKey map[string]*list.Element
	lru    list.List
}

// NewPasswordGate creates a gate with a global 1/s bucket, per-key 1/5s
// buckets, bursts of five, and at most 1024 cached per-key buckets.
func NewPasswordGate(c clock.Clock) *PasswordGate {
	return &PasswordGate{
		clock: c, sem: make(chan struct{}, 1),
		global: rate.NewLimiter(1, 5), perKey: make(map[string]*list.Element),
	}
}

// Verify waits at most 15 seconds for both rate buckets, then holds the single
// verification slot while checking the password. Failed attempts never lock out a key.
func (g *PasswordGate) Verify(ctx context.Context, key, encoded, pw string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	global, perKey, delay, err := g.reserve(key)
	if err != nil {
		return false, err
	}
	if delay > 0 {
		select {
		case <-g.clock.After(delay):
		case <-ctx.Done():
			g.cancel(global, perKey)
			return false, ctx.Err()
		}
	}
	select {
	case g.sem <- struct{}{}:
		defer func() { <-g.sem }()
	case <-ctx.Done():
		g.cancel(global, perKey)
		return false, ctx.Err()
	}
	// Cancellation can race a ready timer or semaphore; don't start expensive work.
	if err := ctx.Err(); err != nil {
		g.cancel(global, perKey)
		return false, err
	}
	return VerifyPassword(encoded, pw)
}

func (g *PasswordGate) reserve(key string) (*rate.Reservation, *rate.Reservation, time.Duration, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	element, ok := g.perKey[key]
	if !ok {
		if len(g.perKey) == 1024 {
			oldest := g.lru.Back()
			delete(g.perKey, oldest.Value.(keyLimiter).key)
			g.lru.Remove(oldest)
		}
		element = g.lru.PushFront(keyLimiter{key: key, limiter: rate.NewLimiter(rate.Every(5*time.Second), 5)})
		g.perKey[key] = element
	} else {
		g.lru.MoveToFront(element)
	}
	// AIDEV-NOTE: reserve/cancel both buckets together and use injected time
	// everywhere; rate's Reserve, Delay, Wait, and Cancel use wall-clock time.
	now := g.clock.Now()
	global := g.global.ReserveN(now, 1)
	perKey := element.Value.(keyLimiter).limiter.ReserveN(now, 1)
	delay := max(global.DelayFrom(now), perKey.DelayFrom(now))
	if delay > 15*time.Second {
		global.CancelAt(now)
		perKey.CancelAt(now)
		return nil, nil, 0, &domain.Error{
			Code: domain.CodeRateLimited, Detail: "password verification rate limited", RetryAfter: delay,
		}
	}
	return global, perKey, delay, nil
}

func (g *PasswordGate) cancel(global, perKey *rate.Reservation) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.clock.Now()
	global.CancelAt(now)
	perKey.CancelAt(now)
}
