package temporal

import (
	"context"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// Cancel asks the server to cancel the workflow execution. The reason is accepted
// for API symmetry but is not sent to Temporal (the SDK has no cancellation reason).
func (r *Runtime) Cancel(ctx context.Context, ref runtime.RunRef, _ string) error {
	tenant, err := r.tenant(ctx, ref.TenantID)
	if err != nil {
		return err
	}
	if ref.RunID == "" {
		return core.NewError(core.CodeInvalidArgument, "temporal: empty run id", nil)
	}
	cl, err := r.clientFor()
	if err != nil {
		return err
	}
	err = cl.CancelWorkflow(ctx, workflowID(tenant, ref.RunID), "")
	if err != nil {
		return r.clientError("cancel", err)
	}
	return nil
}
