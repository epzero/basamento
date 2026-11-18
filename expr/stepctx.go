package expr

import (
	"context"
	"sync"
	"time"
)

type stepContext struct {
	parent context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	steps  uint64
	limit  uint64
	done   bool
}

func newStepContext(limit uint64) *stepContext {
	parent, cancel := context.WithCancel(context.Background())
	return &stepContext{parent: parent, cancel: cancel, limit: limit}
}

func (c *stepContext) Deadline() (time.Time, bool) { return c.parent.Deadline() }

func (c *stepContext) Done() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.steps++
	if c.steps > c.limit && !c.done {
		c.done = true
		c.cancel()
	}
	return c.parent.Done()
}

func (c *stepContext) Err() error {
	if c.limitExceeded() {
		return context.Canceled
	}
	return c.parent.Err()
}

func (c *stepContext) Value(key any) any { return c.parent.Value(key) }

func (c *stepContext) limitExceeded() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.done
}
