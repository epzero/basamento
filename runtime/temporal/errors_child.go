package temporal

import (
	"errors"

	"go.temporal.io/sdk/temporal"

	"github.com/epzero/basamento/core"
)

func childToCore(err error) error {
	if err == nil {
		return nil
	}
	var pe *core.Error
	if errors.As(err, &pe) {
		return pe
	}
	var child *temporal.ChildWorkflowExecutionError
	if errors.As(err, &child) {
		inner := child.Unwrap()
		var canceled *temporal.CanceledError
		if errors.As(inner, &canceled) {
			return core.NewError(core.CodeCancelled, "temporal: child cancelled", nil)
		}
		var term *temporal.TerminatedError
		if errors.As(inner, &term) {
			return core.NewError(core.CodeCancelled, "temporal: child cancelled", nil)
		}
		var app *temporal.ApplicationError
		if errors.As(inner, &app) {
			code := core.Code(app.Type())
			retryable := false
			var details bool
			if app.Details(&details) == nil {
				retryable = details
			}
			return &core.Error{Code: code, Message: app.Message(), Retryable: retryable}
		}
		return core.NewError(core.CodeInternal, "temporal: child failed", nil)
	}
	return core.NewError(core.CodeInternal, "temporal: child failed", nil)
}
