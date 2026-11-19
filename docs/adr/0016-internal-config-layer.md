# ADR-0016 — The `internal/config` layer

**Status:** accepted · **Date:** 2026-11-18

## Context

WP-L1-05 (EP0-23, `specs/config`) owns `internal/config` (load, validate, report;
no ports). `go tool gate layering` assigns every package to the longest matching
`layers` key; with no key the gate fails, and graph changes need an ADR
(AGENTS.md). This ADR adds the row and grants.

Until **EP0-78** merges, `specs/config/design.md` §1 is out of date on the broad
`internal` grant, on “no layering line for `internal/config`”, and on “no list
value may name `internal/config`”. **This ADR and `layering.yaml` are binding.**
WP-L1-05 does not start before EP0-78. Following the old text — `internal` on
`cmd/kit`'s list — leaves `gate layering` green while `cmd/kit` imports
`internal/gate`, so the gate binary joins the shipped graph and the two-importer
rule is void.

`cmd/kit` and `internal/bdd` (including `steps`) import `internal/config`; no
other layer without a grant. No durable-execution vendors (ADR-0002) or ports
(ADR-0004) in the config package.

## Decision

### Layer row and imports

| Layer | May import (module packages) | Notes |
| --- | --- | --- |
| `internal/config` | `core` | No `platform`, no `runtime`, no port packages. |

`deny_external` for this layer (ADR-0002; overview §2 and ADR-0004): durable
`go.temporal.io/`; port stacks `gocloud.dev/`, `github.com/jackc/pgx/` (Postgres,
including `v5` subpackages), `modernc.org/` (SQLite), `github.com/nats-io/`,
`github.com/tink-crypto/`, and cloud SDK families `github.com/aws/`,
`cloud.google.com/`, `github.com/Azure/`. Standard library and small parsers
config may add later stay allowed.

### Grants to other layers

Path-segment prefix grants only:

| Layer | Added list entry | Effect |
| --- | --- | --- |
| `cmd/kit` | `internal/config` | This tree only. |
| `internal/bdd` | `internal/config` | Same, including `internal/bdd/steps`. |

**Gate vs review:** `go list` imports in the gate build context (Linux on CI);
files for other platforms are reviewed by hand. `_test.go` is out of scope (as
`test_only`). The allow-list blocks `platform` and `runtime`; `deny_external`
blocks the prefixes above. The gate does not classify port vendors — add a new
vendor prefix in the change that brings the library; an unlisted import can pass
and is a review finding. By policy, untrusted code does not live under `internal/`
(not gate-checked); new `internal/<pkg>` rows and grants need an ADR.

Module map: `cmd/kit` → `internal/config` → `core` (`internal/bdd` not drawn).

## Alternatives considered

- **Broad `internal` on `cmd/kit` and `internal/bdd`** (stale §1 until EP0-78):
  opens `internal/gate` and every future `internal/<pkg>`, bare `internal` module-map
  node; narrow grant keeps `internal/gate` and `tools/gate` closed.
- **List grants without a layer row:** no `deny_external` hook (ADR-0002).

## Consequences

- WP-L1-05 owns `internal/config`. `cmd/kit` imports it for `Load` and `Validate`
  and picks port constructors from `platform` on the result; no constructor in
  the config package.
- `internal/gate` and `tools/gate` do not import configuration.

## Revert

Revert the merge commit. Remove the `internal/config` layer and list entries from
`layering.yaml`, this ADR, its row in `docs/adr/README.md`, the regenerated
module map block in `docs/architecture/overview.md` §3, and the §4 dependency
bullet in `docs/engineering/coding-guidelines.md`. WP-L1-05 does not ship
`internal/config` until a replacement graph lands.

## Implementation changes these files

| Path | Change |
| --- | --- |
| `layering.yaml` | `internal/config` layer, grants, `deny_external` port-vendor prefixes. |
| `docs/architecture/overview.md` | Regenerated module map; §3 wording and package table row. |
| `docs/adr/README.md` | Row for ADR-0016. |
| `docs/engineering/coding-guidelines.md` §4 | Bullet: port vendor prefixes in `deny_external`; allow-list for port packages. |
| `specs/config/design.md` §1 | **EP0-78** (designer, prerequisite of EP0-23): replace §1 with this ADR's graph. |
