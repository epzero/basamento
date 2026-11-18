// Package secrets implements the secrets port and the payload codec (WP-L1-04).
//
// OpenURL and OpenCodec build production components from gocloud.dev/secrets
// key service URLs (localsecrets, awskms, gcpkms, hashivault). The secrets port
// encrypts small values; the payload codec stores each tenant's Tink data-key
// keyset wrapped by the master key and encrypts runtime payloads with the
// tenant's primary data key, authenticating tenant and key id.
package secrets
