// Package coretest holds deterministic fakes for core ports.
//
// TEST-ONLY: `gate layering` rejects imports of this package from production
// code. Only _test.go files and internal/bdd may import it.
package coretest

import (
	"fmt"
	"sync"
	"time"
)

// FakeClock is a manually advanced clock.
type FakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []clockWaiter
}

type clockWaiter struct {
	at time.Time
	ch chan time.Time
}

// NewFakeClock starts at t.
func NewFakeClock(t time.Time) *FakeClock { return &FakeClock{now: t.UTC()} }

// Now returns the fake time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Set moves the clock to t.
func (c *FakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t.UTC()
}

// Advance moves the clock forward by d. It wakes every waiter whose deadline
// has been reached; the wake order is unspecified and must not be relied on.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	kept := c.waiters[:0]
	for _, w := range c.waiters {
		if w.at.After(c.now) {
			kept = append(kept, w)
			continue
		}
		w.ch <- c.now
	}
	c.waiters = kept
}

// After returns a channel that receives the clock reading once Advance has
// reached Now()+d. It is the deterministic counterpart of time.After for a
// runtime under test: no goroutine sleeps.
func (c *FakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, clockWaiter{at: c.now.Add(d), ch: ch})
	return ch
}

// SeqIds issues predictable ids "<kind>_<n>" for assertions in tests.
// It restarts at 1 for every instance, so it must never reach production.
type SeqIds struct {
	mu sync.Mutex
	n  map[string]int
}

// New returns the next sequential id for kind.
func (s *SeqIds) New(kind string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.n == nil {
		s.n = map[string]int{}
	}
	s.n[kind]++
	return fmt.Sprintf("%s_%d", kind, s.n[kind]), nil
}
