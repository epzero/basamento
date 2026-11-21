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

// cancelError marks an invocation the middleware could not cancel after it
// started. The provider may still be running it, so the attempt is not
// retryable: a retry under a new client ref could double-start the
// invocation (REQ-MODEL-005, REQ-MODEL-009).
type cancelError struct {
	Err error
}

func (e *cancelError) Error() string { return "cannot cancel invocation: " + e.Err.Error() }

func (e *cancelError) Unwrap() error { return e.Err }

// cancelFailure wraps a provider's Cancel error as a non-retryable
// cancelError. The wrapped platform error keeps the Cancel error's code and
// is never retryable, whatever the provider claimed.
func cancelFailure(err error) error {
	mapped := model.MapError(err)
	var pe *core.Error
	if !errors.As(mapped, &pe) {
		return &cancelError{Err: mapped}
	}
	return &cancelError{Err: &core.Error{
		Code:    pe.Code,
		Message: "cannot cancel invocation: " + pe.Message,
		Cause:   err,
	}}
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
	// The provider owns the run beyond this call's context, so a follow that
	// fails must cancel it rather than leave it alive (REQ-MODEL-009). A
	// failed Cancel is surfaced as a non-retryable cancelError so the call
	// does not start a second invocation under a new client ref.
	cancelled := false
	cancelRun := func() error {
		if cancelled {
			return nil
		}
		cancelled = true
		if cerr := p.Invoker.Cancel(context.WithoutCancel(ctx), h); cerr != nil {
			return cancelFailure(cerr)
		}
		return nil
	}
	events, err := p.Invoker.Events(ctx, h, 0)
	if err != nil {
		if cerr := cancelRun(); cerr != nil {
			return model.Result{}, cerr
		}
		return model.Result{}, model.MapError(err)
	}
	for ev := range events {
		emit(ev)
	}
	if ctx.Err() != nil {
		// The attempt was cancelled (a timeout): tell the provider.
		if cerr := cancelRun(); cerr != nil {
			return model.Result{}, cerr
		}
	}
	res, err := p.Invoker.Result(ctx, h)
	if err != nil {
		if cerr := cancelRun(); cerr != nil {
			return res, cerr
		}
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
	return &core.Error{Code: f.Code, Message: f.Message, Retryable: f.Retryable, RetryAfter: f.RetryAfter}
}
