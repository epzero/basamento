package temporal

import (
	"encoding/json"
	"errors"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// HostInput is the workflow argument that starts the flow host.
type HostInput struct {
	Flow         runtime.FlowRef `json:"flow"`
	Input        json.RawMessage `json:"input"`
	RunID        string          `json:"run_id"`
	Continuation *Continuation   `json:"continuation,omitempty"`
}

// UnmarshalJSON accepts both the "run_id" field and the "RunID" spelling, so
// a history recorded with either name replays with its original run id and
// therefore its original Rand seed and child/Emit run references.
func (in *HostInput) UnmarshalJSON(data []byte) error {
	type hostInput HostInput
	aux := struct {
		*hostInput
		LegacyRunID string `json:"RunID"`
	}{hostInput: (*hostInput)(in)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if in.RunID == "" {
		in.RunID = aux.LegacyRunID
	}
	return nil
}

// hostWorkflow runs the registered flow host behind a DurableContext.
func (r *Runtime) hostWorkflow(ctx workflow.Context, in HostInput) (json.RawMessage, error) {
	tenant := tenantFromWorkflow(ctx)
	runID := in.RunID
	if runID == "" {
		runID = runIDFromWorkflowID(tenant, workflow.GetInfo(ctx).WorkflowExecution.ID)
	}
	dc := newDurableContext(ctx, r, in, tenant)
	if err := dc.registerGateUpdateHandler(); err != nil {
		return nil, err
	}
	var out json.RawMessage
	var err error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				if _, ok := recovered.(taskFailure); ok {
					panic(recovered)
				}
				err = flowPanicErr()
				r.logPanic(tenant, runID, "", recovered)
			}
		}()
		out, err = r.host(dc, in.Flow, in.Input)
	}()
	if dc.continueErr != nil {
		return nil, dc.continueErr
	}
	return out, r.wrapWorkflowError(tenant, runID, err)
}

func (r *Runtime) wrapWorkflowError(tenant core.TenantID, runID string, err error) error {
	if err == nil {
		return nil
	}
	var pe *core.Error
	if errors.As(err, &pe) {
		if !core.IsRetryable(pe) {
			return temporal.NewNonRetryableApplicationError(pe.Message, string(pe.Code), nil)
		}
		return temporal.NewApplicationError(pe.Message, string(pe.Code), pe.Retryable)
	}
	r.logFlowFailure(tenant, runID, err)
	return temporal.NewNonRetryableApplicationError("temporal: flow failed", string(core.CodeInternal), nil)
}
