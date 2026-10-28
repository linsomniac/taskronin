package clock

import (
	"sync"
	"time"
)

type waiter struct {
	deadline time.Time
	ch       chan time.Time
}

// Fake is a manually driven Clock for tests. It is safe for concurrent use.
type Fake struct {
	mu      sync.Mutex
	now     time.Time
	waiters []waiter
}

// NewFake returns a Fake whose current time is t.
func NewFake(t time.Time) *Fake { return &Fake{now: t} }

// Now returns the fake's current time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// After returns a channel that receives the fake time at which the deadline
// was reached. A non-positive d is ready immediately.
func (f *Fake) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- f.now
		return ch
	}
	f.waiters = append(f.waiters, waiter{deadline: f.now.Add(d), ch: ch})
	return ch
}

// Set moves the clock to t (backwards is allowed) and fires every waiter
// whose deadline is at or before t.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setLocked(t)
}

// setLocked implements Set; f.mu must be held.
func (f *Fake) setLocked(t time.Time) {
	f.now = t
	kept := f.waiters[:0]
	for _, w := range f.waiters {
		if w.deadline.After(t) {
			kept = append(kept, w)
			continue
		}
		w.ch <- w.deadline // buffered (cap 1), one send per waiter
	}
	f.waiters = kept
}

// Advance moves the clock forward by d.
func (f *Fake) Advance(d time.Duration) {
	// AIDEV-NOTE: add and fire under one lock so concurrent Advance calls
	// cannot lose an increment.
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setLocked(f.now.Add(d))
}
