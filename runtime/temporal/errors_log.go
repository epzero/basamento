package temporal

import (
	"errors"
	"log/slog"

	"github.com/epzero/basamento/core"
)

func (r *Runtime) logStepFailure(tenant core.TenantID, runID, step string, err error) {
	logger := r.logger
	if logger == nil {
		logger = slog.Default()
	}
	if err == nil {
		return
	}
	var pe *core.Error
	if errors.As(err, &pe) {
		return
	}
	logger.Error("temporal: step failed",
		slog.String("tenant", string(tenant)),
		slog.String("run", runID),
		slog.String("step", step),
		slog.String("code", string(core.CodeInternal)),
	)
}

func (r *Runtime) logFlowFailure(tenant core.TenantID, runID string, err error) {
	logger := r.logger
	if logger == nil {
		logger = slog.Default()
	}
	if err == nil {
		return
	}
	var pe *core.Error
	if errors.As(err, &pe) {
		return
	}
	logger.Error("temporal: flow failed",
		slog.String("tenant", string(tenant)),
		slog.String("run", runID),
		slog.String("code", string(core.CodeInternal)),
	)
}
