package temporal

import (
	"context"
	"errors"
	"log/slog"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/temporal"

	"github.com/epzero/basamento/core"
)

func opDetail(op string) map[string]any {
	return map[string]any{"op": op}
}

func (r *Runtime) runtimeLogger() *slog.Logger {
	if r.logger != nil {
		return r.logger
	}
	return slog.Default()
}

func (r *Runtime) clientError(op string, err error) error {
	if err == nil {
		return nil
	}
	var pe *core.Error
	if errors.As(err, &pe) {
		return pe
	}
	if errors.Is(err, context.Canceled) {
		return core.NewError(core.CodeCancelled, "temporal: the request was cancelled", opDetail(op))
	}
	if ce := (*serviceerror.Canceled)(nil); errors.As(err, &ce) {
		return core.NewError(core.CodeCancelled, "temporal: the request was cancelled", opDetail(op))
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return core.NewError(core.CodeTimeout, "temporal: the request timed out", opDetail(op))
	}
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) {
		return mapApplicationError(op, appErr)
	}
	if nf := (*serviceerror.NotFound)(nil); errors.As(err, &nf) {
		return core.NewError(core.CodeNotFound, "temporal: run not found", opDetail(op))
	}
	if was := (*serviceerror.WorkflowExecutionAlreadyStarted)(nil); errors.As(err, &was) {
		return core.NewError(core.CodeConflict, "temporal: a run with that id already exists", opDetail(op))
	}
	if ia := (*serviceerror.InvalidArgument)(nil); errors.As(err, &ia) {
		return core.NewError(core.CodeInvalidArgument, "temporal: invalid request", opDetail(op))
	}
	if pd := (*serviceerror.PermissionDenied)(nil); errors.As(err, &pd) {
		return core.NewError(core.CodePermissionDenied, "temporal: permission denied", opDetail(op))
	}
	if un := (*serviceerror.Unavailable)(nil); errors.As(err, &un) {
		return core.NewError(core.CodeUnavailable, "temporal: the service is unavailable", opDetail(op))
	}
	if de := (*serviceerror.DeadlineExceeded)(nil); errors.As(err, &de) {
		return core.NewError(core.CodeUnavailable, "temporal: the service is unavailable", opDetail(op))
	}
	if re := (*serviceerror.ResourceExhausted)(nil); errors.As(err, &re) {
		return core.NewError(core.CodeRateLimited, "temporal: rate limited", opDetail(op))
	}
	if wnr := (*serviceerror.WorkflowNotReady)(nil); errors.As(err, &wnr) {
		return core.NewError(core.CodeUnavailable, "temporal: the run is not ready for the call", opDetail(op))
	}
	if nnf := (*serviceerror.NamespaceNotFound)(nil); errors.As(err, &nnf) {
		return core.NewError(core.CodeUnavailable, "temporal: the namespace does not exist", opDetail(op))
	}
	r.logClientFailure(op, err)
	return core.NewError(core.CodeInternal, "temporal: the server call failed", opDetail(op))
}

func mapApplicationError(op string, appErr *temporal.ApplicationError) error {
	code := core.Code(appErr.Type())
	switch code {
	case core.CodeNotFound, core.CodeConflict, core.CodeUnavailable:
		return core.NewError(code, appErr.Message(), opDetail(op))
	default:
		return core.NewError(core.CodeInternal, "temporal: the server call failed", opDetail(op))
	}
}

func (r *Runtime) logClientFailure(op string, err error) {
	r.runtimeLogger().Error("temporal: server call failed",
		slog.String("op", op),
		slog.String("error", truncateLogTextRunes(err.Error())),
	)
}
