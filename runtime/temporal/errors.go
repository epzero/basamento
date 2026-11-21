package temporal

import (
	"errors"

	"go.temporal.io/sdk/temporal"

	"github.com/epzero/basamento/core"
)

// activityApplicationError maps a step error for Temporal. Causes are never
// attached: the SDK stores them in plaintext history. Core codes and messages
// cross the boundary; Details maps are dropped.
func activityApplicationError(err error) error {
	if err == nil {
		return nil
	}
	var pe *core.Error
	if errors.As(err, &pe) {
		if !core.IsRetryable(pe) {
			return temporal.NewNonRetryableApplicationError(pe.Message, string(pe.Code), nil)
		}
		return temporal.NewApplicationError(pe.Message, string(pe.Code), pe.Retryable)
	}
	return temporal.NewNonRetryableApplicationError("temporal: step failed", string(core.CodeInternal), nil)
}

func activityToCore(err error) error {
	if err == nil {
		return nil
	}
	var pe *core.Error
	if errors.As(err, &pe) {
		return pe
	}
	var app *temporal.ApplicationError
	if errors.As(err, &app) {
		code := core.Code(app.Type())
		retryable := false
		var details bool
		if app.Details(&details) == nil {
			retryable = details
		}
		return &core.Error{Code: code, Message: app.Message(), Retryable: retryable}
	}
	var canceled *temporal.CanceledError
	if errors.As(err, &canceled) {
		return core.NewError(core.CodeCancelled, "temporal: activity cancelled", nil)
	}
	return core.NewError(core.CodeInternal, "temporal: activity failed", nil)
}
