package authz

import "context"

// Bindings reports the roles a principal holds in one project. Callers get
// bindings from the request's tenant, never from the resource name: an
// implementation reads core.TenantFrom(ctx) and returns no roles when the
// context carries no tenant, so a binding of one tenant can never authorize a
// call in another (REQ-AUTH-002, REQ-AUTH-004).
//
// The implementation stores bindings in SQL and returns a fresh slice. An error
// is a failed lookup, not an absence: Check denies the action when it is not
// nil, so a store outage never grants access.
type Bindings interface {
	RolesFor(ctx context.Context, principalID, project string) ([]Role, error)
}
