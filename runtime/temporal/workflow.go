package temporal

import (
	"encoding/json"
	"errors"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// HostInput is the workflow argument that starts the flow host. PR (b) may extend
// it with checkpoint thresholds and continuation state.
type HostInput struct {
	Flow   runtime.FlowRef
	Input  json.RawMessage
	RunID  string
	Tenant core.TenantID
}

// hostWorkflow runs the registered flow host behind a DurableContext.
func (r *Runtime) hostWorkflow(ctx workflow.Context, in HostInput) (json.RawMessage, error) {
	ctx = withWorkflowTenant(ctx, in.Tenant)
	var out json.RawMessage
	var err error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = flowPanicErr()
				r.logPanic(in.Tenant, in.RunID, "", recovered)
			}
		}()
		dc := newDurableContext(ctx, r, in.RunID, in.Tenant)
		out, err = r.host(dc, in.Flow, in.Input)
	}()
	return out, wrapWorkflowError(err)
}

func wrapWorkflowError(err error) error {
	if err == nil {
		return nil
	}
	var pe *core.Error
	if errors.As(err, &pe) {
		return temporal.NewApplicationError(pe.Message, string(pe.Code), pe.Retryable)
	}
	return err
}
