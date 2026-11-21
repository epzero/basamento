// Package authn implements the authentication modes of ADR-0008.
//
// Every mode only listens on loopback until request authentication is wired
// into kit serve (REQ-AUTH-001 for none). The oidc mode accepts
// JWTs signed by the issuer's published keys, for the configured audience and
// not expired (REQ-AUTH-003); OIDC and JWKS values implement the KeySet
// interface, and a JWKS is refreshed when a token names an unknown key id, so
// issuer key rotation needs no restart. The local mode stores only hashes of
// opaque bearer tokens: NewToken mints a secret once, Token.Verify compares it
// in constant time and enforces its fixed expiry (REQ-AUTH-005).
//
// OIDC verification uses cancelled or timeout for caller context errors and JWKS
// fetch deadlines; other verification failures are unauthenticated. Local
// bearer verification is always unauthenticated. No message carries a
// credential, a claim, or a JWKS URL.
package authn
