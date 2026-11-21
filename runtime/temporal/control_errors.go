package temporal

import (
	"context"
	"errors"
	"log/slog"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/temporal"

	"github.com/epzero/basamento/core"
)

func (r *Runtime) clientError(op string, err error) error {
	if err == nil {
		return nil
	}
	var pe *core.Error
	if errors.As(err, &pe) {
		return pe
	}
	if errors.Is(err, context.Canceled) {
		return core.NewError(core.CodeCancelled, "temporal: the request was cancelled", nil)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return core.NewError(core.CodeTimeout, "temporal: the request timed out", nil)
	}
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) {
		return mapApplicationError(appErr)
	}
	if nf := (*serviceerror.NotFound)(nil); errors.As(err, &nf) {
		return core.NewError(core.CodeNotFound, "temporal: run not found", nil)
	}
	if was := (*serviceerror.WorkflowExecutionAlreadyStarted)(nil); errors.As(err, &was) {
		return core.NewError(core.CodeConflict, "temporal: a run with that id already exists", nil)
	}
	if ia := (*serviceerror.InvalidArgument)(nil); errors.As(err, &ia) {
		return core.NewError(core.CodeInvalidArgument, "temporal: invalid request", nil)
	}
	if pd := (*serviceerror.PermissionDenied)(nil); errors.As(err, &pd) {
		return core.NewError(core.CodePermissionDenied, "temporal: permission denied", nil)
	}
	if un := (*serviceerror.Unavailable)(nil); errors.As(err, &un) {
		return core.NewError(core.CodeUnavailable, "temporal: the service is unavailable", nil)
	}
	if de := (*serviceerror.DeadlineExceeded)(nil); errors.As(err, &de) {
		return core.NewError(core.CodeUnavailable, "temporal: the service is unavailable", nil)
	}
	if re := (*serviceerror.ResourceExhausted)(nil); errors.As(err, &re) {
		return core.NewError(core.CodeRateLimited, "temporal: rate limited", nil)
	}
	if nnf := (*serviceerror.NamespaceNotFound)(nil); errors.As(err, &nnf) {
		return core.NewError(core.CodeUnavailable, "temporal: the namespace does not exist", nil)
	}
	r.logClientFailure(op, err)
	return core.NewError(core.CodeInternal, "temporal: the server call failed", map[string]any{"op": op})
}

func mapApplicationError(appErr *temporal.ApplicationError) error {
	code := core.Code(appErr.Type())
	switch code {
	case core.CodeNotFound, core.CodeConflict, core.CodeUnavailable:
		return core.NewError(code, appErr.Message(), nil)
	default:
		return core.NewError(core.CodeInternal, "temporal: the server call failed", nil)
	}
}

func (r *Runtime) logClientFailure(op string, err error) {
	logger := r.logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Error("temporal: server call failed",
		slog.String("op", op),
		slog.String("error", truncateLogTextRunes(err.Error())),
	)
}
