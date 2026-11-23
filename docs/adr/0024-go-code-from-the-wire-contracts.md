# ADR-0024 — Go code from the wire contracts

**Status:** proposed · **Issue:** EP0-230 · **Date:** 2026-11-23

## Context

`contracts/` holds four `basamento/*/v1` packages behind `buf lint` and
`buf breaking` (`WIRE_JSON`). Nothing generates Go from them today; no Go
package imports a `.proto`. ADR-0012 (amended) fixes dial-out
`basamento.worker.v1.WorkerService` over Connect for EP0-227; `overview.md` §3
names `api` as connect-go services (WP-L6-03); ADR-0009 and WP-L9-01 need Go
clients for `basamento.browser.v1` conformance against an external runner.
`stream/stream.go` is a hand-written mirror of `stream.proto`
(`docs/architecture/streaming.md` §2).

The root module already pins `google.golang.org/protobuf` v1.36.12 and
`google.golang.org/grpc` v1.84.0. It has no `connectrpc.com/connect`, no
`protoc-gen-*`, no `buf.gen.yaml`, and no generate or drift step. `make tools`
pins `buf` v1.75.0. CI sets `GATE_STRICT=1` and runs `make tools`
(`.github/workflows/gate.yml`).

Probes on a copy of `main` with `contracts/buf.gen.yaml`, plugins from `go tool`,
and committed output under `contracts/gen/go` (re-run 2026-11-23): eight packages,
6 509 lines; `worker.pb.go` 2 187 + `worker.connect.go` 384; warm `buf generate`
≈0.4 s; `go build` / `go vet` / `golangci-lint` / `go-licenses` / `go tool gate
hygiene` pass; second generate is byte-identical; `go tool gate layering` fails
until eight `contracts/gen/go/...` rows exist.

## Decision

### D1 — Generator and pins

- **Buf** v1.75.0 (`Makefile` `BUF_VERSION`; bumped only in `Makefile` / `make
  tools`, not in `go.mod`).
- **Plugins** pinned as root `go.mod` **`tool`** entries:
  `google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12` (same patch as the
  runtime `require`) and
  `connectrpc.com/connect/cmd/protoc-gen-connect-go@v1.21.1`.
- **Runtime:** `require connectrpc.com/connect v1.21.1` (generated connect
  stubs import it; adapters may import it directly).
- **`contracts/buf.gen.yaml`:** `version: v2`, `managed.enabled: true` with
  `go_package_prefix: github.com/epzero/basamento/contracts/gen/go`, plugins
  `local: [go, tool, protoc-gen-go]` and `local: [go, tool,
  protoc-gen-connect-go]`, `out: gen/go`, `opt: paths=source_relative`. Protos
  stay unedited; managed mode supplies `go_package`.
- **Bump rule:** protobuf runtime patch → match `protoc-gen-go`, regenerate;
  connect → match `protoc-gen-connect-go` and `connect` require, regenerate; buf
  → `BUF_VERSION` only unless `buf.gen.yaml` schema changes.

### D2 — Committed output

Generated Go is **committed** under `contracts/gen/go/basamento/<svc>/v1/`
(import path `github.com/epzero/basamento/contracts/gen/go/...`). Files carry the
standard generated header and are never hand-edited. No other artifacts in that
tree.

Rejected for this repository: build-time-only output (`go install @latest` and
agents without `buf` break); top-level `wire/` (extra name, no gain);
`internal/` (blocks external conformance importers); a separate tagged contracts
module (release overhead); mixing generated and hand-written Go in one package.

### D3 — Drift check in the gate

- **`make generate`:** `cd contracts && buf generate` via the existing `tool`
  macro (needs `buf` from `make tools` and plugin `tool` entries in `go.mod`).
- **`go tool gate contracts-gen`:** regenerate into a temp directory offline,
  compare with `contracts/gen/go`, fail with a message naming `make generate`.
  Unit-tested in `internal/gate` (table cases: clean tree, stale file, missing
  dir).
- **Wiring:** `contracts-gen` on the `static` / G1 path next to `buf` in the
  `Makefile`. Same skip behaviour as `buf`: missing binary → notice locally,
  `GATE_STRICT=1` → hard fail (CI already installs tools). **No**
  `.github/workflows/gate.yml` or `make tools` change.

`buf breaking` (wire compatibility vs base) and `contracts-gen` (committed Go
matches current protos and plugin pins) are orthogonal.

### D4 — Layering

New layer **`contracts/gen`** with **`[]`** imports (third-party only via
generated code: protobuf, connect).

| Layer | Add to allow-list |
| --- | --- |
| `model/worker` | `contracts/gen` |
| `api` | `contracts/gen` |
| `cmd/kit` | `contracts/gen` |
| `internal/bdd` | `contracts/gen` |

**Not** granted now: `stream` (hand mirror stays), `internal/gate` / `tools/gate`
(drift shells out to buf; no import of generated packages).

**`deny_external`** — add prefix `connectrpc.com/` on:
`core`, `expr`, `flow`, `flow/host`, `prompt`, `model`, `model/node`,
`model/worker`, `tool`, `agent`, `stream` (direct imports only; importing
`contracts/gen` stays allowed). Add `google.golang.org/grpc` on the same layers so
RPC stubs stay in adapters, not domain code (`grpc` today appears only in
`internal/bdd` temporal stubs and tests).

**Exact `layering.yaml` edits:** one new `layers` key `contracts/gen: []`; four
allow-list entries above; two `deny_external` keys per domain layer listed (same
pattern as `go.temporal.io/`).

### D5 — Domain types stay domain types

Exported APIs of `model`, `model/worker`, `stream`, `agent`, and siblings use
domain types only. Generated messages and connect clients appear in transport
adapters (`api`, `cmd/kit` wiring, `internal/bdd` conformance), with conversion
at the boundary.

**`stream/stream.go`:** unchanged in the toolchain work item. Replace it with
generated types only when a follow-up removes the mirror and grants `stream` →
`contracts/gen` (proto or domain change, own review).

### D6 — Review and noise

- **`contracts/**` stays high tier** (`review.yaml`); regenerated files inherit
  that — no new rule.
- **`.gitattributes`:** `contracts/gen/** linguist-generated=true` so hosts collapse
  diffs. Reviewers validate `.proto` edits and a green `contracts-gen`, not
  generated line noise.
- **No `contracts/REVIEW.md`** (`**/REVIEW.md` is high tier; little added value).

### D7 — Dependencies and updates

- **Connect** v1.21.1: Apache-2.0; `govulncheck ./contracts/...` clean on probe
  tree; module graph is essentially connect plus existing protobuf stack.
- **Procedure:** any bump to protobuf, connect, or plugin tools lands with
  regeneration in the same change; stale committed output fails `contracts-gen`.
- **`buf breaking`** guards published wire JSON; **`contracts-gen`** guards Go
  output and pins.

### D8 — What is generated first

**All four contracts** in one change (invoke, stream, worker, browser). Partial
trees complicate layering and drift for no benefit (~6.5k lines, one compile
cost).

### D9 — Operations

- **Regenerate:** `make tools` (once) + `make generate`; needs network only when
  filling the module cache (`make tools` / first `go` download). After that,
  agents with `GOPROXY=off` and a warm cache build and run the gate without
  network; subagents on leads follow the lead-only network rule, but **committed
  gen** lets implementers compile without `buf`.
- **TypeScript clients:** out of scope (ADR-0001). Layout leaves room: protos in
  `contracts/basamento/...`, `buf.gen.yaml` beside `buf.yaml`, output outside the
  proto tree — a later `buf.gen` plugin can target another `out` without revisiting
  Go decisions.

### D10 — Work items (ordering)

| Item | Owns | Risk | Proof | Order |
| --- | --- | --- | --- | --- |
| **Toolchain** (this ADR) | `contracts/`, `go.mod`, `layering.yaml`, `Makefile`, `internal/gate`, `.gitattributes`, docs | High (`contracts/**`, `go.mod`, `layering.yaml`, gate) | `contracts-gen` unit tests; `make gate`; packages compile | **Before** EP0-227 |
| **EP0-227** dial-out worker transport | `model/worker`, `api`, `cmd/kit` adapters | High | BDD MODEL-089-01, MODEL-090-01 | After toolchain |
| **WP-L6-03** connect `api` surface | `api` | Medium–high | Scenarios as they land | Parallel after toolchain |
| **WP-L9-01** browser conformance | `internal/bdd`, `contracts` usage | High | Browser conformance suite | After toolchain; runner external |

No BDD scenarios for the toolchain; behaviour is enforced by `contracts-gen`,
compilation, and downstream transport scenarios.

## Alternatives considered

| Option | Cost | Why not |
| --- | --- | --- |
| Hand-written mirrors (status quo) | Duplication, drift from `.proto` | EP0-227 and conformance need wire-accurate types |
| Generate at build time, do not commit | Every build needs buf + plugins | Breaks `go install`, offline agents, simple CI |
| Buf BSR remote generation | Account, network, third-party availability | Pins already in `go.mod`; repo stays self-contained |
| grpc-go + `protoc-gen-go-grpc` | grpc stack in domain; ADR-0012 chose Connect | ADR-0012; UIs already on connect clients |
| Output under `internal/` | External conformance cannot import | Browser/worker contracts consumed from this module |
| Separate contracts Go module | Tags, version skew, two `go.mod` gates | One binary, one gate (ADR-0001) |
| Worker-only generation | Odd layering / drift with three stale protos | All four are small |

## Consequences

- **Review noise:** large diffs on proto changes; mitigated by linguist-generated
  and reviewer focus on `.proto` + drift.
- **Repository size:** ~6.5k generated lines (stable until protos grow).
- **New dependency:** `connectrpc.com/connect` (and generated code imports).
- **Regenerate-only tool:** `buf` not required for `go build` after commit; only
  for `make generate` and `contracts-gen`.

## Revert

Remove `contracts/gen`, `buf.gen.yaml`, tool pins, `contracts-gen`, layer row and
grants, `.gitattributes` entry, and this ADR; restore docs bullets; regenerate
`overview.md` §3 via `go tool gate docs -write`.

## Implementation changes these files

| Path | Change | Who | Tier |
| --- | --- | --- | --- |
| `contracts/buf.gen.yaml` | As in D1 | Developer | High |
| `contracts/gen/go/**` | Generated output (all four services) | Developer | High |
| `go.mod`, `go.sum` | `tool` plugin pins; `require connect` | Developer | High |
| `layering.yaml` | D4 row, grants, `deny_external` | Developer | High |
| `Makefile` | `generate` target; `contracts-gen` on G1 | Developer | High |
| `internal/gate/contracts_gen.go` (+ tests) | Drift check | Developer | High |
| `tools/gate` | Register `contracts-gen` subcommand | Developer | High |
| `.gitattributes` | `linguist-generated` for `contracts/gen/**` | Developer | Medium |
| `docs/adr/README.md` | Index row | Developer | High |
| `docs/adr/0024-…` | Status → accepted when owner approves | Architect | High |
| `docs/engineering/qa-gate.md` | G1 lists `contracts-gen`, `make generate` | Developer | Medium |
| `docs/engineering/coding-guidelines.md` | Generated-code paragraph (paths, regen) | Developer | Medium |
| `docs/architecture/overview.md` §3 | Regenerated module map (`gate docs -write`) | Developer | Medium |
| `docs/architecture/streaming.md` | No change (D5) | — | — |

EP0-227 and later WPs add adapters that import `contracts/gen`; they do not
change generation policy.