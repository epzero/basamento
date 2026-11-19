// Package auth provides authentication modes and in-app authorization.
//
// Modes (ADR-0008): none (loopback only), local (argon2id passwords, passkeys,
// sessions, scoped API tokens) and oidc (Logto or any OIDC provider; JWT
// validation only). Authorization is in-app RBAC with optional CEL conditions;
// repositories require a tenant context; Postgres row-level security adds a
// second line in SaaS.
//
// Status: the none and oidc modes and the local bearer-token records are
// implemented in auth/authn (WP-L8-01); role- and scope-based, tenant-scoped
// permission checks with an audit event on every denial are implemented in
// auth/authz (WP-L8-02). Local accounts, the role-binding store, request wiring
// in the API, CEL conditions and row-level security are planned.
package auth
