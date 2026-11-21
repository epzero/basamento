package temporal

import (
	"context"
	"errors"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// FlowSearchAttribute is the keyword custom search attribute that stores the
// flow name on each execution (registration happens during Serve).
const FlowSearchAttribute = "BasamentoFlow"

var flowSearchAttributeKey = temporal.NewSearchAttributeKeyKeyword(FlowSearchAttribute)

// Start starts a workflow execution for req.Flow. The tenant comes from ctx and
// must agree with req.TenantID when set.
func (r *Runtime) Start(ctx context.Context, req runtime.StartRequest) (runtime.RunRef, error) {
	tenant, err := r.tenant(ctx, req.TenantID)
	if err != nil {
		return runtime.RunRef{}, err
	}
	if req.Flow.Name == "" {
		return runtime.RunRef{}, core.NewError(core.CodeInvalidArgument, "temporal: flow name is required", nil)
	}
	if req.IdempotencyKey != "" && req.RunID != "" {
		return runtime.RunRef{}, core.NewError(core.CodeInvalidArgument,
			"temporal: an idempotency key and a run id cannot be combined", nil)
	}
	derived := req.IdempotencyKey != ""
	runID := req.RunID
	if derived {
		runID = deriveRunID(tenant, req.IdempotencyKey)
	} else if runID == "" {
		runID, err = r.newRunID()
		if err != nil {
			return runtime.RunRef{}, core.NewError(core.CodeInternal, "temporal: generate run id", map[string]any{"cause": err.Error()})
		}
	} else if err := validateRunID(runID); err != nil {
		return runtime.RunRef{}, err
	}

	cl, err := r.clientFor()
	if err != nil {
		return runtime.RunRef{}, err
	}
	wfID := workflowID(tenant, runID)
	searchAttrs := temporal.NewSearchAttributes(flowSearchAttributeKey.ValueSet(req.Flow.Name))
	opts := client.StartWorkflowOptions{
		ID:                                       wfID,
		TaskQueue:                                r.taskQueue,
		WorkflowIDReusePolicy:                    enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		WorkflowIDConflictPolicy:                 enumspb.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
		TypedSearchAttributes:                    searchAttrs,
	}
	_, err = cl.ExecuteWorkflow(ctx, opts, HostWorkflowName, HostInput{
		Flow:  req.Flow,
		Input: req.Input,
		RunID: runID,
	})
	if err != nil {
		var started *serviceerror.WorkflowExecutionAlreadyStarted
		if derived && errors.As(err, &started) {
			return runtime.RunRef{RunID: runID, TenantID: tenant}, nil
		}
		return runtime.RunRef{}, r.mapStartClientError(runID, err)
	}
	return runtime.RunRef{RunID: runID, TenantID: tenant}, nil
}

func (r *Runtime) mapStartClientError(runID string, err error) error {
	mapped := r.clientError("start", err)
	var pe *core.Error
	if errors.As(mapped, &pe) && pe.Code == core.CodeConflict {
		return core.NewError(core.CodeConflict,
			"temporal: a run with that id already exists", map[string]any{"run": runID})
	}
	return mapped
}
