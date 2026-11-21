package temporal

import (
	"context"
	"encoding/json"
	"errors"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// Resolve delivers payload to the gate wait via a synchronous workflow update.
func (r *Runtime) Resolve(ctx context.Context, ref runtime.RunRef, gate string, payload json.RawMessage) error {
	tenant, err := r.tenant(ctx, ref.TenantID)
	if err != nil {
		return err
	}
	if ref.RunID == "" {
		return core.NewError(core.CodeInvalidArgument, "temporal: empty run id", nil)
	}
	if gate == "" {
		return core.NewError(core.CodeInvalidArgument, "temporal: gate name is required", nil)
	}
	cl, err := r.clientFor()
	if err != nil {
		return err
	}
	handle, err := cl.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
		WorkflowID:   workflowID(tenant, ref.RunID),
		UpdateName:   ResolveGateUpdate,
		Args:         []any{GateResolution{Gate: gate, Payload: payload}},
		WaitForStage: client.WorkflowUpdateStageCompleted,
	})
	if err != nil {
		return r.mapResolveError("resolve", err)
	}
	err = handle.Get(ctx, nil)
	if err != nil {
		return r.mapResolveError("resolve", err)
	}
	return nil
}

func (r *Runtime) mapResolveError(op string, err error) error {
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) {
		return mapApplicationError(appErr)
	}
	return r.clientError(op, err)
}
