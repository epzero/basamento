package secrets

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestRotateMasterKeyRetiresDifferentInstanceSameID(t *testing.T) {
	t.Parallel()
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	var oldN, newN atomic.Int32
	old := &closeCountKey{budgetKey: budgetKey{id: "mk"}, closes: &oldN}
	newK := &closeCountKey{budgetKey: budgetKey{id: "mk"}, closes: &newN}
	c, err := NewCodec(store, []KeyService{old})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RotateMasterKey(context.Background(), newK); err != nil {
		t.Fatal(err)
	}
	if oldN.Load() != 0 || newN.Load() != 0 {
		t.Fatalf("closes during rotate old=%d new=%d", oldN.Load(), newN.Load())
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if oldN.Load() != 1 || newN.Load() != 1 {
		t.Fatalf("closes after Close old=%d new=%d", oldN.Load(), newN.Load())
	}
}

func TestRotateMasterKeyOntoSameInstanceInRing(t *testing.T) {
	t.Parallel()
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	var nFirst, nSecond atomic.Int32
	first := &closeCountKey{budgetKey: budgetKey{id: "a"}, closes: &nFirst}
	second := &closeCountKey{budgetKey: budgetKey{id: "b"}, closes: &nSecond}
	c, err := NewCodec(store, []KeyService{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RotateMasterKey(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if nFirst.Load() != 0 || nSecond.Load() != 0 {
		t.Fatalf("closes during rotate first=%d second=%d", nFirst.Load(), nSecond.Load())
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if nFirst.Load() != 1 || nSecond.Load() != 1 {
		t.Fatalf("closes after Close first=%d second=%d", nFirst.Load(), nSecond.Load())
	}
}

type stallUnwrapKey struct {
	closeCountKey
	unwrapWait  chan struct{}
	unwrapStart chan struct{}
}

func (k *stallUnwrapKey) Unwrap(ctx context.Context, tenant string, ct []byte) ([]byte, error) {
	if tenant != canaryTenant && k.unwrapStart != nil {
		k.unwrapStart <- struct{}{}
		<-k.unwrapWait
	}
	return k.budgetKey.Unwrap(ctx, tenant, ct)
}

func TestDecodeDuringMasterRotationDoesNotCloseStalledKey(t *testing.T) {
	t.Parallel()
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	var oldN atomic.Int32
	wait := make(chan struct{})
	start := make(chan struct{}, 1)
	old := &stallUnwrapKey{
		closeCountKey: closeCountKey{budgetKey: budgetKey{id: "mk"}, closes: &oldN},
		unwrapWait:    wait,
		unwrapStart:   start,
	}
	c, err := NewCodec(store, []KeyService{old})
	if err != nil {
		t.Fatal(err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	enc, err := c.Encode(ctx, plainPayload("secret"))
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	delete(c.cache, "acme")
	c.mu.Unlock()
	var wg sync.WaitGroup
	wg.Add(1)
	decodeErr := make(chan error, 1)
	go func() {
		defer wg.Done()
		_, err := c.Decode(ctx, enc)
		decodeErr <- err
	}()
	<-start
	var newN atomic.Int32
	newK := &closeCountKey{budgetKey: budgetKey{id: "mk"}, closes: &newN}
	if err := c.RotateMasterKey(ctx, newK); err != nil {
		t.Fatal(err)
	}
	if oldN.Load() != 0 {
		t.Fatalf("old closed during stalled decode: %d", oldN.Load())
	}
	close(wait)
	wg.Wait()
	if err := <-decodeErr; err != nil {
		t.Fatal(err)
	}
	if oldN.Load() != 0 {
		t.Fatalf("old closed before unwrap returned: %d", oldN.Load())
	}
	_ = c.Close()
}

func TestCodecCloseKeepsClosingAfterError(t *testing.T) {
	t.Parallel()
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	want := errors.New("close failed")
	var n1, n2 atomic.Int32
	k1 := &closeCountKey{budgetKey: budgetKey{id: "a"}, closes: &n1, closeErr: want}
	k2 := &closeCountKey{budgetKey: budgetKey{id: "b"}, closes: &n2}
	c, err := NewCodec(store, []KeyService{k1, k2})
	if err != nil {
		t.Fatal(err)
	}
	err = c.Close()
	if !errors.Is(err, want) {
		t.Fatalf("Close err=%v want %v", err, want)
	}
	if n1.Load() != 1 || n2.Load() != 1 {
		t.Fatalf("closes k1=%d k2=%d", n1.Load(), n2.Load())
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if n1.Load() != 1 || n2.Load() != 1 {
		t.Fatalf("second Close re-closed k1=%d k2=%d", n1.Load(), n2.Load())
	}
}
