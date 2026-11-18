package secrets

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/epzero/basamento/core"
)

type budgetKey struct {
	id         string
	unwraps    atomic.Int32
	wraps      atomic.Int32
	gate       chan struct{}
	failUnwrap bool
}

func (b *budgetKey) MasterKeyID() string { return b.id }
func (b *budgetKey) Close() error        { return nil }

func (b *budgetKey) Wrap(ctx context.Context, tenant string, plain []byte) ([]byte, error) {
	if b.gate != nil && tenant != canaryTenant {
		<-b.gate
	}
	b.wraps.Add(1)
	inner, err := FrameBoundPlaintext(tenant, plain)
	if err != nil {
		return nil, err
	}
	return append([]byte("wrap:"), inner...), nil
}

func (b *budgetKey) Unwrap(ctx context.Context, tenant string, ct []byte) ([]byte, error) {
	b.unwraps.Add(1)
	if b.failUnwrap {
		return nil, errUnavailableRetry("awskms")
	}
	if len(ct) < 5 || string(ct[:5]) != "wrap:" {
		return nil, errInternal(msgKeysetCannotUnwrap)
	}
	return ParseBoundPlaintext(tenant, ct[5:])
}

type spyGets struct {
	memKeysetStore
	gets int
}

func (s *spyGets) Get(ctx context.Context, tenant string) (StoredKeyset, bool, error) {
	s.gets++
	return s.memKeysetStore.Get(ctx, tenant)
}

func plainPayload(data string) Payload {
	return Payload{Metadata: map[string][]byte{"encoding": []byte("json/plain")}, Data: []byte(data)}
}

func TestUnwrapBudgetColdTamperedAtMostOne(t *testing.T) {
	t.Parallel()
	ks := &budgetKey{id: "mk"}
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	c, err := NewCodec(store, []KeyService{ks})
	if err != nil {
		t.Fatal(err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	enc, err := c.Encode(ctx, plainPayload("hi"))
	if err != nil {
		t.Fatal(err)
	}
	ks.unwraps.Store(0)
	enc.Data[0] ^= 1
	_, _ = c.Decode(ctx, enc)
	if ks.unwraps.Load() > 1 {
		t.Fatalf("unwraps %d want <=1", ks.unwraps.Load())
	}
}

func TestUnwrapBudgetWarmTamperedZero(t *testing.T) {
	t.Parallel()
	ks := &budgetKey{id: "mk"}
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	c, _ := NewCodec(store, []KeyService{ks})
	ctx := core.WithTenant(context.Background(), "acme")
	enc, _ := c.Encode(ctx, plainPayload("hi"))
	if _, err := c.Decode(ctx, enc); err != nil {
		t.Fatal(err)
	}
	ks.unwraps.Store(0)
	for i := 0; i < 5; i++ {
		bad := enc
		bad.Data = append([]byte(nil), enc.Data...)
		bad.Data[0] ^= byte(i + 1)
		_, _ = c.Decode(ctx, bad)
	}
	if ks.unwraps.Load() != 0 {
		t.Fatalf("unwraps %d want 0", ks.unwraps.Load())
	}
}

func TestUnwrapBudgetTamperThenManyDecodes(t *testing.T) {
	t.Parallel()
	ks := &budgetKey{id: "mk"}
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	c, _ := NewCodec(store, []KeyService{ks})
	ctx := core.WithTenant(context.Background(), "acme")
	enc, _ := c.Encode(ctx, plainPayload("hi"))
	if _, err := c.Decode(ctx, enc); err != nil {
		t.Fatal(err)
	}
	ks.unwraps.Store(0)
	bad := enc
	bad.Data = append([]byte(nil), enc.Data...)
	bad.Data[0] ^= 1
	_, _ = c.Decode(ctx, bad)
	for i := 0; i < 100; i++ {
		if _, err := c.Decode(ctx, enc); err != nil {
			t.Fatal(err)
		}
	}
	if ks.unwraps.Load() > 1 {
		t.Fatalf("unwraps %d want <=1", ks.unwraps.Load())
	}
}

func TestUnwrapBudgetBadKeyIDNoStoreNoUnwrap(t *testing.T) {
	t.Parallel()
	ks := &budgetKey{id: "mk"}
	store := &spyGets{memKeysetStore: memKeysetStore{data: map[string]StoredKeyset{}}}
	c, _ := NewCodec(store, []KeyService{ks})
	ctx := core.WithTenant(context.Background(), "acme")
	cases := []string{"not-a-number", strings.Repeat("9", 1_000_000)}
	for _, id := range cases {
		store.gets = 0
		ks.unwraps.Store(0)
		p := Payload{
			Metadata: map[string][]byte{
				"encoding":          []byte(EncodingEncrypted),
				MetaEncryptionKeyID: []byte(id),
			},
			Data: []byte("x"),
		}
		_, _ = c.Decode(ctx, p)
		if store.gets != 0 || ks.unwraps.Load() != 0 {
			t.Fatalf("id %q: gets=%d unwraps=%d", id, store.gets, ks.unwraps.Load())
		}
	}
}

func TestUnwrapBudgetUnknownKeyIDOneGetNoUnwrap(t *testing.T) {
	t.Parallel()
	ks := &budgetKey{id: "mk"}
	store := &spyGets{memKeysetStore: memKeysetStore{data: map[string]StoredKeyset{}}}
	c, _ := NewCodec(store, []KeyService{ks})
	ctx := core.WithTenant(context.Background(), "acme")
	enc, _ := c.Encode(ctx, plainPayload("hi"))
	if _, err := c.Decode(ctx, enc); err != nil {
		t.Fatal(err)
	}
	enc.Metadata = map[string][]byte{
		"encoding":          []byte(EncodingEncrypted),
		MetaEncryptionKeyID: []byte("3847562921"),
	}
	store.gets = 0
	ks.unwraps.Store(0)
	_, err := c.Decode(ctx, enc)
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err=%v", err)
	}
	if store.gets != 1 || ks.unwraps.Load() != 0 {
		t.Fatalf("gets=%d unwraps=%d", store.gets, ks.unwraps.Load())
	}
}

func TestUnwrapBudgetTwoCodecsDataKeyRotation(t *testing.T) {
	t.Parallel()
	ks := &budgetKey{id: "mk"}
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	cA, _ := NewCodec(store, []KeyService{ks})
	cB, _ := NewCodec(store, []KeyService{ks})
	ctx := core.WithTenant(context.Background(), "acme")
	enc1, _ := cA.Encode(ctx, plainPayload("v1"))
	if _, err := cB.Decode(ctx, enc1); err != nil {
		t.Fatal(err)
	}
	if err := cA.RotateDataKey(ctx); err != nil {
		t.Fatal(err)
	}
	enc2, _ := cB.Encode(ctx, plainPayload("v2"))
	if string(enc1.Metadata[MetaEncryptionKeyID]) == string(enc2.Metadata[MetaEncryptionKeyID]) {
		t.Fatal("expected new primary key id after rotation")
	}
	if _, err := cB.Decode(ctx, enc1); err != nil {
		t.Fatal("history readable")
	}
}

func TestUnwrapBudgetTwoCodecsMasterRotation(t *testing.T) {
	t.Parallel()
	old := &budgetKey{id: "mk-old"}
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	cA, _ := NewCodec(store, []KeyService{old})
	cB, _ := NewCodec(store, []KeyService{old})
	ctx := core.WithTenant(context.Background(), "acme")
	enc, _ := cA.Encode(ctx, plainPayload("x"))
	old.unwraps.Store(0)
	newK := &budgetKey{id: "mk-new"}
	if err := cA.RotateMasterKey(ctx, newK); err != nil {
		t.Fatal(err)
	}
	if err := cB.RotateMasterKey(ctx, newK); err != nil {
		t.Fatal(err)
	}
	if store.data["acme"].MasterKeyID != "mk-new" {
		t.Fatal("record not rewrapped")
	}
	newK.unwraps.Store(0)
	if _, err := cB.Decode(ctx, enc); err != nil {
		t.Fatal(err)
	}
	if newK.unwraps.Load() > 1 {
		t.Fatalf("decode after master rotation unwraps=%d", newK.unwraps.Load())
	}
}
