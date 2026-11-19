# ADR-0017 — Keep Vault Transit as its own key service

**Status:** accepted · **Date:** 2026-11-19

## Context

ADR-0004, REQ-CONFIG-012, scenario CONFIG-012-01, the glossary, the architecture
overview, WP-L1-04 and `platform/platform.go` all name **Vault Transit** as a
cloud key service through the `hashivault://` scheme.

`gocloud.dev/secrets/hashivault` is its own module published in lockstep with
`gocloud.dev` v0.49.0. It registers the `hashivault` scheme on
`secrets.DefaultURLMux()` and its `URLOpener` accepts a custom `Client`; the
Vault token is supplied through the module's `Dial`/`Config.Token` or
`client.SetToken`. It requires `github.com/hashicorp/vault/api v1.23.0`, which is
MPL-2.0 and therefore allowed only unmodified under
`docs/engineering/coding-guidelines.md` §4.

So the owner has three real choices: import the official driver, write a small
stdlib driver, or remove the scheme.

## Decision

We keep `hashivault://` in the contract and implement it as a **hand-written
stdlib driver** in its own work package after WP-L1-04.

| Rule | Detail |
| --- | --- |
| **Contract** | `hashivault://` stays in the configuration contract and approved specs. Removing it would be a P1 spec change across REQ-CONFIG-012, CONFIG-012-01, the glossary and ADR-0004, and would drop the only self-hosted cloud key service. |
| **URL** | `hashivault://<host>[:<port>]/<mount>/<key>?token_file=<path>[&ca_file=<path>]`. The scheme means HTTPS only (no plain-HTTP variant). The path is the Transit mount (it may have several segments) plus the key name as its last segment; `token_file` is required, `ca_file` optional. Configuration validation checks the **scheme only**, as approved (CONFIG-012-01 and `hashivault://vault.example.test` stay valid); missing key, missing `token_file` or unreadable token file are checked at secrets **open** with `invalid_argument`. As specced today the example passes validation and fails at open for lack of a driver; once WP-L1-04 and WP-L1-05 land that spec-level refusal becomes live; once this ADR's driver lands it can fail at open for lack of key and token file. |
| **Driver** | Vault Transit is implemented in a **work package of its own**, after WP-L1-04 merges the secrets store, not inside WP-L1-04. It is a small driver of ours: a `gocloud.dev/secrets/driver.Keeper` with `Encrypt`, `Decrypt`, `Close`, `ErrorAs` and `ErrorCode`, talking to Transit over HTTPS (`POST /v1/<mount>/encrypt/<key>` and `.../decrypt/<key>`; ciphertext `vault:v<n>:...`). It uses the Go standard library only; we do **not** import `gocloud.dev/secrets/hashivault` or `github.com/hashicorp/vault/api`. Schedule is the owner's; nothing blocks the work package once the store is on `main`. |
| **Authentication** | The Vault token is read from the file named by `token_file`, written by the operator or a Vault Agent / OpenBao Agent sink; refresh semantics are for the work package's design. The driver **never sends an empty token**; a token file it cannot read **fails the call** (not "no authentication"). The token is never in the URL, in a process environment variable, in the credential store or in logs or errors; the path is not a secret. **TLS verification is always on**; `ca_file` is an optional PEM bundle **added** to the system roots (private PKI; in the gate the simulator's cert via `ca_file`). No plain-HTTP mode and no skip-verify; Vault without TLS is unsupported. The driver has **no login flow** (no AppRole, no Kubernetes auth): login is the agent's job. REQ-CONFIG-007 and REQ-CONFIG-008 stay satisfied because the URL carries no secret. KMS uses ambient identity; Vault uses a file the deployment provides. |
| **Dependencies** | The Transit work package depends on **WP-L1-04 only**. A token from `cred://` would require the credential store to open before the key service can unwrap the credential store's keysets, because credentials are envelope-encrypted through Secrets (ADR-0004, WP-L1-06); that is a bootstrap cycle. This ADR forbids a `platform/secrets` → `platform/credentials` edge going forward; the driver must read `token_file` directly. |
| **Interim** | Until that work package lands, `hashivault://` remains valid in validation and is refused at secrets **open** with `invalid_argument`; `kit serve` fails start-up and names the secrets port (REQ-CONFIG-020, REQ-CONFIG-021). |
| **Gate** | The gate exercises the driver only against a Transit **simulator in `internal/bdd`**, beside the simulated key service of `specs/secrets/design.md` §5 (not a new test-only package). Driver unit tests may use `net/http/httptest` in their own `_test.go` files. The simulator is HTTPS; tests pass its certificate via `ca_file`. No live keeper in the gate (`docs/engineering/qa-gate.md`). A `@live` scenario (same pattern as `BLOB-018-02`) runs nightly against a real Vault or OpenBao with TLS. |

## Alternatives considered

| Option | Cost | Verdict |
| --- | --- | --- |
| **1. Import `gocloud.dev/secrets/hashivault`** | One Apache-2.0 module. Against the store-as-designed baseline (`gocloud.dev/secrets` + `github.com/google/tink/go v1.7.0`; 195 modules / 412 packages), adding `gocloud.dev/secrets/hashivault` costs +27 modules (+14%) and +32 packages, because Tink already pulls `github.com/hashicorp/vault/api v1.4.1`, so the MPL-2.0 SDK is in the graph either way. Against the narrower baseline of WP-L1-04's `gocloud.dev/secrets` import alone (154 modules / 379 packages), the marginal cost is +37 modules (+24%) / +33 packages. The real binary cost is +32 compiled packages versus 0 for the stdlib driver, plus the MVS bump of `vault/api` to v1.23.0, a custom `URLOpener` still required for `token_file` and `ca_file`, and an official `ErrorCode` that is `gcerrors.Unknown` so it saves no error mapping. | Rejected. The official driver saves only the two REST calls; the Vault SDK dependency is heavy for that, and every MPL-2.0 dependency must remain unmodified and tracked. |
| **2. Hand-written stdlib driver** (recommended) | A small `gocloud.dev/secrets/driver.Keeper` using `net/http`; no new production dependencies; tested against an `internal/bdd` simulator and `httptest`. | Chosen. It avoids the Vault SDK, keeps the binary small, and keeps all authentication behavior explicit in our code. |
| **3. Remove `hashivault://`** | `git grep -i 'hashivault\|vault transit' origin/main` → **11** lines: **7** under `specs/` (config 2, glossary 2, secrets 3) and **4** in docs and code comments (`docs/adr`, overview, roadmap, `platform/platform.go`); P1 spec churn on approved config and glossary among them. ADR-0004 would need amending now, followed by a P1 spec change. | Rejected. Re-adding a scheme later breaks the additive contract story, and Transit is the only self-hosted cloud key service in the current contract. |

The official driver's existence removes the original "no driver" reason for
refusal, but it does not change the dependency cost: the gocloud module is thin
and the Vault SDK it pulls in is not. For two HTTPS POSTs, keeping the
implementation in the standard library is smaller, easier to review, and easier
to simulate in the gate.

## Consequences

- Cloud operators can target self-hosted Transit once the work package ships; until then a cloud deployment uses **`awskms` or `gcpkms`** (`localsecrets` is embedded-only per REQ-CONFIG-011).
- **No layering change is expected**: the driver is standard-library `net/http` inside `platform/secrets` (or a subpackage importing only `core`). A vendor client library would need an ADR. An import of `platform/credentials` from `platform/secrets` is forbidden by this ADR.
- Once this ADR has merged, the designer amends `specs/secrets/design.md` §1 so the clause "whether it stays is an open architecture decision" points here and the interim open-time refusal stays; when it opens the Transit work package it writes the driver's design and scenarios.
- Spec edits are the designer's; this pull request changes architecture docs only.

## Revert

Revert the merge commit. Remove ADR-0017, its README row, and the cross-references added to ADR-0004, `docs/architecture/overview.md` and `docs/roadmap/L1-platform.md`. The interim open-time refusal in WP-L1-04 is unchanged.

## Implementation changes these files

| Path | Change |
| --- | --- |
| `docs/adr/0017-vault-transit-key-service.md` | This record. |
| `docs/adr/README.md` | Row for ADR-0017. |
| `docs/adr/0004-ports-with-embedded-and-cloud-implementations.md` | Pointer to ADR-0017 for Transit driver ownership and authentication. |
| `docs/architecture/overview.md` | Secrets row: interim refusal and Transit driver work package. |
| `docs/roadmap/L1-platform.md` | WP-L1-04 note: Transit driver is a separate work package. |
