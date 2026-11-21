// Panic logging mirrors runtime/local/paniclog.go; layering forbids sharing the file.
package temporal

import (
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/epzero/basamento/core"
)

const (
	maxPanicValue = 4096
	maxPanicStack = 16 << 10
)

func (r *Runtime) logPanic(tenant core.TenantID, runID, step string, recovered any) {
	logger := r.logger
	if logger == nil {
		logger = slog.Default()
	}
	attrs := []any{
		"tenant", string(tenant),
		"panic", formatPanicValue(recovered),
		"stack", formatPanicStack(debug.Stack()),
	}
	if step != "" {
		attrs = append(attrs, "workflow", runID, "step", step)
	} else {
		attrs = append(attrs, "run", runID)
	}
	logger.Error("temporal runtime recovered a panic", attrs...)
}

func formatPanicValue(recovered any) string {
	if s, ok := recovered.(string); ok {
		return truncateLogTextRunes(s)
	}
	return truncateLogTextRunes(fmt.Sprintf("%v", recovered))
}

// truncateLogTextRunes bounds text written to logs (panics and non-core errors).
func truncateLogTextRunes(s string) string {
	n := 0
	for i := range s {
		if n == maxPanicValue {
			return s[:i] + "... (truncated)"
		}
		n++
	}
	return s
}

func formatPanicStack(stack []byte) string {
	if len(stack) <= maxPanicStack {
		return string(stack)
	}
	return string(stack[:maxPanicStack]) + "... (stack truncated)"
}
