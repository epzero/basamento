# ADR-0017 — Keep Vault Transit as its own key service

**Status:** accepted · **Date:** 2026-11-19

## Context

ADR-0004, REQ-CONFIG-012, scenario CONFIG-012-01, the glossary, the architecture
overview, WP-L1-04 and `platform/platform.go` all name **Vault Transit** as a
cloud key service through the `hashivault://` scheme. `gocloud.dev` v0.49.0
(the version in `go.mod`) ships KMS keepers under `secrets/` (`awskms`,
`azurekeyvault`, `gcpkms`, `localsecrets`) but **no Vault Transit driver**.

WP-L1-04's secrets store (`platform/secrets`) opens the key service in `OpenURL`:
`openKeeper` accepts
only `localsecrets`, `awskms` and `gcpkms`; any other scheme, including
`hashivault`, returns `invalid_argument` before the listener is ready. The config
loader still accepts `hashivault://` in the cloud profile (CONFIG-012-01). That
pair is an intentional interim state until a driver exists. Ticket EP0-88 asked
whether to keep or remove the scheme; this ADR keeps it.

## Decision

| Rule | Detail |
| --- | --- |
| **Contract** | `hashivault://` stays in the configuration contract and approved specs. Removing it would be a P1 spec change across REQ-CONFIG-012, CONFIG-012-01, the glossary and ADR-0004, and would drop the only self-hosted cloud key service. |
| **Driver** | Vault Transit is implemented in a **work package of its own after WP-L1-06**, not inside WP-L1-04. It is a small driver of ours: an `gocloud.dev/secrets/driver.Keeper` with `Encrypt`, `Decrypt`, `Close`, `ErrorAs` and `ErrorCode`, talking to Transit over HTTPS with TLS required (`POST /v1/<mount>/encrypt/<key>` and `.../decrypt/<key>`; ciphertext `vault:v<n>:...`). Master-key rotation in the secrets port uses Transit **rewrap** when the driver is active. No `github.com/hashicorp/vault/api`. |
| **Credentials** | The Vault token is a secret. The driver takes it from a **`cred://` reference** resolved through the credential store (WP-L1-06), not from a process environment variable and not embedded in config errors (REQ-CONFIG-007, REQ-CONFIG-008). `specs/secrets/design.md` §1 keeps the credential store out of WP-L1-04; this driver depends on WP-L1-06. |
| **Interim** | Until that work package lands, `hashivault://` remains valid in validation and is refused at secrets **open** with `invalid_argument`; `kit serve` fails start-up and names the secrets port (REQ-CONFIG-020, REQ-CONFIG-021). |
| **Gate** | The gate exercises the driver only against a Transit **simulator** (`net/http/httptest` in a test-only package; `docs/engineering/bdd-convention.md` §1). A `@live` scenario (same pattern as `BLOB-018-02` in `specs/blob/opening.feature`) runs nightly against a real Vault or OpenBao. |

## Alternatives considered

| Alternative | Cost | Why rejected |
| --- | --- | --- |
| **Remove `hashivault://`** | Eleven references on `origin/main` across ADR-0004, overview, roadmap, `platform/platform.go`, `specs/config`, `specs/glossary` and `specs/secrets` (four approved surfaces among them); P1 spec churn and no self-hosted Transit option. | Avoids a driver nobody needs yet, but re-adding a scheme later breaks the additive contract story. |
| **`github.com/hashicorp/vault/api`** | MPL-2.0 (allowed only unmodified per coding guidelines); scratch `go get` raised the module closure from **24 to 51** modules versus this repository's tree today. | Heavy for two REST calls; `make licenses` would need a new pin justification. |
| **Vault only as credential-store backend** | Already allowed for the Credentials port (ADR-0004). | Does not wrap Tink keysets; Transit is still needed as a key service. |
| **Wait for upstream gocloud** | No driver in v0.49.0 module cache; upstream history **not verified** in this session. | Unknown schedule; cloud profile already promises Transit. |

## Consequences

- Cloud operators can target self-hosted Transit once the work package ships; until then they must use KMS keepers or embedded `localsecrets`.
- A new layering grant for the driver package may be needed when code lands; graph changes follow the usual ADR rule.
- Spec work for the driver is **not** this pull request; the designer opens the work package and drafts `specs/secrets` (or a sibling capability) scenarios tagged `@planned`.

## Revert

Revert the merge commit. Remove ADR-0017, its README row, and the cross-references added to ADR-0004, `docs/architecture/overview.md` and `docs/roadmap/L1-platform.md`. The interim open-time refusal in WP-L1-04 is unchanged.

## Implementation changes these files

| Path | Change |
| --- | --- |
| `docs/adr/0017-vault-transit-key-service.md` | This record. |
| `docs/adr/README.md` | Row for ADR-0017. |
| `docs/adr/0004-ports-with-embedded-and-cloud-implementations.md` | Pointer to ADR-0017 for Transit timing and driver ownership. |
| `docs/architecture/overview.md` | Secrets row: interim refusal and post–WP-L1-06 driver. |
| `docs/roadmap/L1-platform.md` | WP-L1-04 note: Transit driver is a separate work package after WP-L1-06. |
