package core

import "context"

// TenantID identifies the tenant a call acts for: the isolation boundary for
// data and spend. Every layer reads it from the context with TenantFrom.
type TenantID string

// tenantKeyType is unexported so no other package can write the key, and a
// value can only be set through WithTenant.
type tenantKeyType struct{}

var tenantKey tenantKeyType

// WithTenant returns a context that carries id as the tenant. A later call on
// the same context replaces the earlier id; the parent context is unchanged.
// An empty id is stored as is, and TenantFrom reports it as absent.
func WithTenant(ctx context.Context, id TenantID) context.Context {
	return context.WithValue(ctx, tenantKey, id)
}

// TenantFrom returns the tenant carried by ctx, and false when ctx carries
// none or an empty id.
func TenantFrom(ctx context.Context) (TenantID, bool) {
	id, ok := ctx.Value(tenantKey).(TenantID)
	if !ok || id == "" {
		return "", false
	}
	return id, true
}
