package local

import (
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/epzero/basamento/core"
)

// logPanic records one Error for a recovered flow or step panic. It runs outside
// the run lock from the recovering goroutine. A nil Runtime logger uses
// slog.Default() at call time.
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
