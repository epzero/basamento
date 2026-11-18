package secrets

import (
	"context"
	"errors"
	"testing"

	"github.com/epzero/basamento/core"
)

type failAfterWrapKey struct {
	id         string
	wraps      int
	failAfter  int
	wrappedIDs []string
}

func (f *failAfterWrapKey) MasterKeyID() string { return f.id }

func (f *failAfterWrapKey) Wrap(ctx context.Context, tenant string, plaintext []byte) ([]byte, error) {
	f.wraps++
	f.wrappedIDs = append(f.wrappedIDs, tenant)
	if f.wraps > f.failAfter {
		return nil, errUnavailable("test")
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
	data map[string]StoredKeyset
}

func (m *memKeysetStore) Get(tenant string) (StoredKeyset, bool, error) {
	r, ok := m.data[tenant]
	return r, ok, nil
}

func (m *memKeysetStore) CreateIfAbsent(tenant string, record StoredKeyset) (bool, error) {
	if _, ok := m.data[tenant]; ok {
		return false, nil
	}
	m.data[tenant] = record
	return true, nil
}

func (m *memKeysetStore) CompareAndSwap(tenant string, expectRevision uint64, record StoredKeyset) error {
	old, ok := m.data[tenant]
	if !ok || old.Revision != expectRevision {
		return errInvalidArgument("conflict")
	}
	m.data[tenant] = record
	return nil
}

func (m *memKeysetStore) Count() (int, error) { return len(m.data), nil }

func (m *memKeysetStore) Tenants() ([]string, error) {
	out := make([]string, 0, len(m.data))
	for k := range m.data {
		out = append(out, k)
	}
	return out, nil
}

func TestRotateMasterKeyRegistersNewKeyBeforePartialFailure(t *testing.T) {
	t.Parallel()
	ctx := core.WithTenant(context.Background(), "acme")
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	old := &failAfterWrapKey{id: "mk-1", failAfter: 99}
	mid := &failAfterWrapKey{id: "mk-2", failAfter: 1}
	c, err := NewCodec(store, []KeyService{old})
	if err != nil {
		t.Fatal(err)
	}
	_ = store.data
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
	c.invalidate("acme")
	c.invalidate("globex")
	p := Payload{Metadata: map[string][]byte{"encoding": []byte("json/plain")}, Data: []byte("x")}
	for _, tenant := range []string{"acme", "globex"} {
		enc, err := c.Encode(core.WithTenant(ctx, core.TenantID(tenant)), p)
		if err != nil {
			t.Fatalf("%s encode: %v", tenant, err)
		}
		if _, err := c.Decode(core.WithTenant(ctx, core.TenantID(tenant)), enc); err != nil {
			t.Fatalf("%s decode: %v", tenant, err)
		}
	}
}
