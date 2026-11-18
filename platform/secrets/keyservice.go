package secrets

import (
	"context"

	"github.com/epzero/basamento/core"
)

// KeyService wraps and unwraps blobs with a deployment master key (design §2).
//
// Custom implementations must authenticate the tenant inside the wrapped
// plaintext using the exported helpers FrameBoundPlaintext on Wrap and
// ParseBoundPlaintext on Unwrap—the same uvarint-length-prefixed framing as the
// production keeper—or tenants lose cryptographic binding to their keysets.
type KeyService interface {
	Wrap(ctx context.Context, tenant string, plaintext []byte) ([]byte, error)
	Unwrap(ctx context.Context, tenant string, ciphertext []byte) ([]byte, error)
	MasterKeyID() string
}

func unwrapWithKeys(ctx context.Context, tenant string, ct []byte, id string, keys []KeyService) ([]byte, KeyService, error) {
	if err := ValidateTenant(tenant); err != nil {
		return nil, nil, err
	}
	try := func(ks KeyService) ([]byte, error) {
		return ks.Unwrap(ctx, tenant, ct)
	}
	for _, ks := range keys {
		if ks.MasterKeyID() != id {
			continue
		}
		p, err := try(ks)
		if err == nil {
			return p, ks, nil
		}
		if core.CodeOf(err) == core.CodeInternal {
			return nil, nil, err
		}
	}
	var last error
	for _, ks := range keys {
		p, err := try(ks)
		if err == nil {
			return p, ks, nil
		}
		if core.CodeOf(err) == core.CodeInternal {
			return nil, nil, err
		}
		last = err
	}
	if last != nil && core.CodeOf(last) == core.CodeUnavailable {
		return nil, nil, last
	}
	return nil, nil, errInternal(msgKeysetCannotUnwrap)
}
