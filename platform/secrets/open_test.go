package secrets_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform/secrets"
)

func TestValueDecryptWrongKeyFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	k3 := bytesFromSeed(3, 32)
	k4 := bytesFromSeed(4, 32)
	p3, err := secrets.OpenURL(ctx, "localsecrets://"+base64.StdEncoding.EncodeToString(k3))
	if err != nil {
		t.Fatal(err)
	}
	ct, err := p3.Encrypt(ctx, []byte("sk-live-4f9a2c"))
	if err != nil {
		t.Fatal(err)
	}
	p4, err := secrets.OpenURL(ctx, "localsecrets://"+base64.StdEncoding.EncodeToString(k4))
	if err != nil {
		t.Fatal(err)
	}
	_, err = p4.Decrypt(ctx, ct)
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("wrong key: err=%v", err)
	}
}

func bytesFromSeed(seed int64, n int) []byte {
	var out []byte
	h := sha256.Sum256([]byte(fmt.Sprintf("basamento.seed.%d", seed)))
	for len(out) < n {
		out = append(out, h[:]...)
		h = sha256.Sum256(h[:])
	}
	return out[:n]
}
