package secrets

import (
	"context"
	"encoding/base64"
	"log/slog"
	"strings"

	"github.com/epzero/basamento/core"
)

const (
	msgUnsupportedScheme = `unsupported key service scheme %q`
	msgInvalidKeyURL     = `invalid key service URL for scheme %q`
	msgLocalKeyMissing   = "local key is missing"
	msgLocalKeySize      = "local key must be 32 bytes, base64 encoded"
)

// OpenURL opens the secrets port from a key service URL (design §1).
func OpenURL(ctx context.Context, raw string, opts ...Option) (*Port, error) {
	o := applyOptions(opts)
	ks, err := newKeeperService(ctx, raw)
	if err != nil {
		logOpenFailure(o, schemeOf(raw), err)
		return nil, err
	}
	return &Port{ks: ks, opts: o}, nil
}

func schemeOf(raw string) string {
	s, _, ok := strings.Cut(raw, "://")
	if !ok {
		return ""
	}
	return s
}

func logOpenFailure(o options, scheme string, err error) {
	o.loggerOrDefault().Debug("secrets port open failed",
		slog.String("scheme", scheme),
		slog.String("code", string(core.CodeOf(err))))
}

func decodeLocalKey(keyB64 string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil || len(key) != 32 {
		return nil, errInvalidArgument(msgLocalKeySize)
	}
	return key, nil
}
