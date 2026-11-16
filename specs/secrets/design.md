# Secrets: design

Decisions the implementer of WP-L1-04 follows. Package: `platform/secrets`
(it may import `core` and `platform` only, `layering.yaml`).

## 1. Parts

| Part | What it is |
| --- | --- |
| Key service | Holds the master key and wraps and unwraps with it. Opened from a URL through `gocloud.dev/secrets`: `localsecrets://<base64 32-byte key>` (embedded; mapped onto gocloud's `localsecrets` keeper, whose own scheme is `base64key`), `awskms://`, `gcpkms://`, `hashivault://` (Vault Transit). Any other scheme is refused (REQ-SECRET-019). |
| Secrets port | Implements `platform.Secrets` (`Encrypt`/`Decrypt` of small values) over the key service, and owns tenants' keysets: creation, wrapping, rotation, rewrap. |
| Keyset store | Port declared by `platform/secrets` that persists one wrapped keyset record per tenant with compare-and-swap. This WP ships a file-backed store for the embedded profile; a SQL store can follow behind the same port without a spec change. |
| Payload codec | Encodes and decodes the runtimes' payloads with the tenant's keyset. The tenant comes from the context (`ctx`), as for every repository call. |

Out of scope: the credential store and `cred://` (WP-L1-06), moving large
payloads to blob storage (the runtimes, `docs/architecture/runtime.md`), and
wiring the codec into Temporal's data converter (WP-L5-*), which must use a
context-aware converter so the codec sees the tenant.

## 2. Keysets

- One Tink keyset per tenant, created on the tenant's first encode, holding
  AEAD keys of template `AES256_GCM` (Tink). One key is primary; earlier ones
  stay enabled for decryption. No key is deleted by this WP.
- Stored record: tenant id, the keyset encrypted by the key service (Tink
  `keyset.Handle` written with the key service as its AEAD), the identity of
  the master key that wrapped it (the key service URL without its secret part;
  for `localsecrets` a SHA-256 fingerprint of the key), and a revision.
- Creation is create-if-absent: when two codecs race, the loser discards its
  keyset and loads the stored one (REQ-SECRET-010).
- Data key rotation adds a new primary key and writes the record with
  compare-and-swap (REQ-SECRET-011).
- Master key rotation rewraps record by record, each a compare-and-swap; a
  record already wrapped by the new key is skipped, so a rerun completes an
  interrupted rotation (REQ-SECRET-013, REQ-SECRET-014). The codec accepts a
  current master key and optional previous ones, and unwraps each record with
  the master key it names.
- Unwrapped keysets are cached in memory per tenant and revision. A payload
  naming a key id missing from the cache triggers one reload of the record
  (REQ-SECRET-017, REQ-SECRET-018). Nothing unwrapped is written to disk.
- A record that the configured master keys cannot unwrap is never replaced
  (REQ-SECRET-015): no fallback keyset, which would make history unreadable.

## 3. Encoded payload

A payload is `metadata map[string][]byte` plus `data []byte`, the shape of a
Temporal payload.

| Field | Value |
| --- | --- |
| `metadata["encoding"]` | `binary/encrypted` |
| `metadata["encryption-key-id"]` | the decimal Tink key id of the data key |
| `data` | Tink AEAD ciphertext of the original payload (its metadata and data, serialized as a protobuf `Payload`) |

Associated data: `basamento.payload.v1`, a zero byte, the tenant id, a zero
byte, the key id. The tenant and the key id are therefore authenticated: a
changed key id or another tenant fails like a flipped bit (REQ-SECRET-004,
REQ-SECRET-005).

## 4. Errors

Codes come from `core` (no new code).

| Case | Code | Message |
| --- | --- | --- |
| Authentication fails, unknown key id, missing key id, other tenant, tenant without a keyset | `invalid_argument` | `payload cannot be decrypted` |
| Payload not marked `binary/encrypted` | `invalid_argument` | `payload is not encrypted` |
| Stored keyset cannot be unwrapped by the configured master keys | `internal` | `keyset of the tenant cannot be unwrapped` |
| Key service unreachable | `unavailable` | names the key service scheme only |
| Bad key service URL | `invalid_argument` | names the problem, never the key |

Decoding never creates a keyset. Errors and log records carry the tenant and
key id at most: never payload data, data keys, master keys or URL secrets
(REQ-SECRET-007).

## 5. Test support

The gate uses the **simulated key service** from a test-only package (not
`gocloud.dev` live keepers): it holds named master keys, can be made
unreachable at once or after a given rewrap, counts wrap and unwrap requests
per caller, and can delete a master key. Seeded random bytes come from
`core/coretest`. Live keepers (`awskms`, `gcpkms`,
`hashivault`) are exercised only in `@live` checks, outside this spec.
