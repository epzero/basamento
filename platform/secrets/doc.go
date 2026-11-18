// Package secrets implements the secrets port and the payload codec (WP-L1-04).
//
// The key service holds the master key (gocloud.dev/secrets). The secrets port
// encrypts small values and stores each tenant's Tink data-key keyset wrapped by
// that master key. The payload codec encrypts runtime payloads with the
// tenant's primary data key and authenticates tenant and key id.
package secrets
