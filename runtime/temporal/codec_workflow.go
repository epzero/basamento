package temporal

import (
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
func workflowCodecPolicy(wfBound bool, log func(*core.Error), err error) error {
	if err == nil {
		return nil
	}
	if !wfBound {
		return err
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
