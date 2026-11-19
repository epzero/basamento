# Basamento

A Go foundation for durable agent and flow orchestration. Flows are data,
checked with CEL and run on a runtime-neutral durable contract. Models are
addressed by capability (chat, embeddings, image, video, audio) and reached
through generic HTTP API and CLI providers, in process or on workers that
declare what they serve. Runs outlive client connections. It ships as **one
pure-Go binary** that embeds everything for self-hosting, and swaps to cloud
services for SaaS.

> Status: **foundation stage.** Contracts, conventions, specs and the gate are in
> place; `core` identifiers are implemented. Everything else is specified and
> planned in [`docs/roadmap/`](docs/roadmap/README.md).

## How this repository works

Coding agents build Basamento, guided by executable specs:

1. Requirements (EARS) and scenarios (Gherkin) live in [`specs/`](specs/).
2. A reviewer agent approves them and assigns a priority tier. People review
   later, P0 first. Approved text is locked in each capability's `spec.lock.yaml`.
3. Implementers turn `@planned` scenarios green. `make gate` is the single
   definition of done.

Start with [`AGENTS.md`](AGENTS.md), then [`specs/constitution.md`](specs/constitution.md)
and [`docs/architecture/overview.md`](docs/architecture/overview.md).

## Requirements

- Go 1.26.11 or newer (repository minimum pin, ADR-0014, including the `net/http` advisory fix). `make gate` sets `GOTOOLCHAIN` to that patch from the root `go.mod` unless you already exported it; with an older toolchain, Go may still download the required patch.
- Optional locally (required in CI): `golangci-lint` v2, `buf`, `govulncheck`, `go-licenses`; `make tools` installs them.

## Commands

```bash
make gate                          # the full PR gate (skips missing optional tools locally)
make bdd                           # run scenarios
go tool gate wp               # every work package: done, in-progress, ready or blocked
go tool gate spec queue       # specs awaiting human review, P0 first
go tool gate spec trace       # requirement → scenario matrix
go tool gate nightly          # scheduled nightly ok, failed or stale (gh JSON on stdin)
```

## Layout

| Path | Contents |
| --- | --- |
| `core/` | ids, clock, errors (implemented); `core/coretest` test-only fakes |
| `platform/`, `expr/`, `model/`, `prompt/`, `flow/`, `runtime/`, `stream/`, `agent/`, `tool/`, `auth/`, `api/` | Ports and contracts per layer (see `layering.yaml`) |
| `contracts/` | Protobuf contracts (stream, invocation, browser runner), linted with buf |
| `cmd/kit` | The product binary (skeleton) |
| `tools/gate`, `internal/gate` | The QA gate tool |
| `internal/bdd` | godog suite and step library |
| `specs/` | Constitution, glossary, requirements, scenarios, approval ledgers |
| `docs/` | Architecture, ADRs, engineering conventions, roadmap |

## License

Apache License 2.0; see [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE).
