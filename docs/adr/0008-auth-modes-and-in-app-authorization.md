# ADR-0008 — Auth modes none/local/oidc; in-app authorization

**Status:** accepted · **Date:** 2026-11-01

## Decision
- `none`: loopback only. `local`: in-app accounts (argon2id, passkeys, sessions, scoped API tokens). `oidc`: any issuer, with Logto (self-hosted or cloud) as the reference; the server only validates JWTs.
- Authorization is in-app: permissions declared in Go, role bindings per project in SQL, one check in a connect interceptor, optional CEL conditions, audit events for denials. Casbin is not used. Postgres row-level security backs tenancy in the cloud profile.
- External workers get tokens scoped to one session with a fixed expiry.

## Consequences
No identity service in the embedded binary. Logto is optional infrastructure for teams and SaaS.
