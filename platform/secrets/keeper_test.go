package secrets_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform/secrets"
)

func TestKeeperWrapRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	k := key32(3)
	url := "localsecrets://" + base64.StdEncoding.EncodeToString(k)
	ks, err := secrets.OpenKeyService(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("keyset-bytes")
	ct, err := ks.Wrap(ctx, "acme", plain)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ks.Unwrap(ctx, "acme", ct)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(plain) {
		t.Fatalf("got %q", out)
	}
}

func TestKeeperTenantSwapInternal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ks, err := secrets.OpenKeyService(ctx, localURL(3))
	if err != nil {
		t.Fatal(err)
	}
	ct, err := ks.Wrap(ctx, "acme", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = ks.Unwrap(ctx, "other", ct)
	if err == nil || core.CodeOf(err) != core.CodeInternal {
		t.Fatalf("err=%v", err)
	}
}

func TestKeeperTamperCannotUnwrap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ks, err := secrets.OpenKeyService(ctx, localURL(3))
	if err != nil {
		t.Fatal(err)
	}
	ct, err := ks.Wrap(ctx, "acme", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	ct[0] ^= 1
	_, err = ks.Unwrap(ctx, "acme", ct)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestKeeperWrongKeyCannotDecrypt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, err := secrets.OpenKeyService(ctx, localURL(3))
	if err != nil {
		t.Fatal(err)
	}
	b, err := secrets.OpenKeyService(ctx, localURL(4))
	if err != nil {
		t.Fatal(err)
	}
	ct, err := a.Wrap(ctx, "acme", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.Unwrap(ctx, "acme", ct)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestKeeperMasterKeyIDStableNoSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	k := key32(3)
	url := "localsecrets://" + base64.StdEncoding.EncodeToString(k)
	ks, err := secrets.OpenKeyService(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	id := ks.MasterKeyID()
	sum := sha256.Sum256(k)
	want := "localsecrets:" + hex.EncodeToString(sum[:])
	if id != want {
		t.Fatalf("id %q want %q", id, want)
	}
	if strings.Contains(id, base64.StdEncoding.EncodeToString(k)) {
		t.Fatal("id contains secret")
	}
	id2 := ks.MasterKeyID()
	if id != id2 {
		t.Fatal("id not stable")
	}
}

func TestOpenCodecLocalsecrets(t *testing.T) {
	t.Parallel()
	ctx := tenantCtx("acme")
	store, err := secrets.NewFileStore(filepath.Join(t.TempDir(), "ks"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := secrets.OpenCodec(ctx, store, localURL(3), nil)
	if err != nil {
		t.Fatal(err)
	}
	p := secrets.Payload{Metadata: map[string][]byte{"encoding": []byte("json/plain")}, Data: []byte("hi")}
	enc, err := c.Encode(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := c.Decode(ctx, enc)
	if err != nil {
		t.Fatal(err)
	}
	if string(dec.Data) != "hi" {
		t.Fatal(dec)
	}
}

func localURL(seed int64) string {
	return "localsecrets://" + base64.StdEncoding.EncodeToString(key32(seed))
}

func key32(seed int64) []byte {
	var out []byte
	h := sha256.Sum256([]byte{byte(seed)})
	for len(out) < 32 {
		out = append(out, h[:]...)
		h = sha256.Sum256(h[:])
	}
	return out[:32]
}

func tenantCtx(tenant string) context.Context {
	return core.WithTenant(context.Background(), core.TenantID(tenant))
}
