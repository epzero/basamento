package secrets_test

import (
	"context"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform/secrets"
)

// passThroughKey does not bind keyset material to a tenant at the master layer.
type passThroughKey struct {
	id string
}

func (k passThroughKey) MasterKeyID() string { return k.id }

func (k passThroughKey) Close() error { return nil }

func (k passThroughKey) Wrap(_ context.Context, _ string, plain []byte) ([]byte, error) {
	return append([]byte(nil), plain...), nil
}

func (k passThroughKey) Unwrap(_ context.Context, _ string, ct []byte) ([]byte, error) {
	return append([]byte(nil), ct...), nil
}

func TestPayloadTenantAADEnforced(t *testing.T) {
	t.Parallel()
	store := secrets.NewTestMemKeysetStore()
	ks := passThroughKey{id: "aad-mk-tenant"}
	c, err := secrets.NewCodec(store, []secrets.KeyService{ks})
	if err != nil {
		t.Fatal(err)
	}
	ctxAcme := core.WithTenant(context.Background(), "acme")
	ctxGlobex := core.WithTenant(context.Background(), "globex")
	plain := secrets.Payload{Metadata: map[string][]byte{"encoding": []byte("json/plain")}, Data: []byte("secret")}
	enc, err := c.Encode(ctxAcme, plain)
	if err != nil {
		t.Fatal(err)
	}
	acmeRec, ok, err := store.Get(ctxAcme, "acme")
	if err != nil || !ok {
		t.Fatalf("acme keyset: ok=%v err=%v", ok, err)
	}
	if _, err := store.CreateIfAbsent(ctxGlobex, "globex", acmeRec); err != nil {
		t.Fatal(err)
	}
	_, err = c.Decode(ctxGlobex, enc)
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("globex decode: err=%v", err)
	}
	if !strings.Contains(err.Error(), "payload cannot be decrypted") {
		t.Fatalf("message: %v", err)
	}
	out, err := c.Decode(ctxAcme, enc)
	if err != nil {
		t.Fatalf("acme decode: %v", err)
	}
	if string(out.Data) != "secret" {
		t.Fatalf("data %q", out.Data)
	}
}

func TestPayloadKeyIDAADRejectsRelabelAfterRotation(t *testing.T) {
	t.Parallel()
	store := secrets.NewTestMemKeysetStore()
	ks := passThroughKey{id: "aad-mk-keyid"}
	c, err := secrets.NewCodec(store, []secrets.KeyService{ks})
	if err != nil {
		t.Fatal(err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	plain := secrets.Payload{Metadata: map[string][]byte{"encoding": []byte("json/plain")}, Data: []byte("data")}
	enc, err := c.Encode(ctx, plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RotateDataKey(ctx); err != nil {
		t.Fatal(err)
	}
	newID, err := c.PrimaryDataKeyID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(enc.Metadata[secrets.MetaEncryptionKeyID]) == newID {
		t.Fatal("rotation must change primary id")
	}
	forged := enc
	forged.Metadata = map[string][]byte{}
	for meta, v := range enc.Metadata {
		forged.Metadata[meta] = append([]byte(nil), v...)
	}
	forged.Metadata[secrets.MetaEncryptionKeyID] = []byte(newID)
	_, err = c.Decode(ctx, forged)
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("forged key id decode: %v", err)
	}
	if !strings.Contains(err.Error(), "payload cannot be decrypted") {
		t.Fatalf("message: %v", err)
	}
	out, err := c.Decode(ctx, enc)
	if err != nil {
		t.Fatalf("original decode after rotation: %v", err)
	}
	if string(out.Data) != "data" {
		t.Fatalf("data %q", out.Data)
	}
}
