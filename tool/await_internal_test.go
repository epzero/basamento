package tool

import (
	"context"
	"testing"
	"time"
)

// awaitBounded carries the priority rule of Step and RunSandbox. The test
// fills every source before calling it, so when several are ready the result
// must not depend on select's random choice.
func TestAwaitBoundedPrefersFinishedThenDeadlineThenCancelled(t *testing.T) {
	cancelled := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}

	t.Run("a finished call wins over a fired deadline and cancellation", func(t *testing.T) {
		done := make(chan int, 1)
		done <- 7
		deadline := make(chan time.Time, 1)
		deadline <- time.Time{}
		out, cause := awaitBounded(done, deadline, cancelled())
		if cause != waitFinished || out != 7 {
			t.Fatalf("cause %v out %d, want finished 7", cause, out)
		}
	})

	t.Run("a fired deadline wins over cancellation", func(t *testing.T) {
		done := make(chan int, 1)
		deadline := make(chan time.Time, 1)
		deadline <- time.Time{}
		if _, cause := awaitBounded(done, deadline, cancelled()); cause != waitDeadline {
			t.Fatalf("cause %v, want deadline", cause)
		}
	})

	t.Run("a finished call alone", func(t *testing.T) {
		done := make(chan int, 1)
		done <- 3
		if _, cause := awaitBounded(done, make(chan time.Time), context.Background()); cause != waitFinished {
			t.Fatalf("cause %v, want finished", cause)
		}
	})

	t.Run("cancellation alone", func(t *testing.T) {
		if _, cause := awaitBounded(make(chan int), make(chan time.Time), cancelled()); cause != waitCancelled {
			t.Fatalf("cause %v, want cancelled", cause)
		}
	})
}
