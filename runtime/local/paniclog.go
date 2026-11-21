package local

import (
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/epzero/basamento/core"
)

// Panic logs must not copy megabyte panic values or stack dumps from deep recursion.
const (
	maxPanicValue = 4096
	maxPanicStack = 16 << 10
)

// logPanic records one Error for a recovered flow or step panic. It runs outside
// the run lock from the recovering goroutine. A nil Runtime logger uses
// slog.Default() at call time. The formatted panic value is cut at maxPanicValue
// runes and the stack at maxPanicStack bytes so a huge panic or deep recursion
// cannot produce a megabyte log record.
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
	if s, ok := recovered.(string); ok {
		return truncatePanicValueRunes(s)
	}
	return truncatePanicValueRunes(fmt.Sprintf("%v", recovered))
}

func truncatePanicValueRunes(s string) string {
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
