package secrets_test

import (
	"context"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform/secrets"
)

type spyKeysetStore struct {
	get, create, cas, countCalls, tenantsCalls int
}

func (s *spyKeysetStore) Get(context.Context, string) (secrets.StoredKeyset, bool, error) {
	s.get++
	return secrets.StoredKeyset{}, false, nil
}

func (s *spyKeysetStore) CreateIfAbsent(context.Context, string, secrets.StoredKeyset) (bool, error) {
	s.create++
	return false, nil
}

func (s *spyKeysetStore) CompareAndSwap(context.Context, string, uint64, secrets.StoredKeyset) error {
	s.cas++
	return nil
}

func (s *spyKeysetStore) Count(context.Context) (int, error) {
	s.countCalls++
	return 0, nil
}

func (s *spyKeysetStore) Tenants(context.Context) ([]string, error) {
	s.tenantsCalls++
	return nil, nil
}

type stubKeyService struct{}

func (stubKeyService) MasterKeyID() string { return "stub" }

func (stubKeyService) Wrap(context.Context, string, []byte) ([]byte, error) { return nil, nil }

func (stubKeyService) Unwrap(context.Context, string, []byte) ([]byte, error) { return nil, nil }

func (stubKeyService) Close() error { return nil }

func TestCodecRejectsMalformedTenantBeforeStore(t *testing.T) {
	t.Parallel()
	malformed := []string{"", "acme\x00evil", stringsRepeat('x', 129), "../acme", "a/b"}
	for _, tenant := range malformed {
		store := &spyKeysetStore{}
		c, err := secrets.NewCodec(store, []secrets.KeyService{stubKeyService{}})
		if err != nil {
			t.Fatal(err)
		}
		ctx := core.WithTenant(context.Background(), core.TenantID(tenant))
		if err := c.RotateDataKey(ctx); err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("RotateDataKey(%q): err=%v", tenant, err)
		}
		if store.get != 0 {
			t.Fatalf("RotateDataKey(%q): store Get %d times", tenant, store.get)
		}
		store = &spyKeysetStore{}
		c, _ = secrets.NewCodec(store, []secrets.KeyService{stubKeyService{}})
		if _, err := c.PrimaryDataKeyID(ctx); err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("PrimaryDataKeyID(%q): err=%v", tenant, err)
		}
		if store.get != 0 {
			t.Fatalf("PrimaryDataKeyID(%q): store Get %d times", tenant, store.get)
		}
		store = &spyKeysetStore{}
		c, _ = secrets.NewCodec(store, []secrets.KeyService{stubKeyService{}})
		if _, err := c.DataKeyCount(ctx); err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("DataKeyCount(%q): err=%v", tenant, err)
		}
		if store.get != 0 {
			t.Fatalf("DataKeyCount(%q): store Get %d times", tenant, store.get)
		}
	}
}

type recordingKeysetStore struct {
	inner secrets.KeysetStore
	ops   []string
}

func (r *recordingKeysetStore) Get(ctx context.Context, tenant string) (secrets.StoredKeyset, bool, error) {
	r.ops = append(r.ops, "get:"+tenant)
	return r.inner.Get(ctx, tenant)
}

func (r *recordingKeysetStore) CreateIfAbsent(ctx context.Context, tenant string, record secrets.StoredKeyset) (bool, error) {
	r.ops = append(r.ops, "create:"+tenant)
	return r.inner.CreateIfAbsent(ctx, tenant, record)
}

func (r *recordingKeysetStore) CompareAndSwap(ctx context.Context, tenant string, rev uint64, record secrets.StoredKeyset) error {
	r.ops = append(r.ops, "cas:"+tenant)
	return r.inner.CompareAndSwap(ctx, tenant, rev, record)
}

func (r *recordingKeysetStore) Count(ctx context.Context) (int, error) {
	r.ops = append(r.ops, "count")
	return r.inner.Count(ctx)
}

func (r *recordingKeysetStore) Tenants(ctx context.Context) ([]string, error) {
	r.ops = append(r.ops, "tenants")
	return r.inner.Tenants(ctx)
}

type stubKeyServiceRecord struct{}

func (stubKeyServiceRecord) MasterKeyID() string { return "stub" }

func (stubKeyServiceRecord) Wrap(ctx context.Context, tenant string, plain []byte) ([]byte, error) {
	inner, err := secrets.FrameBoundPlaintext(tenant, plain)
	if err != nil {
		return nil, err
	}
	return append([]byte("wrap:"), inner...), nil
}

func (stubKeyServiceRecord) Unwrap(ctx context.Context, tenant string, ct []byte) ([]byte, error) {
	if len(ct) < 5 || string(ct[:5]) != "wrap:" {
		return nil, core.NewError(core.CodeInternal, "keyset of the tenant cannot be unwrapped", nil)
	}
	return secrets.ParseBoundPlaintext(tenant, ct[5:])
}

func (stubKeyServiceRecord) Close() error { return nil }

func TestCodecTenantIsolationNoCrossTenantMutate(t *testing.T) {
	t.Parallel()
	inner := secrets.NewTestMemKeysetStore()
	c, err := secrets.NewCodec(inner, []secrets.KeyService{stubKeyServiceRecord{}})
	if err != nil {
		t.Fatal(err)
	}
	ctxA := core.WithTenant(context.Background(), "acme")
	p := secrets.Payload{Metadata: map[string][]byte{"encoding": []byte("json/plain")}, Data: []byte("secret")}
	if _, err := c.Encode(ctxA, p); err != nil {
		t.Fatal(err)
	}
	acmeBefore, ok, err := inner.Get(ctxA, "acme")
	if err != nil || !ok {
		t.Fatalf("acme snapshot: ok=%v err=%v", ok, err)
	}
	rec := &recordingKeysetStore{inner: inner}
	c2, err := secrets.NewCodec(rec, []secrets.KeyService{stubKeyServiceRecord{}})
	if err != nil {
		t.Fatal(err)
	}
	ctxB := core.WithTenant(context.Background(), "globex")
	if err := c2.RotateDataKey(ctxB); err == nil || core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("RotateDataKey globex: err=%v", err)
	}
	if _, err := c2.PrimaryDataKeyID(ctxB); err == nil || core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("PrimaryDataKeyID globex: err=%v", err)
	}
	if _, err := c2.DataKeyCount(ctxB); err == nil || core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("DataKeyCount globex: err=%v", err)
	}
	for _, op := range rec.ops {
		if strings.HasSuffix(op, ":acme") {
			t.Fatalf("acme touched: %q", op)
		}
		if strings.HasPrefix(op, "get:") || strings.HasPrefix(op, "create:") || strings.HasPrefix(op, "cas:") {
			if !strings.HasSuffix(op, ":globex") {
				t.Fatalf("unexpected tenant op %q", op)
			}
		}
	}
	acmeAfter, ok, err := inner.Get(ctxA, "acme")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if !storedKeysetEqual(acmeBefore, acmeAfter) {
		t.Fatal("acme record changed")
	}
}

func storedKeysetEqual(a, b secrets.StoredKeyset) bool {
	if a.MasterKeyID != b.MasterKeyID || a.Revision != b.Revision || len(a.Wrapped) != len(b.Wrapped) {
		return false
	}
	for i := range a.Wrapped {
		if a.Wrapped[i] != b.Wrapped[i] {
			return false
		}
	}
	return true
}

func stringsRepeat(s byte, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = s
	}
	return string(b)
}
