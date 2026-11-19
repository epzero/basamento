# ADR-0017 — Keep Vault Transit as its own key service

**Status:** accepted · **Date:** 2026-11-19

## Context

ADR-0004, REQ-CONFIG-012, scenario CONFIG-012-01, the glossary, the architecture
overview, WP-L1-04 and `platform/platform.go` all name **Vault Transit** as a
cloud key service through the `hashivault://` scheme. `gocloud.dev` v0.49.0
(the version in `go.mod`) ships KMS keepers under `secrets/` (`awskms`,
`azurekeyvault`, `gcpkms`, `localsecrets`) but **no Vault Transit driver**.

`specs/secrets/design.md` §1 records that the contract lists `hashivault://`
while no driver exists in the pinned gocloud version, so when the secrets store
opens the key service it refuses an unsupported scheme with `invalid_argument`
(REQ-SECRET-019). The configuration loader still accepts `hashivault://` in the
cloud profile (CONFIG-012-01). That pairing is an intentional interim state
until a driver exists. Ticket EP0-88 asked whether to keep or remove the
scheme; this ADR keeps it.

## Decision

| Rule | Detail |
| --- | --- |
| **Contract** | `hashivault://` stays in the configuration contract and approved specs. Removing it would be a P1 spec change across REQ-CONFIG-012, CONFIG-012-01, the glossary and ADR-0004, and would drop the only self-hosted cloud key service. |
| **URL** | `hashivault://<host>[:<port>]/<mount>/<key>?token_file=<path>[&ca_file=<path>]`. The scheme means HTTPS only (no plain-HTTP variant). The path is the Transit mount (it may have several segments) plus the key name as its last segment; `token_file` is required, `ca_file` optional. Configuration validation checks the **scheme only**, as approved (CONFIG-012-01 and `hashivault://vault.example.test` stay valid); missing key, missing `token_file` or unreadable token file are checked at secrets **open** with `invalid_argument`. Today the example passes validation and fails at open for lack of a driver; later it can fail at open for lack of key and token file. |
| **Driver** | Vault Transit is implemented in a **work package of its own**, after WP-L1-04 merges the secrets store, not inside WP-L1-04. It is a small driver of ours: a `gocloud.dev/secrets/driver.Keeper` with `Encrypt`, `Decrypt`, `Close`, `ErrorAs` and `ErrorCode`, talking to Transit over HTTPS (`POST /v1/<mount>/encrypt/<key>` and `.../decrypt/<key>`; ciphertext `vault:v<n>:...`). No `github.com/hashicorp/vault/api`. Schedule is the owner's; nothing blocks the work package once the store is on `main`. |
| **Authentication** | The Vault token is read from the file named by `token_file`, written by the operator or a Vault Agent / OpenBao Agent sink; refresh semantics are for the work package's design. The driver **never sends an empty token**; a token file it cannot read **fails the call** (not "no authentication"). The token is never in the URL, in a process environment variable, in the credential store or in logs or errors; the path is not a secret. **TLS verification is always on**; `ca_file` is an optional PEM bundle **added** to the system roots (private PKI; in the gate the simulator's cert via `ca_file`). No plain-HTTP mode and no skip-verify; Vault without TLS is unsupported. The driver has **no login flow** (no AppRole, no Kubernetes auth): login is the agent's job. REQ-CONFIG-007 and REQ-CONFIG-008 stay satisfied because the URL carries no secret. KMS uses ambient identity; Vault uses a file the deployment provides. |
| **Dependencies** | The Transit work package depends on **WP-L1-04 only**. A token from `cred://` would require the credential store (envelope-encrypted through Secrets per ADR-0004 and WP-L1-06) to open before the key service can unwrap the credential store's keysets — a bootstrap cycle and a forbidden `platform/secrets` → `platform/credentials` edge against the existing credentials → secrets dependency. |
| **Interim** | Until that work package lands, `hashivault://` remains valid in validation and is refused at secrets **open** with `invalid_argument`; `kit serve` fails start-up and names the secrets port (REQ-CONFIG-020, REQ-CONFIG-021). |
| **Gate** | The gate exercises the driver only against a Transit **simulator in `internal/bdd`**, beside the simulated key service of `specs/secrets/design.md` §5 (not a new test-only package). Driver unit tests may use `net/http/httptest` in their own `_test.go` files. The simulator is HTTPS; tests pass its certificate via `ca_file`. No live keeper in the gate (`docs/engineering/qa-gate.md`). A `@live` scenario (same pattern as `BLOB-018-02`) runs nightly against a real Vault or OpenBao with TLS. |

## Alternatives considered

| Alternative | Cost | Why rejected |
| --- | --- | --- |
| **Remove `hashivault://`** | `git grep -i 'hashivault\|vault transit' origin/main` → **11** lines: **7** under `specs/` (config 2, glossary 2, secrets 3) and **4** in docs and code comments (`docs/adr`, overview, roadmap, `platform/platform.go`); P1 spec churn on approved config and glossary among them. | Re-adding a scheme later breaks the additive contract story. |
| **`github.com/hashicorp/vault/api`** | MPL-2.0 (coding guidelines §4: allowed only unmodified); scratch module on 2026-11-19: `go list -m all` **24 → 51** modules versus this repository. | Heavy for two REST calls. |
| **Token in the URL userinfo or query** | A long-lived secret in configuration; renewal needs a config change and restart. | REQ-CONFIG-007/008; file + agent sink rotates without touching config. |
| **Token from `cred://`** | Credential store must open before key service (cycle above). | Bootstrap deadlock; layering violation. |
| **Register `hashivault` only when the driver is linked** | Validation would depend on the binary build. | REQ-CONFIG-012 lists cloud schemes statically. |
| **Separate Go module for the keeper** | Split release graph. | Driver is standard-library only; no dependency saving. |
| **Vault only as credential-store backend** | ADR-0004 allows an external store for the **Credentials** port (vendor API keys). | Payload keysets stay under Secrets; Transit as key service remains open. |
| **Wait for upstream gocloud** | No driver in v0.49.0 module cache; upstream **not verified** here. | Unknown schedule; cloud profile already promises Transit. |

## Consequences

- Cloud operators can target self-hosted Transit once the work package ships; until then a cloud deployment uses **`awskms` or `gcpkms`** (`localsecrets` is embedded-only per REQ-CONFIG-011).
- **No layering change is expected**: the driver is standard-library `net/http` inside `platform/secrets` (or a subpackage importing only `core`). A vendor client library or an import of `platform/credentials` would need an ADR.
- When the designer opens the Transit work package, it amends `specs/secrets/design.md` §1 so the open-decision clause points here and the interim refusal stays, and it writes the driver's design and scenarios.
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
