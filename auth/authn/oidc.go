package authn

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/epzero/basamento/core"
)

// MaxLeeway is the largest clock skew OIDC verification accepts.
const MaxLeeway = 5 * time.Minute

// KeySet resolves the verification key an issuer published. kid names the key
// a token was signed with; an empty kid asks for the set's only key.
type KeySet interface {
	VerificationKey(ctx context.Context, kid string) (any, error)
}

// StaticKeys is a KeySet fixed in configuration, keyed by key id. It is meant
// for an issuer with a long-lived key; a JWKS follows rotation instead.
type StaticKeys map[string]any

// VerificationKey returns the key named kid, or the set's only key when kid is
// empty.
func (s StaticKeys) VerificationKey(_ context.Context, kid string) (any, error) {
	if kid != "" {
		if key, ok := s[kid]; ok {
			return key, nil
		}
		return nil, ErrUnknownKey
	}
	if len(s) == 1 {
		for _, key := range s {
			return key, nil
		}
	}
	return nil, ErrUnknownKey
}

// OIDCOptions configures an OIDC verifier. Issuer, Audience, Keys and Clock are
// required; Leeway is explicit clock skew and should stay small.
type OIDCOptions struct {
	Issuer   string
	Audience string
	Keys     KeySet
	Clock    core.Clock
	Leeway   time.Duration

	// TenantClaim is the claim that names the tenant. It defaults to "tenant".
	// A token without the claim is tenantless; a claim that is not a valid
	// tenant id rejects the token.
	TenantClaim string
}

// OIDC validates JWTs from one issuer: signature against the issuer's keys,
// audience, expiry and, when present, the tenant claim (REQ-AUTH-003).
type OIDC struct {
	tenantClaim string
	keys        KeySet
	parser      *jwt.Parser
}

// NewOIDC returns a verifier for opts. The issuer URL may be http or https; it
// is matched against the token's iss claim and is not fetched. It reports
// invalid_argument for missing or malformed options and never echoes a key.
func NewOIDC(opts OIDCOptions) (*OIDC, error) {
	u, err := url.Parse(opts.Issuer)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, core.NewError(core.CodeInvalidArgument, "auth.oidc.issuer must be an http or https URL", map[string]any{"key": "auth.oidc.issuer"})
	}
	if opts.Audience == "" {
		return nil, core.NewError(core.CodeInvalidArgument, "auth.oidc.audience is required", map[string]any{"key": "auth.oidc.audience"})
	}
	if opts.Keys == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "auth.oidc.keys is required", map[string]any{"key": "auth.oidc.keys"})
	}
	if opts.Clock == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "auth.oidc.clock is required", map[string]any{"key": "auth.oidc.clock"})
	}
	if opts.Leeway < 0 {
		return nil, core.NewError(core.CodeInvalidArgument, "auth.oidc.leeway must not be negative", map[string]any{"key": "auth.oidc.leeway"})
	}
	if opts.Leeway > MaxLeeway {
		return nil, core.NewError(core.CodeInvalidArgument, "auth.oidc.leeway exceeds the maximum", map[string]any{"key": "auth.oidc.leeway"})
	}
	tenantClaim := opts.TenantClaim
	if tenantClaim == "" {
		tenantClaim = "tenant"
	}
	return &OIDC{
		tenantClaim: tenantClaim,
		keys:        opts.Keys,
		parser: jwt.NewParser(
			jwt.WithValidMethods([]string{"RS256", "ES256", "EdDSA"}),
			jwt.WithIssuer(opts.Issuer),
			jwt.WithAudience(opts.Audience),
			jwt.WithExpirationRequired(),
			jwt.WithTimeFunc(opts.Clock.Now),
			jwt.WithLeeway(opts.Leeway),
		),
	}, nil
}

// Authenticate returns the principal of a bearer token. A malformed, tampered,
// wrongly signed, wrongly addressed or expired token is unauthenticated; the
// error never carries the token or a claim, so a caller cannot use it to probe
// which check failed.
func (o *OIDC) Authenticate(ctx context.Context, raw string) (Principal, error) {
	if raw == "" {
		return Principal{}, unauthenticated()
	}
	claims := jwt.MapClaims{}
	token, err := o.parser.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		return o.keys.VerificationKey(ctx, kid)
	})
	if err != nil || token == nil || !token.Valid {
		// The key function runs only after the token parsed; mapping a cancelled
		// or timed-out context to cancelled/timeout reveals nothing the caller
		// does not already know. JWKS fetch failures stay unauthenticated.
		if errors.Is(err, context.Canceled) {
			return Principal{}, core.NewError(core.CodeCancelled, "cancelled", nil)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return Principal{}, core.NewError(core.CodeTimeout, "timeout", nil)
		}
		return Principal{}, unauthenticated()
	}

	subject, _ := claims["sub"].(string)
	if subject == "" {
		return Principal{}, unauthenticated()
	}
	principal := Principal{ID: subject}
	if rawTenant, ok := claims[o.tenantClaim]; ok {
		tenant, ok := rawTenant.(string)
		if !ok || !validTenant(core.TenantID(tenant)) {
			return Principal{}, unauthenticated()
		}
		principal.Tenant = core.TenantID(tenant)
	}
	if scope, ok := claims["scope"].(string); ok {
		principal.Scopes = strings.Fields(scope)
	}
	return principal, nil
}
