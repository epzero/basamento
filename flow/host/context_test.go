package host

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
)

type errContext struct {
	err error
}

func (c errContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c errContext) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
func (c errContext) Err() error        { return c.err }
func (c errContext) Value(key any) any { return nil }

func TestRunContextError(t *testing.T) {
	t.Run("live", func(t *testing.T) {
		if err := runContextError(context.Background()); err != nil {
			t.Fatalf("live context: %v, want nil", err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := runContextError(ctx)
		if core.CodeOf(err) != core.CodeCancelled {
			t.Fatalf("code %v, want cancelled", core.CodeOf(err))
		}
		var pe *core.Error
		if !errors.As(err, &pe) || pe.Message != "flow: cancelled" {
			t.Fatalf("error %v", err)
		}
		if !errors.Is(pe.Cause, context.Canceled) {
			t.Fatalf("cause %v, want context.Canceled", pe.Cause)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		err := runContextError(errContext{err: context.DeadlineExceeded})
		if core.CodeOf(err) != core.CodeTimeout {
			t.Fatalf("code %v, want timeout", core.CodeOf(err))
		}
		var pe *core.Error
		if !errors.As(err, &pe) || pe.Message != "flow: timeout" {
			t.Fatalf("error %v", err)
		}
	})
	t.Run("wrapped_deadline", func(t *testing.T) {
		err := runContextError(errContext{err: fmt.Errorf("wrapped: %w", context.DeadlineExceeded)})
		if core.CodeOf(err) != core.CodeTimeout {
			t.Fatalf("code %v, want timeout", core.CodeOf(err))
		}
	})
	t.Run("unknown", func(t *testing.T) {
		err := runContextError(errContext{err: errors.New("custom")})
		if core.CodeOf(err) != core.CodeCancelled {
			t.Fatalf("code %v, want cancelled fallback", core.CodeOf(err))
		}
		var pe *core.Error
		if !errors.As(err, &pe) || pe.Message != "flow: cancelled" {
			t.Fatalf("error %v", err)
		}
	})
}
