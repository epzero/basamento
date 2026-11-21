package local

import (
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/epzero/basamento/core"
)

func (r *Runtime) logPanic(tenant core.TenantID, runID, step string, recovered any) {
	logger := r.logger
	if logger == nil {
		logger = slog.Default()
	}
	attrs := []any{
		"tenant", string(tenant),
		"run", runID,
		"panic", fmt.Sprintf("%v", recovered),
		"stack", string(debug.Stack()),
	}
	if step != "" {
		attrs = append(attrs, "step", step)
	}
	logger.Error("local runtime recovered a panic", attrs...)
}
