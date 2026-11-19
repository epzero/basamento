# Secrets: key wrapping and payload encryption

Status: **Planned** (WP-L1-04, `platform/secrets`). Design: ADR-0004, ADR-0002,
[`design.md`](design.md). Constitution article 7: payloads in history are
encrypted.

The secrets port wraps keys with a master key held by a key service
(`localsecrets://` in the embedded profile, a KMS or Vault Transit in the
cloud one). The payload codec encrypts the payloads a runtime records in
history with the tenant's data keys, which are stored only wrapped. The
credential store (`cred://`, WP-L1-06) builds on this port and is specified
separately; storing large payloads by reference is the runtimes' concern
(`docs/architecture/runtime.md`).

## Payload codec

### REQ-SECRET-001 — Round trip
The payload codec shall decode every payload it encoded for a tenant, for that tenant, to the same data and the same metadata.

### REQ-SECRET-002 — Encrypted form
The payload codec shall encode a payload as ciphertext with only the metadata `encoding: binary/encrypted` and `encryption-key-id` naming the data key that encrypted it.

### REQ-SECRET-003 — Distinct ciphertexts
When the same payload is encoded more than once, the payload codec shall produce a different ciphertext each time.

### REQ-SECRET-004 — Authentication failure
If an encoded payload cannot be authenticated or lacks a matching data key for the decoding tenant, then the payload codec shall reject it with code `invalid_argument` and the message `payload cannot be decrypted`, whatever check failed, and return no payload.

### REQ-SECRET-005 — Tenant binding
The payload codec shall bind each encoded payload to the tenant it was encoded for, so that it decodes for that tenant only.

### REQ-SECRET-006 — Unencrypted payloads
If a payload without the encoding `binary/encrypted` is decoded, then the payload codec shall reject it with code `invalid_argument` and the message `payload is not encrypted`.

### REQ-SECRET-007 — No secrets in errors or logs
The secrets port and the payload codec shall never write payload data, data keys, master keys or URL secrets into an error or a log record.

### REQ-SECRET-022 — Tenant required
If a payload is encoded or decoded without a tenant in context, then the payload codec shall reject the call with code `invalid_argument` and return no payload.

## Data keys and master keys

### REQ-SECRET-008 — Wrapped at rest
The secrets port shall store a tenant's keyset only wrapped by the master key, together with the identity of that master key.

### REQ-SECRET-009 — Keysets survive restarts
When the payload codec restarts with the same keyset store and master key, the payload codec shall decode payloads encoded before the restart.

### REQ-SECRET-010 — One keyset per tenant
When several payload codecs sharing one keyset store encode the first payload of a tenant at the same time, the secrets port shall keep exactly one keyset for that tenant.

### REQ-SECRET-011 — Data key rotation
When a tenant's data key is rotated, the payload codec shall encode that tenant's later payloads under a new primary data key and leave other tenants' keysets unchanged.

### REQ-SECRET-012 — Earlier data keys
While a tenant's keyset holds data keys that are no longer primary, the payload codec shall decode payloads encoded under them.

### REQ-SECRET-013 — Master key rotation
When the master key is rotated, the secrets port shall rewrap every stored keyset under the new master key without changing the data keys it holds.

### REQ-SECRET-014 — Interrupted master key rotation
If a master key rotation stops before every keyset is rewrapped, then the secrets port shall leave each keyset wrapped by either the old or the new master key, decodable with both master keys configured, and rewrap the remaining keysets on a retry.

### REQ-SECRET-015 — Wrong master key
If the configured master key cannot unwrap a tenant's stored keyset, then the payload codec shall fail that tenant's encodes and decodes with code `internal` and leave the stored keyset unchanged.

### REQ-SECRET-016 — Key service unavailable
If the key service cannot be reached when a keyset must be wrapped, unwrapped or rewrapped, then the secrets port and the payload codec shall fail the operation with code `unavailable` and return no payload.

### REQ-SECRET-017 — Unwrap once
While a tenant's keyset is unchanged in the keyset store, the payload codec shall ask the key service to unwrap it at most once per process.

### REQ-SECRET-018 — Keyset reload
If an encoded payload names a data key absent from the payload codec's cached keyset, then the payload codec shall reload the tenant's keyset from the keyset store once before it decides whether the payload decodes.

### REQ-SECRET-023 — Keysets bound to their tenant
If the stored keyset of a tenant is not the one that was wrapped for that tenant, then the payload codec shall fail with code `internal` and the message `keyset of the tenant cannot be unwrapped`, and return no payload.

### REQ-SECRET-024 — Rejected payloads unwrap a keyset at most once
If an encoded payload is rejected because it cannot be decrypted, then the payload codec shall ask the key service to unwrap the tenant's keyset at most once, and not at all while that keyset is already loaded.

## Key service and values

### REQ-SECRET-019 — Key service URL
If the key service URL has an unsupported scheme or a malformed local key, then the secrets port shall refuse to open with code `invalid_argument` and a message that names the problem without the key.

### REQ-SECRET-020 — Value encryption
The secrets port shall encrypt a value so that it decrypts to the same bytes with the same master key.

### REQ-SECRET-021 — Value integrity
If a value ciphertext was altered or made under another master key, then the secrets port shall reject its decryption with code `invalid_argument` and return no value.
