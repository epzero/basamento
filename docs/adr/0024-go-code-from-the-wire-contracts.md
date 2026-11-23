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

The root module pins `google.golang.org/protobuf` v1.36.12 and
`google.golang.org/grpc` v1.84.0. It has no `connectrpc.com/connect`, no
`protoc-gen-*`, no `buf.gen.yaml`, and no generate or drift step. `make tools`
pins `buf` v1.75.0. CI sets `GATE_STRICT=1` and runs `make tools`.

Probes (2026-11-23, scratch tree with `buf.gen.yaml` and committed
`contracts/gen/go`): eight packages, 6 509 lines; warm `buf generate` ≈0.4 s;
`go build` / `go vet` / `golangci-lint` / `go-licenses` / `gate hygiene` pass;
`go tool gate layering` reports `package not assigned to a layer` for those
eight packages until one layer key `contracts/gen` exists.

## Decision

### D1 — Generator and pins

- **Buf** v1.75.0 (`Makefile` `BUF_VERSION`; not in `go.mod`).
- **`tool` entries:** `google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12`
  (same patch as runtime `require`) and
  `connectrpc.com/connect/cmd/protoc-gen-connect-go@v1.21.1` — exactly two new
  `tool` lines (no bare `connectrpc.com/connect` in `tool`).
- **`require`:** after the first committed `contracts/gen/go`, the implementer
  runs **`go mod tidy` with network** (full tree needs it for test-only
  modules). `go mod tidy` then writes **`require connectrpc.com/connect
  v1.21.1` as a direct requirement** (no `// indirect`); probe after
  `go build ./contracts/...` and `go mod tidy -e`. A bare `go get connect`
  before generated imports exist may leave `// indirect` until tidy.
- **`contracts/buf.gen.yaml`:** `version: v2`, top-level **`clean: true`**
  (deletes output dirs before generate; stale `.pb.go` from removed protos do
  not survive), `managed` with `go_package_prefix:
  github.com/epzero/basamento/contracts/gen/go`, plugins `local: [go, tool,
  protoc-gen-go]` and `local: [go, tool, protoc-gen-connect-go]`, `out: gen/go`,
  `opt: paths=source_relative`.
- **Bumps:** protobuf patch → match `protoc-gen-go`, regenerate; connect → match
  `protoc-gen-connect-go` and `require`, regenerate. A `BUF_VERSION` bump lands
  with regeneration; `contracts-gen` catches stale output if plugin bytes change.

### D2 — Committed output

Generated Go is **committed** under `contracts/gen/go/basamento/<svc>/v1/`
(import path `github.com/epzero/basamento/contracts/gen/go/...`). Never
hand-edited. Any change under `contracts/basamento/**/*.proto` includes the
regenerated `contracts/gen/go/**` in the **same pull request** (`contracts-gen`
enforces it).

Rejected: build-time-only output, top-level `wire/`, `internal/`, a separate
contracts module, partial generation.

### D3 — Drift check in the gate

- **`make generate`** (not a gate check): runs `cd contracts && buf generate`
  **directly** — no `tool` macro. If `buf` is missing, the recipe fails with a
  hint to run `make tools`.
- **G1 — `contracts-gen`:** `$(call tool,buf,$(GATE) contracts-gen)` on the
  `static` prerequisite list (same `tool` macro as `buf`: run, skip locally when
  `buf` is absent, or fail when `GATE_STRICT=1`). The **Makefile is the only
  owner** of skip-versus-strict for this check. **`go tool gate contracts-gen`**
  assumes `buf` is on `PATH`; when invoked by hand without it, exit with
  `buf not found: run make tools` — it does **not** read `GATE_STRICT`.
- **Drift algorithm:** `cd contracts && buf generate --output <tmpdir>` (does
  not touch the committed tree), then recursively compare `<tmpdir>/gen/go` with
  `contracts/gen/go` (differing, missing, extra files). Failure names
  **`make generate`**. If only `.proto` changes, **`go build` may still pass**
  until imports need new symbols; `contracts-gen` fails in `static` anyway.
  Unit tests in `internal/gate` with generator injected (no `buf` in tests):
  clean tree; differing, missing, extra files; generate error. Extend
  `internal/gate/makefile_test.go` to pin that `generate` and `contracts-gen`
  exist and `contracts-gen` is on the `static` list (same style as existing pin
  tests).

`buf breaking` (wire JSON vs base) and `contracts-gen` (committed Go vs protos
and pins) are orthogonal. No `.github/workflows/gate.yml` or `make tools` change.

### D4 — Layering

Layer **`contracts/gen`** with **`[]`** (generated code imports protobuf and
connect only).

**Toolchain `layering.yaml` grants:**

| Layer | Add to allow-list |
| --- | --- |
| `api` | `contracts/gen` |
| `internal/bdd` | `contracts/gen` |

**Added by EP0-227 under this ADR:**

| Layer | Allow-list |
| --- | --- |
| `model/worker/dialout` | `[core, platform, model, contracts/gen]` |

**EP0-227** adds that row and the package tree in its own change; **this ADR is
the authority** — no further ADR. Listing `model` grants every `model/...`
sub-layer by path-segment prefix (`internal/gate/layering.go`), so `cmd/kit`
(already lists `model`) mounts `model/worker/dialout` without a `contracts/gen`
grant.

**`model/worker`** stays wire-free: **no** `contracts/gen` grant; keep
`deny_external` `connectrpc.com/` and `google.golang.org/grpc` on `model/worker`.
**`model/worker/dialout`** is **not** under those denies.

**Weakness (honest):** allow-lists match by prefix, so any layer listing
`model` may import `model/worker/dialout`. The gate enforces **direct** imports
only: domain layers do not import connect/grpc. **Domain layers must not import
adapter layers** (`model/worker/dialout`, wire handlers in `api`) and generated
wire types stay at the edge — the gate does **not** enforce that direction.
Reviewers see it in root **`REVIEW.md`** §Always (Layering bullet), this ADR,
and the **`coding-guidelines.md`** bullet the implementation adds.

**`deny_external`:** add `connectrpc.com/` and `google.golang.org/grpc` on
`core`, `expr`, `flow`, `flow/host`, `prompt`, `model`, `model/node`,
`model/worker`, `tool`, `agent`, `stream`.

**Exact toolchain edits:** `contracts/gen: []`; `api` and `internal/bdd`
grants; `deny_external` keys above.

Probe (2026-11-23): `model/worker` importing connect → denied; same import in
`model/worker/dialout` → `layering: ok`.

### D5 — Domain types stay domain types

Exported APIs of `model`, `model/worker`, `stream`, `agent`, and siblings use
domain types only. Wire shapes live in **`api`**, **`model/worker/dialout`**, and
**`internal/bdd`** conformance steps, with conversion at the boundary. **`cmd/kit`**
wires adapters and does not import generated types.

**`stream/stream.go`:** unchanged in the toolchain WP; replace only when a
follow-up grants `stream` → `contracts/gen`.

### D6 — Review and noise

- **`contracts/**` stays high tier**; regenerated files inherit it.
- **`.gitattributes`:** `contracts/gen/** linguist-generated=true` (GitHub UI).
- **Repository metrics:** extend root **`REVIEW.md`** §Skip and
  **`docs/engineering/code-review.md`** §3 (non-generated lines) to include
  **`contracts/gen/**`**, alongside `go.sum`, mermaid blocks, and
  `spec.lock.yaml` hashes — with note that **`contracts-gen` checks it**. That
  is what keeps finder depth and line-count rules from counting regenerated Go.
- **No `contracts/REVIEW.md`.** Reviewers read `.proto` diffs and trust
  `contracts-gen`.

### D7 — Dependencies and updates

- **Connect** v1.21.1: Apache-2.0; its `go.mod` requires Go **1.26.0** and
  `google.golang.org/protobuf` **v1.36.11** (≤ repository pin v1.36.12; no Go
  bump needed, ADR-0014). If a future connect release raises `go` above the
  repository pin, it waits on a toolchain ADR. `govulncheck ./contracts/...`
  clean on probe tree.
- Co-bump protobuf, connect, or plugins with regeneration in one change.

### D8 — What is generated first

All four contracts in one toolchain change (~6.5k lines).

### D9 — Operations

`make tools` + `make generate` when regenerating; committed gen lets **`go
build`** work without `buf`. **`go build ./...`** still needs the module cache
to hold **`connectrpc.com/connect`** and protobuf, because generated packages
import them — same as every other dependency, with this ADR adding connect. CI
has network; on a machine with `GOPROXY=off` the cache must be filled once by
someone with network (under AGENTS.md the **lead** does that before handing off
the toolchain change; subagent worktrees share the cache). TypeScript clients out
of scope.

### D10 — Work items (ordering)

| Item | Owns | Risk | Proof | Order |
| --- | --- | --- | --- | --- |
| **Toolchain** (this ADR) | `contracts/`, `go.mod`, `layering.yaml` (through D4), `Makefile`, `internal/gate`, `.gitattributes`, `REVIEW.md`, `code-review.md` | High | `contracts-gen` tests; compile; `make gate` | **Before EP0-227** |
| **EP0-227** dial-out transport | `model/worker/dialout`, `cmd/kit` wiring; `.proto` + regen if proto changes | High | MODEL-089-01, MODEL-090-01 | After toolchain |
| **WP-L6-03** | `api` adapters | Medium–high | Scenarios as they land | After toolchain |
| **WP-L9-01** | `internal/bdd` | High | Browser conformance | After toolchain |

No BDD for the toolchain itself.

## Alternatives considered

| Option | Cost | Why not |
| --- | --- | --- |
| Hand-written mirrors | Drift from `.proto` | EP0-227 / conformance need wire types |
| Generate at build, do not commit | Every build needs buf | Breaks `go install`, offline agents |
| Buf BSR remote gen | Account, network | Self-contained pins in `go.mod` |
| grpc-go + `protoc-gen-go-grpc` | grpc in domain; ADR-0012 chose Connect | UIs on connect clients |
| Output under `internal/` | Blocks conformance importers | Conformance and `internal/bdd` import wire types from the main module; an external repo (e.g. browser runner) cannot import `internal/` |
| Separate contracts module | Two gates, tags | ADR-0001 single binary |
| Connect adapters inside `model/worker` | Conflicts with `deny_external` on connect | Needs second ADR for EP0-227 |

## Consequences

- Large proto PRs; mitigated by linguist-generated, §Skip policy, and
  `.proto`-first review.
- ~6.5k generated lines; `connect` dependency.
- `buf` only for `make generate` and G1 `contracts-gen`, not for `go build`.
- **G5 mutation testing** (WP-L0 follow-up, `qa-gate.md`): when enabled, must
  **skip** files with the generated header (record as requirement on that work).

## Revert

Remove generation artifacts, layer row and grants, drift check, `.gitattributes`;
regenerate `overview.md` §3 via `go tool gate docs -write`.

## Implementation changes these files

| Path | Change | Who | Tier |
| --- | --- | --- | --- |
| `contracts/buf.gen.yaml` | D1 (`clean: true`, plugins) | Developer | High |
| `contracts/gen/go/**` | D2, D8 output | Developer | High |
| `go.mod`, `go.sum` | D1 `tool` + tidy (network) | Developer | High |
| `layering.yaml` | D4 toolchain portion; EP0-227 adds `model/worker/dialout` | Developer | High |
| `Makefile` | `generate` (direct buf); `contracts-gen: $(call tool,buf,$(GATE) contracts-gen)` on `static` | Developer | High |
| `internal/gate/contracts_gen.go` (+ tests, `makefile_test` pins) | D3 | Developer | High |
| `tools/gate` | Register `contracts-gen` | Developer | High |
| `.gitattributes` | D6 | Developer | Medium |
| `REVIEW.md` | §Skip: add `contracts/gen/**`; §Always Layering: adapter rule (D4) | Developer | High |
| `docs/engineering/code-review.md` | §3 non-generated lines: add `contracts/gen/**` | Developer | Medium |
| `docs/adr/0024-…` | Status → accepted after owner approve | Architect | High |
| `docs/engineering/qa-gate.md` | G1: `` `gate contracts-gen` `` | Developer | Medium |
| `docs/engineering/coding-guidelines.md` | Generated paths, `make generate`, adapter import rule | Developer | Medium |
| `docs/architecture/overview.md` §3 | `go tool gate docs -write` after layering | Developer | Medium |

EP0-227 and later WPs add `model/worker/dialout` and other adapters; they do not
reopen generation policy.