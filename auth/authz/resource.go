package authz

// Resource is what a permission check acts on. The caller supplies every field;
// authz never loads domain rows (REQ-AUTH-009).
type Resource struct {
	Project string
	Kind    string
	ID      string
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
