package core

import (
	"context"
	"unicode"
	"unicode/utf8"
)

// TenantID identifies the tenant a call acts for: the isolation boundary for
// data and spend. Every layer reads it from the context with TenantFrom, which
// reports no tenant for an id outside the grammar (REQ-CORE-005).
type TenantID string

// tenantKeyType is unexported so no other package can write the key, and a
// value can only be set through WithTenant.
type tenantKeyType struct{}

var tenantKey tenantKeyType

// WithTenant returns a context that carries id as the tenant. A later call on
// the same context replaces the earlier id; the parent context is unchanged.
// Every id is stored as is, valid or invalid: TenantFrom decides validity when
// the context is read, so an invalid id reads as no tenant.
func WithTenant(ctx context.Context, id TenantID) context.Context {
	return context.WithValue(ctx, tenantKey, id)
}

// TenantFrom returns the tenant carried by ctx, and false when ctx is nil,
// carries no id, or carries an id that is not a valid tenant id. Callers that
// must not run without a tenant check the second result instead of comparing
// against "".
//
// A valid id satisfies REQ-CORE-005: non-empty, at most 128 bytes, valid
// UTF-8, free of control characters from U+0000 to U+001F and U+007F, neither
// "." nor "..", free of "/" and "\", and neither begins nor ends with a
// Unicode White_Space character. Dots and whitespace inside a longer id are
// allowed. An invalid id never falls back to the parent context's id: it
// reports no tenant.
func TenantFrom(ctx context.Context) (TenantID, bool) {
	if ctx == nil {
		return "", false
	}
	id, ok := ctx.Value(tenantKey).(TenantID)
	if !ok || !validTenantID(id) {
		return "", false
	}
	return id, true
}

// validTenantID reports whether id satisfies the tenant id grammar of
// REQ-CORE-005. It is the single implementation of the rule; see TenantFrom.
func validTenantID(id TenantID) bool {
	s := string(id)
	if s == "" || len(s) > 128 || !utf8.ValidString(s) {
		return false
	}
	if s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		if r <= 0x1f || r == 0x7f || r == '/' || r == '\\' {
			return false
		}
	}
	first, _ := utf8.DecodeRuneInString(s)
	last, _ := utf8.DecodeLastRuneInString(s)
	return !unicode.IsSpace(first) && !unicode.IsSpace(last)
}
