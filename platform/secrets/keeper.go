package secrets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/epzero/basamento/core"
	"gocloud.dev/gcerrors"
	"gocloud.dev/secrets"
	"gocloud.dev/secrets/localsecrets"
)

// keeperKeyService wraps tenant keysets with a gocloud Keeper (design §2).
type keeperKeyService struct {
	keeper *secrets.Keeper
	scheme string
	id     string
}

// OpenKeyService opens a production master key from url (current or previous).
func OpenKeyService(ctx context.Context, raw string) (KeyService, error) {
	return newKeeperService(ctx, raw)
}

func newKeeperService(ctx context.Context, raw string) (*keeperKeyService, error) {
	keeper, scheme, id, err := openKeeper(ctx, raw)
	if err != nil {
		return nil, err
	}
	return &keeperKeyService{keeper: keeper, scheme: scheme, id: id}, nil
}

func (k *keeperKeyService) MasterKeyID() string { return k.id }

func (k *keeperKeyService) Close() error {
	return k.keeper.Close()
}

func (k *keeperKeyService) Wrap(ctx context.Context, tenant string, plaintext []byte) ([]byte, error) {
	inner, err := FrameBoundPlaintext(tenant, plaintext)
	if err != nil {
		return nil, err
	}
	ct, err := k.keeper.Encrypt(ctx, inner)
	if err != nil {
		return nil, classifyKeeperErr(ctx, err, k.scheme, opWrapKeyset)
	}
	return ct, nil
}

func (k *keeperKeyService) Unwrap(ctx context.Context, tenant string, ciphertext []byte) ([]byte, error) {
	inner, err := k.keeper.Decrypt(ctx, ciphertext)
	if err != nil {
		return nil, classifyKeeperErr(ctx, err, k.scheme, opUnwrapKeyset)
	}
	return ParseBoundPlaintext(tenant, inner)
}

func (k *keeperKeyService) encryptValue(ctx context.Context, plaintext []byte) ([]byte, error) {
	inner := frameValuePlaintext(plaintext)
	ct, err := k.keeper.Encrypt(ctx, inner)
	if err != nil {
		return nil, classifyKeeperErr(ctx, err, k.scheme, opEncryptValue)
	}
	return ct, nil
}

func (k *keeperKeyService) decryptValue(ctx context.Context, ciphertext []byte) ([]byte, error) {
	inner, err := k.keeper.Decrypt(ctx, ciphertext)
	if err != nil {
		return nil, classifyKeeperErr(ctx, err, k.scheme, opDecryptValue)
	}
	return parseValuePlaintext(inner)
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
		return publicURLID(raw)
	}
}

func openKeeper(ctx context.Context, raw string) (*secrets.Keeper, string, string, error) {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || scheme == "" {
		return nil, "", "", errInvalidArgument(fmt.Sprintf(msgUnsupportedScheme, scheme))
	}
	if !schemeSupported(scheme) {
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
	default:
		id, err := publicURLID(raw)
		if err != nil {
			return nil, "", "", err
		}
		keeper, err := secrets.OpenKeeper(ctx, raw)
		if err != nil {
			return nil, "", "", classifyOpenKeeperErr(ctx, err, scheme)
		}
		return keeper, scheme, id, nil
	}
}

func classifyOpenKeeperErr(ctx context.Context, err error, scheme string) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return &core.Error{Code: core.CodeCancelled, Message: "cancelled", Cause: ctx.Err()}
		}
		return &core.Error{Code: core.CodeTimeout, Message: "timeout", Cause: ctx.Err(), Retryable: true}
	}
	if errors.Is(err, context.Canceled) {
		return &core.Error{Code: core.CodeCancelled, Message: "cancelled", Cause: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &core.Error{Code: core.CodeTimeout, Message: "timeout", Cause: err, Retryable: true}
	}
	if errorCode(err) == gcerrors.ResourceExhausted {
		return errUnavailableRetry(scheme)
	}
	return errInvalidArgument(fmt.Sprintf(msgInvalidKeyURL, scheme))
}
