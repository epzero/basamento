package secrets

import (
	"context"
	"sync"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestMasterKeyIDQueryParamDifference(t *testing.T) {
	t.Parallel()
	a, err := publicURLID("awskms://key/alias/foo?region=us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := publicURLID("awskms://key/alias/foo?region=us-west-2")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("query parameters must distinguish master ids")
	}
}

func TestRotationPartialFailureNewMasterStaysAndRetryCompletes(t *testing.T) {
	t.Parallel()
	old := &budgetKey{id: "mk-old"}
	newK := &failAfterWrapKey{id: "mk-new", failAfter: 1}
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	c, _ := NewCodec(store, []KeyService{old})
	h, _ := newDataKeyset()
	for _, tenant := range []string{"acme", "globex"} {
		tctx := core.WithTenant(context.Background(), core.TenantID(tenant))
		w, err := wrapKeyset(tctx, old, tenant, h)
		if err != nil {
			t.Fatal(err)
		}
		store.data[tenant] = w
	}
	ctx := core.WithTenant(context.Background(), "acme")
	p := plainPayload("secret")
	encAcme, _ := c.Encode(ctx, p)
	encGlobex, _ := c.Encode(core.WithTenant(context.Background(), "globex"), p)
	err := c.RotateMasterKey(ctx, newK)
	if err == nil || core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("partial rotate: %v", err)
	}
	if c.snapshotKeys()[0].MasterKeyID() != "mk-new" {
		t.Fatal("new master must remain configured")
	}
	if _, err := c.Decode(ctx, encAcme); err != nil {
		t.Fatalf("acme decode after partial: %v", err)
	}
	if _, err := c.Decode(core.WithTenant(context.Background(), "globex"), encGlobex); err != nil {
		t.Fatalf("globex decode after partial: %v", err)
	}
	newK.failAfter = 99
	if err := c.RotateMasterKey(ctx, newK); err != nil {
		t.Fatal(err)
	}
	for _, tenant := range []string{"acme", "globex"} {
		if store.data[tenant].MasterKeyID != "mk-new" {
			t.Fatalf("%s still on old master", tenant)
		}
	}
}

func TestRotationRecordNamesUnknownMaster(t *testing.T) {
	t.Parallel()
	ks := &budgetKey{id: "mk"}
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	c, _ := NewCodec(store, []KeyService{ks})
	ctx := core.WithTenant(context.Background(), "acme")
	enc, err := c.Encode(ctx, plainPayload("x"))
	if err != nil {
		t.Fatal(err)
	}
	rec := store.data["acme"]
	rec.MasterKeyID = "missing"
	store.data["acme"] = rec
	c2, _ := NewCodec(store, []KeyService{ks})
	_, err = c2.Decode(ctx, enc)
	if err == nil || core.CodeOf(err) != core.CodeInternal {
		t.Fatalf("err=%v", err)
	}
}

func TestRotationVerifiedSkipUnwrapDenied(t *testing.T) {
	t.Parallel()
	newK := &budgetKey{id: "mk-new", failUnwrap: true}
	old := &budgetKey{id: "mk-old"}
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	c, _ := NewCodec(store, []KeyService{old})
	h, _ := newDataKeyset()
	w, _ := wrapKeyset(context.Background(), newK, "acme", h)
	w.MasterKeyID = "mk-new"
	store.data["acme"] = w
	err := c.RotateMasterKey(core.WithTenant(context.Background(), "acme"), newK)
	if err == nil {
		t.Fatal("expected verified skip failure")
	}
}

func TestCanaryFailureTouchesNothing(t *testing.T) {
	t.Parallel()
	old := &budgetKey{id: "mk-old"}
	bad := &failCanaryKey{id: "mk-bad"}
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	c, _ := NewCodec(store, []KeyService{old})
	h, _ := newDataKeyset()
	w, _ := wrapKeyset(context.Background(), old, "acme", h)
	store.data["acme"] = w
	snapRev := store.data["acme"].Revision
	snapID := store.data["acme"].MasterKeyID
	err := c.RotateMasterKey(context.Background(), bad)
	if err == nil {
		t.Fatal("expected canary failure")
	}
	if c.snapshotKeys()[0].MasterKeyID() != "mk-old" {
		t.Fatal("key list changed on canary failure")
	}
	if store.data["acme"].Revision != snapRev || store.data["acme"].MasterKeyID != snapID {
		t.Fatal("store changed on canary failure")
	}
}

type failCanaryKey struct{ id string }

func (f *failCanaryKey) MasterKeyID() string { return f.id }
func (f *failCanaryKey) Close() error        { return nil }

func (f *failCanaryKey) Wrap(ctx context.Context, tenant string, plain []byte) ([]byte, error) {
	if tenant == canaryTenant {
		return nil, errInternal(msgKeyServiceRefused)
	}
	return append([]byte("wrap:"), plain...), nil
}

func (f *failCanaryKey) Unwrap(context.Context, string, []byte) ([]byte, error) {
	return nil, errInternal(msgKeyServiceRefused)
}

func TestCreateKeysetRaceMasterRotation(t *testing.T) {
	t.Parallel()
	gate := make(chan struct{})
	old := &budgetKey{id: "mk-old", gate: gate}
	newK := &budgetKey{id: "mk-new"}
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	c, _ := NewCodec(store, []KeyService{old})
	ctx := core.WithTenant(context.Background(), "newco")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = c.Encode(ctx, plainPayload("first"))
	}()
	errCh := make(chan error, 1)
	go func() {
		errCh <- c.RotateMasterKey(ctx, newK)
	}()
	close(gate)
	wg.Wait()
	if err := <-errCh; err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if store.data["newco"].MasterKeyID != "mk-new" {
		t.Fatalf("record master %q", store.data["newco"].MasterKeyID)
	}
}

func TestRotationRecordUnconfiguredMasterUnchanged(t *testing.T) {
	t.Parallel()
	store := &memKeysetStore{data: map[string]StoredKeyset{
		"acme": {Wrapped: []byte("cipher"), MasterKeyID: "other", Revision: 1},
	}}
	beforeRev := store.data["acme"].Revision
	beforeWrap := append([]byte(nil), store.data["acme"].Wrapped...)
	newK := &budgetKey{id: "mk-new"}
	c, _ := NewCodec(store, []KeyService{&budgetKey{id: "mk"}})
	err := c.RotateMasterKey(context.Background(), newK)
	if err == nil {
		t.Fatal("expected failure unwrapping missing master")
	}
	if store.data["acme"].Revision != beforeRev || !bytesEqual(store.data["acme"].Wrapped, beforeWrap) {
		t.Fatal("record must be unchanged")
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
