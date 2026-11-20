package telemetry

import (
	"context"

	"github.com/epzero/basamento/core"
)

// runKeyType is unexported so only WithRunID can set a run id.
type runKeyType struct{}

var runKey runKeyType

// WithRunID returns a context carrying the run id that log records attach as
// run_id (REQ-TELEM-005). The empty id reads as no run.
func WithRunID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, runKey, id)
}

// RunIDFrom returns the run id carried by ctx. The second result is false when
// ctx is nil or carries no non-empty id.
func RunIDFrom(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	id, ok := ctx.Value(runKey).(string)
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

func runIDOrEmpty(ctx context.Context) string {
	id, _ := RunIDFrom(ctx)
	return id
}

func tenantOrEmpty(ctx context.Context) string {
	id, ok := core.TenantFrom(ctx)
	if !ok {
		return ""
	}
	return string(id)
}
