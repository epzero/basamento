package local

import (
	"fmt"
	"log/slog"
	"runtime/debug"
	"unicode/utf8"

	"github.com/epzero/basamento/core"
)

const (
	maxPanicValue = 4096
	maxPanicStack = 16 << 10
)

// logPanic records one Error for a recovered flow or step panic. It runs outside
// the run lock from the recovering goroutine. A nil Runtime logger uses
// slog.Default() at call time. Panic values and stacks are truncated before
// formatting so a huge panic or deep recursion cannot megabyte-size log records.
func (r *Runtime) logPanic(tenant core.TenantID, runID, step string, recovered any) {
	logger := r.logger
	if logger == nil {
		logger = slog.Default()
	}
	attrs := []any{
		"tenant", string(tenant),
		"run", runID,
		"panic", formatPanicValue(recovered),
		"stack", formatPanicStack(debug.Stack()),
	}
	if step != "" {
		attrs = append(attrs, "step", step)
	}
	logger.Error("local runtime recovered a panic", attrs...)
}

func formatPanicValue(recovered any) string {
	probe := fmt.Sprintf("%.*v", maxPanicValue+1, recovered)
	if utf8.RuneCountInString(probe) > maxPanicValue {
		return fmt.Sprintf("%.*v", maxPanicValue, recovered) + "... (truncated)"
	}
	return probe
}

func formatPanicStack(stack []byte) string {
	if len(stack) <= maxPanicStack {
		return string(stack)
	}
	return string(stack[:maxPanicStack]) + "... (stack truncated)"
}
