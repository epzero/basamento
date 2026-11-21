package temporal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/epzero/basamento/core"
)

// workflowCodecTimeout bounds blob and key I/O in workflow-bound converter calls
// while the SDK deadlock detector is paused around the data converter (G1).
const workflowCodecTimeout = 30 * time.Second

// taskFailure is a sanitized panic value for transient payload codec errors in
// workflow code. The SDK fails the workflow task so the server can retry; the
// value must not carry the original error text (plaintext history).
type taskFailure struct {
	code core.Code
}

func (e taskFailure) Error() string {
	return fmt.Sprintf("temporal: workflow task failed (%s); server will retry", e.code)
}

func codecTransient(pe *core.Error) bool {
	if pe == nil {
		return false
	}
	switch pe.Code {
	case core.CodeUnavailable, core.CodeTimeout, core.CodeRateLimited:
		return true
	case core.CodeCancelled:
		// Workflow-bound I/O uses our context (tenant plus the 30 s bound); flow code
		// cannot cancel it, so cancelled here is not a caller decision: bound expiry
		// (the blob layer may map a deadline to cancelled) or the store reported
		// cancellation. Both are retried like unavailable.
		return true
	default:
		return core.IsRetryable(pe)
	}
}

func errorFromPanic(v any, logger *slog.Logger) error {
	if err, ok := v.(error); ok {
		return err
	}
	if logger == nil {
		logger = slog.Default()
	}
	logger.Error("temporal: non-error panic value in workflow code",
		slog.String("panic", formatPanicValue(v)))
	return core.NewError(core.CodeInternal, "temporal: unexpected panic value", nil)
}

// workflowCodecPolicy applies transient-vs-permanent rules for workflow-bound converter calls.
func workflowCodecPolicy(wfBound bool, log func(*core.Error), ioCtx context.Context, err error) error {
	if !wfBound {
		return err
	}
	if err == nil {
		return nil
	}
	if ioCtx != nil && ioCtx.Err() != nil {
		pe := &core.Error{Code: core.CodeTimeout, Message: "workflow codec I/O bound exceeded", Cause: ioCtx.Err()}
		if log != nil {
			log(pe)
		}
		panic(taskFailure{code: core.CodeTimeout})
	}
	var pe *core.Error
	if !errors.As(err, &pe) {
		// The codec returned text we do not control (store, vendor or blob
		// detail). Log it bounded; history gets a fixed internal error.
		if log != nil {
			log(&core.Error{Code: core.CodeInternal, Message: err.Error()})
		}
		return core.NewError(core.CodeInternal, "temporal: payload codec failed", nil)
	}
	if log != nil {
		log(pe)
	}
	if codecTransient(pe) {
		panic(taskFailure{code: pe.Code})
	}
	return err
}
