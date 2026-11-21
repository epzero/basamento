package temporal

import (
	"errors"

	"go.temporal.io/sdk/temporal"

	"github.com/epzero/basamento/core"
)

// activityApplicationError returns an error Temporal carries with a stable type
// (the core code) across the activity boundary.
func activityApplicationError(err error) error {
	if err == nil {
		return nil
	}
	var pe *core.Error
	if errors.As(err, &pe) {
		return temporal.NewApplicationError(pe.Message, string(pe.Code), pe.Retryable)
	}
	return temporal.NewApplicationError(err.Error(), string(core.CodeInternal), false)
}

// FromActivityError maps a Temporal activity error back to a core error for
// Future.Get (REQ-RT-041).
func FromActivityError(err error) error {
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
		if err := app.Details(&details); err == nil {
			retryable = details
		}
		return &core.Error{Code: code, Message: app.Message(), Retryable: retryable}
	}
	var canceled *temporal.CanceledError
	if errors.As(err, &canceled) {
		return core.NewError(core.CodeCancelled, canceled.Error(), nil)
	}
	return core.NewError(core.CodeInternal, "temporal: activity failed", map[string]any{"cause": err.Error()})
}

// FromWorkflowError maps a failed workflow execution error to a core error.
func FromWorkflowError(err error) error {
	if err == nil {
		return nil
	}
	var pe *core.Error
	if errors.As(err, &pe) {
		return pe
	}
	var wf *temporal.WorkflowExecutionError
	if errors.As(err, &wf) {
		return FromWorkflowError(wf.Unwrap())
	}
	return FromActivityError(err)
}
