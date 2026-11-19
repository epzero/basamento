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

func TestValueDecryptTamperedFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	k3 := bytesFromSeed(3, 32)
	p3, err := secrets.OpenURL(ctx, "localsecrets://"+base64.StdEncoding.EncodeToString(k3))
	if err != nil {
		t.Fatal(err)
	}
	ct, err := p3.Encrypt(ctx, []byte("sk-live-4f9a2c"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ct) > 0 {
		ct[0] ^= 1
	}
	_, err = p3.Decrypt(ctx, ct)
	if err == nil {
		t.Fatal("expected decrypt error")
	}
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("tampered ciphertext: err=%v", err)
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
