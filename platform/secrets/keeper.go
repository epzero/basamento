package secrets

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"

	"gocloud.dev/secrets"
	"gocloud.dev/secrets/localsecrets"

	"github.com/epzero/basamento/core"
)

// keeperKeyService wraps tenant keysets with a gocloud Keeper (design §2).
type keeperKeyService struct {
	keeper *secrets.Keeper
	scheme string
	id     string
	opts   options
}

// OpenKeyService opens a production master key from url (current or previous).
func OpenKeyService(ctx context.Context, raw string, opts ...Option) (KeyService, error) {
	o := applyOptions(opts)
	return newKeeperService(ctx, raw, o)
}

func newKeeperService(ctx context.Context, raw string, o options) (*keeperKeyService, error) {
	keeper, scheme, id, err := openKeeper(ctx, raw)
	if err != nil {
		return nil, err
	}
	return &keeperKeyService{keeper: keeper, scheme: scheme, id: id, opts: o}, nil
}

func (k *keeperKeyService) Scheme() string { return k.scheme }

func (k *keeperKeyService) MasterKeyID() string { return k.id }

func (k *keeperKeyService) Wrap(ctx context.Context, tenant string, plaintext []byte) ([]byte, error) {
	inner := make([]byte, 0, len(tenant)+1+len(plaintext))
	inner = append(inner, tenant...)
	inner = append(inner, 0)
	inner = append(inner, plaintext...)
	ct, err := k.keeper.Encrypt(ctx, inner)
	if err != nil {
		return nil, keeperFail(err, k.scheme)
	}
	return ct, nil
}

func (k *keeperKeyService) Unwrap(ctx context.Context, tenant string, ciphertext []byte) ([]byte, error) {
	inner, err := k.keeper.Decrypt(ctx, ciphertext)
	if err != nil {
		if unreachable(err) {
			return nil, errUnavailable(k.scheme)
		}
		return nil, core.NewError(core.CodeInvalidArgument, "unwrap failed", nil)
	}
	return splitTenantBoundPlaintext(tenant, inner)
}

func (k *keeperKeyService) encryptValue(ctx context.Context, plaintext []byte) ([]byte, error) {
	ct, err := k.keeper.Encrypt(ctx, plaintext)
	if err != nil {
		return nil, keeperFail(err, k.scheme)
	}
	return ct, nil
}

func (k *keeperKeyService) decryptValue(ctx context.Context, ciphertext []byte) ([]byte, error) {
	pt, err := k.keeper.Decrypt(ctx, ciphertext)
	if err != nil {
		return nil, errInvalidArgument(msgValueCannotDecrypt)
	}
	return pt, nil
}

func splitTenantBoundPlaintext(tenant string, inner []byte) ([]byte, error) {
	i := bytes.IndexByte(inner, 0)
	if i < 0 {
		return nil, errInternal(msgKeysetCannotUnwrap)
	}
	bound := string(inner[:i])
	if subtle.ConstantTimeCompare([]byte(bound), []byte(tenant)) != 1 {
		return nil, errInternal(msgKeysetCannotUnwrap)
	}
	return append([]byte(nil), inner[i+1:]...), nil
}

func keeperFail(err error, scheme string) error {
	if unreachable(err) {
		return errUnavailable(scheme)
	}
	return errUnavailable(scheme)
}

func unreachable(err error) bool {
	if err == nil {
		return false
	}
	if core.CodeOf(err) == core.CodeUnavailable {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "context canceled") ||
		strings.Contains(msg, "context deadline exceeded")
}

func masterKeyID(raw, scheme string, localKey []byte) (string, error) {
	switch scheme {
	case "localsecrets":
		if len(localKey) != 32 {
			return "", errInvalidArgument(msgLocalKeySize)
		}
		sum := sha256.Sum256(localKey)
		return "localsecrets:" + hex.EncodeToString(sum[:]), nil
	default:
		return stripURLSecret(raw), nil
	}
}

func stripURLSecret(raw string) string {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return raw
	}
	if before, after, ok := strings.Cut(rest, "@"); ok && strings.Contains(before, ":") {
		return scheme + "://" + after
	}
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		return scheme + "://" + rest[:i]
	}
	return raw
}

func openKeeper(ctx context.Context, raw string) (*secrets.Keeper, string, string, error) {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || scheme == "" {
		return nil, "", "", errInvalidArgument(fmt.Sprintf(msgUnsupportedScheme, scheme))
	}
	switch scheme {
	case "localsecrets":
		keyB64 := rest
		if keyB64 == "" {
			return nil, "", "", errInvalidArgument(msgLocalKeyMissing)
		}
		key, err := decodeLocalKey(keyB64)
		if err != nil {
			return nil, "", "", err
		}
		id, err := masterKeyID(raw, scheme, key)
		if err != nil {
			return nil, "", "", err
		}
		var sk [32]byte
		copy(sk[:], key)
		return localsecrets.NewKeeper(sk), scheme, id, nil
	case "awskms", "gcpkms", "hashivault":
		keeper, err := secrets.OpenKeeper(ctx, raw)
		if err != nil {
			return nil, "", "", errUnavailable(scheme)
		}
		id := stripURLSecret(raw)
		return keeper, scheme, id, nil
	default:
		return nil, "", "", errInvalidArgument(fmt.Sprintf(msgUnsupportedScheme, scheme))
	}
}

// OpenCodec opens a payload codec with the current master key url and optional
// previous master key urls (design §2).
func OpenCodec(ctx context.Context, store KeysetStore, currentURL string, prevURLs []string, opts ...Option) (*Codec, error) {
	o := applyOptions(opts)
	cur, err := OpenKeyService(ctx, currentURL, WithLogger(o.logger))
	if err != nil {
		return nil, err
	}
	keys := []KeyService{cur}
	for _, u := range prevURLs {
		ks, err := OpenKeyService(ctx, u, WithLogger(o.logger))
		if err != nil {
			return nil, err
		}
		keys = append(keys, ks)
	}
	c, err := NewCodec(store, keys...)
	if err != nil {
		return nil, err
	}
	c.setOptions(o)
	return c, nil
}
