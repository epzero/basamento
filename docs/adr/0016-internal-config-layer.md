# ADR-0016 — The `internal/config` layer

**Status:** accepted · **Date:** 2026-11-18

## Context

WP-L1-05 (`specs/config`) introduces `internal/config`: load, validate and
report kit configuration with no port implementations. `go tool gate layering`
assigns every module package to the longest `layers` key that prefix-matches its
path; a package with no key fails the gate. The graph has no row for
`internal/config` today, so the implementer cannot add the directory until the
graph changes. AGENTS.md requires an ADR for that (`layering.yaml`).

`cmd/kit` and the BDD harness (`internal/bdd`, including `steps`) must import
`internal/config`. Nothing else in the product graph should gain that access
without an explicit grant. Configuration must stay free of durable-execution
vendors (ADR-0002) and of port implementations (ADR-0004):
`specs/config/design.md`.

## Decision

### Layer row and imports

| Layer | May import (module packages) | Notes |
| --- | --- | --- |
| `internal/config` | `core` | No `platform`, no `runtime`, no port packages. |

`deny_external` gains a line for this layer (ADR-0002: rules are per layer key):

| Layer | Denied import prefixes |
| --- | --- |
| `internal/config` | `go.temporal.io/` |

### Grants to other layers

List values are path-segment prefixes (`matchLayer` on the importer's list), not
only names of other layer keys. Grant the narrow prefix on the two importers that
need configuration:

| Layer | Added list entry | Effect |
| --- | --- | --- |
| `cmd/kit` | `internal/config` | May import this tree only, not arbitrary siblings under `internal/`. |
| `internal/bdd` | `internal/config` | Same, including `internal/bdd/steps`. |

No other layer list changes.

### Module map and policy

`internal/config` is product code; the generated map shows it with edges from
`cmd/kit` (and `core`). Tooling layers stay hidden (`devLayers`). A future
`internal/<pkg>` needs its own `layers` row before code lands; an import grant
needs an ADR. Untrusted or generated code does not live under `internal/`.

## Alternatives considered

- **Broad `internal` on `cmd/kit` and `internal/bdd`** (`specs/config/design.md` §1).
  Rejected: least privilege, misleading module-map node, and it still leaves
  `internal/gate` / `tools/gate` unable to import config (gate experiment).
- **List grants only, no `internal/config` layer key.** Rejected: `deny_external`
  and the package allow-list need the layer row (ADR-0002).

## Consequences

- WP-L1-05 can add `internal/config` after this change merges; it still depends
  on platform constructors only from `cmd/kit`.
- `internal/gate` and `tools/gate` cannot import configuration (narrow graph).
- The module map shows `cmd/kit` → `internal/config` → `core`.
- A later package such as `internal/foo` remains blocked from `cmd/kit` until an
  ADR adds either a layer row and a deliberate grant, or a broader prefix (not
  chosen here).

## Revert

Revert the merge commit. Remove the `internal/config` layer and list entries from
`layering.yaml`, the ADR, and the regenerated module map block in
`docs/architecture/overview.md` §3. WP-L1-05 must not ship `internal/config`
until a replacement graph lands.

## Implementation changes these files

- `layering.yaml` — `internal/config` layer, grants on `cmd/kit` and
  `internal/bdd`, `deny_external` line.
- `docs/architecture/overview.md` — regenerated module map; §3 wording and package
  table row for `internal/config`.
- `docs/adr/README.md` — row for ADR-0016.
