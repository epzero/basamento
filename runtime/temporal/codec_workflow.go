package temporal

import (
	"context"
	"errors"
	"fmt"
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
		// Workflow-bound codec I/O uses a timeout-only context; nothing else cancels it.
		return true
	default:
		return core.IsRetryable(pe)
	}
}

func errorFromPanic(v any) error {
	if err, ok := v.(error); ok {
		return err
	}
	return fmt.Errorf("%v", v)
}

// workflowCodecPolicy applies transient-vs-permanent rules for workflow-bound converter calls.
func workflowCodecPolicy(wfBound bool, log func(*core.Error), ioCtx context.Context, err error) error {
	if !wfBound {
		return err
	}
	if ioCtx != nil && ioCtx.Err() != nil {
		pe := &core.Error{Code: core.CodeTimeout, Message: "workflow codec I/O bound exceeded", Cause: ioCtx.Err()}
		if log != nil {
			log(pe)
		}
		panic(taskFailure{code: core.CodeTimeout})
	}
	if err == nil {
		return nil
	}
	var pe *core.Error
	if !errors.As(err, &pe) {
		return err
	}
	if log != nil {
		log(pe)
	}
	if codecTransient(pe) {
		panic(taskFailure{code: pe.Code})
	}
	return err
}
