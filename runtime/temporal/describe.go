package temporal

import (
	"context"
	"strings"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	workflowpb "go.temporal.io/api/workflow/v1"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// Describe returns the observable state of ref.RunID. StartedAt is the start
// time of the current execution (it moves across continue-as-new).
func (r *Runtime) Describe(ctx context.Context, ref runtime.RunRef) (runtime.RunStatus, error) {
	tenant, err := r.tenant(ctx, ref.TenantID)
	if err != nil {
		return runtime.RunStatus{}, err
	}
	wfID, err := controlWorkflowID(tenant, ref.RunID)
	if err != nil {
		return runtime.RunStatus{}, err
	}
	cl, err := r.clientFor()
	if err != nil {
		return runtime.RunStatus{}, err
	}
	resp, err := cl.DescribeWorkflowExecution(ctx, wfID, "")
	if err != nil {
		return runtime.RunStatus{}, r.clientError("describe", err)
	}
	return runStatusFromInfo(runtime.RunRef{RunID: ref.RunID, TenantID: tenant}, resp.GetWorkflowExecutionInfo())
}

func runStatusFromInfo(ref runtime.RunRef, info *workflowpb.WorkflowExecutionInfo) (runtime.RunStatus, error) {
	state, err := mapWorkflowStatus(info.GetStatus())
	if err != nil {
		return runtime.RunStatus{}, err
	}
	st := runtime.RunStatus{
		Ref:       ref,
		State:     state,
		StartedAt: info.GetStartTime().AsTime().UTC(),
	}
	if t, ok := workflowCloseTime(info); ok {
		st.ClosedAt = &t
	}
	return st, nil
}

func workflowCloseTime(info *workflowpb.WorkflowExecutionInfo) (time.Time, bool) {
	closed := info.GetCloseTime()
	if closed == nil || closed.AsTime().IsZero() || !isClosedStatus(info.GetStatus()) {
		return time.Time{}, false
	}
	t := closed.AsTime().UTC()
	return t, true
}

func mapWorkflowStatus(st enumspb.WorkflowExecutionStatus) (string, error) {
	switch st {
	case enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
		enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW:
		return "running", nil
	case enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED:
		return "completed", nil
	case enumspb.WORKFLOW_EXECUTION_STATUS_FAILED,
		enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
		return "failed", nil
	case enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED:
		return "cancelled", nil
	case enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED:
		return "terminated", nil
	default:
		return "", core.NewError(core.CodeInternal, "temporal: unknown workflow status", nil)
	}
}

func isClosedStatus(st enumspb.WorkflowExecutionStatus) bool {
	switch st {
	case enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
		enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW,
		enumspb.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED:
		return false
	default:
		return true
	}
}

func statusFromExecutionInfo(tenant core.TenantID, info *workflowpb.WorkflowExecutionInfo) (runtime.RunStatus, bool) {
	wfID := info.GetExecution().GetWorkflowId()
	prefix := string(tenant) + "/"
	if !strings.HasPrefix(wfID, prefix) {
		return runtime.RunStatus{}, false
	}
	runID := strings.TrimPrefix(wfID, prefix)
	st, err := runStatusFromInfo(runtime.RunRef{RunID: runID, TenantID: tenant}, info)
	if err != nil {
		return runtime.RunStatus{}, false
	}
	return st, true
}
