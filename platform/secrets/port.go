package secrets

import (
	"context"
	"log/slog"

	"github.com/epzero/basamento/core"
)

// Port implements platform.Secrets over the same production key service as the
// payload codec (design §1).
type Port struct {
	ks   *keeperKeyService
	opts options
}

// Close closes the underlying gocloud keeper.
func (p *Port) Close() error {
	return p.ks.Close()
}

// Encrypt wraps plaintext with the master key.
func (p *Port) Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	ct, err := p.ks.encryptValue(ctx, plaintext)
	if err != nil {
		return nil, err
	}
	p.opts.loggerOrDefault().Debug("secrets value encrypted", slog.String("scheme", p.ks.scheme))
	return ct, nil
}

// Decrypt unwraps ciphertext with the master key.
func (p *Port) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	pt, err := p.ks.decryptValue(ctx, ciphertext)
	if err != nil {
		p.opts.loggerOrDefault().Debug("secrets value decrypt failed",
			slog.String("scheme", p.ks.scheme),
			slog.String("code", string(core.CodeOf(err))))
		return nil, err
	}
	p.opts.loggerOrDefault().Debug("secrets value decrypted", slog.String("scheme", p.ks.scheme))
	return pt, nil
}

var _ interface {
	Encrypt(ctx context.Context, plaintext []byte) ([]byte, error)
	Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error)
} = (*Port)(nil)
