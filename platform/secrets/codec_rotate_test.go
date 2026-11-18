package secrets

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/epzero/basamento/core"
)

type failAfterWrapKey struct {
	id        string
	wraps     atomic.Int32
	failAfter int
}

func (f *failAfterWrapKey) MasterKeyID() string { return f.id }

func (f *failAfterWrapKey) Close() error { return nil }

func (f *failAfterWrapKey) Wrap(ctx context.Context, tenant string, plaintext []byte) ([]byte, error) {
	if tenant == canaryTenant {
		inner, err := FrameBoundPlaintext(tenant, plaintext)
		if err != nil {
			return nil, err
		}
		return append([]byte("wrap:"), inner...), nil
	}
	n := f.wraps.Add(1)
	if int(n) > f.failAfter {
		return nil, errUnavailableRetry("test")
	}
	inner, err := FrameBoundPlaintext(tenant, plaintext)
	if err != nil {
		return nil, err
	}
	return append([]byte("wrap:"), inner...), nil
}

func (f *failAfterWrapKey) Unwrap(ctx context.Context, tenant string, ct []byte) ([]byte, error) {
	if len(ct) < 5 || string(ct[:5]) != "wrap:" {
		return nil, errors.New("bad")
	}
	return ParseBoundPlaintext(tenant, ct[5:])
}

type memKeysetStore struct {
	mu   sync.Mutex
	data map[string]StoredKeyset
}

func (m *memKeysetStore) Get(_ context.Context, tenant string) (StoredKeyset, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.data[tenant]
	return r, ok, nil
}

func (m *memKeysetStore) CreateIfAbsent(_ context.Context, tenant string, record StoredKeyset) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.data[tenant]; ok {
		return false, nil
	}
	m.data[tenant] = record
	return true, nil
}

func (m *memKeysetStore) CompareAndSwap(_ context.Context, tenant string, expect uint64, record StoredKeyset) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.data[tenant]
	if !ok || old.Revision != expect {
		return errConflict(msgKeysetRevisionConflict)
	}
	m.data[tenant] = record
	return nil
}

func (m *memKeysetStore) Count(context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.data), nil
}

func (m *memKeysetStore) Tenants(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.data))
	for k := range m.data {
		out = append(out, k)
	}
	return out, nil
}

func TestRotateMasterKeyFailsBeforeAnyRecordWrapped(t *testing.T) {
	t.Parallel()
	ctx := core.WithTenant(context.Background(), "acme")
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	old := &failAfterWrapKey{id: "mk-1", failAfter: 99}
	mid := &failAfterWrapKey{id: "mk-2", failAfter: 0}
	c, err := NewCodec(store, []KeyService{old})
	if err != nil {
		t.Fatal(err)
	}
	h, err := newDataKeyset()
	if err != nil {
		t.Fatal(err)
	}
	for _, tenant := range []string{"acme", "globex"} {
		tctx := core.WithTenant(context.Background(), core.TenantID(tenant))
		w, err := wrapKeyset(tctx, old, tenant, h)
		if err != nil {
			t.Fatal(err)
		}
		store.data[tenant] = w
	}
	err = c.RotateMasterKey(ctx, mid)
	if err == nil || core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("rotate err=%v", err)
	}
	if mid.wraps.Load() != 1 {
		t.Fatalf("expected one failed rewrap attempt, wraps=%d", mid.wraps.Load())
	}
	for _, tenant := range []string{"acme", "globex"} {
		if store.data[tenant].MasterKeyID != "mk-1" {
			t.Fatalf("%s record changed", tenant)
		}
	}
	if c.snapshotKeys()[0].MasterKeyID() != "mk-2" {
		t.Fatal("new master must stay configured after partial failure")
	}
}
