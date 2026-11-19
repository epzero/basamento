package expr

import (
	"context"
	"sync"
	"time"
)

type stepContext struct {
	mu     sync.Mutex
	steps  uint64
	limit  uint64
	done   chan struct{}
	closed bool
}

func newStepContext(limit uint64) *stepContext {
	return &stepContext{limit: limit, done: make(chan struct{})}
}

func (c *stepContext) Deadline() (time.Time, bool) { return time.Time{}, false }

func (c *stepContext) Done() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.steps++
	if c.steps > c.limit && !c.closed {
		c.closed = true
		close(c.done)
	}
	return c.done
}

func (c *stepContext) Err() error {
	if c.limitExceeded() {
		return context.Canceled
	}
	return nil
}

func (c *stepContext) Value(key any) any { return nil }

func (c *stepContext) limitExceeded() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}
