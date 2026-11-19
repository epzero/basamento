# Secrets: design

Decisions the implementer of WP-L1-04 follows. Package: `platform/secrets`
(it may import `core` and `platform` only, `layering.yaml`).

## 1. Parts

| Part | What it is |
| --- | --- |
| Key service | Holds the master key and wraps and unwraps with it. Opened from a URL through `gocloud.dev/secrets`: `localsecrets://<base64 32-byte key>` (embedded; mapped onto gocloud's `localsecrets` keeper, whose own scheme is `base64key`), `awskms://`, `gcpkms://`, and `hashivault://` (Vault Transit), which ADR-0004 lists. Any other scheme is refused (REQ-SECRET-019). The gocloud version in use has no Vault Transit driver, so until a key service for it is added the store refuses `hashivault://` with `invalid_argument` as well. |
| Secrets port | Implements `platform.Secrets` (`Encrypt`/`Decrypt` of small values) over the key service, and owns tenants' keysets: creation, wrapping, rotation, rewrap. |
| Keyset store | Port declared by `platform/secrets` that persists one wrapped keyset record per tenant with compare-and-swap. This WP ships a file-backed store for the embedded profile; a SQL store can follow behind the same port without a spec change. |
| Payload codec | Encodes and decodes the runtimes' payloads with the tenant's keyset. The tenant comes from the context (`ctx`), as for every repository call. |

The cloud key services arrive with their vendor client stacks. The package
imports `gocloud.dev/secrets/awskms` and `gocloud.dev/secrets/gcpkms` for
their registration, and those modules bring the AWS and Google KMS SDKs. A
cloud implementation of a port carries its vendor's client stack: the KMS
SDKs live behind this port and are imported by no other package of the
repository (constitution article 1, `docs/engineering/coding-guidelines.md`
§4).

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
- A record that the configured master keys cannot unwrap is never replaced
  (REQ-SECRET-015): no fallback keyset, which would make history unreadable.
- The tenant id is authenticated together with the wrapped keyset: it travels
  inside the wrapped plaintext, in the keyset frame of §2.1, and is checked
  after unwrapping. A record whose authenticated tenant id is not the tenant
  whose keyset is being loaded fails with code `internal` and message `keyset
  of the tenant cannot be unwrapped` (REQ-SECRET-023): swapping two tenants'
  stored keyset records cannot give one tenant another tenant's keys
  (constitution article 8).
- `DataKeyCount` reports every key the keyset lists, whatever its status
  (enabled, disabled or destroyed); this WP deletes no key, so the count grows
  by one per data key rotation. `PrimaryDataKeyID` reports the decimal id of
  the primary key.

### 2.1 Two domains for the master key

The same master key seals two kinds of plaintext, so each is tagged with its
purpose before sealing, and each parser refuses the other's frame before it
parses anything else:

| Frame | Plaintext |
| --- | --- |
| Keyset | `basamento.keyset.v1`, a zero byte, the tenant id's length as a varint, the tenant id, the serialized keyset |
| Value | `basamento.value.v1`, a zero byte, the value |

A value ciphertext offered as a wrapped keyset fails as `keyset of the tenant
cannot be unwrapped`; a wrapped keyset offered as a value fails as `value
cannot be decrypted`. Without the tags a keyset wrap would come back as value
plaintext, and a value ciphertext could load as keyset material whenever its
first bytes fitted the keyset frame. The tenant check separates tenants, not
the two uses of the same key, so the tag is what keeps one use from standing in
for the other (REQ-SECRET-023). Payload ciphertexts are a third, separate
domain: they are sealed by a data key with associated data of their own (§3),
never by the master key.

### 2.2 Key-service failure classes

The production key service is a `gocloud.dev/secrets` keeper. Its errors are
classified before they leave the package; the operation says what was being
done with the master key, and the keeper's own code says why it failed.

| The keeper returns | Cloud key service (`awskms`, `gcpkms`) | `localsecrets` |
| --- | --- | --- |
| Context cancelled | `cancelled` | `cancelled` |
| Deadline exceeded | `timeout`, retryable | `timeout`, retryable |
| `InvalidArgument`, `FailedPrecondition` | Wrapping or unwrapping a keyset, or encrypting a value: `internal`, `keyset of the tenant cannot be unwrapped`. Decrypting a value: `invalid_argument`, `value cannot be decrypted`. | Unwrapping a keyset: `internal`, `keyset of the tenant cannot be unwrapped`. Wrapping a keyset or encrypting a value: `internal`, `key service refused the request`. Decrypting a value: `invalid_argument`, `value cannot be decrypted`. |
| `PermissionDenied`, `NotFound`, `Unimplemented`, `AlreadyExists` | Unwrapping a keyset: `internal`, `keyset of the tenant cannot be unwrapped`. Any other operation: `internal`, `key service refused the request`. | Same. |
| Any other code | `unavailable`, retryable, the message is the scheme only | Unwrapping a keyset: `internal`, `keyset of the tenant cannot be unwrapped`. Wrapping a keyset or encrypting a value: `internal`, `key service refused the request`. Decrypting a value: `invalid_argument`, `value cannot be decrypted`. |

A cloud keeper that cannot be reached therefore fails a value decrypt with
`unavailable`, retryable, never with `invalid_argument`. `localsecrets` holds
the key in the process and has no unreachable state, so none of its failures
is `unavailable`. Opening a cloud key service URL goes through the same
classification; a failure there that is neither `unavailable` nor a cancelled
context is a configuration error and becomes `invalid_argument` with `invalid
key service URL for scheme "<scheme>"`, while an outage keeps its `unavailable`
code.

### 2.3 The keyset cache and what it costs

- The cache holds one entry per tenant: the unwrapped `keyset.Handle` and the
  store revision it was loaded from. Nothing unwrapped is written to disk
  (REQ-SECRET-017, REQ-SECRET-018).
- Every encode reads the tenant's record once from the keyset store and
  compares its revision with the cached entry's; it unwraps only when the
  revision differs or nothing is cached. An encode therefore costs one point
  read of a small record and, while the revision holds, no key-service call.
  On a SQL store that is one row lookup per encoded payload: a cost taken on
  purpose, traded for the correctness below.
- A decode reads the payload's key id first. If the cached keyset holds it,
  the store is not read and nothing is unwrapped. Otherwise the record is read
  once: a cached entry at the same revision rejects the payload without an
  unwrap, and a different revision, or no cached entry, unwraps once and looks
  for the key id again. An authentication failure never causes a reload or an
  unwrap (REQ-SECRET-024).
- There is no time-based expiry. A TTL would let a codec keep using a keyset
  older than the one stored until the entry expired, which is exactly the
  window after a rotation or a revocation in which the newer keyset must
  already be in force. One small read per encode is preferred to that window.

### 2.4 Master-key rotation

- Before any record is touched, the new master key proves itself: the codec
  wraps and unwraps a canary under a tenant id no real tenant uses, and a
  failure stops the rotation before the first write. A broken new key would
  otherwise rewrap records into something no configured key can open.
- The new key becomes the current master key and the previous ones stay
  configured, so every record still unwraps with the master key it names. The
  codec accepts a current master key and optional previous ones.
- Rewrapping is record by record, each a compare-and-swap that bumps the
  revision. A record already naming the new master key is skipped after a
  check that it still unwraps; every other record is unwrapped with the
  configured master key it names and rewrapped under the new one
  (REQ-SECRET-013).
- A rotation that stops halfway therefore leaves each keyset wrapped by either
  the old or the new master key, every one still decodable while both master
  keys are configured, and a rerun rewraps the remaining keysets
  (REQ-SECRET-014). A failed rotation leaves the new key configured, so
  payloads decode with both keys until the rerun completes.
- The rotation sweeps the store until a pass changes nothing. A record it
  cannot unwrap fails the rotation with that error, and a store that keeps
  changing under it fails it with `conflict`, retryable; neither writes a
  partial record. A completed rotation clears the codec's cache, so the next
  encode or decode of each tenant loads the rewrapped record.

### 2.5 Tenant ids

- Every tenant id is checked against the grammar of REQ-CORE-005 by
  `ValidateTenant`: non-empty, at most 128 bytes, valid UTF-8, no control
  characters from U+0000 to U+001F and U+007F, not `.` or `..`, no `/` or
  `\`, and no leading or trailing Unicode white space. The file store, the
  keyset framing and the codec's context lookup all call it.
- The local check is interim: `core.TenantFrom` enforces the same grammar, and
  the package will rely on it and drop the copy. Until then `ValidateTenant`
  is a copy of the rule and changes with it.

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
| Payload encoded or decoded without a tenant in context (REQ-SECRET-022) | `invalid_argument` | `tenant is required` |
| Payload not marked `binary/encrypted` | `invalid_argument` | `payload is not encrypted` |
| Stored keyset cannot be unwrapped by the configured master keys, or its authenticated tenant id is not the decoding tenant (REQ-SECRET-023) | `internal` | `keyset of the tenant cannot be unwrapped` |
| Key service unreachable while a keyset is wrapped, unwrapped or rewrapped (REQ-SECRET-016), or while a value is encrypted or decrypted | `unavailable`, retryable | names the key service scheme only |
| Key service URL with an unsupported scheme (REQ-SECRET-019) | `invalid_argument` | `unsupported key service scheme "<scheme>"` |
| `localsecrets` URL without a local key (REQ-SECRET-019) | `invalid_argument` | `local key is missing` |
| `localsecrets` local key that is not 32 base64-encoded bytes (REQ-SECRET-019) | `invalid_argument` | `local key must be 32 bytes, base64 encoded` |
| `Secrets.Decrypt` of an altered value ciphertext or one made under another master key (REQ-SECRET-021) | `invalid_argument` | `value cannot be decrypted` |

Decoding never creates a keyset. Errors and log records carry the tenant and
key id at most: never payload data, data keys, master keys or URL secrets
(REQ-SECRET-007).

## 5. Test support

The gate uses a **simulated key service** that lives in the BDD step library
of this work package, `internal/bdd` — the steps in
`internal/bdd/steps/secrets.go` and helpers beside it — not in a new test-only
package and not `gocloud.dev` live keepers: it holds named master keys, can be
made unreachable at once or after a given number of keyset wraps, counts unwrap
requests per codec and in total, and can delete a master key. The seeded random
helper the scenarios use for deterministic payloads and keys lives beside it in
`internal/bdd`; no production code path accepts a seeded random source. Unit
tests in `platform/secrets` use fakes defined in their own `_test.go` files.
`core/coretest` remains the only package listed as test-only in
`layering.yaml` and belongs to another work package, so this WP adds none.
Live keepers (`awskms`, `gcpkms`) are exercised only in `@live` checks,
outside this spec.

Three behaviors stay in unit tests because the gate cannot drive them
deterministically with the simulator:

- the two frames of §2.1, which need a value ciphertext the simulator never
  produces;
- a cloud key service failing during a value decrypt, which needs a keeper
  the simulator does not stand in for;
- a master-key rotation racing another codec's first encode of a tenant, whose
  interleaving no scenario can fix.
