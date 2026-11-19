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
| **Driver** | Vault Transit is implemented in a **work package of its own**, after WP-L1-04 merges the secrets store, not inside WP-L1-04. It is a small driver of ours: an `gocloud.dev/secrets/driver.Keeper` with `Encrypt`, `Decrypt`, `Close`, `ErrorAs` and `ErrorCode`, talking to Transit over HTTPS with TLS required (`POST /v1/<mount>/encrypt/<key>` and `.../decrypt/<key>`; ciphertext `vault:v<n>:...`). No `github.com/hashicorp/vault/api`. Schedule is the owner's; nothing blocks the work package once the store is on `main`. |
| **Authentication** | The Vault token is read from a **file** named in the URL query (`token_file=<path>`), written by the operator or a Vault Agent / OpenBao Agent sink. The driver re-reads the file when it changes so a renewed token needs no restart. The token is never in the URL body, in a process environment variable, in the credential store or in logs or errors; the path is not a secret. The driver has **no login flow** (no AppRole, no Kubernetes auth): login is the agent's job. REQ-CONFIG-007 and REQ-CONFIG-008 stay satisfied because the URL carries no secret. Same shape as other cloud key services: KMS uses ambient identity; Vault uses a file the deployment provides. |
| **Dependencies** | The Transit work package depends on **WP-L1-04 only**. A token from `cred://` would require the credential store (envelope-encrypted through Secrets per ADR-0004 and WP-L1-06) to open before the key service can unwrap the credential store's keysets — a bootstrap cycle and a forbidden `platform/secrets` → `platform/credentials` edge against the existing credentials → secrets dependency. |
| **Interim** | Until that work package lands, `hashivault://` remains valid in validation and is refused at secrets **open** with `invalid_argument`; `kit serve` fails start-up and names the secrets port (REQ-CONFIG-020, REQ-CONFIG-021). |
| **Gate** | The gate exercises the driver only against a Transit **simulator** (`net/http/httptest` in a test-only package; `docs/engineering/bdd-convention.md` §1). A `@live` scenario (same pattern as `BLOB-018-02` in `specs/blob/opening.feature`) runs nightly against a real Vault or OpenBao. |

## Alternatives considered

| Alternative | Cost | Why rejected |
| --- | --- | --- |
| **Remove `hashivault://`** | `git grep -i 'hashivault\|vault transit' origin/main` → **11** lines across ADR-0004, overview, roadmap, `platform/platform.go`, `specs/config`, `specs/glossary` and `specs/secrets` (four approved surfaces among them); P1 spec churn and no self-hosted Transit option. | Avoids a driver nobody needs yet, but re-adding a scheme later breaks the additive contract story. |
| **`github.com/hashicorp/vault/api`** | MPL-2.0 (coding guidelines §4: allowed only unmodified); scratch module on 2026-11-19: `go list -m all` **24 → 51** modules versus this repository. | Heavy for two REST calls. |
| **Token in the URL userinfo or query** | A long-lived secret in configuration; renewal needs a config change and restart. | Same leakage rules as REQ-CONFIG-007/008 forbid echoing it; file + agent sink rotates without touching config. |
| **Token from `cred://`** | Requires credential store open before key service (cycle above). | Bootstrap deadlock; layering violation. |
| **Vault only as credential-store backend** | ADR-0004 already allows an external store for the **Credentials** port (vendor API keys). | That port does not protect payload keysets; they stay under Secrets, so a Vault-backed credential store leaves Transit as key service open. |
| **Wait for upstream gocloud** | No driver in v0.49.0 module cache; upstream history **not verified** in this session. | Unknown schedule; cloud profile already promises Transit. |

## Consequences

- Cloud operators can target self-hosted Transit once the work package ships; until then a cloud deployment uses **`awskms` or `gcpkms`** (`localsecrets` is embedded-only per REQ-CONFIG-011).
- **No layering change is expected**: the driver is standard-library `net/http` inside `platform/secrets` (or a subpackage importing only `core`). A vendor client library or an import of `platform/credentials` would need an ADR.
- Spec work for the driver is **not** this pull request; the designer opens the work package and drafts scenarios tagged `@planned`.

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
