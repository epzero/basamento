package core

import (
	"errors"
	"fmt"
	"time"
)

// Code is a closed, stable error taxonomy shared by every layer and mapped to
// API problem responses. Add codes; never rename or remove them.
type Code string

const (
	CodeInvalidArgument   Code = "invalid_argument"
	CodeNotFound          Code = "not_found"
	CodeConflict          Code = "conflict"
	CodePermissionDenied  Code = "permission_denied"
	CodeUnauthenticated   Code = "unauthenticated"
	CodeBudgetExceeded    Code = "budget_exceeded"
	CodeRateLimited       Code = "rate_limited"
	CodeTimeout           Code = "timeout"
	CodeUnavailable       Code = "unavailable"
	CodeUnsupported       Code = "unsupported"
	CodeVendor            Code = "vendor_error"
	CodeAmbiguousStart    Code = "ambiguous_start"
	CodeCancelled         Code = "cancelled"
	CodeInternal          Code = "internal"
	CodeRecordingMissing  Code = "recording_missing"
	CodeDeterminismBroken Code = "determinism_violation"
)

// Error is the platform error. It carries retry semantics (for runtimes and
// clients) and an optional message key (for localized presentation at the edge).
type Error struct {
	Code       Code
	Message    string         // English, for logs; never shown to end users as-is
	Retryable  bool           // safe to retry the same request
	RetryAfter time.Duration  // provider hint, zero when unknown
	MessageKey string         // i18n key for user-facing text, optional
	Params     map[string]any // i18n params; must not contain secrets
	Details    map[string]any // redacted before leaving the process
	Cause      error
}

// NewError builds a non-retryable error.
func NewError(code Code, msg string, details map[string]any) *Error {
	return &Error{Code: code, Message: msg, Details: details}
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

// CodeOf returns the platform code of err, or CodeInternal.
func CodeOf(err error) Code {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return CodeInternal
}

// IsRetryable reports whether err is marked retryable.
func IsRetryable(err error) bool {
	var pe *Error
	return errors.As(err, &pe) && pe.Retryable
}
