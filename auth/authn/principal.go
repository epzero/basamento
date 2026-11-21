package authn

import (
	"context"

	"github.com/epzero/basamento/core"
)

// Principal is an authenticated user or service token acting within a tenant
// (specs/glossary.md). ID is never a secret: it is a user id or a token id, the
// value audit events and authorizers receive.
type Principal struct {
	ID     string
	Tenant core.TenantID
	Scopes []string
}

// validTenant reports whether id satisfies the tenant grammar of REQ-CORE-005.
// It reuses the single implementation in core rather than growing a second
// parser; an invalid claim is treated as an unauthenticated token.
func validTenant(id core.TenantID) bool {
	_, ok := core.TenantFrom(core.WithTenant(context.Background(), id))
	return ok
}

func unauthenticated() error {
	return core.NewError(core.CodeUnauthenticated, "token is not valid", nil)
}
