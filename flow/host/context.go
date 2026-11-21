package host

import (
	"context"
	"errors"

	"github.com/epzero/basamento/core"
)

// runContextError maps a done run context to the platform error the host
// returns when the run ends because the context was cancelled or timed out.
func runContextError(ctx context.Context) error {
	err := ctx.Err()
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return &core.Error{Code: core.CodeCancelled, Message: "flow: cancelled", Cause: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &core.Error{Code: core.CodeTimeout, Message: "flow: timeout", Cause: err}
	}
	return &core.Error{Code: core.CodeCancelled, Message: "flow: cancelled", Cause: err}
}
