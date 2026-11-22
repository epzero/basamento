package temporal

import (
	"context"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// Capabilities reports durable execution with synchronous gate updates,
// continue-as-new and polyglot task queues on the Temporal runtime.
func (r *Runtime) Capabilities() runtime.Caps {
	return runtime.Caps{
		Durable:         true,
		SyncGateUpdates: true,
		ContinueAsNew:   true,
		PolyglotQueues:  true,
	}
}

// tenant enforces the shared rule: the tenant of a call is the context's, and
// a tenant field may only be empty or equal to it.
func (r *Runtime) tenant(ctx context.Context, field core.TenantID) (core.TenantID, error) {
	id, ok := core.TenantFrom(ctx)
	if !ok {
		return "", core.NewError(core.CodeInvalidArgument, "temporal: no tenant in the context", nil)
	}
	if field != "" && field != id {
		return "", core.NewError(core.CodeInvalidArgument, "temporal: the request tenant differs from the context tenant",
			map[string]any{"context_tenant": string(id), "request_tenant": string(field)})
	}
	if err := rejectTemporalUnsafeTenant(id); err != nil {
		return "", err
	}
	return id, nil
}

// rejectTemporalUnsafeTenant rejects tenant ids that cannot be embedded safely in
// Temporal visibility queries (quotes break the STARTS_WITH literal).
func rejectTemporalUnsafeTenant(id core.TenantID) error {
	for _, r := range string(id) {
		if unsafeVisibilityRune(r) {
			return core.NewError(core.CodeInvalidArgument, "temporal: this tenant id is not supported by the Temporal runtime", nil)
		}
	}
	return nil
}
