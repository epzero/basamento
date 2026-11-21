package secrets

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/epzero/basamento/core"
)

type stallCanaryWrapKey struct {
	closeCountKey
	canaryGate chan struct{}
}

func (k *stallCanaryWrapKey) Wrap(ctx context.Context, tenant string, plain []byte) ([]byte, error) {
	if tenant == canaryTenant && k.canaryGate != nil {
		<-k.canaryGate
	}
	return k.closeCountKey.Wrap(ctx, tenant, plain)
}

func TestRotateMasterKeyAfterCloseRefusesAndKeepsNewKey(t *testing.T) {
	t.Parallel()
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	var oldN atomic.Int32
	old := &closeCountKey{budgetKey: budgetKey{id: "mk"}, closes: &oldN}
	c, err := NewCodec(store, []KeyService{old})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	var newN atomic.Int32
	newK := &closeCountKey{budgetKey: budgetKey{id: "mk-new"}, closes: &newN}
	err = c.RotateMasterKey(context.Background(), newK)
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err=%v", err)
	}
	if newN.Load() != 0 {
		t.Fatalf("codec closed new key: %d", newN.Load())
	}
	if err := newK.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if oldN.Load() != 1 {
		t.Fatalf("old closes=%d want 1", oldN.Load())
	}
}

func TestCloseRacesRotateMasterKeyCanaryBeforeLock(t *testing.T) {
	t.Parallel()
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	gate := make(chan struct{})
	var oldN atomic.Int32
	old := &closeCountKey{budgetKey: budgetKey{id: "mk"}, closes: &oldN}
	c, err := NewCodec(store, []KeyService{old})
	if err != nil {
		t.Fatal(err)
	}
	var newN atomic.Int32
	newK := &stallCanaryWrapKey{
		closeCountKey: closeCountKey{budgetKey: budgetKey{id: "mk-new"}, closes: &newN},
		canaryGate:    gate,
	}
	var wg sync.WaitGroup
	wg.Add(1)
	rotErr := make(chan error, 1)
	go func() {
		defer wg.Done()
		rotErr <- c.RotateMasterKey(context.Background(), newK)
	}()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	close(gate)
	wg.Wait()
	err = <-rotErr
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("rotate err=%v", err)
	}
	if newN.Load() != 0 {
		t.Fatalf("codec closed new key: %d", newN.Load())
	}
	_ = newK.Close()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if oldN.Load() != 1 {
		t.Fatalf("old closes=%d want 1", oldN.Load())
	}
}

func TestCloseWhileRotateMasterKeyRewraps(t *testing.T) {
	t.Parallel()
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	ctx := core.WithTenant(context.Background(), "acme")
	var oldN atomic.Int32
	old := &closeCountKey{budgetKey: budgetKey{id: "mk-old"}, closes: &oldN}
	c, err := NewCodec(store, []KeyService{old})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Encode(ctx, plainPayload("x")); err != nil {
		t.Fatal(err)
	}
	inWrap := make(chan struct{}, 1)
	gate := make(chan struct{})
	var newN atomic.Int32
	newK := &closeCountKey{
		budgetKey: budgetKey{id: "mk-new", inWrap: inWrap, gate: gate},
		closes:    &newN,
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = c.RotateMasterKey(ctx, newK)
	}()
	<-inWrap
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	close(gate)
	wg.Wait()
	if newN.Load() != 1 || oldN.Load() != 1 {
		t.Fatalf("closes new=%d old=%d want 1 each", newN.Load(), oldN.Load())
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if newN.Load() != 1 || oldN.Load() != 1 {
		t.Fatalf("second Close re-closed new=%d old=%d", newN.Load(), oldN.Load())
	}
}

type valueKey struct {
	id   string
	salt int
}

func (k valueKey) MasterKeyID() string { return k.id }
func (k valueKey) Close() error        { return nil }

func (k valueKey) Wrap(ctx context.Context, tenant string, plain []byte) ([]byte, error) {
	b := &budgetKey{id: k.id}
	return b.Wrap(ctx, tenant, plain)
}

func (k valueKey) Unwrap(ctx context.Context, tenant string, ct []byte) ([]byte, error) {
	b := &budgetKey{id: k.id}
	return b.Unwrap(ctx, tenant, ct)
}

type keyWithMap struct {
	id string
	m  map[string]byte
}

func (k keyWithMap) MasterKeyID() string { return k.id }
func (k keyWithMap) Close() error        { return nil }
func (k keyWithMap) Wrap(ctx context.Context, tenant string, plain []byte) ([]byte, error) {
	return nil, nil
}
func (k keyWithMap) Unwrap(ctx context.Context, tenant string, ct []byte) ([]byte, error) {
	return nil, nil
}

type keyWithFunc struct {
	id string
	f  func()
}

func (k keyWithFunc) MasterKeyID() string { return k.id }
func (k keyWithFunc) Close() error        { return nil }
func (k keyWithFunc) Wrap(ctx context.Context, tenant string, plain []byte) ([]byte, error) {
	return nil, nil
}
func (k keyWithFunc) Unwrap(ctx context.Context, tenant string, ct []byte) ([]byte, error) {
	return nil, nil
}

func TestKeyServiceMustBeComparable(t *testing.T) {
	t.Parallel()
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	ok := valueKey{id: "mk", salt: 1}
	if _, err := NewCodec(store, []KeyService{ok}); err != nil {
		t.Fatalf("comparable value key: %v", err)
	}
	for _, bad := range []KeyService{
		keyWithMap{id: "x", m: map[string]byte{}},
		keyWithFunc{id: "x", f: func() {}},
	} {
		_, err := NewCodec(store, []KeyService{bad})
		if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("NewCodec bad=%T err=%v", bad, err)
		}
	}
	c, err := NewCodec(store, []KeyService{&budgetKey{id: "mk"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RotateMasterKey(context.Background(), nil); err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("nil rotate err=%v", err)
	}
	if err := c.RotateMasterKey(context.Background(), keyWithMap{id: "y", m: map[string]byte{}}); err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("map key rotate err=%v", err)
	}
}

func TestSameValueKeyReinstallRecognised(t *testing.T) {
	t.Parallel()
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	vk := valueKey{id: "a", salt: 7}
	c, err := NewCodec(store, []KeyService{vk, valueKey{id: "b", salt: 0}})
	if err != nil {
		t.Fatal(err)
	}
	if !sameKeyService(vk, vk) {
		t.Fatal("same value key not recognised")
	}
	if err := c.RotateMasterKey(context.Background(), vk); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}
