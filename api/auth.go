package api

import (
	"context"
	"net/http"

	"github.com/epzero/basamento/auth/authn"
	"github.com/epzero/basamento/core"
)

// Authenticator turns a request into the principal it acts for. Implementations
// return unauthenticated for a missing or invalid credential; the error's code
// is what the caller receives. The platform's oidc verifier fits behind an
// adapter: Authenticate(ctx, bearer(r)) (authn.Principal, error).
type Authenticator interface {
	Authenticate(ctx context.Context, r *http.Request) (authn.Principal, error)
}

// AuthenticatorFunc adapts a function to Authenticator.
type AuthenticatorFunc func(ctx context.Context, r *http.Request) (authn.Principal, error)

// Authenticate calls f.
func (f AuthenticatorFunc) Authenticate(ctx context.Context, r *http.Request) (authn.Principal, error) {
	return f(ctx, r)
}

// principalKey is the context key of the request's principal. It is unexported
// so only this package can set it.
type principalKey struct{}

// withPrincipal returns a context carrying p.
func withPrincipal(ctx context.Context, p authn.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// principalFrom returns the principal the middleware stored, if any.
func principalFrom(ctx context.Context) (authn.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(authn.Principal)
	return p, ok
}

// authenticate resolves the request's principal and puts its tenant on the
// context. It is the only place a request tenant is set: no handler reads a
// tenant from a header, a query or a body (REQ-AUTH-002).
func (s *Server) authenticate(r *http.Request) (context.Context, authn.Principal, error) {
	ctx := r.Context()
	principal, err := s.opts.Authenticator.Authenticate(ctx, r)
	if err != nil {
		return nil, authn.Principal{}, err
	}
	// An invalid or empty tenant is stored as no tenant, so a principal without
	// one cannot read tenant data even if it reaches a handler.
	ctx = core.WithTenant(ctx, principal.Tenant)
	return withPrincipal(ctx, principal), principal, nil
}

// bearer returns the request's bearer credential, or the empty string.
func bearer(r *http.Request) string {
	const prefix = "Bearer "
	got := r.Header.Get("Authorization")
	if len(got) <= len(prefix) || got[:len(prefix)] != prefix {
		return ""
	}
	return got[len(prefix):]
}
