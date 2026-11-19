package secrets

import (
	"context"
	"sync"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestCodecConcurrentRotationAndPayloadOps(t *testing.T) {
	t.Parallel()
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	old := &budgetKey{id: "mk-old"}
	newK := &budgetKey{id: "mk-new"}
	c, err := NewCodec(store, []KeyService{old})
	if err != nil {
		t.Fatal(err)
	}
	tenants := []string{"acme", "globex", "initech"}
	ctxs := make([]context.Context, len(tenants))
	for i, tnt := range tenants {
		ctxs[i] = core.WithTenant(context.Background(), core.TenantID(tnt))
	}
	encoded := make([]Payload, len(tenants))
	var mu sync.Mutex
	var wg sync.WaitGroup
	run := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn()
		}()
	}
	for i := range tenants {
		i := i
		run(func() {
			enc, err := c.Encode(ctxs[i], plainPayload("data"))
			if err != nil {
				panic(err)
			}
			mu.Lock()
			encoded[i] = enc
			mu.Unlock()
			if _, err := c.Decode(ctxs[i], enc); err != nil {
				t.Error(err)
			}
			_ = c.RotateDataKey(ctxs[i])
		})
	}
	run(func() {
		_ = c.RotateMasterKey(context.Background(), newK)
	})
	wg.Wait()
	for i, tnt := range tenants {
		if _, err := c.Decode(ctxs[i], encoded[i]); err != nil {
			t.Fatalf("%s final decode: %v", tnt, err)
		}
	}
}
