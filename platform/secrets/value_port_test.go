package secrets

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestPortDecryptRejectsKeysetCiphertext(t *testing.T) {
	t.Parallel()
	key := bytesFromTestSeed(11, 32)
	keyB64 := base64.StdEncoding.EncodeToString(key)
	raw := "localsecrets://" + keyB64
	ctx := context.Background()
	port, err := OpenURL(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	ks, err := OpenKeyService(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	h, err := newDataKeyset()
	if err != nil {
		t.Fatal(err)
	}
	plain, err := serializeKeyset(h)
	if err != nil {
		t.Fatal(err)
	}
	keysetCT, err := ks.Wrap(ctx, "acme", plain)
	if err != nil {
		t.Fatal(err)
	}
	_, err = port.Decrypt(ctx, keysetCT)
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("Decrypt keyset ct: err=%v", err)
	}
	valueCT, err := port.Encrypt(ctx, []byte("sk-live"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = ks.Unwrap(ctx, "acme", valueCT)
	if err == nil || core.CodeOf(err) != core.CodeInternal {
		t.Fatalf("Unwrap value ct: err=%v", err)
	}
}

func bytesFromTestSeed(seed int, n int) []byte {
	var out []byte
	h := sha256.Sum256([]byte(fmt.Sprintf("basamento.seed.%d", seed)))
	for len(out) < n {
		out = append(out, h[:]...)
		h = sha256.Sum256(h[:])
	}
	return out[:n]
}
