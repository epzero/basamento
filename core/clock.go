package core

import "time"

// Clock is the only source of wall time for production code outside the
// composition root. Durable flow code uses DurableContext.Now instead.
type Clock interface {
	Now() time.Time
}

// SystemClock reads the operating-system clock.
type SystemClock struct{}

// Now returns the current UTC time.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// After returns a channel that receives once after d on the system clock.
func (SystemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
