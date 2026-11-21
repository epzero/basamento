package temporal

import (
	"fmt"

	"github.com/epzero/basamento/core"
)

// workflowID forms a tenant-scoped Temporal workflow id. Tenant ids never
// contain "/" (core.TenantFrom grammar), so two tenants cannot collide when
// they use the same run id.
func workflowID(tenant core.TenantID, runID string) string {
	if tenant == "" {
		return runID
	}
	return fmt.Sprintf("%s/%s", tenant, runID)
}
