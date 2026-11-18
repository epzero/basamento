# ADR-0016 — The `internal/config` layer

**Status:** accepted · **Date:** 2026-11-18

## Context

WP-L1-05 (`specs/config`) owns `internal/config` (load, validate, report; no ports).
`go tool gate layering` assigns every package to the longest matching `layers` key;
with no key the gate fails, and graph changes need an ADR (AGENTS.md). This ADR
adds the row and grants for WP-L1-05.

`cmd/kit` and the BDD harness (`internal/bdd`, including `steps`) must import
`internal/config`. Nothing else in the product graph should gain that access
without a grant. No durable-execution vendors (ADR-0002) or ports (ADR-0004).

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

List values are path-segment prefixes, not only layer names. Grant the narrow
prefix on the two importers that need configuration:

| Layer | Added list entry | Effect |
| --- | --- | --- |
| `cmd/kit` | `internal/config` | May import this tree only, not arbitrary siblings under `internal/`. |
| `internal/bdd` | `internal/config` | Same, including `internal/bdd/steps`. |

No other layer list changes.

Gate scope for these grants:

- Production imports only (`go list`); `_test.go`, external tests, and build-tagged
  files are out of scope (as for `test_only` in `layering.yaml`).
- `deny_external` here is `go.temporal.io/` only; port vendor libraries are not
  denied (the allow-list keeps `platform` and `runtime` out).
- By policy: untrusted or generated code does not live under `internal/` (not
  gate-checked). Each new `internal/<pkg>` still needs a `layers` row and grants
  need an ADR.

The generated module map is product-facing: `cmd/kit` → `internal/config` → `core`
(`internal/bdd` has a grant but is tooling and is not drawn).

## Alternatives considered

- **Broad `internal` on `cmd/kit` and `internal/bdd`** (`specs/config/design.md` §1):
  least privilege, bad module-map node; does not open `internal/gate` / `tools/gate`.
- **List grants without a layer row:** no `deny_external` hook (ADR-0002).

## Consequences

- WP-L1-05 owns `internal/config`; `cmd/kit` wires port constructors, not the
  config package.
- `internal/gate` and `tools/gate` do not import configuration under this graph.

## Revert

Revert the merge commit. Remove the `internal/config` layer and list entries from
`layering.yaml`, this ADR, its row in `docs/adr/README.md`, and the regenerated
module map block in `docs/architecture/overview.md` §3. WP-L1-05 does not ship
`internal/config` until a replacement graph lands.

## Implementation changes these files

| Path | Change |
| --- | --- |
| `layering.yaml` | `internal/config` layer, grants on `cmd/kit` and `internal/bdd`, `deny_external` line. |
| `docs/architecture/overview.md` | Regenerated module map; §3 wording and package table row for `internal/config`. |
| `docs/adr/README.md` | Row for ADR-0016. |
| `specs/config/design.md` §1 | Designer: replace prerequisite §1 with this ADR (layer row, narrow grants, path-prefix list values, who may import); spec PR before WP-L1-05 implements. |
