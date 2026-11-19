package authn

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"io"
	"slices"
	"time"

	"github.com/epzero/basamento/core"
)

// tokenBytes is the entropy of a bearer secret. 256 bits leave no practical
// chance of collision or guessing.
const tokenBytes = 32

// Token is a local-mode bearer credential as stored: the SHA-256 hash of the
// secret, never the secret itself, plus the tenant, scopes and the fixed expiry
// REQ-AUTH-005 requires of a scoped token.
type Token struct {
	ID      string
	Tenant  core.TenantID
	Scopes  []string
	Expires time.Time
	Hash    []byte
}

// NewToken mints a random bearer secret and returns it with the record to
// store. The secret is returned once; only the hash in Token is persisted.
//
// entropy is the randomness source, crypto/rand.Reader in production. The
// tenant must satisfy REQ-CORE-005 and the expiry is required: neither an
// unscoped nor a non-expiring token may be issued. A caller rotates a token by
// minting a new one and replacing the stored record; ids are never reused.
func NewToken(entropy io.Reader, id string, tenant core.TenantID, scopes []string, expires time.Time) (string, Token, error) {
	if entropy == nil {
		return "", Token{}, core.NewError(core.CodeInvalidArgument, "token entropy source is required", nil)
	}
	if id == "" {
		return "", Token{}, core.NewError(core.CodeInvalidArgument, "token id is required", nil)
	}
	if !validTenant(tenant) {
		return "", Token{}, core.NewError(core.CodeInvalidArgument, "token tenant id is not valid", nil)
	}
	if expires.IsZero() {
		return "", Token{}, core.NewError(core.CodeInvalidArgument, "token expiry is required", nil)
	}

	raw := make([]byte, tokenBytes)
	if _, err := io.ReadFull(entropy, raw); err != nil {
		return "", Token{}, core.NewError(core.CodeInternal, "token could not be generated", nil)
	}
	secret := base64.RawURLEncoding.EncodeToString(raw)
	return secret, Token{
		ID:      id,
		Tenant:  tenant,
		Scopes:  slices.Clone(scopes),
		Expires: expires.UTC(),
		Hash:    Hash(secret),
	}, nil
}

// Hash returns the SHA-256 of a bearer secret, the only form stored. It is
// deterministic so a store can look a token up without holding the secret.
func Hash(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

// Verify returns the principal of a presented secret. The hash comparison runs
// in constant time and an expired token fails, including exactly at its expiry.
// Every rejection is the same unauthenticated error: which check failed is not
// observable.
func (t Token) Verify(secret string, now time.Time) (Principal, error) {
	if len(t.Hash) != sha256.Size || subtle.ConstantTimeCompare(Hash(secret), t.Hash) != 1 {
		return Principal{}, unauthenticated()
	}
	if !now.UTC().Before(t.Expires) {
		return Principal{}, unauthenticated()
	}
	return Principal{ID: t.ID, Tenant: t.Tenant, Scopes: slices.Clone(t.Scopes)}, nil
}
