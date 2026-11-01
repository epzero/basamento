// Package auth provides authentication modes and in-app authorization.
//
// Modes (ADR-0008): none (loopback only), local (argon2id passwords, passkeys,
// sessions, scoped API tokens) and oidc (Logto or any OIDC provider; JWT
// validation only). Authorization is in-app RBAC with optional CEL conditions;
// repositories require a tenant context; Postgres row-level security adds a
// second line in SaaS.
//
// Status: planned (WP-L8-*).
package auth
