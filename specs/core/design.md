# Core: tenant context

Requirements: [`requirements.md`](requirements.md). The code is `core/tenant.go`.

## 1. Shape

`core.TenantID` is the isolation boundary for data and spend. A tenant travels
in the context: `core.WithTenant(ctx, id)` sets it, and `core.TenantFrom(ctx)`
reads it.

- `WithTenant` keeps its signature `(context.Context, TenantID) context.Context`.
  It stores every id it is given, valid or invalid, and returns no error. A
  later call replaces the earlier id; the parent context is unchanged.
- `TenantFrom` returns `(TenantID, bool)`. It reports no tenant — an empty id
  and `false` — when the context is nil, carries no id, or carries an id that
  is not valid; anything outside the grammar is treated as no tenant, so port
  calls on that context return `invalid_argument`.
- `TenantFrom` is the single choke point for the grammar. Validity is decided
  when a context is read, not when it is built, so no caller can carry an
  invalid id past it.

`WithTenant` returns no error because every port already rejects a call with no
tenant, with code `invalid_argument` (`REQ-SQL-010`, `REQ-BLOB-011`,
`REQ-SECRET-022`, `REQ-RT-040`). Reading the tenant is the only place that has
to enforce the grammar; an error return would change every call site and split
the rule in two. A context built with an invalid id is simply tenantless, and
the port's existing rule does the rest.

## 2. Valid tenant ids

An id is valid when it is non-empty and at most 128 bytes, valid UTF-8, free of
control characters from U+0000 to U+001F and U+007F, neither `.` nor `..`, and
contains neither `/` nor `\`, and neither begins nor ends with a whitespace
character (Unicode `White_Space`) (`REQ-CORE-005`).
Dots inside a longer id, such as `acme.eu`, are allowed; only the two whole-id
values are refused. The edge rule keeps two ids that look the same, such as
" acme" and "acme", from naming two different tenants.

The grammar keeps a tenant id a single opaque segment. Capabilities place it in
positions that are not escaped: blob objects live at `<tenant id>/<content
key>`, and the payload codec's associated data concatenates
`basamento.payload.v1`, a zero byte, the tenant id, a zero byte and the key id.
A `/` or `\` would leave the segment, `.` and `..` would leave the namespace,
control characters and invalid UTF-8 would corrupt keys, logs and headers, and
the 128-byte bound keeps the id compact enough for those positions.

Capabilities rely on this rule instead of checking tenant ids themselves. Any
local tenant-id checks they still have, in path construction or framing, are
removed as follow-up implementation work; the rule is defined once here so they
can rely on it.
