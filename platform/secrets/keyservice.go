package secrets

import (
	"context"
)

// KeyService wraps and unwraps blobs with a deployment master key (design §2).
//
// Custom implementations must authenticate the tenant inside the wrapped
// plaintext using FrameBoundPlaintext on Wrap and ParseBoundPlaintext on
// Unwrap (keyset domain framing). Value ciphertexts use a separate domain;
// Port.Encrypt / Decrypt use the production keeper's value framing.
type KeyService interface {
	Wrap(ctx context.Context, tenant string, plaintext []byte) ([]byte, error)
	Unwrap(ctx context.Context, tenant string, ciphertext []byte) ([]byte, error)
	MasterKeyID() string
	Close() error
}

func unwrapWithKeys(ctx context.Context, tenant string, ct []byte, id string, keys []KeyService) ([]byte, error) {
	if err := tenantStringOK(tenant); err != nil {
		return nil, err
	}
	for _, ks := range keys {
		if ks.MasterKeyID() != id {
			continue
		}
		p, err := ks.Unwrap(ctx, tenant, ct)
		if err == nil {
			return p, nil
		}
		return nil, err
	}
	return nil, errInternal(msgKeysetCannotUnwrap)
}
