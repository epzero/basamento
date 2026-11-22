package temporal

import (
	"strings"

	"github.com/epzero/basamento/core"
)

// controlWorkflowID validates runID and forms the tenant-scoped workflow id
// passed to the SDK. No control call may hand an empty workflow id to the client.
func controlWorkflowID(tenant core.TenantID, runID string) (string, error) {
	if err := validateRunID(runID); err != nil {
		return "", err
	}
	id := workflowID(tenant, runID)
	if id == "" {
		return "", core.NewError(core.CodeInvalidArgument, "temporal: invalid run id", nil)
	}
	return id, nil
}

// workflowID forms a tenant-scoped Temporal workflow id. The first "/" is the
// tenant/run separator, so it returns "" when the tenant is invalid or the run
// id already contains "/": such an id cannot be split back into its parts.
func workflowID(tenant core.TenantID, runID string) string {
	if strings.ContainsRune(runID, '/') {
		return ""
	}
	if tenant == "" {
		return runID
	}
	id := checkedTenant(tenant)
	if id == "" {
		return ""
	}
	return string(id) + "/" + runID
}

// runIDFromWorkflowID recovers a run id from a scheduling workflow id. A child
// and a tenant-scoped top-level run use "<tenant>/<run id>"; a top-level run
// with a bare workflow id is returned unchanged.
func runIDFromWorkflowID(tenant core.TenantID, wfID string) string {
	if tenant != "" {
		if rest, ok := strings.CutPrefix(wfID, string(tenant)+"/"); ok {
			return rest
		}
	}
	return wfID
}
