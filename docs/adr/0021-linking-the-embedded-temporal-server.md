# ADR-0021 — Linking the embedded Temporal server into `kit`

**Status:** accepted · **Date:** 2026-11-22

## Context

WP-L5-03 must run Temporal server v1.32.2 in-process in the single `kit`
binary (`ADR-0001`, `specs/constitution.md` principle 1, REQ-RT-003). ADR-0002
keeps the server in a nested module so product libraries do not import it. A
root `require` on that nested module with only a path `replace` does **not**
produce a compiling graph: the server and the root module disagree on the
OpenTelemetry versions (below).

`go.temporal.io/server` v1.32.2 (and the newest pre-release on the public
proxy) builds against `go.opentelemetry.io/otel/log` **v0.20.0** and
`go.opentelemetry.io/otel` **v1.45.0**. The root module already requires
**v1.47.0** of the OpenTelemetry family (directly and through `gocloud.dev`
v0.49.0, Google contrib modules, and `gax-go/v2`). A unified build fails with
`undefined: log.KeyValue` because `otel/log` v1.x removed that API. One binary
requires one module graph.

A consumer that `require`s only the root module does not compile or download
`go.temporal.io/server` packages (`go list -deps` lists none) and, when it imports
only `runtime`, resolves `go.opentelemetry.io/otel` **v1.47.0** because a
consumer never sees the root module's `replace` lines. Measured **2026-11-22**
with path `replace` on the root (stand-in for a published tag), using
`go list -m -e all`: imports **only** `runtime` — **185** modules before the
nested `require`, **282** after (**+97**); imports `runtime/temporal`,
`platform/blob` and `platform/sql` — **384** before, **450** after (**+66**).
The listing grows by roughly 40–50%; nothing from the server tree is compiled.
ADR-0002 stated that library users never inherit the server's dependencies;
that remains true for **build and binary** isolation and is **false for the
module graph** listing.

## Decision

### Imports

- `cmd/kit` imports `github.com/epzero/basamento/runtime/temporal/embedded`.
- Only `cmd/kit` and, if a test helper needs it, `internal/bdd` may import that
  module. Never `runtime`, `flow`, `agent`, `model`, or product libraries.
- `go tool gate layering` already allows the import (layer prefix `runtime`).

### Root `go.mod`

Add:

```text
require github.com/epzero/basamento/runtime/temporal/embedded v0.1.0

replace github.com/epzero/basamento/runtime/temporal/embedded => ./runtime/temporal/embedded

// Temporal server v1.32.x (nested module) does not compile against otel/log v1.x
// used by the root graph (gocloud.dev v0.49.0 and friends). Cap the OpenTelemetry
// family until a server release builds against otel/log v1.x; then delete these
// replace lines and re-tidy.
replace go.opentelemetry.io/otel => go.opentelemetry.io/otel v1.45.0
replace go.opentelemetry.io/otel/log => go.opentelemetry.io/otel/log v0.20.0
replace go.opentelemetry.io/otel/metric => go.opentelemetry.io/otel/metric v1.45.0
replace go.opentelemetry.io/otel/trace => go.opentelemetry.io/otel/trace v1.45.0
replace go.opentelemetry.io/otel/sdk => go.opentelemetry.io/otel/sdk v1.45.0
replace go.opentelemetry.io/otel/sdk/metric => go.opentelemetry.io/otel/sdk/metric v1.45.0
```

**Apply (implementation pull request):**

1. Owner pushes tag `runtime/temporal/embedded/v0.1.0` on `main` (prerequisite
   below).
2. Add exactly the lines above to the root `go.mod`.
3. Run `go mod tidy` (needs network once; the lines alone do not produce the
   final `go.mod` and `go.sum`).
4. Expect the module growth in **Measured link cost** and the version moves in
   **Shared dependency versions** below.
5. `go build ./...`, `go vet ./...` and unit tests pass on the capped graph,
   then `make gate` (Postgres BDD scenarios exercise the `pgx` bump).

A `replace M => M vX` caps every version of `M` at `vX`; root `require` lines
keep the floors the Go CDK and Google libraries declare. The six capped modules
keep their `require` floors but the build uses the capped versions
(`go list -m` prints `v1.47.0 => v1.45.0`).

### Shared dependency versions

Measured **2026-11-22** after `go mod tidy` on a capped graph: the only **direct**
dependency that changes is `github.com/jackc/pgx/v5` **v5.9.2 → v5.10.0** (the
server tree needs it). **14** shared modules rise in total: `pgx`, `dustin/go-humanize`,
`golang-jwt/jwt/v4`, `golang/snappy`, `konsorten/go-windows-terminal-sequences`,
`mailru/easyjson`, `mattn/go-colorable`, `modern-go/reflect2`, `ncruces/go-strftime`,
`onsi/gomega`, `prometheus/procfs`, `sirupsen/logrus`, `go.uber.org/mock`,
`gopkg.in/yaml.v2`. **One** experimental indirect module falls:
`go.opentelemetry.io/otel/metric/x` **v0.69.0 → v0.67.0**.

**Guard:** `CGO_ENABLED=0 go build ./cmd/kit` inside `make gate`'s `build` **fails
when a cap is removed or weakened** while the server still needs it. Raising a
`require` above a cap has **no effect** while the cap stands, so nothing flags a
cap that has become unnecessary. The caps bind **only the root module's own
builds**; consumers ignore them (see Context). **Exit condition:** in a scratch
copy, delete the six OpenTelemetry `replace` lines and run
`go build ./cmd/kit`; when it succeeds, the caps can go (re-run whenever the
pinned server version changes). When blocked on a dependency bump, either wait
for a Temporal server release that compiles against `otel/log` v1.x or keep the
caps.

### Nested module tag (owner prerequisite)

The root `require` names `v0.1.0`, so the tag
`runtime/temporal/embedded/v0.1.0` must exist on `main` before the
implementation pull request merges (it makes the nested `go.mod` resolvable for
tools that read a consumer's module graph; consumers never build the package).
The owner pushes the tag; tags are never
moved or deleted. **Ordering:** tag the nested module, then bump the root
`require`, then tag the root module release. Later nested releases: tag only when
the nested public API changes and bump the root `require` in the same commit.

Checkouts use the root `replace => ./runtime/temporal/embedded`. There is no
`go.work` in CI (`ADR-0014`).

### `go install`

`go install github.com/epzero/basamento/cmd/kit@version` is **not** supported:
a root `replace` is ignored by remote installs. Neither README nor ADR-0001
promises `go install`. Release binaries are built from a checkout with
`CGO_ENABLED=0 go build -o kit ./cmd/kit`.

### Version parity

Today the root is newer than the nested module on many shared requirements.
The implementation aligns the nested `go.mod` to the root's versions for every
shared module **except** the pinned OpenTelemetry family (`go get` in the nested
module, then `go vet`, `go build` and `bench-cli crash` there). Record results
in the pull request. An automated `gate gomod` parity check is a separate
follow-up (high-risk `tools/gate`; owner decides).

`modernc.org/sqlite` resolves to **v1.57.0** (root) in the unified build, not
v1.51.0 in the nested module alone. `BENCHMARKS.md` was measured at v1.51.0;
re-run **crash and replay** checks at the aligned pin (not the full benchmark
matrix).

### Measured link cost

Measured **2026-11-22** on darwin/arm64, Go 1.26.11 (`CGO_ENABLED=0`,
`go build -o kit ./cmd/kit`). **MB = 10⁶ bytes.** CI numbers will differ.

| Metric | Before link | After link (capped graph) |
| --- | --- | --- |
| `kit` binary (unstripped) | 106 MB | 196 MB |
| `kit` binary (`-ldflags='-s -w'`, as in `BENCHMARKS.md`) | 72 MB | 134 MB |
| `go build ./cmd/kit` (warm cache) | ~2.3 s | ~20–70 s wall |
| `go build ./cmd/kit` (cold) | — | ~4 CPU-minutes |
| Root `go.mod` lines | 199 | 310 (+111, 96 `// indirect`) |
| Root `go.sum` lines | 737 | 1073 (+336) |
| `go list -m all` modules | 426 | 563 (+137) |
| `go list -deps ./cmd/kit` packages | 1127 | 1729 |

ADR-0002 and `BENCHMARKS.md` **116 MB** is the stripped **benchmark** binary
(`runtime/temporal/embedded/bench`), not `kit`. A stripped linked `kit` is
**134 MB** under the same strip flags.

### Single instance (see also `docs/architecture/runtime.md` D17)

WAL SQLite does **not** stop a second in-process server on the same file. On
Unix the embedded module takes an exclusive non-blocking `flock` on
`<DBFile>.lock` before open (`platform/sql/migrate_lock_unix.go` discipline). On
non-Unix builds `Start` returns `ErrUnsupportedPlatform` (see
`docs/architecture/runtime.md` D17); it does not start without a cross-process
lock. CI runs on Linux (`ubuntu-latest`); developers also build on macOS;
nothing in the specs promises Windows.

### Upgrade test

**Default (A), confirmed by approving this ADR:** the nested module carries a
golden database file written by the previous server pin plus a test that opens
it with the current pin and completes a workflow; refresh the file at every pin
bump (`-update` and a reason in the commit body, `AGENTS.md` golden-file rule);
file size is **to be measured in the implementation pull request** (expected
about one megabyte).

**Alternative (B):** documented manual backup only, no automated test; a pin
bump can lose user runs with no test signal.

## Alternatives considered

| Option | Why rejected |
| --- | --- |
| Child process or helper binary | Violates constitution principle 1, ADR-0001 in-process embedded profile, and REQ-RT-003 "in-process". |
| Downgrade `gocloud.dev` to v0.46.0 instead of capping OpenTelemetry | `platform/blob` tests stop compiling (`driver.DeleteOptions` is v0.47+); freezes blob adapters. |
| Fork or patch `go.temporal.io/server` | ~5 packages use the old log API; ongoing fork maintenance. |
| Wait for upstream | Newest server pre-release still needs `otel/log` v0.20.0 and Go 1.27.4. |
| Nested `cmd/kit` module (or thin `main` in the nested module) | Keeps root `go.mod` listings closer to ADR-0002's wording, but refactor `cmd/kit`, retarget `make build`, BDD `buildKitBinary`, layering, and `module-roots`. The OpenTelemetry caps would sit in the nested `go.mod`, so the root module's tests would run against versions that do not ship; caps in the **root** `go.mod` make tests and the shipped `kit` use one graph — that is why caps stay on the root. **Owner may still prefer nested `cmd/kit`**; cost is a multi-PR layout change. |

## Decisions confirmed by the owner's approval

Each line states the default this ADR sets; the alternative and its cost.

- **OpenTelemetry caps in the root `go.mod`** — alternative: nested `cmd/kit`
  module (multi-PR layout; caps in the nested `go.mod`, root tests diverge from
  what ships).
- **Tag `runtime/temporal/embedded/v0.1.0` before implementation merges** —
  alternative: delay implementation until tagged.
- **Non-Unix refusal** (`ErrUnsupportedPlatform`, D17) — alternative: unlock
  start like SQL migrations on non-Unix (unacceptable data hazard).
- **Upgrade test A** (golden DB file in the nested module) — alternative B:
  manual backup only (pin bumps undetected).
- **`RT-003-01` on the PR gate** with **>3 min** gate growth → `@slow` (D23) —
  alternative: `@slow` from day one.
- **Embedded loopback trust boundary (D14)** — accepted risk: any local process
  can reach loopback Temporal APIs; payloads are encrypted per tenant, but a
  hostile local user can cancel or terminate runs; multi-user hosts need cloud
  profile or OS-level isolation.

## Consequences

- The root module's tests and the shipped `kit` binary use the same capped
  dependency graph.
- Library packages stay free of server imports; `kit` pays compile, link and
  binary size cost.
- Downstream modules that `require` the root see the server's module list in
  `go list -m all` even when they import only `runtime` (ADR-0002 amended); none
  of it is compiled and consumers do not see the caps.
- The `pgx` v5.10.0 bump touches the cloud profile's Postgres adapter; the
  implementation pull request must run full `make gate`, not only unit tests.
- OpenTelemetry pins are operational debt until Temporal server catches up; a
  removed or weakened cap fails `go build ./cmd/kit` while the server still needs
  it.
- `make gate` `build` (`go build ./...`) already compiles `cmd/kit` once the
  import lands; no extra gate target is required for the link.
- Follow-ups: root `make licenses` ignore for
  `github.com/cactus/go-statsd-client/statsd` (as `embedded.yml`); widen
  `embedded.yml` path filters to `go.mod`, `go.sum`, `cmd/kit/**`; nested
  parity gate (owner); harness steps and `linear` placeholder for RT-003-01;
  `WorkflowTaskTimeout` option on `temporal.Options` in a later work package.