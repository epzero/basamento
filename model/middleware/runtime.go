package middleware

import (
	"context"
	"time"

	"github.com/epzero/basamento/core"
)

// Runtime is the middleware's time source. Production uses SystemRuntime; the
// gate injects a virtual runtime so no test reads a wall clock.
type Runtime interface {
	core.Clock
	// Sleep blocks until d has elapsed on this runtime or ctx ends. It returns
	// ctx.Err() when ctx ends first.
	Sleep(ctx context.Context, d time.Duration) error
	// AfterFunc runs f once after d on this runtime.
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is one scheduled function.
type Timer interface {
	// Stop prevents the function from running if it has not run yet.
	Stop()
}

// SystemRuntime is the production runtime: the operating-system clock, timers
// and a context-aware sleep.
type SystemRuntime struct{}

// Now returns the operating-system clock's time in UTC.
func (SystemRuntime) Now() time.Time { return core.SystemClock{}.Now() }

// Sleep waits on the operating-system clock, returning early when ctx ends.
func (SystemRuntime) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// AfterFunc schedules f after d.
func (SystemRuntime) AfterFunc(d time.Duration, f func()) Timer {
	return systemTimer{time.AfterFunc(d, f)}
}

type systemTimer struct {
	*time.Timer
}

func (t systemTimer) Stop() { t.Timer.Stop() }
