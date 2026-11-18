// Package secrets implements the secrets port and the payload codec (WP-L1-04).
//
// OpenURL and OpenCodec build production components from gocloud.dev/secrets
// key service URLs. Supported schemes are localsecrets, awskms and gcpkms
// (Vault Transit is not implemented). The secrets port encrypts small values;
// the payload codec stores each tenant's Tink data-key keyset wrapped by the
// master key and encrypts runtime payloads with the tenant's primary data key,
// authenticating tenant and key id.
//
// Tenant ids must satisfy REQ-CORE-005 (see ValidateTenant); until core ships a
// shared helper (EP0-52), this package enforces the same rule locally.
//
// Each Encode performs one keyset store read; when the stored revision matches
// the in-memory cache, no master-key unwrap runs.
//
// After a master-key rotation, another process or a partially failed rotation
// may leave records on an old master id; rerun the rotation before removing
// the previous master from configuration (idempotent).
package secrets
