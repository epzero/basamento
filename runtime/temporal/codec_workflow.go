package temporal

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/epzero/basamento/core"
)

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

func (r *Runtime) logCodecFailure(tenant core.TenantID, workflowID, step string, err error) {
	logger := r.logger
	if logger == nil {
		logger = slog.Default()
	}
	var pe *core.Error
	if !errors.As(err, &pe) {
		return
	}
	attrs := []any{
		slog.String("tenant", string(tenant)),
		slog.String("workflow", workflowID),
		slog.String("code", string(pe.Code)),
		slog.String("error", truncateLogTextRunes(pe.Message)),
	}
	if step != "" {
		attrs = append(attrs, slog.String("step", step))
	}
	logger.Error("temporal: payload codec failed in workflow", attrs...)
}

// handleCodecPanic classifies a recovered encode/decode panic. Transient errors
// re-panic as taskFailure; permanent errors return for a failed Step future.
func (r *Runtime) handleCodecPanic(tenant core.TenantID, workflowID, step string, recovered any) (permanent *core.Error) {
	var pe *core.Error
	if !errors.As(errorFromPanic(recovered), &pe) {
		panic(recovered)
	}
	r.logCodecFailure(tenant, workflowID, step, pe)
	if codecTransient(pe) {
		panic(taskFailure{code: pe.Code})
	}
	return pe
}

func (r *Runtime) handleCodecError(tenant core.TenantID, workflowID, step string, err error) error {
	if err == nil {
		return nil
	}
	var pe *core.Error
	if !errors.As(err, &pe) {
		return err
	}
	r.logCodecFailure(tenant, workflowID, step, pe)
	if codecTransient(pe) {
		panic(taskFailure{code: pe.Code})
	}
	return pe
}
