// Package secrets implements the secrets port and the payload codec (WP-L1-04).
//
// OpenURL and OpenCodec build production components from gocloud.dev/secrets
// key service URLs (localsecrets, awskms, gcpkms, hashivault). The secrets port
// encrypts small values; the payload codec stores each tenant's Tink data-key
// keyset wrapped by the master key and encrypts runtime payloads with the
// tenant's primary data key, authenticating tenant and key id.
//
// Tenant ids must be non-empty, at most 128 bytes, and must not contain ASCII
// control characters (U+0000..U+001F, U+007F). Until EP0-52 lands shared
// grammar in core, this package enforces that rule locally via ValidateTenant.
package secrets
