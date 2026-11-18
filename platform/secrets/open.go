package secrets

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"gocloud.dev/secrets"
	"gocloud.dev/secrets/localsecrets"
)

const (
	msgUnsupportedScheme = `unsupported key service scheme %q`
	msgLocalKeyMissing   = "local key is missing"
	msgLocalKeySize      = "local key must be 32 bytes, base64 encoded"
)

// OpenURL opens the secrets port from a key service URL (design §1).
func OpenURL(ctx context.Context, raw string) (*Port, error) {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || scheme == "" {
		return nil, errInvalidArgument(fmt.Sprintf(msgUnsupportedScheme, scheme))
	}
	switch scheme {
	case "localsecrets":
		keyB64 := rest
		if keyB64 == "" {
			return nil, errInvalidArgument(msgLocalKeyMissing)
		}
		key, err := decodeLocalKey(keyB64)
		if err != nil {
			return nil, err
		}
		var sk [32]byte
		copy(sk[:], key)
		return &Port{keeper: localsecrets.NewKeeper(sk), scheme: "localsecrets"}, nil
	case "awskms", "gcpkms", "hashivault":
		keeper, err := localsecretsOpenRemote(ctx, raw)
		if err != nil {
			return nil, errUnavailable(scheme)
		}
		return &Port{keeper: keeper, scheme: scheme}, nil
	default:
		return nil, errInvalidArgument(fmt.Sprintf(msgUnsupportedScheme, scheme))
	}
}

func localsecretsOpenRemote(ctx context.Context, raw string) (*secrets.Keeper, error) {
	return secrets.OpenKeeper(ctx, raw)
}

func decodeLocalKey(keyB64 string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil || len(key) != 32 {
		return nil, errInvalidArgument(msgLocalKeySize)
	}
	return key, nil
}
