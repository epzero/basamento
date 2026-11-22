package authz

// Resource is what a permission check acts on. The caller supplies every field;
// authz never loads domain rows (REQ-AUTH-009).
type Resource struct {
	Project string
	Kind    string
	ID      string
	// Session is set by the per-RPC resolver for every resource a worker token
	// can address. It is not stored, not audited, and not visible to CEL.
	Session string
	Attrs   map[string]any
}

// Binding names a principal, role, optional project scope, and optional condition.
// An empty Project is tenant-wide.
type Binding struct {
	Principal string
	Role      Role
	Project   string
	Condition string
}
