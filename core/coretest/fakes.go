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
	mu  sync.Mutex
	now time.Time
}

// NewFakeClock starts at t.
func NewFakeClock(t time.Time) *FakeClock { return &FakeClock{now: t.UTC()} }

// Now returns the fake time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward by d.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
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
