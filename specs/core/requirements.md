# Core: identifiers and tenant context

Identifiers: **Implemented** (`core/ids.go`). Design: ULIDs with cryptographic
entropy, monotonic within a millisecond in one process; fakes in `core/coretest`
are test-only. Tenant context: **Planned** (`core/tenant.go`), design
[`design.md`](design.md).

### REQ-CORE-001 — Unique across restarts
The id generator shall issue identifiers that are unique across process restarts.

### REQ-CORE-002 — Time ordered
When two identifiers are issued one after the other, the id generator shall issue the later one so that it sorts after the earlier one.

### REQ-CORE-003 — Kind prefix
The id generator shall format each identifier as its kind, an underscore and 26 Crockford base32 characters.

### REQ-CORE-004 — Invalid kinds
If a requested kind is not 1–16 lowercase letters or digits starting with a letter, then the id generator shall reject the request with code `invalid_argument`.

## Tenant context

### REQ-CORE-005 — Tenant id grammar
The tenant context shall treat an id as valid only when it is non-empty, at most 128 bytes, valid UTF-8, free of control characters from U+0000 to U+001F and U+007F, neither "." nor "..", and contains neither "/" nor "\".

### REQ-CORE-006 — Invalid tenant ids are absent
If a context carries no id, or an id that is not a valid tenant id, then the tenant context shall report no tenant.
