package model

import (
	"context"
	"errors"
	"fmt"

	"github.com/epzero/basamento/core"
)

// Failure is the error an invocation records in Result.Error when it fails.
// It carries the platform code so a caller that follows events can classify
// the end of an invocation without parsing vendor text.
type Failure struct {
	Code      core.Code `json:"code"`
	Message   string    `json:"message"`
	Retryable bool      `json:"retryable,omitempty"`
}

// FailureOf describes err as a Failure suitable for persisting on Result.Error.
// A *core.Error keeps its code and message; any other error keeps code
// vendor_error with the fixed message "vendor call failed" so raw vendor text
// never lands in the stored record. Call MapError first when the original
// should remain on Cause for in-process callers. A nil error is an internal
// failure: it only reaches here from a bug, never from a vendor.
func FailureOf(err error) Failure {
	if err == nil {
		return Failure{Code: core.CodeInternal, Message: "invocation failed without an error"}
	}
	var pe *core.Error
	if errors.As(err, &pe) {
		return Failure{Code: pe.Code, Message: pe.Message, Retryable: pe.Retryable}
	}
	return Failure{Code: core.CodeVendor, Message: "vendor call failed"}
}

// MapError returns err as a platform error. A *core.Error passes through; a
// canceled or expired context becomes "cancelled" or a retryable "timeout";
// any other error becomes a non-retryable "vendor_error" with message
// "vendor call failed" that wraps the original in Cause. Adapters use it at
// the boundary of a vendor call so callers see one error taxonomy (ADR-0006).
func MapError(err error) error {
	if err == nil {
		return nil
	}
	var pe *core.Error
	if errors.As(err, &pe) {
		return err
	}
	switch {
	case errors.Is(err, context.Canceled):
		return &core.Error{Code: core.CodeCancelled, Message: "invocation cancelled", Cause: err}
	case errors.Is(err, context.DeadlineExceeded):
		return &core.Error{Code: core.CodeTimeout, Message: "invocation timed out", Retryable: true, Cause: err}
	default:
		return &core.Error{Code: core.CodeVendor, Message: "vendor call failed", Cause: err}
	}
}

// Select returns the descriptor in ds that serves modelID with capability op.
// It fails with "not_found" when no descriptor holds the id and "unsupported"
// when the model exists but does not declare op. Adapters use it so a request
// reaches the vendor only when the adapter can serve it.
func Select(ds []Descriptor, modelID string, op Capability) (Descriptor, error) {
	for _, d := range ds {
		if d.ID != modelID {
			continue
		}
		for _, c := range d.Caps {
			if c == op {
				return d, nil
			}
		}
		return Descriptor{}, core.NewError(core.CodeUnsupported,
			fmt.Sprintf("model %q does not support %s", modelID, op),
			map[string]any{"model": modelID, "op": string(op)})
	}
	return Descriptor{}, core.NewError(core.CodeNotFound,
		fmt.Sprintf("model %q is not served by this adapter", modelID),
		map[string]any{"model": modelID})
}
