# Core: identifiers

Status: **Implemented** (`core/ids.go`). Design: ULIDs with cryptographic entropy,
monotonic within a millisecond in one process; fakes in `core/coretest` are test-only.

### REQ-CORE-001 — Unique across restarts
The id generator shall issue identifiers that are unique across process restarts.

### REQ-CORE-002 — Time ordered
When two identifiers are issued one after the other, the id generator shall issue the later one so that it sorts after the earlier one.

### REQ-CORE-003 — Kind prefix
The id generator shall format each identifier as its kind, an underscore and 26 Crockford base32 characters.

### REQ-CORE-004 — Invalid kinds
If a requested kind is not 1–16 lowercase letters or digits starting with a letter, then the id generator shall reject the request with code `invalid_argument`.
