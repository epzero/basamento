package authz

// Safe denial reason categories written to audit events (REQ-AUTH-024).
const (
	ReasonNoBinding           = "no-binding"
	ReasonTenantMismatch      = "tenant-mismatch"
	ReasonConditionError      = "condition-error"
	ReasonConditionCostExceed = "condition-cost-exceeded"
	ReasonScopeNarrowed       = "scope-narrowed"
	ReasonUndeclaredAction    = "undeclared-action"
)
