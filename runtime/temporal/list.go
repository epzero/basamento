package temporal

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// List returns the context tenant's workflow executions, optionally filtered by
// flow name and state. Order is the visibility store's default (newest first);
// this backend does not support ORDER BY in list queries.
func (r *Runtime) List(ctx context.Context, q runtime.RunQuery) ([]runtime.RunStatus, string, error) {
	tenant, err := r.tenant(ctx, q.TenantID)
	if err != nil {
		return nil, "", err
	}
	query, err := listQuery(tenant, q.Flow, q.State)
	if err != nil {
		return nil, "", err
	}
	limit, err := listLimit(q.Limit)
	if err != nil {
		return nil, "", err
	}
	var pageToken []byte
	if q.Cursor != "" {
		pageToken, err = parseListCursor(q.Cursor, tenant, query)
		if err != nil {
			return nil, "", err
		}
	}
	cl, err := r.clientFor()
	if err != nil {
		return nil, "", err
	}
	resp, err := cl.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{
		Namespace:     r.clientNamespace(),
		Query:         query,
		PageSize:      int32(limit),
		NextPageToken: pageToken,
	})
	if err != nil {
		return nil, "", r.clientError("list", err)
	}
	out, droppedForeign, droppedStatus := r.mapListExecutions(tenant, resp.GetExecutions())
	r.logListDrops(droppedForeign, droppedStatus)
	if len(resp.GetNextPageToken()) == 0 {
		return out, "", nil
	}
	next, err := encodeListCursor(tenant, query, resp.GetNextPageToken())
	if err != nil {
		return nil, "", err
	}
	return out, next, nil
}

func listLimit(n int) (int, error) {
	if n == 0 {
		return 100, nil
	}
	if n < 0 {
		return 0, core.NewError(core.CodeInvalidArgument, "temporal: invalid list limit", nil)
	}
	if n > 1000 {
		return 1000, nil
	}
	return n, nil
}

func listQuery(tenant core.TenantID, flow, state string) (string, error) {
	if err := validateFlowFilter(flow); err != nil {
		return "", err
	}
	if state != "" {
		if err := validateListState(state); err != nil {
			return "", err
		}
	}
	q := fmt.Sprintf(`WorkflowId STARTS_WITH "%s/" AND ExecutionStatus != "ContinuedAsNew"`, string(tenant))
	if flow != "" {
		q += fmt.Sprintf(` AND %s = "%s"`, FlowSearchAttribute, flow)
	}
	if clause, ok := stateListClause(state); ok {
		q += " AND " + clause
	}
	return q, nil
}

func validateListState(state string) error {
	switch state {
	case "running", "completed", "cancelled", "terminated", "failed":
		return nil
	default:
		return core.NewError(core.CodeInvalidArgument, "temporal: invalid list state", nil)
	}
}

func stateListClause(state string) (string, bool) {
	switch state {
	case "":
		return "", false
	case "running":
		return `ExecutionStatus = "Running"`, true
	case "completed":
		return `ExecutionStatus = "Completed"`, true
	case "cancelled":
		return `ExecutionStatus = "Canceled"`, true
	case "terminated":
		return `ExecutionStatus = "Terminated"`, true
	case "failed":
		return `ExecutionStatus IN ("Failed", "TimedOut")`, true
	default:
		return "", false
	}
}

func (r *Runtime) mapListExecutions(tenant core.TenantID, execs []*workflowpb.WorkflowExecutionInfo) ([]runtime.RunStatus, int, int) {
	out := make([]runtime.RunStatus, 0, len(execs))
	var droppedForeign, droppedStatus int
	for _, info := range execs {
		st, ok := statusFromExecutionInfo(tenant, info)
		if !ok {
			prefix := string(tenant) + "/"
			if !strings.HasPrefix(info.GetExecution().GetWorkflowId(), prefix) {
				droppedForeign++
			} else {
				droppedStatus++
			}
			continue
		}
		out = append(out, st)
	}
	return out, droppedForeign, droppedStatus
}

func (r *Runtime) logListDrops(foreign, badStatus int) {
	if foreign == 0 && badStatus == 0 {
		return
	}
	logger := r.logger
	if logger == nil {
		logger = slog.Default()
	}
	if foreign > 0 {
		logger.Warn("temporal: dropped foreign workflow ids from list page", slog.Int("count", foreign))
	}
	if badStatus > 0 {
		logger.Warn("temporal: dropped unmappable workflow status from list page", slog.Int("count", badStatus))
	}
}
