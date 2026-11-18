package secrets

import (
	"context"

	"gocloud.dev/secrets"
)

// Port implements platform.Secrets over a gocloud key keeper.
type Port struct {
	keeper *secrets.Keeper
	scheme string
}

// Encrypt wraps plaintext with the master key.
func (p *Port) Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	ct, err := p.keeper.Encrypt(ctx, plaintext)
	if err != nil {
		return nil, errUnavailable(p.scheme)
	}
	return ct, nil
}

// Decrypt unwraps ciphertext with the master key.
func (p *Port) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	pt, err := p.keeper.Decrypt(ctx, ciphertext)
	if err != nil {
		return nil, errInvalidArgument(msgValueCannotDecrypt)
	}
	return pt, nil
}

var _ interface {
	Encrypt(ctx context.Context, plaintext []byte) ([]byte, error)
	Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error)
} = (*Port)(nil)
