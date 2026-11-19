package middleware

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
)

// StartError marks a failed start the provider did not acknowledge
// (REQ-MODEL-005). The retry of such a start reuses that attempt's client ref
// instead of deriving a new one (REQ-MODEL-054).
type StartError struct {
	Err error
}

// Error is the wrapped error's message.
func (e *StartError) Error() string { return e.Err.Error() }

// Unwrap exposes the wrapped error to core.IsRetryable and errors.Is.
func (e *StartError) Unwrap() error { return e.Err }

// UnacknowledgedStart reports whether err came from an unacknowledged start.
func UnacknowledgedStart(err error) bool {
	var se *StartError
	return err != nil && errors.As(err, &se)
}

// InvokerProvider adapts model.Invoker to the middleware's Provider port. It
// starts the invocation, follows its events and returns its result, mapping
// the contract's terminal states back to *core.Error failures.
type InvokerProvider struct {
	Invoker model.Invoker
}

// Attempt runs one invocation on the invoker.
func (p InvokerProvider) Attempt(ctx context.Context, a Attempt, emit func(model.Event)) (model.Result, error) {
	h, err := p.Invoker.Start(ctx, a.Request)
	if err != nil {
		return model.Result{}, &StartError{Err: model.MapError(err)}
	}
	events, err := p.Invoker.Events(ctx, h, 0)
	if err != nil {
		return model.Result{}, model.MapError(err)
	}
	for ev := range events {
		emit(ev)
	}
	if ctx.Err() != nil {
		// The attempt was cancelled (a timeout): tell the provider, which owns
		// the run beyond this call's context.
		_ = p.Invoker.Cancel(context.WithoutCancel(ctx), h)
	}
	res, err := p.Invoker.Result(ctx, h)
	if err != nil {
		return res, model.MapError(err)
	}
	switch res.State {
	case model.StateFailed:
		return res, failureError(res)
	case model.StateCancelled:
		return res, &core.Error{Code: core.CodeCancelled, Message: "invocation cancelled"}
	default:
		return res, nil
	}
}

func failureError(res model.Result) error {
	if len(res.Error) == 0 {
		return core.NewError(core.CodeInternal, "invocation failed without an error", nil)
	}
	var f model.Failure
	if err := json.Unmarshal(res.Error, &f); err != nil {
		return core.NewError(core.CodeInternal, "invocation failure is not a failure object", nil)
	}
	return &core.Error{Code: f.Code, Message: f.Message, Retryable: f.Retryable}
}
