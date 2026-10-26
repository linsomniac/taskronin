// Package clock provides the injected time source. It is the only package
// allowed to call time.Now.
package clock

import "time"

// Clock is the time source every other package receives by injection.
type Clock interface {
	// Now returns the current instant.
	Now() time.Time
	// After returns a channel that receives the time once d has elapsed.
	// Intended for schedulers only.
	After(d time.Duration) <-chan time.Time
}

// Real is the wall-clock implementation of Clock.
type Real struct{}

// Now returns the current wall-clock time.
func (Real) Now() time.Time {
	// AIDEV-NOTE: the only time.Now call in the module (forbidigo elsewhere).
	return time.Now()
}

// After is time.After.
func (Real) After(d time.Duration) <-chan time.Time { return time.After(d) }
