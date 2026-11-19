// Package authn implements the authentication modes of ADR-0008.
//
// The none mode only listens on loopback (REQ-AUTH-001). The oidc mode accepts
// JWTs signed by the issuer's published keys, for the configured audience and
// not expired (REQ-AUTH-003); OIDC and JWKS values implement the KeySet
// interface, and a JWKS is refreshed when a token names an unknown key id, so
// issuer key rotation needs no restart. The local mode stores only hashes of
// opaque bearer tokens: NewToken mints a secret once, Token.Verify compares it
// in constant time and enforces its fixed expiry (REQ-AUTH-005).
//
// Every failure is a *core.Error with code unauthenticated and a message that
// never carries the credential, a claim or a verification-key detail.
package authn
