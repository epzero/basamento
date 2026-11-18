package secrets

import (
	"context"

	"github.com/epzero/basamento/core"
)

// KeyService wraps and unwraps blobs with a deployment master key. Associated
// data carries the tenant when the implementation supports it; otherwise the
// tenant is authenticated inside the plaintext (design §2).
type KeyService interface {
	Wrap(ctx context.Context, tenant string, plaintext []byte) ([]byte, error)
	Unwrap(ctx context.Context, tenant string, ciphertext []byte) ([]byte, error)
	MasterKeyID() string
	Scheme() string
}

func unwrapWithKeys(ctx context.Context, tenant string, ct []byte, id string, keys []KeyService) ([]byte, KeyService, error) {
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
